package generator

import (
	"encoding/json"

	openapiv3 "github.com/google/gnostic/openapiv3"
)

func DecodeOpenAPIFromBytes(data []byte) (*openapiv3.Document, error) {
	if json.Valid(data) {
		var raw any
		if err := json.Unmarshal(data, &raw); err == nil {
			normalize31(raw)
			if remarshal, err := json.Marshal(raw); err == nil {
				data = remarshal
			}
		}
	}
	return openapiv3.ParseDocument(data)
}

// normalize31 recursively patches a decoded OpenAPI document to remove
// OpenAPI 3.1 / JSON Schema 2020-12 constructs that gnostic's openapiv3
// parser does not support. The transformations are lossy but preserve
// enough structure for proto generation.
func normalize31(v any) {
	switch node := v.(type) {
	case map[string]any:
		normalize31Schema(node)
		for _, child := range node {
			normalize31(child)
		}
	case []any:
		for _, child := range node {
			normalize31(child)
		}
	}
}

func normalize31Schema(m map[string]any) {
	// Remove keywords gnostic does not recognise.
	delete(m, "$recursiveAnchor")
	delete(m, "propertyNames")
	delete(m, "optional")
	delete(m, "x-stainless-const")

	// $recursiveRef → treat as opaque object.
	if _, ok := m["$recursiveRef"]; ok {
		delete(m, "$recursiveRef")
		m["type"] = "object"
	}

	// const → single-element enum.
	if val, ok := m["const"]; ok {
		delete(m, "const")
		if _, hasEnum := m["enum"]; !hasEnum {
			m["enum"] = []any{val}
		}
	}

	// type as array (e.g. ["string", "null"]) → pick first non-null type.
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

	// $ref alongside other properties: gnostic treats $ref as exclusive.
	// Strip everything except $ref so gnostic can parse it.
	if _, hasRef := m["$ref"]; hasRef {
		for k := range m {
			if k != "$ref" {
				delete(m, k)
			}
		}
		return
	}

	// anyOf / oneOf with a {type: "null"} branch → collapse nullable.
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
			// Single remaining branch → merge it into this schema.
			if replacement, ok := nonNull[0].(map[string]any); ok {
				delete(m, combo)
				for k, v := range replacement {
					m[k] = v
				}
				// Re-clean: the merged branch may introduce unsupported keys.
				delete(m, "propertyNames")
				delete(m, "$recursiveAnchor")
				delete(m, "optional")
				delete(m, "x-stainless-const")
			}
		} else if len(nonNull) == 0 {
			delete(m, combo)
			m["type"] = "string"
		} else {
			m[combo] = nonNull
		}
	}

	// If anyOf/oneOf remain and there's no type or properties, treat as
	// opaque object so gnostic doesn't reject nested schemas.
	for _, combo := range []string{"anyOf", "oneOf"} {
		if _, ok := m[combo]; ok {
			if _, hasType := m["type"]; !hasType {
				if _, hasProps := m["properties"]; !hasProps {
					m["type"] = "object"
					delete(m, combo)
				}
			}
		}
	}

	// Object with type + oneOf/anyOf that just refines via composition:
	// gnostic handles the properties but chokes on the composition.
	if m["type"] == "object" {
		if _, hasProps := m["properties"]; hasProps {
			delete(m, "oneOf")
			delete(m, "anyOf")
		}
	}

	// Strip non-array min_items / max_items.
	if m["type"] != "array" {
		delete(m, "min_items")
		delete(m, "max_items")
	}
}
