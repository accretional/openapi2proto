package generator

import (
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// sanitizeForGnostic rewrites a parsed OpenAPI document so that gnostic's
// strict OpenAPI 3.0 model can ingest it. It performs a best-effort
// OpenAPI 3.1 -> 3.0 downconversion and strips non-standard sibling
// properties that gnostic rejects at validated positions.
//
// It operates on a *yaml.Node tree (which preserves key order for both JSON
// and YAML inputs) so that generated proto field numbering stays stable.
//
// Transformations:
//   - openapi: 3.1.x            -> openapi: 3.0.3
//   - type: [T, "null"]         -> type: T, nullable: true
//   - type: ["null"] / "null"   -> nullable: true (type dropped)
//   - anyOf/oneOf [S, null]     -> S + nullable: true
//   - const: X                  -> enum: [X]
//   - boolean schemas in items/not/contains normalized
//   - numeric exclusiveMinimum/Maximum -> 3.0 boolean form
//   - $ref with siblings        -> bare $ref (3.0 forbids siblings)
//   - 3.1-only schema keywords dropped (examples, prefixItems, propertyNames,
//     unevaluated*, dependent*, contentMediaType, $schema/$id, etc.)
//   - unknown sibling keywords on a schema stripped (e.g. ElevenLabs "embed")
//   - info.externalDocs and other stray info siblings stripped (Square)
//   - info.license.identifier dropped (3.1-only)
func sanitizeForGnostic(root *yaml.Node) {
	doc := documentRoot(root)
	if doc == nil || doc.Kind != yaml.MappingNode {
		return
	}

	// Force a version gnostic accepts.
	if v := mapValue(doc, "openapi"); v != nil && len(v.Value) >= 3 && v.Value[:3] == "3.1" {
		v.Value = "3.0.3"
		v.Tag = "!!str"
	}

	// Strip OpenAPI 3.1-only / non-standard top-level root keys that gnostic's
	// strict 3.0 model rejects (e.g. "webhooks", "jsonSchemaDialect"). x-
	// extensions are preserved.
	stripUnknownKeys(doc, rootAllowed)

	// Strip non-standard keys from path items and operations (e.g. the stray
	// "request" key in the Meilisearch spec) so gnostic can ingest them.
	if paths := mapValue(doc, "paths"); paths != nil && paths.Kind == yaml.MappingNode {
		for i := 1; i < len(paths.Content); i += 2 {
			sanitizePathItem(paths.Content[i])
		}
	} else {
		// gnostic's 3.0 model requires a "paths" property. Some 3.1 specs are
		// webhooks-only (e.g. Adyen MarketPayNotificationService) and omit it;
		// since we strip 3.1 "webhooks", inject an empty Paths Object so the
		// document still validates and its component messages are emitted.
		setKey(doc, "paths", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	}

	// Drop any externalDocs object that lacks the required "url" property.
	// gnostic rejects such objects; they appear both in source specs and as a
	// byproduct of the Swagger 2.0 -> 3.0 conversion (e.g. Azure ServiceBus,
	// whose per-operation externalDocs carry only a description).
	dropInvalidExternalDocs(doc)

	// Tag Objects require a scalar string "description". Some specs (e.g.
	// DigitalOcean's bundled output) leave an unresolved external $ref map there;
	// drop any non-scalar description so gnostic can ingest the tags array.
	if tags := mapValue(doc, "tags"); tags != nil && tags.Kind == yaml.SequenceNode {
		for _, tag := range tags.Content {
			if d := mapValue(tag, "description"); d != nil && d.Kind != yaml.ScalarNode {
				deleteKey(tag, "description")
			}
		}
	}

	// Clean the info object (3.0 fields + x- extensions only).
	if info := mapValue(doc, "info"); info != nil && info.Kind == yaml.MappingNode {
		stripUnknownKeys(info, infoAllowed)
		if lic := mapValue(info, "license"); lic != nil && lic.Kind == yaml.MappingNode {
			stripUnknownKeys(lic, licenseAllowed)
		}
		if contact := mapValue(info, "contact"); contact != nil && contact.Kind == yaml.MappingNode {
			stripUnknownKeys(contact, contactAllowed)
		}
	}

	walkSchemas(doc)
}

// dropInvalidExternalDocs recursively removes any "externalDocs" object that is
// missing its required "url" property, which gnostic's 3.0 model rejects. Values
// under example/default/enum/examples are skipped so user data is never touched.
func dropInvalidExternalDocs(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		if ed := mapValue(n, "externalDocs"); ed != nil &&
			ed.Kind == yaml.MappingNode && !hasKey(ed, "url") {
			deleteKey(n, "externalDocs")
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			switch n.Content[i].Value {
			case "example", "default", "enum", "examples":
				// Raw user data — do not descend.
			default:
				dropInvalidExternalDocs(n.Content[i+1])
			}
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			dropInvalidExternalDocs(c)
		}
	}
}

// documentRoot unwraps a yaml DocumentNode to its first content node.
func documentRoot(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

var infoAllowed = map[string]bool{
	"title": true, "description": true, "termsOfService": true,
	"contact": true, "license": true, "version": true, "summary": true,
}

// rootAllowed is the set of OpenAPI 3.0 root document keys gnostic accepts.
// Top-level 3.1 additions like "webhooks" and "jsonSchemaDialect" are stripped.
var rootAllowed = map[string]bool{
	"openapi": true, "info": true, "servers": true, "paths": true,
	"components": true, "security": true, "tags": true, "externalDocs": true,
}

// pathItemAllowed is the set of OpenAPI 3.0 Path Item keys. Operation verbs are
// included; non-standard siblings (e.g. Meilisearch's stray "request") are
// stripped so the path item validates.
var pathItemAllowed = map[string]bool{
	"$ref": true, "summary": true, "description": true, "servers": true,
	"parameters": true, "get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// operationAllowed is the set of OpenAPI 3.0 Operation keys.
var operationAllowed = map[string]bool{
	"tags": true, "summary": true, "description": true, "externalDocs": true,
	"operationId": true, "parameters": true, "requestBody": true,
	"responses": true, "callbacks": true, "deprecated": true, "security": true,
	"servers": true,
}

var operationVerbs = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

// mediaTypeAllowed is the set of OpenAPI 3.0 Media Type Object keys.
var mediaTypeAllowed = map[string]bool{
	"schema": true, "example": true, "examples": true, "encoding": true,
}

// normalizeMediaType fixes Media Type Objects whose schema keywords were placed
// directly on the media type (instead of under "schema"). It moves any stray
// schema-defining keywords into a nested "schema" object and drops other
// non-media-type siblings, so gnostic can ingest the response/request body.
func normalizeMediaType(mt *yaml.Node) {
	if mt == nil || mt.Kind != yaml.MappingNode || hasKey(mt, "schema") {
		return
	}
	if !looksLikeSchema(mt) {
		return
	}
	var schemaContent []*yaml.Node
	keep := mt.Content[:0]
	for i := 0; i+1 < len(mt.Content); i += 2 {
		k := mt.Content[i].Value
		if mediaTypeAllowed[k] || isExtension(k) {
			keep = append(keep, mt.Content[i], mt.Content[i+1])
			continue
		}
		schemaContent = append(schemaContent, mt.Content[i], mt.Content[i+1])
	}
	mt.Content = keep
	if len(schemaContent) > 0 {
		setKey(mt, "schema", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: schemaContent})
	}
}

// sanitizePathItem strips non-standard sibling keys from a Path Item Object and
// from each Operation Object it contains. A path item whose only verb body is a
// bare $ref to an external file (OpenAPI 3.0 forbids $ref siblings on path
// items, and gnostic cannot resolve external operation refs) has that
// unresolvable verb dropped so the rest of the document still parses.
func sanitizePathItem(item *yaml.Node) {
	if item == nil || item.Kind != yaml.MappingNode {
		return
	}
	stripUnknownKeys(item, pathItemAllowed)
	for _, verb := range operationVerbs {
		op := mapValue(item, verb)
		if op == nil || op.Kind != yaml.MappingNode {
			continue
		}
		// An operation whose body is an external/unresolvable $ref (no inline
		// responses) cannot be ingested by gnostic; drop it.
		if hasKey(op, "$ref") && !hasKey(op, "responses") {
			deleteKey(item, verb)
			continue
		}
		stripUnknownKeys(op, operationAllowed)
		if resp := mapValue(op, "responses"); resp != nil {
			normalizeResponses(resp)
		}
	}
}

// responseRangeKeys maps non-standard response range keys to the OpenAPI 3.0
// canonical uppercase wildcard form gnostic requires (e.g. lowercase "4xx" or
// "4XX"/"4xX" -> "4XX"). Several specs (e.g. Cloudflare) use the lowercase
// form, which gnostic rejects.
var responseRangeKeys = map[string]string{
	"1xx": "1XX", "2xx": "2XX", "3xx": "3XX", "4xx": "4XX", "5xx": "5XX",
}

// validResponseKey reports whether key is a Responses Object key gnostic's
// strict 3.0 model accepts: "default", a three-digit integer status code, or an
// uppercase wildcard range (1XX..5XX).
func validResponseKey(key string) bool {
	if key == "default" {
		return true
	}
	if responseRangeKeys[key] == key { // already canonical uppercase wildcard
		return true
	}
	if len(key) == 3 {
		for i := 0; i < 3; i++ {
			if key[i] < '0' || key[i] > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// normalizeResponses canonicalizes the keys of a Responses Object so gnostic can
// ingest it. Non-standard range keys (lowercase "4xx", mixed case) are folded to
// the uppercase wildcard form ("4XX"); any remaining keys gnostic does not
// recognize are dropped along with their value. x- extensions are preserved.
func normalizeResponses(resp *yaml.Node) {
	if resp == nil || resp.Kind != yaml.MappingNode {
		return
	}
	out := resp.Content[:0]
	for i := 0; i+1 < len(resp.Content); i += 2 {
		keyNode := resp.Content[i]
		key := keyNode.Value
		if isExtension(key) {
			out = append(out, resp.Content[i], resp.Content[i+1])
			continue
		}
		if canon, ok := responseRangeKeys[strings.ToLower(key)]; ok {
			keyNode.Value = canon
			keyNode.Tag = "!!str"
			out = append(out, keyNode, resp.Content[i+1])
			continue
		}
		if validResponseKey(key) {
			// Force string tag so purely numeric codes (e.g. 200) don't render
			// as YAML integers, which gnostic rejects as response keys.
			keyNode.Tag = "!!str"
			out = append(out, keyNode, resp.Content[i+1])
			continue
		}
		// Unknown / non-standard response key gnostic rejects: drop it.
	}
	resp.Content = out
}

// validScalarTypes is the set of OpenAPI 3.0 Schema Object "type" values.
// Any other scalar type (e.g. "any") is non-standard and dropped.
var validScalarTypes = map[string]bool{
	"string": true, "number": true, "integer": true,
	"boolean": true, "array": true, "object": true, "null": true,
}

var licenseAllowed = map[string]bool{"name": true, "url": true}

var contactAllowed = map[string]bool{"name": true, "url": true, "email": true}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// hasKey reports whether a mapping node contains key.
func hasKey(m *yaml.Node, key string) bool {
	return mapValue(m, key) != nil
}

// deleteKey removes the key/value pair for key from a mapping node.
func deleteKey(m *yaml.Node, key string) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

// setKey sets key to value, appending if absent.
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// boolNode and similar helpers build scalar nodes.
func boolNode(b bool) *yaml.Node {
	v := "false"
	if b {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

// stripUnknownKeys removes mapping keys not in allowed and not x- extensions.
func stripUnknownKeys(m *yaml.Node, allowed map[string]bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	out := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i].Value
		if allowed[k] || isExtension(k) {
			out = append(out, m.Content[i], m.Content[i+1])
		}
	}
	m.Content = out
}

func isExtension(k string) bool { return len(k) >= 2 && k[0] == 'x' && k[1] == '-' }

// dropSchemaKeywords are 3.1 / JSON-Schema keywords gnostic's 3.0 model cannot
// represent and that we drop during downconversion.
var dropSchemaKeywords = []string{
	"$schema", "$id", "$anchor", "$dynamicRef", "$dynamicAnchor", "$comment",
	"$defs", "examples", "prefixItems", "propertyNames", "contains",
	"minContains", "maxContains", "patternProperties", "dependentSchemas",
	"dependentRequired", "unevaluatedItems", "unevaluatedProperties",
	"if", "then", "else", "const", "contentEncoding", "contentMediaType",
	"contentSchema",
}

// schemaKeywords is the set of OpenAPI 3.0 Schema Object keywords gnostic
// accepts. Unknown sibling keywords on a confirmed schema (e.g. the stray
// "embed" flag in the ElevenLabs spec) are stripped to keep gnostic happy.
var schemaKeywords = map[string]bool{
	"$ref": true, "type": true, "format": true, "title": true,
	"description": true, "default": true, "example": true, "enum": true,
	"nullable": true, "readOnly": true, "writeOnly": true, "deprecated": true,
	"properties": true, "required": true, "additionalProperties": true,
	"items": true, "allOf": true, "anyOf": true, "oneOf": true, "not": true,
	"discriminator": true, "xml": true, "externalDocs": true,
	"multipleOf": true, "maximum": true, "exclusiveMaximum": true,
	"minimum": true, "exclusiveMinimum": true, "maxLength": true,
	"minLength": true, "pattern": true, "maxItems": true, "minItems": true,
	"uniqueItems": true, "maxProperties": true, "minProperties": true,
}

// walkSchemas recursively descends the document, applying the 3.1 -> 3.0
// schema rewrites everywhere a schema can appear. Schemas nest unpredictably,
// so every mapping is treated as a potential schema; the rewrites are
// idempotent and safe on non-schema mappings. Values under
// example/default/enum/examples are left untouched so user data is never
// mangled, while "properties" values are always treated as schemas regardless
// of their key names.
func walkSchemas(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		downconvertSchema(n)
		stripUnknownSchemaKeywords(n)
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			val := n.Content[i+1]
			switch key {
			case "properties":
				if val.Kind == yaml.MappingNode {
					for j := 1; j < len(val.Content); j += 2 {
						walkSchemas(val.Content[j])
					}
				} else {
					walkSchemas(val)
				}
			case "example", "default", "enum", "examples":
				// Raw user data — do not apply schema rewrites.
			case "securitySchemes":
				// Security Scheme Objects carry a "type" key (http/apiKey/oauth2/
				// openIdConnect) that would otherwise make them look like schemas
				// and get their non-schema keywords (scheme, flows, name, ...)
				// stripped. Skip them entirely.
			case "content":
				// "content" maps media-type strings to Media Type Objects. Some
				// specs place schema keywords (type/properties/...) directly on
				// the media type instead of nesting them under "schema" (e.g.
				// Meilisearch's text/plain "/metrics" response). Lift such stray
				// schema keywords into a proper "schema" subobject, then recurse.
				if val.Kind == yaml.MappingNode {
					for j := 1; j < len(val.Content); j += 2 {
						normalizeMediaType(val.Content[j])
					}
				}
				walkSchemas(val)
			default:
				walkSchemas(val)
			}
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			walkSchemas(c)
		}
	}
}

// stripUnknownSchemaKeywords removes non-standard sibling keywords from a
// mapping that is confidently a schema. It only acts when a schema-defining
// keyword is present, so non-schema objects are left intact. x- extensions are
// always preserved.
func stripUnknownSchemaKeywords(m *yaml.Node) {
	if !looksLikeSchema(m) {
		return
	}
	out := m.Content[:0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i].Value
		if schemaKeywords[k] || isExtension(k) {
			out = append(out, m.Content[i], m.Content[i+1])
		}
	}
	m.Content = out
}

// looksLikeSchema reports whether m carries a schema-defining keyword that
// distinguishes it from arbitrary objects such as path items or media types.
func looksLikeSchema(m *yaml.Node) bool {
	// A "type" key only signals a schema when its value is an actual schema type
	// (string/number/integer/boolean/array/object/null, possibly as a 3.1 type
	// array). A "type" of http/apiKey/oauth2/... denotes a Security Scheme, not a
	// schema, and must not be treated as one.
	if t := mapValue(m, "type"); t != nil {
		if t.Kind == yaml.SequenceNode {
			return true
		}
		if t.Kind == yaml.ScalarNode && validScalarTypes[t.Value] {
			return true
		}
	}
	for _, k := range []string{"properties", "items", "allOf", "anyOf", "oneOf", "enum", "format", "nullable"} {
		if hasKey(m, k) {
			return true
		}
	}
	return false
}

// downconvertSchema applies the in-place 3.1 -> 3.0 rewrites to a single
// mapping that may be a schema. It is conservative: keys that don't apply are
// left untouched.
func downconvertSchema(m *yaml.Node) {
	// Collapse the 3.1 nullable idiom on anyOf/oneOf.
	collapseNullableUnion(m, "anyOf")
	collapseNullableUnion(m, "oneOf")

	// A boolean "required" inside a schema is a JSON-Schema draft-3 / non-standard
	// idiom (e.g. Meilisearch, Zendesk). OpenAPI 3.0 requires "required" to be an
	// array of property names at the object level; a boolean here is invalid, so
	// drop it. (Genuine object-level required arrays are left untouched.)
	if r := mapValue(m, "required"); r != nil && r.Kind == yaml.ScalarNode && r.Tag == "!!bool" {
		deleteKey(m, "required")
	}

	// type handling.
	if t := mapValue(m, "type"); t != nil {
		switch t.Kind {
		case yaml.ScalarNode:
			if t.Value == "null" {
				deleteKey(m, "type")
				setKey(m, "nullable", boolNode(true))
			} else if !validScalarTypes[t.Value] {
				// Non-standard / JSON-Schema scalar types gnostic's 3.0 model
				// rejects (e.g. "any" in Meilisearch). Drop the type so the
				// schema is treated as freeform (google.protobuf.Struct).
				deleteKey(m, "type")
			}
		case yaml.SequenceNode:
			var nonNull []*yaml.Node
			nullable := false
			for _, item := range t.Content {
				if item.Kind == yaml.ScalarNode && item.Value == "null" {
					nullable = true
					continue
				}
				nonNull = append(nonNull, item)
			}
			if nullable {
				setKey(m, "nullable", boolNode(true))
			}
			switch len(nonNull) {
			case 0:
				deleteKey(m, "type")
			case 1:
				setKey(m, "type", nonNull[0])
			default:
				// Multiple real types can't be expressed in 3.0; drop it.
				deleteKey(m, "type")
			}
		}
	}

	// const: X -> enum: [X].
	if c := mapValue(m, "const"); c != nil && !hasKey(m, "enum") {
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{c}}
		setKey(m, "enum", seq)
	}

	// Numeric exclusiveMinimum/Maximum -> 3.0 boolean form.
	convertExclusiveBound(m, "exclusiveMinimum", "minimum")
	convertExclusiveBound(m, "exclusiveMaximum", "maximum")

	// Normalize boolean schemas in schema-valued fields.
	normalizeBooleanSchemaField(m, "items")
	normalizeBooleanSchemaField(m, "not")
	normalizeBooleanSchemaField(m, "contains")

	// $ref with siblings: 3.0 forbids siblings; keep only $ref.
	if ref := mapValue(m, "$ref"); ref != nil && len(m.Content) > 2 {
		m.Content = []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "$ref"}, ref,
		}
		return
	}

	// Drop 3.1-only / JSON-Schema keywords gnostic can't model.
	for _, k := range dropSchemaKeywords {
		deleteKey(m, k)
	}
}

// collapseNullableUnion handles the OpenAPI 3.1 nullable idiom where a schema
// is anyOf/oneOf of a real schema and a null type. When exactly one non-null
// member remains, it is inlined into m and the schema is marked nullable.
func collapseNullableUnion(m *yaml.Node, key string) {
	union := mapValue(m, key)
	if union == nil || union.Kind != yaml.SequenceNode {
		return
	}
	var real []*yaml.Node
	nullable := false
	for _, mem := range union.Content {
		if mem.Kind == yaml.MappingNode && isNullSchema(mem) {
			nullable = true
			continue
		}
		real = append(real, mem)
	}
	if !nullable || len(real) != 1 || real[0].Kind != yaml.MappingNode {
		return
	}
	deleteKey(m, key)
	setKey(m, "nullable", boolNode(true))
	// Inline the single real member's keys without clobbering existing siblings.
	inner := real[0]
	for i := 0; i+1 < len(inner.Content); i += 2 {
		k := inner.Content[i].Value
		if !hasKey(m, k) {
			setKey(m, k, inner.Content[i+1])
		}
	}
}

// isNullSchema reports whether a schema mapping denotes only the null type.
func isNullSchema(m *yaml.Node) bool {
	t := mapValue(m, "type")
	if t == nil {
		return false
	}
	if t.Kind == yaml.ScalarNode {
		return t.Value == "null"
	}
	if t.Kind == yaml.SequenceNode {
		if len(t.Content) == 0 {
			return false
		}
		for _, item := range t.Content {
			if item.Kind != yaml.ScalarNode || item.Value != "null" {
				return false
			}
		}
		return true
	}
	return false
}

// convertExclusiveBound translates a numeric exclusive bound (JSON Schema
// 2020-12 / OpenAPI 3.1) into the OpenAPI 3.0 form: a numeric minimum/maximum
// plus the boolean exclusiveMinimum/exclusiveMaximum flag.
func convertExclusiveBound(m *yaml.Node, exclKey, boundKey string) {
	v := mapValue(m, exclKey)
	if v == nil || v.Kind != yaml.ScalarNode {
		return
	}
	if v.Tag == "!!int" || v.Tag == "!!float" {
		setKey(m, boundKey, &yaml.Node{Kind: yaml.ScalarNode, Tag: v.Tag, Value: v.Value})
		setKey(m, exclKey, boolNode(true))
	}
}

// normalizeBooleanSchemaField replaces a boolean schema (true/false) in a
// schema-valued field with an equivalent object form gnostic accepts.
func normalizeBooleanSchemaField(m *yaml.Node, key string) {
	v := mapValue(m, key)
	if v == nil || v.Kind != yaml.ScalarNode || v.Tag != "!!bool" {
		return
	}
	if v.Value == "true" {
		setKey(m, key, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	} else {
		deleteKey(m, key) // "false" = allow nothing; drop the constraint
	}
}

// sanitizeSwaggerV2 normalizes an OpenAPI 2.0 document so kin-openapi can
// convert it. Swagger 2.0 requires "items" to be a single Schema object, but
// some specs (e.g. Slack) supply an array — typically the JSON-Schema tuple /
// nullable idiom [{realSchema}, {type: "null"}]. Collapse such arrays to their
// single non-null member.
func sanitizeSwaggerV2(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		if items := mapValue(n, "items"); items != nil && items.Kind == yaml.SequenceNode {
			setKey(n, "items", collapseItemsArray(items))
		}
		// kin-openapi's v2->v3 converter aborts on external $refs (those that
		// point at a sibling file rather than "#/definitions/..."). Multi-file
		// Azure specs use these heavily. We cannot resolve the sibling file, so
		// per the partial-conversion policy replace the external $ref with an
		// empty schema, which downstream renders as google.protobuf.Struct.
		if ref := mapValue(n, "$ref"); ref != nil && ref.Kind == yaml.ScalarNode &&
			ref.Value != "" && !strings.HasPrefix(ref.Value, "#") {
			deleteKey(n, "$ref")
		}
		for i := 1; i < len(n.Content); i += 2 {
			sanitizeSwaggerV2(n.Content[i])
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			sanitizeSwaggerV2(c)
		}
	}
}

// collapseItemsArray reduces a tuple-form "items" sequence to a single schema:
// the lone non-null member when present, otherwise the first element.
func collapseItemsArray(items *yaml.Node) *yaml.Node {
	var nonNull []*yaml.Node
	for _, it := range items.Content {
		if it.Kind == yaml.MappingNode && isNullSchema(it) {
			continue
		}
		nonNull = append(nonNull, it)
	}
	if len(nonNull) == 1 {
		return nonNull[0]
	}
	if len(items.Content) > 0 {
		return items.Content[0]
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
}
