package generator

import (
	"encoding/json"

	openapiv3 "github.com/google/gnostic/openapiv3"
)

func DecodeOpenAPIFromBytes(data []byte) (*openapiv3.Document, error) {
	if json.Valid(data) {
		var normalized any
		if err := json.Unmarshal(data, &normalized); err == nil {
			if remarshal, err := json.Marshal(normalized); err == nil {
				data = remarshal
			}
		}
	}
	return openapiv3.ParseDocument(data)
}
