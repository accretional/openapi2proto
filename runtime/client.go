// Package runtime provides the generic HTTP client used by openapi2proto-generated
// gRPC service handlers. It handles bearer-auth REST calls, error mapping, and
// JSON ↔ proto serialisation. Import it in projects that use generated services.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
)

// Client holds the credentials and transport used for every REST API call.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

// EscapePath percent-escapes a path-parameter value while preserving "/"
// separators. Google (and other) REST APIs frequently put resource names like
// "projects/my-proj/serviceAccounts/x" into a single path position via reserved
// expansion ({+name} in discovery), where the embedded slashes are literal path
// separators and must NOT be encoded to %2F. Each slash-delimited segment is
// escaped individually with url.PathEscape.
func EscapePath(s string) string {
	if !strings.Contains(s, "/") {
		return url.PathEscape(s)
	}
	parts := strings.Split(s, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

type tokenCtxKey struct{}

// WithToken returns a context carrying a per-request bearer token. When set, it
// overrides the Client's configured token for any call made with that context.
// This lets a server forward a caller-supplied token (e.g. from gRPC metadata)
// to the upstream API while falling back to its own service-account token.
func WithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenCtxKey{}, token)
}

// tokenFromContext returns the per-request token set by WithToken, if any.
func tokenFromContext(ctx context.Context) string {
	t, _ := ctx.Value(tokenCtxKey{}).(string)
	return t
}

// New returns a Client that authenticates with the given bearer token against baseURL.
func New(baseURL, token string) *Client {
	return &Client{
		token:   token,
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Do executes a REST call and returns the raw response body, HTTP status code, and any
// transport-level error. path must start with "/" (e.g. "/zones/abc/dns_records").
// query and body may be nil.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, int, error) {
	return c.DoWithHeaders(ctx, method, path, query, body, nil)
}

// DoWithHeaders behaves like Do but additionally sets the given static headers
// on the outgoing request. Use for per-operation headers that aren't part of
// the OpenAPI parameter model, such as a required beta opt-in header.
func (c *Client) DoWithHeaders(ctx context.Context, method, path string, query url.Values, body []byte, headers map[string]string) ([]byte, int, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	// A caller-supplied token on the context (e.g. forwarded from gRPC
	// metadata) takes precedence over the Client's configured token.
	token := c.token
	if t := tokenFromContext(ctx); t != "" {
		token = t
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	return data, resp.StatusCode, nil
}

// apiError models one error entry. Code is left raw because REST APIs are
// inconsistent about its type: some use a numeric error code, others a
// string slug (e.g. "invalid_request_error"), others omit it.
type apiError struct {
	Message string          `json:"message"`
	Code    json.RawMessage `json:"code"`
}

// apiEnvelope covers the handful of error-envelope shapes REST APIs
// commonly use, so StatusError can surface a useful message regardless of
// which convention a given API follows:
//   - {"errors": [{"code":.., "message":..}, ...]}
//   - {"error": {"code":.., "message":..}}
//   - {"message": ".."}
type apiEnvelope struct {
	Errors  []apiError `json:"errors"`
	Error   *apiError  `json:"error"`
	Message string     `json:"message"`
}

// StatusError converts a non-2xx response body and HTTP status into a gRPC status error.
func StatusError(data []byte, httpStatus int) error {
	var env apiEnvelope
	_ = json.Unmarshal(data, &env)

	code := codes.Internal
	switch {
	case httpStatus == 401 || httpStatus == 403:
		code = codes.PermissionDenied
	case httpStatus == 404:
		code = codes.NotFound
	case httpStatus == 429:
		code = codes.ResourceExhausted
	case httpStatus >= 400 && httpStatus < 500:
		code = codes.InvalidArgument
	}

	msg := fmt.Sprintf("api: HTTP %d", httpStatus)
	switch {
	case len(env.Errors) > 0 && env.Errors[0].Message != "":
		msg = fmt.Sprintf("api: %s%s", env.Errors[0].Message, formatErrorCode(env.Errors[0].Code))
	case env.Error != nil && env.Error.Message != "":
		msg = fmt.Sprintf("api: %s%s", env.Error.Message, formatErrorCode(env.Error.Code))
	case env.Message != "":
		msg = fmt.Sprintf("api: %s", env.Message)
	}
	return status.Error(code, msg)
}

// formatErrorCode renders an apiError.Code (string, number, or absent/null)
// as a " (code ...)" suffix, or "" if there's nothing usable to show.
func formatErrorCode(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return ""
		}
		return fmt.Sprintf(" (code %s)", s)
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return fmt.Sprintf(" (code %g)", n)
	}
	return ""
}

// Unmarshal deserialises a JSON API response body into a proto message.
// It strips "errors" and "messages" envelope keys, drops null members and
// duplicate field spellings, normalises slash-delimited JSON keys to
// underscores, coerces freeform JSON objects/arrays into proto string
// fields, and silently ignores unknown fields.
func Unmarshal(data []byte, msg proto.Message) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		delete(raw, "errors")
		delete(raw, "messages")
		if cleaned, err := json.Marshal(raw); err == nil {
			data = cleaned
		}
	}
	data = sanitizeResponseJSON(data)
	data = normalizeSlashKeys(data)
	data = coerceStringFields(data, msg.ProtoReflect().Descriptor())
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, msg)
}

// sanitizeResponseJSON rewrites a REST response body so protojson can accept
// it. Null object members are dropped — protojson rejects null for scalar
// fields, and for proto3 absence means the same thing. Members whose names
// normalise to the same proto field are deduped, preferring the snake_case
// spelling: some APIs (e.g. WorkOS list envelopes) send both "list_metadata"
// and "listMetadata", which protojson treats as the same field appearing
// twice and rejects. Numbers round-trip via json.Number so 64-bit values
// keep their precision.
func sanitizeResponseJSON(data []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return data
	}
	cleaned, changed := sanitizeValue(v)
	if !changed {
		return data
	}
	out, err := json.Marshal(cleaned)
	if err != nil {
		return data
	}
	return out
}

func sanitizeValue(v any) (any, bool) {
	switch node := v.(type) {
	case map[string]any:
		changed := false
		canon := make(map[string]string, len(node)) // canonical form -> kept key
		for k := range node {
			c := canonicalJSONKey(k)
			prior, seen := canon[c]
			if !seen {
				canon[c] = k
				continue
			}
			keep, drop := prior, k
			if strings.Contains(k, "_") && !strings.Contains(prior, "_") {
				keep, drop = k, prior
			}
			canon[c] = keep
			delete(node, drop)
			changed = true
		}
		for k, val := range node {
			if val == nil {
				delete(node, k)
				changed = true
				continue
			}
			if sub, subChanged := sanitizeValue(val); subChanged {
				node[k] = sub
				changed = true
			}
		}
		return node, changed
	case []any:
		changed := false
		for i, item := range node {
			if sub, subChanged := sanitizeValue(item); subChanged {
				node[i] = sub
				changed = true
			}
		}
		return node, changed
	default:
		return v, false
	}
}

func canonicalJSONKey(k string) string {
	return strings.ToLower(strings.ReplaceAll(k, "_", ""))
}

// UnmarshalStruct decodes a JSON API response into a structpb.Struct, suitable
// for a response "body" field of type google.protobuf.Struct (the catch-all body
// type the converter emits when an operation has no typed response schema). An
// empty body yields an empty, non-nil Struct.
func UnmarshalStruct(data []byte) (*structpb.Struct, error) {
	s := &structpb.Struct{}
	if len(data) == 0 {
		return s, nil
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, s); err != nil {
		return nil, err
	}
	return s, nil
}

// MarshalBody serialises a proto message to JSON for use as a request body.
// Returns nil for a nil message.
func MarshalBody(msg proto.Message) ([]byte, error) {
	if msg == nil {
		return nil, nil
	}
	data, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return unquoteIntegerFields(data, msg.ProtoReflect().Descriptor()), nil
}

// isIntegerKind reports whether k is one of protobuf's 64-bit (or fixed-width)
// integer kinds, the kinds protojson encodes as JSON strings by default to
// preserve precision for JavaScript consumers.
func isIntegerKind(k protoreflect.Kind) bool {
	switch k {
	case protoreflect.Int64Kind, protoreflect.Uint64Kind,
		protoreflect.Sint64Kind, protoreflect.Fixed64Kind, protoreflect.Sfixed64Kind:
		return true
	default:
		return false
	}
}

// unquoteIntegerFields walks freshly protojson-marshaled request JSON and
// rewrites 64-bit integer fields from protojson's default quoted-string
// encoding to bare JSON numbers. protojson quotes them per the proto3 JSON
// mapping (to avoid precision loss for JavaScript's float64 numbers), but the
// REST APIs this runtime bridges to overwhelmingly expect a plain JSON
// integer for an integer request field and reject a quoted one.
func unquoteIntegerFields(data []byte, md protoreflect.MessageDescriptor) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data
	}

	changed := false
	for k, v := range raw {
		if len(v) == 0 {
			continue
		}

		fd := md.Fields().ByJSONName(k)
		if fd == nil {
			fd = md.Fields().ByName(protoreflect.Name(k))
		}
		if fd == nil {
			continue
		}

		switch {
		case fd.IsMap():
			if isIntegerKind(fd.MapValue().Kind()) {
				if r, ok := unquoteMapValues(v); ok {
					raw[k] = r
					changed = true
				}
			}

		case isIntegerKind(fd.Kind()) && fd.IsList() && v[0] == '[':
			if r, ok := unquoteArrayElements(v); ok {
				raw[k] = r
				changed = true
			}

		case isIntegerKind(fd.Kind()) && !fd.IsList() && v[0] == '"':
			if s, ok := unquoteJSONString(v); ok {
				raw[k] = json.RawMessage(s)
				changed = true
			}

		case fd.Kind() == protoreflect.MessageKind && !fd.IsList() && v[0] == '{':
			if r := unquoteIntegerFields(v, fd.Message()); !bytes.Equal(r, v) {
				raw[k] = r
				changed = true
			}

		case fd.Kind() == protoreflect.MessageKind && fd.IsList() && v[0] == '[':
			var elems []json.RawMessage
			if err := json.Unmarshal(v, &elems); err != nil {
				continue
			}
			elemChanged := false
			for i, elem := range elems {
				if len(elem) > 0 && elem[0] == '{' {
					if r := unquoteIntegerFields(elem, fd.Message()); !bytes.Equal(r, elem) {
						elems[i] = r
						elemChanged = true
					}
				}
			}
			if elemChanged {
				if reenc, err := json.Marshal(elems); err == nil {
					raw[k] = reenc
					changed = true
				}
			}
		}
	}

	if !changed {
		return data
	}
	result, err := json.Marshal(raw)
	if err != nil {
		return data
	}
	return result
}

// unquoteJSONString extracts the string content of a JSON string literal
// (e.g. `"123"`) and reports whether it looks like a bare JSON integer, safe
// to splice back in unquoted.
func unquoteJSONString(v json.RawMessage) (string, bool) {
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	if s == "" {
		return "", false
	}
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if c < '0' || c > '9' {
			return "", false
		}
	}
	return s, true
}

// unquoteArrayElements applies unquoteJSONString to every element of a JSON
// array of quoted integers.
func unquoteArrayElements(v json.RawMessage) (json.RawMessage, bool) {
	var elems []json.RawMessage
	if err := json.Unmarshal(v, &elems); err != nil {
		return nil, false
	}
	changed := false
	for i, elem := range elems {
		if len(elem) > 0 && elem[0] == '"' {
			if s, ok := unquoteJSONString(elem); ok {
				elems[i] = json.RawMessage(s)
				changed = true
			}
		}
	}
	if !changed {
		return nil, false
	}
	reenc, err := json.Marshal(elems)
	if err != nil {
		return nil, false
	}
	return reenc, true
}

// unquoteMapValues applies unquoteJSONString to every value of a JSON object
// representing a map<string, int64-family> field.
func unquoteMapValues(v json.RawMessage) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(v, &m); err != nil {
		return nil, false
	}
	changed := false
	for k, elem := range m {
		if len(elem) > 0 && elem[0] == '"' {
			if s, ok := unquoteJSONString(elem); ok {
				m[k] = json.RawMessage(s)
				changed = true
			}
		}
	}
	if !changed {
		return nil, false
	}
	reenc, err := json.Marshal(m)
	if err != nil {
		return nil, false
	}
	return reenc, true
}

// MarshalBodyAny marshals any Go value to JSON for use as a request body.
// Use this for repeated or non-proto-message body fields.
func MarshalBodyAny(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	return json.Marshal(v)
}

// UnmarshalInto decodes an API response envelope into any Go value.
// It strips "errors" and "messages" from the top level, extracts the "result"
// field if present, then uses encoding/json to decode into v.
func UnmarshalInto(data []byte, v any) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		delete(raw, "errors")
		delete(raw, "messages")
		if result, ok := raw["result"]; ok {
			return json.Unmarshal(result, v)
		}
		if cleaned, err := json.Marshal(raw); err == nil {
			data = cleaned
		}
	}
	return json.Unmarshal(data, v)
}

// normalizeSlashKeys recursively rewrites JSON object keys that contain "/"
// by replacing every "/" with "_". openapi2proto converts slash-delimited
// annotation keys (e.g. "workers/message") to underscore-separated proto
// field names ("workers_message"), so protojson won't match the raw key
// unless we normalise it first.
func normalizeSlashKeys(data []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data
	}
	changed := false
	normalized := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		newKey := strings.ReplaceAll(k, "/", "_")
		if newKey != k {
			changed = true
		}
		if len(v) > 0 && v[0] == '{' {
			if r := normalizeSlashKeys(v); !bytes.Equal(r, []byte(v)) {
				v = r
				changed = true
			}
		} else if len(v) > 0 && v[0] == '[' {
			var elems []json.RawMessage
			if err := json.Unmarshal(v, &elems); err == nil {
				anyElem := false
				for i, elem := range elems {
					if len(elem) > 0 && elem[0] == '{' {
						if r := normalizeSlashKeys(elem); !bytes.Equal(r, []byte(elem)) {
							elems[i] = r
							anyElem = true
						}
					}
				}
				if anyElem {
					if reenc, err := json.Marshal(elems); err == nil {
						v = reenc
						changed = true
					}
				}
			}
		}
		normalized[newKey] = v
	}
	if !changed {
		return data
	}
	result, err := json.Marshal(normalized)
	if err != nil {
		return data
	}
	return result
}

// coerceStringFields walks JSON data against the proto message descriptor and
// converts any JSON object/array value that corresponds to a proto string field
// into a JSON-encoded string. This handles mismatches where openapi2proto emits
// 'string' for an OpenAPI field typed as a freeform 'object'.
func coerceStringFields(data []byte, md protoreflect.MessageDescriptor) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data
	}

	changed := false
	for k, v := range raw {
		if len(v) == 0 {
			continue
		}
		first := v[0]

		fd := md.Fields().ByJSONName(k)
		if fd == nil {
			fd = md.Fields().ByName(protoreflect.Name(k))
		}
		if fd == nil {
			continue
		}

		switch {
		case fd.Kind() == protoreflect.StringKind && !fd.IsList() && (first == '{' || first == '['):
			s, err := json.Marshal(string(v))
			if err == nil {
				raw[k] = s
				changed = true
			}

		case fd.Kind() == protoreflect.StringKind && fd.IsList() && first == '[':
			var elems []json.RawMessage
			if err := json.Unmarshal(v, &elems); err != nil {
				continue
			}
			elemChanged := false
			for i, elem := range elems {
				if len(elem) > 0 && elem[0] != '"' {
					s, err := json.Marshal(string(elem))
					if err == nil {
						elems[i] = s
						elemChanged = true
					}
				}
			}
			if elemChanged {
				if reenc, err := json.Marshal(elems); err == nil {
					raw[k] = reenc
					changed = true
				}
			}

		case fd.Kind() == protoreflect.MessageKind && !fd.IsList() && first != '{' && first != '[' && first != 'n':
			// Scalar JSON value for a message-typed field. If the message is a
			// wrapper type (single "value" field), wrap the scalar automatically.
			// This handles oneOf/anyOf schemas that collapse to a scalar but
			// still generate a wrapper message (e.g. DnsRecordsTtl).
			if fd.Message().Fields().ByName("value") != nil {
				wrapped, err := json.Marshal(map[string]json.RawMessage{"value": v})
				if err == nil {
					raw[k] = wrapped
					changed = true
				}
			}

		case fd.Kind() == protoreflect.MessageKind && !fd.IsList() && first == '{':
			if coerced := coerceStringFields(v, fd.Message()); !bytes.Equal(coerced, []byte(v)) {
				raw[k] = coerced
				changed = true
			}

		case fd.IsList() && fd.Kind() == protoreflect.MessageKind && first == '[':
			var elems []json.RawMessage
			if err := json.Unmarshal(v, &elems); err != nil {
				continue
			}
			elemChanged := false
			for i, elem := range elems {
				if len(elem) > 0 && elem[0] == '{' {
					if coerced := coerceStringFields(elem, fd.Message()); !bytes.Equal(coerced, []byte(elem)) {
						elems[i] = coerced
						elemChanged = true
					}
				}
			}
			if elemChanged {
				if reenc, err := json.Marshal(elems); err == nil {
					raw[k] = reenc
					changed = true
				}
			}
		}
	}

	if !changed {
		return data
	}
	result, err := json.Marshal(raw)
	if err != nil {
		return data
	}
	return result
}
