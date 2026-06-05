package generator

import (
	"strconv"
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
		// Drop stray non-path keys (those not starting with "/" and not x-
		// extensions) that gnostic rejects as invalid Paths Object properties
		// (e.g. WhizzoPay places a duplicate "openapi" key inside "paths").
		kept := paths.Content[:0]
		for i := 0; i+1 < len(paths.Content); i += 2 {
			k := paths.Content[i].Value
			if strings.HasPrefix(k, "/") || isExtension(k) {
				kept = append(kept, paths.Content[i], paths.Content[i+1])
			}
		}
		paths.Content = kept
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

	// Tag Objects require a scalar string "description" and gnostic's strict 3.0
	// model rejects any other key (e.g. the stray "summary" key in the GeoAPI
	// spec). Drop a non-scalar description and strip unknown tag keys.
	if tags := mapValue(doc, "tags"); tags != nil && tags.Kind == yaml.SequenceNode {
		for _, tag := range tags.Content {
			if d := mapValue(tag, "description"); d != nil && d.Kind != yaml.ScalarNode {
				deleteKey(tag, "description")
			}
			stripUnknownKeys(tag, tagAllowed)
		}
	}

	// Clean the info object (3.0 fields + x- extensions only) and repair the
	// required "title"/"version" properties that several real-world specs omit
	// or mistype, which gnostic's strict 3.0 model rejects.
	if info := mapValue(doc, "info"); info != nil && info.Kind == yaml.MappingNode {
		stripUnknownKeys(info, infoAllowed)
		if lic := mapValue(info, "license"); lic != nil && lic.Kind == yaml.MappingNode {
			stripUnknownKeys(lic, licenseAllowed)
			// license.name is required by gnostic's 3.0 model; some specs (e.g.
			// MeldRx) supply only a "url". Inject a placeholder name so the
			// document still validates rather than failing the whole conversion.
			if !hasKey(lic, "name") {
				setKey(lic, "name", scalarStr(""))
			}
		}
		if contact := mapValue(info, "contact"); contact != nil && contact.Kind == yaml.MappingNode {
			stripUnknownKeys(contact, contactAllowed)
		}
		// info.title and info.version are required. Inject placeholders when
		// absent (e.g. Happy Returns, propio-ls omit version) and coerce a
		// non-string version (e.g. prh's YAML float 1.0) to a string, since
		// gnostic rejects a numeric version value.
		if !hasKey(info, "title") {
			setKey(info, "title", scalarStr(""))
		}
		if v := mapValue(info, "version"); v == nil {
			setKey(info, "version", scalarStr("0.0.0"))
		} else if v.Kind == yaml.ScalarNode {
			v.Tag = "!!str"
		}
	} else {
		// gnostic requires an Info Object with title+version; inject a minimal
		// one when the document omits "info" entirely.
		setKey(doc, "info", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			scalarStr("title"), scalarStr(""),
			scalarStr("version"), scalarStr("0.0.0"),
		}})
	}

	// Sanitize reusable components (responses, headers, parameters, security
	// schemes) the same way as their inline counterparts so gnostic accepts them.
	if comp := mapValue(doc, "components"); comp != nil && comp.Kind == yaml.MappingNode {
		sanitizeComponents(comp)
	}

	walkSchemas(doc)
}

// scalarStr builds a string scalar node.
func scalarStr(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// sanitizeComponents repairs the reusable Component Objects gnostic validates
// strictly. Misplaced or non-standard component entries are normalized rather
// than dropped where possible (partial-conversion policy).
func sanitizeComponents(comp *yaml.Node) {
	// components.responses entries are Response Objects: ensure required
	// "description" and lift any 2.0-style "schema" into content.
	if resps := mapValue(comp, "responses"); resps != nil && resps.Kind == yaml.MappingNode {
		for i := 1; i < len(resps.Content); i += 2 {
			normalizeResponseObject(resps.Content[i])
		}
	}
	// components.headers entries are Header Objects. Some specs (e.g. NZXplorer)
	// store a *map of named headers* under a single component header instead of
	// a Header Object; gnostic rejects the header names as unknown keys. Strip
	// any key that isn't a valid Header Object key so the (now possibly empty)
	// header still validates.
	if hdrs := mapValue(comp, "headers"); hdrs != nil && hdrs.Kind == yaml.MappingNode {
		for i := 1; i < len(hdrs.Content); i += 2 {
			h := hdrs.Content[i]
			if h.Kind == yaml.MappingNode && !hasKey(h, "$ref") {
				normalizeParamLikeSchema(h)
				stripUnknownKeys(h, headerAllowed)
			}
		}
	}
	// components.parameters entries are Parameter Objects.
	if params := mapValue(comp, "parameters"); params != nil && params.Kind == yaml.MappingNode {
		for i := 1; i < len(params.Content); i += 2 {
			normalizeParameter(params.Content[i])
		}
	}
	// components.securitySchemes entries are Security Scheme Objects, which
	// gnostic requires to carry a valid "type" (apiKey/http/oauth2/
	// openIdConnect). Repair common mistakes: canonicalize the type case (e.g.
	// JuniperSecurity's "APIKey" -> "apiKey") and drop schemes with no usable
	// type (e.g. FleetHD's empty "Bearer: {}"), which would otherwise fail the
	// whole document.
	if sec := mapValue(comp, "securitySchemes"); sec != nil && sec.Kind == yaml.MappingNode {
		kept := sec.Content[:0]
		for i := 0; i+1 < len(sec.Content); i += 2 {
			scheme := sec.Content[i+1]
			if scheme.Kind == yaml.MappingNode && !hasKey(scheme, "$ref") {
				if t := mapValue(scheme, "type"); t != nil && t.Kind == yaml.ScalarNode {
					if canon, ok := securitySchemeTypes[strings.ToLower(t.Value)]; ok {
						t.Value = canon
					}
				}
				// Drop a scheme whose type isn't a recognized 3.0 value.
				t := mapValue(scheme, "type")
				if t == nil || t.Kind != yaml.ScalarNode || !validSecuritySchemeType[t.Value] {
					continue
				}
			}
			kept = append(kept, sec.Content[i], sec.Content[i+1])
		}
		sec.Content = kept
	}
	// components.schemas entries are Schema Objects by definition. Some specs
	// (e.g. carbon-transparency) misplace a Response Object here, leaving a
	// non-schema "content" sibling that gnostic rejects as an invalid schema.
	// Strip any key that isn't a 3.0 Schema Object keyword (preserving $ref and
	// x- extensions); a now-empty schema renders downstream as a freeform
	// google.protobuf.Struct, matching the existing $ref-fallback behavior.
	if schemas := mapValue(comp, "schemas"); schemas != nil && schemas.Kind == yaml.MappingNode {
		for i := 1; i < len(schemas.Content); i += 2 {
			s := schemas.Content[i]
			if s.Kind != yaml.MappingNode || hasKey(s, "$ref") {
				continue
			}
			out := s.Content[:0]
			for j := 0; j+1 < len(s.Content); j += 2 {
				k := s.Content[j].Value
				if schemaKeywords[k] || isExtension(k) {
					out = append(out, s.Content[j], s.Content[j+1])
				}
			}
			s.Content = out
		}
	}
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

// parameterAllowed is the set of OpenAPI 3.0 Parameter Object keys gnostic
// accepts. Non-standard siblings (e.g. AdButler's "parameter") are stripped.
var parameterAllowed = map[string]bool{
	"name": true, "in": true, "description": true, "required": true,
	"deprecated": true, "allowEmptyValue": true, "style": true, "explode": true,
	"allowReserved": true, "schema": true, "example": true, "examples": true,
	"content": true,
}

// responseAllowed is the set of OpenAPI 3.0 Response Object keys gnostic
// accepts.
var responseAllowed = map[string]bool{
	"description": true, "headers": true, "content": true, "links": true,
}

// headerAllowed is the set of OpenAPI 3.0 Header Object keys gnostic accepts.
var headerAllowed = map[string]bool{
	"description": true, "required": true, "deprecated": true,
	"allowEmptyValue": true, "style": true, "explode": true,
	"allowReserved": true, "schema": true, "example": true, "examples": true,
	"content": true,
}

// tagAllowed is the set of OpenAPI 3.0 Tag Object keys gnostic accepts.
var tagAllowed = map[string]bool{
	"name": true, "description": true, "externalDocs": true,
}

// securitySchemeTypes maps a lowercased Security Scheme "type" to its canonical
// OpenAPI 3.0 spelling so miscased values (e.g. "APIKey") are accepted.
var securitySchemeTypes = map[string]string{
	"apikey": "apiKey", "http": "http", "oauth2": "oauth2",
	"openidconnect": "openIdConnect",
}

// validSecuritySchemeType is the set of canonical 3.0 Security Scheme types.
var validSecuritySchemeType = map[string]bool{
	"apiKey": true, "http": true, "oauth2": true, "openIdConnect": true,
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
	// Path-item-level parameters apply to every operation.
	if params := mapValue(item, "parameters"); params != nil && params.Kind == yaml.SequenceNode {
		for _, p := range params.Content {
			normalizeParameter(p)
		}
	}
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
		// gnostic's Request Body Object requires a "content" property. Some specs
		// (e.g. AdButler) attach an empty requestBody ({}); drop a requestBody
		// that is neither a $ref nor has content so the operation validates.
		if rb := mapValue(op, "requestBody"); rb != nil && rb.Kind == yaml.MappingNode &&
			!hasKey(rb, "content") && !hasKey(rb, "$ref") {
			deleteKey(op, "requestBody")
		}
		if params := mapValue(op, "parameters"); params != nil && params.Kind == yaml.SequenceNode {
			for _, p := range params.Content {
				normalizeParameter(p)
			}
		}
		if resp := mapValue(op, "responses"); resp != nil {
			normalizeResponses(resp)
			// Each Response Object value (after key canonicalization) must carry a
			// "description" and use the 3.0 content form.
			for j := 1; j < len(resp.Content); j += 2 {
				normalizeResponseObject(resp.Content[j])
			}
		} else {
			// gnostic requires every Operation Object to have a "responses"
			// property. Some specs (e.g. HuggingFace, geokon) omit it; inject an
			// empty Responses Object so the operation validates.
			setKey(op, "responses", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
		}
	}
}

// normalizeParameter repairs a single Parameter Object so gnostic's strict 3.0
// model accepts it. Non-standard sibling keys (e.g. AdButler's "parameter")
// are stripped; schema-defining keywords placed directly on the parameter
// (the OpenAPI 2.0 idiom, e.g. "type"/"format"/"nullable" in civicplus,
// eaglealpha, unified) are lifted into a nested "schema" object.
func normalizeParameter(p *yaml.Node) {
	if p == nil || p.Kind != yaml.MappingNode || hasKey(p, "$ref") {
		return
	}
	normalizeParamLikeSchema(p)
	stripUnknownKeys(p, parameterAllowed)
	// gnostic requires a scalar "description"; some specs (e.g.
	// globalforestwatch) supply an array. Drop a non-scalar description.
	if d := mapValue(p, "description"); d != nil && d.Kind != yaml.ScalarNode {
		deleteKey(p, "description")
	}
	// gnostic requires "name" and "in" on every Parameter Object. Some specs
	// (e.g. aviowiki, ChannelAdvisor) omit "in"; default it to "query" so the
	// parameter still validates. A missing "name" is similarly defaulted.
	if !hasKey(p, "in") {
		setKey(p, "in", scalarStr("query"))
	}
	if !hasKey(p, "name") {
		setKey(p, "name", scalarStr("param"))
	}
}

// normalizeParamLikeSchema lifts any schema-defining keyword found directly on a
// Parameter or Header Object into a nested "schema" object (OpenAPI 2.0 placed
// them inline). Existing "schema" objects are left untouched.
func normalizeParamLikeSchema(p *yaml.Node) {
	if hasKey(p, "schema") {
		return
	}
	var schemaContent []*yaml.Node
	keep := p.Content[:0]
	for i := 0; i+1 < len(p.Content); i += 2 {
		k := p.Content[i].Value
		// "collectionFormat" is a 2.0-only keyword with no 3.0 schema equivalent;
		// drop it rather than carry it into the schema.
		if k == "collectionFormat" {
			continue
		}
		if schemaKeywords[k] && k != "$ref" && k != "description" {
			schemaContent = append(schemaContent, p.Content[i], p.Content[i+1])
			continue
		}
		keep = append(keep, p.Content[i], p.Content[i+1])
	}
	p.Content = keep
	if len(schemaContent) > 0 {
		setKey(p, "schema", &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: schemaContent})
	}
}

// normalizeResponseObject repairs a single Response Object: it injects the
// required "description" when absent (e.g. arep, estes-express, pointofrental)
// and lifts a 2.0-style top-level "schema" into the 3.0 content form (e.g.
// hyperhuman, innovation, junipersecurity).
func normalizeResponseObject(r *yaml.Node) {
	if r == nil || r.Kind != yaml.MappingNode || hasKey(r, "$ref") {
		return
	}
	// 2.0-style response: a "schema" sibling instead of "content". Wrap it in an
	// application/json media type so gnostic's 3.0 Response Object accepts it.
	if sch := mapValue(r, "schema"); sch != nil && !hasKey(r, "content") {
		deleteKey(r, "schema")
		mt := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			scalarStr("schema"), sch,
		}}
		content := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			scalarStr("application/json"), mt,
		}}
		setKey(r, "content", content)
	}
	if !hasKey(r, "description") {
		setKey(r, "description", scalarStr(""))
	}
	stripUnknownKeys(r, responseAllowed)
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
func walkSchemas(n *yaml.Node) { walkSchemasNode(n, false) }

// walkSchemasNode is the worker for walkSchemas. The "forced" flag is set when
// the current mapping occupies a position that is *always* a Schema Object
// (a value under "properties", "items", "additionalProperties", a member of
// "allOf"/"anyOf"/"oneOf", or "not"). In those positions non-standard sibling
// keywords are stripped unconditionally — without the looksLikeSchema guard —
// because malformed specs frequently put a 2.0-style "schema"/"example" pair or
// a 3.1 "examples"/"$id"/"$schema" keyword on a schema that carries no other
// schema-defining marker (e.g. jambase's validFrom, pointofrental's $id blocks).
func walkSchemasNode(n *yaml.Node, forced bool) {
	switch n.Kind {
	case yaml.MappingNode:
		downconvertSchema(n)
		if forced {
			stripSchemaKeywordsForced(n)
		} else {
			stripUnknownSchemaKeywords(n)
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			val := n.Content[i+1]
			switch key {
			case "properties":
				if val.Kind == yaml.MappingNode {
					for j := 1; j < len(val.Content); j += 2 {
						walkSchemasNode(val.Content[j], true)
					}
				} else {
					walkSchemasNode(val, false)
				}
			case "items", "additionalProperties", "not":
				// Always-schema positions (additionalProperties may also be a
				// bool, which walkSchemasNode ignores for scalars).
				walkSchemasNode(val, true)
			case "allOf", "anyOf", "oneOf":
				if val.Kind == yaml.SequenceNode {
					for _, m := range val.Content {
						walkSchemasNode(m, true)
					}
				} else {
					walkSchemasNode(val, false)
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
						mt := val.Content[j]
						normalizeMediaType(mt)
						// Strip keys that don't belong on a Media Type Object
						// (e.g. BillionVerify places a Response-level "headers"
						// map on the media type), which gnostic rejects.
						if mt != nil && mt.Kind == yaml.MappingNode {
							stripUnknownKeys(mt, mediaTypeAllowed)
						}
					}
				}
				walkSchemasNode(val, false)
			default:
				walkSchemasNode(val, false)
			}
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			walkSchemasNode(c, false)
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
	stripSchemaKeywordsForced(m)
}

// stripSchemaKeywordsForced removes every key that is not a 3.0 Schema Object
// keyword (or x- extension or $ref) from a mapping known to be a schema.
func stripSchemaKeywordsForced(m *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
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

	// gnostic requires a scalar string "description"; some specs (e.g.
	// carbonmapper) supply an array. Drop a non-scalar description so the schema
	// validates.
	if d := mapValue(m, "description"); d != nil && d.Kind != yaml.ScalarNode {
		deleteKey(m, "description")
	}
	// Likewise "title" must be a scalar string.
	if t := mapValue(m, "title"); t != nil && t.Kind != yaml.ScalarNode {
		deleteKey(m, "title")
	}

	// gnostic requires numeric-valued constraints to be YAML numbers; some specs
	// (e.g. jikan-rest's "score") quote them as strings ("5"), which gnostic
	// rejects. Coerce a quoted numeric scalar to a number, or drop it if it
	// isn't a valid number.
	normalizeNumericKeyword(m, "maximum")
	normalizeNumericKeyword(m, "minimum")
	normalizeNumericKeyword(m, "multipleOf")
	normalizeNumericKeyword(m, "maxLength")
	normalizeNumericKeyword(m, "minLength")
	normalizeNumericKeyword(m, "maxItems")
	normalizeNumericKeyword(m, "minItems")
	normalizeNumericKeyword(m, "maxProperties")
	normalizeNumericKeyword(m, "minProperties")

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

// normalizeNumericKeyword coerces a string-quoted numeric schema constraint to
// a YAML number so gnostic accepts it. A value that is not a valid number is
// dropped (partial-conversion policy). Already-numeric values are left as-is.
func normalizeNumericKeyword(m *yaml.Node, key string) {
	v := mapValue(m, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return
	}
	if v.Tag == "!!int" || v.Tag == "!!float" {
		return
	}
	if _, err := strconv.ParseFloat(v.Value, 64); err == nil {
		if strings.ContainsAny(v.Value, ".eE") {
			v.Tag = "!!float"
		} else {
			v.Tag = "!!int"
		}
		// Clear any quoting style carried over from JSON parsing so the value
		// marshals as a bare number rather than a quoted string.
		v.Style = 0
		return
	}
	deleteKey(m, key)
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

// sanitizeSwaggerV2Doc repairs OpenAPI 2.0 document-level shapes that
// kin-openapi's openapi2conv.ToV3 rejects with a hard error. It operates on the
// document root mapping.
//
// Transformations (partial-conversion policy — each drops/merges minimally):
//   - Operation parameters: at most one "in: body" parameter is kept (the
//     first); extras are dropped. ToV3 aborts on "multiple body parameters"
//     (brla, burlingtonnc, daconoco, norfolk).
//   - Path-item-level parameters: any "in: body" or schema-bearing parameter is
//     dropped. ToV3 aborts with "pathItem must not have a body/schema
//     parameter" (fitbit, netlify-com, opto22-com-groov).
//   - Internal $refs that resolve to nothing (the target definition/response/
//     parameter is absent) are replaced with an empty schema, which downstream
//     renders as google.protobuf.Struct (rexsoftinc references a missing
//     #/definitions/form).
func sanitizeSwaggerV2Doc(doc *yaml.Node) {
	if doc == nil || doc.Kind != yaml.MappingNode {
		return
	}
	stubMissingV2Refs(doc)
	paths := mapValue(doc, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return
	}
	// Drop path entries whose key is an HTTP verb (e.g. Fitbit's "post" path):
	// these are operations mis-promoted to the Paths Object and would otherwise
	// surface their parameters as illegal path-item-level body/schema params.
	keptPaths := paths.Content[:0]
	for i := 0; i+1 < len(paths.Content); i += 2 {
		if verbSet[paths.Content[i].Value] {
			continue
		}
		keptPaths = append(keptPaths, paths.Content[i], paths.Content[i+1])
	}
	paths.Content = keptPaths
	for i := 1; i < len(paths.Content); i += 2 {
		item := paths.Content[i]
		if item.Kind != yaml.MappingNode {
			continue
		}
		// Path-item-level parameters must not be body or schema parameters in
		// the 2.0->3.0 conversion.
		if pp := mapValue(item, "parameters"); pp != nil && pp.Kind == yaml.SequenceNode {
			pp.Content = filterV2PathParams(pp.Content)
		}
		for _, verb := range operationVerbs {
			op := mapValue(item, verb)
			if op == nil || op.Kind != yaml.MappingNode {
				continue
			}
			if params := mapValue(op, "parameters"); params != nil && params.Kind == yaml.SequenceNode {
				params.Content = dedupeV2BodyParams(params.Content)
			}
		}
	}
}

// verbSet is the set of HTTP operation verb names.
var verbSet = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// filterV2PathParams drops parameters that the v2->v3 converter forbids at the
// path-item level: body parameters, schema-bearing parameters, and the 2.0
// "formData"/typed parameters that ToV3 treats as schema parameters.
func filterV2PathParams(params []*yaml.Node) []*yaml.Node {
	out := params[:0]
	for _, p := range params {
		if p.Kind == yaml.MappingNode {
			if in := mapValue(p, "in"); in != nil && (in.Value == "body" || in.Value == "formData") {
				continue
			}
			if hasKey(p, "schema") {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// dedupeV2BodyParams keeps at most the first "in: body" parameter in an
// operation's parameter list and drops any subsequent ones.
func dedupeV2BodyParams(params []*yaml.Node) []*yaml.Node {
	out := params[:0]
	seenBody := false
	for _, p := range params {
		if p.Kind == yaml.MappingNode {
			if in := mapValue(p, "in"); in != nil && in.Value == "body" {
				if seenBody {
					continue
				}
				seenBody = true
			}
		}
		out = append(out, p)
	}
	return out
}

// stubMissingV2Refs replaces internal $refs whose target component is absent
// from the document with an empty schema. ToV3 fails to resolve such dangling
// refs and aborts the whole conversion; stubbing them yields a partial
// conversion instead.
func stubMissingV2Refs(doc *yaml.Node) {
	// Build the set of present internal targets under the 2.0 component sections.
	present := map[string]bool{}
	for _, section := range []string{"definitions", "responses", "parameters"} {
		if sec := mapValue(doc, section); sec != nil && sec.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(sec.Content); j += 2 {
				present["#/"+section+"/"+sec.Content[j].Value] = true
			}
		}
	}
	stubMissingV2RefsWalk(doc, present)
}

func stubMissingV2RefsWalk(n *yaml.Node, present map[string]bool) {
	switch n.Kind {
	case yaml.MappingNode:
		if ref := mapValue(n, "$ref"); ref != nil && ref.Kind == yaml.ScalarNode &&
			strings.HasPrefix(ref.Value, "#/") && !present[ref.Value] {
			// Dangling internal ref: drop it so the node becomes an empty schema.
			deleteKey(n, "$ref")
		}
		for i := 1; i < len(n.Content); i += 2 {
			stubMissingV2RefsWalk(n.Content[i], present)
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			stubMissingV2RefsWalk(c, present)
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
