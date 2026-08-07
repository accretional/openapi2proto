package runtime

import (
	"encoding/json"
	"testing"
)

func TestSanitizeResponseJSON(t *testing.T) {
	// Mirrors a WorkOS list envelope: the same field spelled both ways plus
	// explicit nulls, and a 64-bit value that must not lose precision.
	in := []byte(`{"object":"list","data":[{"id":"org_1","external_id":null,"big":9007199254740993}],` +
		`"list_metadata":{"before":"org_1","after":null},"listMetadata":{"before":"org_1","after":null}}`)

	out := sanitizeResponseJSON(in)

	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("sanitized output is not valid JSON: %v", err)
	}
	if _, ok := got["listMetadata"]; ok {
		t.Errorf("camelCase duplicate listMetadata should have been dropped: %s", out)
	}
	lm, ok := got["list_metadata"]
	if !ok {
		t.Fatalf("snake_case list_metadata should have been kept: %s", out)
	}
	var lmObj map[string]json.RawMessage
	if err := json.Unmarshal(lm, &lmObj); err != nil {
		t.Fatalf("list_metadata not an object: %v", err)
	}
	if _, ok := lmObj["after"]; ok {
		t.Errorf("null member after should have been dropped: %s", lm)
	}
	var data []map[string]json.RawMessage
	if err := json.Unmarshal(got["data"], &data); err != nil || len(data) != 1 {
		t.Fatalf("data array mangled: %s", out)
	}
	if _, ok := data[0]["external_id"]; ok {
		t.Errorf("null member external_id should have been dropped: %s", out)
	}
	if string(data[0]["big"]) != "9007199254740993" {
		t.Errorf("64-bit number lost precision: got %s", data[0]["big"])
	}
}
