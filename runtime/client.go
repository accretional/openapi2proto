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
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Client holds the credentials and transport used for every REST API call.
type Client struct {
	token   string
	baseURL string
	http    *http.Client
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
	req.Header.Set("Authorization", "Bearer "+c.token)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
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

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type apiEnvelope struct {
	Success bool       `json:"success"`
	Errors  []apiError `json:"errors"`
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

	msg := fmt.Sprintf("http %d", httpStatus)
	if len(env.Errors) > 0 {
		msg = fmt.Sprintf("%s (code %d)", env.Errors[0].Message, env.Errors[0].Code)
	}
	return status.Error(code, msg)
}

// Unmarshal deserialises a JSON API response body into a proto message.
// It strips "errors" and "messages" envelope keys before parsing and
// silently ignores unknown fields.
func Unmarshal(data []byte, msg proto.Message) error {
	// Strip envelope keys that aren't part of the proto schema.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		delete(raw, "errors")
		delete(raw, "messages")
		if cleaned, err := json.Marshal(raw); err == nil {
			data = cleaned
		}
	}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(data, msg)
}

// MarshalBody serialises a proto message to JSON for use as a request body.
// Returns nil for a nil message.
func MarshalBody(msg proto.Message) ([]byte, error) {
	if msg == nil {
		return nil, nil
	}
	return protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
}
