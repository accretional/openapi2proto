package generator

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	openapiv3 "github.com/google/gnostic/openapiv3"
	yaml "go.yaml.in/yaml/v3"
)

// DecodeOpenAPIFromBytes parses an OpenAPI specification (JSON or YAML) into
// gnostic's OpenAPI v3 document model.
//
// It accepts three input flavors transparently:
//   - OpenAPI 2.0 (Swagger): up-converted to v3 in memory via kin-openapi.
//   - OpenAPI 3.1.x: down-converted to a gnostic-compatible 3.0 shape.
//   - OpenAPI 3.0.x: sanitized for stray/non-standard properties, then parsed.
//
// The spec is parsed into a yaml.Node tree so key order (and therefore
// generated proto field numbering) is preserved across sanitization.
func DecodeOpenAPIFromBytes(data []byte) (*openapiv3.Document, error) {
	// yaml.v3 (used for order-preserving parsing) rejects the surrogate-pair
	// \uXXXX escapes that JSON specs use for astral-plane characters (e.g.
	// emoji). Decode those to UTF-8 first when the input is JSON.
	if json.Valid(data) {
		data = decodeJSONSurrogates(data)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}

	// OpenAPI 2.0 (Swagger): up-convert to v3 first.
	if sw := mapValue(documentRoot(&root), "swagger"); sw != nil &&
		len(sw.Value) >= 1 && sw.Value[0] == '2' {
		// Some v2 specs (e.g. Slack) use a non-standard array form for
		// "items"; kin-openapi rejects it. Normalize before conversion.
		sanitizeSwaggerV2(documentRoot(&root))
		v3JSON, err := convertSwaggerV2ToV3(&root)
		if err != nil {
			return nil, fmt.Errorf("converting OpenAPI 2.0 to v3: %w", err)
		}
		// Re-parse the converted v3 document so it flows through the same
		// sanitization path as native v3 specs.
		root = yaml.Node{}
		if err := yaml.Unmarshal(v3JSON, &root); err != nil {
			return nil, err
		}
	}

	// Down-convert 3.1 constructs and strip non-standard properties so
	// gnostic's strict 3.0 model can ingest the document.
	sanitizeForGnostic(&root)

	normalized, err := yaml.Marshal(&root)
	if err != nil {
		return nil, err
	}
	return openapiv3.ParseDocument(normalized)
}

// convertSwaggerV2ToV3 converts an OpenAPI 2.0 document (as a yaml.Node) to an
// OpenAPI 3 document, returning the v3 JSON bytes.
func convertSwaggerV2ToV3(root *yaml.Node) ([]byte, error) {
	// kin-openapi's openapi2.T uses JSON struct tags; feed it JSON.
	v2JSON, err := nodeToJSON(root)
	if err != nil {
		return nil, err
	}
	var doc2 openapi2.T
	if err := doc2.UnmarshalJSON(v2JSON); err != nil {
		return nil, fmt.Errorf("parsing swagger 2.0: %w", err)
	}
	doc3, err := openapi2conv.ToV3(&doc2)
	if err != nil {
		return nil, err
	}
	return doc3.MarshalJSON()
}

// decodeJSONSurrogates rewrites JSON \uXXXX\uXXXX surrogate pairs into their
// UTF-8 encoding so a YAML parser (which rejects lone-surrogate escapes) can
// read the document. Byte order is otherwise preserved exactly.
func decodeJSONSurrogates(data []byte) []byte {
	s := string(data)
	if !strings.Contains(s, `\u`) {
		return data
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if i+6 <= len(s) && s[i] == '\\' && s[i+1] == 'u' {
			hi, ok := parseHex4(s[i+2 : i+6])
			if ok && utf16.IsSurrogate(rune(hi)) && i+12 <= len(s) &&
				s[i+6] == '\\' && s[i+7] == 'u' {
				if lo, ok := parseHex4(s[i+8 : i+12]); ok {
					if r := utf16.DecodeRune(rune(hi), rune(lo)); r != unicode.ReplacementChar {
						b.WriteRune(r)
						i += 12
						continue
					}
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return []byte(b.String())
}

func parseHex4(s string) (uint16, bool) {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, false
	}
	return uint16(v), true
}

// nodeToJSON renders a yaml.Node tree to JSON bytes.
func nodeToJSON(root *yaml.Node) ([]byte, error) {
	var v any
	if err := root.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
