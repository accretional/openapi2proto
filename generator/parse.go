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
	"github.com/google/gnostic/conversions"
	discovery "github.com/google/gnostic/discovery"
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
	// A common malformation in otherwise-JSON specs is a trailing comma before a
	// closing "}"/"]" (e.g. automotive-iq), which both JSON and yaml.v3 reject.
	// Strip such commas first so the document can then flow through the
	// valid-JSON cleanup path below. The transformation is a no-op on
	// well-formed input.
	if !json.Valid(data) {
		data = stripTrailingCommas(data)
	}

	// yaml.v3 (used for order-preserving parsing) rejects the surrogate-pair
	// \uXXXX escapes that JSON specs use for astral-plane characters (e.g.
	// emoji). Decode those to UTF-8 first when the input is JSON.
	if json.Valid(data) {
		data = decodeJSONSurrogates(data)
		// yaml.v3 also rejects raw control characters (C0 controls other than
		// tab/newline/CR, plus the C1 control block U+0080-U+009F) that some
		// JSON specs embed directly in string values (e.g. DocuSign). JSON
		// permits these as raw bytes inside strings, but they carry no meaning
		// for the schema, so strip them. JSON syntax outside strings never
		// contains such bytes, so this is safe to apply to the whole document.
		data = stripJSONControlChars(data)
		// yaml.v3 rejects the JSON-only escaped solidus "\/", which several
		// specs use inside string values (e.g. carapi, openapi-schemas, emburse
		// date/URL fields). In valid JSON the two-byte sequence backslash-slash
		// only ever appears as this escape and always denotes "/", so it is safe
		// to unescape it document-wide before YAML parsing.
		data = unescapeJSONSolidus(data)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}

	// Google Discovery Document (kind: discovery#restDescription): these are
	// what the Google APIs discovery service serves at each API's
	// "discoveryRestUrl" and are not OpenAPI. Convert them to gnostic's
	// OpenAPI v3 model directly via gnostic's discovery converter. Detected by
	// the absence of an "openapi"/"swagger" version key together with a
	// discovery marker.
	if isDiscoveryDocument(documentRoot(&root)) {
		return discoveryToOpenAPIv3(&root)
	}

	// OpenAPI 2.0 (Swagger): up-convert to v3 first.
	if sw := mapValue(documentRoot(&root), "swagger"); sw != nil &&
		len(sw.Value) >= 1 && sw.Value[0] == '2' {
		// Some v2 specs (e.g. Slack) use a non-standard array form for
		// "items"; kin-openapi rejects it. Normalize before conversion.
		sanitizeSwaggerV2(documentRoot(&root))
		// Repair operation/path-item shapes the v2->v3 converter rejects
		// outright (multiple body params, path-item-level body/schema params)
		// and stub internal $refs that point at missing definitions.
		sanitizeSwaggerV2Doc(documentRoot(&root))
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

// isDiscoveryDocument reports whether the parsed document root is a Google API
// Discovery Document rather than an OpenAPI/Swagger spec. Discovery documents
// carry no "openapi" or "swagger" version key; they identify themselves with
// "kind": "discovery#restDescription" and/or a "discoveryVersion" field.
func isDiscoveryDocument(doc *yaml.Node) bool {
	if doc == nil {
		return false
	}
	if mapValue(doc, "openapi") != nil || mapValue(doc, "swagger") != nil {
		return false
	}
	if k := mapValue(doc, "kind"); k != nil && k.Value == "discovery#restDescription" {
		return true
	}
	return mapValue(doc, "discoveryVersion") != nil
}

// discoveryToOpenAPIv3 converts a parsed Google Discovery Document to gnostic's
// OpenAPI v3 document model using gnostic's built-in discovery converter (the
// same conversion exposed by gnostic's "disco --openapi3" command). The
// document is first stripped of keys gnostic's strict discovery model would
// reject (see sanitizeDiscoveryDocument).
func discoveryToOpenAPIv3(root *yaml.Node) (*openapiv3.Document, error) {
	sanitizeDiscoveryDocument(documentRoot(root))
	normalized, err := yaml.Marshal(root)
	if err != nil {
		return nil, err
	}
	doc, err := discovery.ParseDocument(normalized)
	if err != nil {
		return nil, fmt.Errorf("parsing discovery document: %w", err)
	}
	v3, err := conversions.OpenAPIv3(doc)
	if err != nil {
		return nil, fmt.Errorf("converting discovery document to OpenAPI v3: %w", err)
	}
	return v3, nil
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

// stripJSONControlChars removes disallowed control characters from a JSON
// document so the order-preserving YAML parser can read it. It drops C0
// controls (U+0000-U+001F) except tab/newline/CR, the entire C1 control block
// (U+0080-U+009F), and the Unicode noncharacters yaml.v3 also rejects (U+FFFE,
// U+FFFF, and the U+FDD0-U+FDEF block — these appear, e.g., as the upper bound
// of a regex character range in the Emburse spec). These appear only inside
// JSON string values; JSON structural syntax never uses them, so removing them
// cannot corrupt the document structure.
func stripJSONControlChars(data []byte) []byte {
	// Fast path: bail out unless a disallowed byte is present. C1 controls are
	// encoded in UTF-8 as 0xC2 0x80-0x9F; the noncharacters start with 0xEF
	// (U+FDD0-U+FDEF and U+FFFE/U+FFFF all begin with the byte 0xEF in UTF-8).
	needsWork := false
	for _, b := range data {
		if (b < 0x20 && b != '\t' && b != '\n' && b != '\r') || b == 0xC2 || b == 0xEF {
			needsWork = true
			break
		}
	}
	if !needsWork {
		return data
	}
	s := string(data)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		// Drop C0 controls (except tab/newline/CR), the C1 control block, and
		// the Unicode noncharacters yaml.v3 forbids.
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') ||
			(r >= 0x80 && r <= 0x9F) ||
			r == 0xFFFE || r == 0xFFFF ||
			(r >= 0xFDD0 && r <= 0xFDEF) {
			continue
		}
		b.WriteRune(r)
	}
	return []byte(b.String())
}

// unescapeJSONSolidus rewrites the JSON escape sequence "\/" (backslash-solidus)
// to "/" so yaml.v3 — which treats backslash-solidus as an unknown escape and
// aborts — can parse the document. It walks the bytes tracking backslash runs so
// that an escaped backslash followed by a slash (e.g. the "\\/" inside a regex
// pattern, which means backslash+slash, not an escaped solidus) is left intact.
// Only a "\/" preceded by an even number of backslashes is the JSON solidus
// escape and is collapsed to "/".
func unescapeJSONSolidus(data []byte) []byte {
	if !strings.Contains(string(data), `\/`) {
		return data
	}
	out := make([]byte, 0, len(data))
	backslashes := 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c == '\\' {
			backslashes++
			out = append(out, c)
			continue
		}
		if c == '/' && backslashes%2 == 1 {
			// Preceding backslash is an escape introducer for this solidus:
			// drop it and keep just the slash.
			out = out[:len(out)-1]
			out = append(out, '/')
			backslashes = 0
			continue
		}
		backslashes = 0
		out = append(out, c)
	}
	return out
}

// stripTrailingCommas removes commas that immediately precede a closing "}" or
// "]" (ignoring intervening whitespace). Such trailing commas are invalid in
// both JSON and YAML flow collections but appear in some hand-edited specs
// (e.g. automotive-iq). The scan tracks string context so commas inside string
// values are never touched.
func stripTrailingCommas(data []byte) []byte {
	s := string(data)
	if !strings.Contains(s, ",") {
		return data
	}
	out := make([]byte, 0, len(s))
	inStr := false
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(s) { // copy escaped char verbatim
				i++
				out = append(out, s[i])
				continue
			}
			if c == quote {
				inStr = false
			}
			continue
		}
		if c == '"' || c == '\'' {
			inStr = true
			quote = c
			out = append(out, c)
			continue
		}
		if c == ',' {
			// Look ahead past whitespace for a closing bracket.
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue // drop the trailing comma
			}
		}
		out = append(out, c)
	}
	return out
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
