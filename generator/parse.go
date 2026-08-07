package generator

import (
	"encoding/json"

	openapiv3 "github.com/accretional/openapi2proto/internal/openapiv3"
)

func DecodeOpenAPIFromBytes(data []byte) (*openapiv3.Document, error) {
	// Google API Discovery Documents (kind: discovery#restDescription) are not
	// OpenAPI; convert them via gnostic's discovery converter (see discovery.go).
	if looksLikeDiscovery(data) {
		return discoveryToOpenAPIv3(data)
	}

	// Pre-process JSON to handle constructs the parser can't handle natively.
	// Unlike the old normalize31 (which stripped all 3.1 keywords), this only
	// handles structural patterns the gnostic parsing architecture can't support:
	// - type arrays, null branches, $ref siblings, non-standard vendor keywords.
	// Keywords like const, $recursiveRef, propertyNames, $defs, etc. are now
	// parsed natively by the generated openapiv3 package.
	if json.Valid(data) {
		var raw any
		if err := json.Unmarshal(data, &raw); err == nil {
			normalizeStructural(raw)
			if remarshal, err := json.Marshal(raw); err == nil {
				data = remarshal
			}
		}
	}
	return openapiv3.ParseDocument(data)
}

// normalizeStructural recursively rewrites structural patterns that the
// gnostic parser architecture cannot represent.
func normalizeStructural(v any) {
	switch node := v.(type) {
	case map[string]any:
		normalizeStructuralSchema(node)
		for _, child := range node {
			normalizeStructural(child)
		}
	case []any:
		for _, child := range node {
			normalizeStructural(child)
		}
	}
}

func normalizeStructuralSchema(m map[string]any) {
	// Remove non-standard keywords that are not x-prefixed.
	delete(m, "optional")
	delete(m, "min_items")
	delete(m, "max_items")

	// patternProperties (JSON Schema keyword, not in the OpenAPI 3.0 model):
	// proto has no notion of key-pattern-constrained fields, so fold the
	// pattern schemas into additionalProperties — the object degrades to a
	// map. An explicit additionalProperties schema wins; additionalProperties
	// absent or false takes the single pattern schema (or true when several
	// patterns disagree).
	if pp, ok := m["patternProperties"].(map[string]any); ok {
		delete(m, "patternProperties")
		ap, hasAP := m["additionalProperties"]
		if !hasAP || ap == false {
			if len(pp) == 1 {
				for _, sub := range pp {
					m["additionalProperties"] = sub
				}
			} else {
				m["additionalProperties"] = true
			}
		}
	}

	// type as array (e.g. ["string", "null"]) → pick first non-null type.
	// The parser's TypeItem handles string-or-array, but the schema defines
	// type as a simple string, so array values fail validation.
	if arr, ok := m["type"].([]any); ok {
		var first string
		for _, t := range arr {
			if s, ok := t.(string); ok && s != "null" {
				first = s
				break
			}
		}
		if first == "" {
			first = "string"
		}
		m["type"] = first
	}

	// $ref alongside other properties: the parser treats $ref as exclusive.
	// Strip siblings so the parser can handle it.
	if _, hasRef := m["$ref"]; hasRef {
		for k := range m {
			if k != "$ref" {
				delete(m, k)
			}
		}
		return
	}

	// anyOf / oneOf with a {type: "null"} branch → collapse to non-null branch.
	// The parser doesn't model nullable types; collapsing is the best we can do.
	for _, combo := range []string{"anyOf", "oneOf"} {
		items, ok := m[combo].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		var nonNull []any
		hasNull := false
		for _, item := range items {
			im, ok := item.(map[string]any)
			if ok && im["type"] == "null" {
				hasNull = true
				continue
			}
			nonNull = append(nonNull, item)
		}
		if !hasNull {
			continue
		}
		if len(nonNull) == 1 {
			if replacement, ok := nonNull[0].(map[string]any); ok {
				delete(m, combo)
				for k, v := range replacement {
					m[k] = v
				}
			}
		} else if len(nonNull) == 0 {
			delete(m, combo)
			m["type"] = "string"
		} else {
			m[combo] = nonNull
		}
	}

	// anyOf / oneOf where every inline branch shares the same scalar type
	// collapses to that scalar type directly, instead of degrading into an
	// opaque object below. This is a common OpenAI spec pattern for fields
	// like "model": anyOf[{type: string}, {type: string, enum: [...]}] — a
	// plain string with a suggested enum, not a real heterogeneous union.
	// Branches that are refs, objects, or arrays keep the union genuinely
	// polymorphic, so they're left for the opaque-object fallback.
	for _, combo := range []string{"anyOf", "oneOf"} {
		items, ok := m[combo].([]any)
		if !ok || len(items) == 0 {
			continue
		}
		if _, hasType := m["type"]; hasType {
			continue
		}
		if _, hasProps := m["properties"]; hasProps {
			continue
		}
		sameType, format := "", ""
		homogeneous := true
		for _, item := range items {
			im, ok := item.(map[string]any)
			if !ok {
				homogeneous = false
				break
			}
			if _, hasRef := im["$ref"]; hasRef {
				homogeneous = false
				break
			}
			t, _ := im["type"].(string)
			if t == "" || t == "object" || t == "array" {
				homogeneous = false
				break
			}
			if sameType == "" {
				sameType = t
				if f, ok := im["format"].(string); ok {
					format = f
				}
			} else if t != sameType {
				homogeneous = false
				break
			}
		}
		if homogeneous {
			delete(m, combo)
			m["type"] = sameType
			if format != "" {
				m["format"] = format
			}
		}
	}

	// If anyOf/oneOf remain with no type or properties, the union is genuinely
	// heterogeneous (e.g. string-or-array, or a mix of object shapes) and
	// can't be represented as a typed proto field. Mark it as an opaque JSON
	// value rather than forcing type: object — the branches may resolve to a
	// scalar or array at runtime, not just an object, and google.protobuf.Value
	// (unlike google.protobuf.Struct, which is object-shaped JSON only) can
	// actually round-trip any of them. See isAnyValueSchema in generator.go.
	for _, combo := range []string{"anyOf", "oneOf"} {
		if _, ok := m[combo]; ok {
			if _, hasType := m["type"]; !hasType {
				if _, hasProps := m["properties"]; !hasProps {
					m[anyValueMarker] = true
					delete(m, combo)
				}
			}
		}
	}

	// Object with type + oneOf/anyOf composition: drop the composition.
	if m["type"] == "object" {
		if _, hasProps := m["properties"]; hasProps {
			delete(m, "oneOf")
			delete(m, "anyOf")
		}
	}
}
