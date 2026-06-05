package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sampleSpec = `
openapi: 3.0.1
info:
  title: Demo API
  version: "1.0"
paths:
  /v1/widgets/{WidgetSid}:
    get:
      operationId: FetchWidget
      tags: [Widget]
      parameters:
        - name: WidgetSid
          in: path
          required: true
          schema:
            type: string
        - name: PageSize
          in: query
          schema:
            type: integer
            format: int32
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/widget'
  /v1/widgets:
    post:
      operationId: CreateWidget
      tags: [Widget]
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              properties:
                FriendlyName:
                  type: string
      responses:
        "201":
          description: created
          content:
            application/json:
              schema:
                type: object
                properties:
                  sid:
                    type: string
components:
  schemas:
    widget:
      type: object
      properties:
        sid:
          type: string
        attributes:
          type: object
          additionalProperties:
            type: string
`

func TestGenerateSampleSpec(t *testing.T) {
	doc, err := DecodeOpenAPIFromBytes([]byte(sampleSpec))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate("demo_api.yaml", doc, Config{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{
		"package demo.api;",
		"service WidgetService",
		"rpc FetchWidget(FetchWidgetRequest) returns (FetchWidgetResponse)",
		"string widget_sid = 1;",
		"int32 page_size = 2;",
		"map<string, string> attributes = 2;",
		"body: \"body\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated proto missing %q\n%s", want, text)
		}
	}
}

func TestGenerateTwilioVoiceSpecCompiles(t *testing.T) {
	specPath := filepath.Join("..", "..", "twilio-oai", "spec", "json", "twilio_voice_v1.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := DecodeOpenAPIFromBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate("twilio_voice_v1.json", doc, Config{})
	if err != nil {
		t.Fatal(err)
	}

	tmp := t.TempDir()
	protoPath := filepath.Join(tmp, "twilio_voice_v1.proto")
	if err := os.WriteFile(protoPath, out, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(
		"protoc",
		"--proto_path="+tmp,
		"--proto_path=third_party",
		"--proto_path=../gnostic/third_party",
		"--descriptor_set_out="+filepath.Join(tmp, "voice.pb"),
		filepath.Base(protoPath),
	)
	cmd.Dir = ".."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("protoc failed: %v\n%s", err, output)
	}
}

func TestDecodeOpenAPIFromBytesNormalizesJSONSurrogates(t *testing.T) {
	const raw = `{"openapi":"3.0.1","info":{"title":"Emoji","version":"1.0"},"paths":{},"components":{"schemas":{"demo":{"type":"object","properties":{"body":{"type":"string","example":"Hello! \ud83d\udc4d"}}}}}}`
	if _, err := DecodeOpenAPIFromBytes([]byte(raw)); err != nil {
		t.Fatalf("DecodeOpenAPIFromBytes should normalize JSON surrogate escapes: %v", err)
	}
}

// TestDecodeOpenAPI31DownConverts exercises the 3.1 -> 3.0 sanitizer: array
// types with null, anyOf-null unions, const, and stray non-standard keywords.
func TestDecodeOpenAPI31DownConverts(t *testing.T) {
	const spec = `
openapi: 3.1.0
info:
  title: V31
  version: "1.0"
  license:
    name: MIT
    identifier: MIT
  externalDocs:
    url: https://example.com
paths:
  /items:
    get:
      operationId: ListItems
      parameters:
        - name: q
          in: query
          schema:
            type: string
            embed: true
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Item'
components:
  schemas:
    Item:
      type: object
      properties:
        id:
          type: [string, "null"]
        kind:
          const: widget
        parent:
          anyOf:
            - $ref: '#/components/schemas/Item'
            - type: "null"
`
	doc, err := DecodeOpenAPIFromBytes([]byte(spec))
	if err != nil {
		t.Fatalf("down-conversion failed: %v", err)
	}
	out, err := Generate("v31.yaml", doc, Config{})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if !strings.Contains(string(out), "service") {
		t.Fatalf("expected a service in output:\n%s", out)
	}
}

// TestDecodeSwaggerV2UpConverts exercises the OpenAPI 2.0 -> v3 path through
// kin-openapi, including the non-standard tuple-form items array.
func TestDecodeSwaggerV2UpConverts(t *testing.T) {
	const spec = `{
  "swagger": "2.0",
  "info": {"title": "V2", "version": "1.0"},
  "basePath": "/api",
  "paths": {
    "/widgets": {
      "get": {
        "operationId": "ListWidgets",
        "responses": {
          "200": {
            "description": "ok",
            "schema": {"$ref": "#/definitions/Widget"}
          }
        }
      }
    }
  },
  "definitions": {
    "Widget": {
      "type": "object",
      "properties": {
        "id": {"type": "string"},
        "latest": {"items": [{"$ref": "#/definitions/Widget"}, {"type": "null"}]}
      }
    }
  }
}`
	doc, err := DecodeOpenAPIFromBytes([]byte(spec))
	if err != nil {
		t.Fatalf("v2 up-conversion failed: %v", err)
	}
	if _, err := Generate("v2.json", doc, Config{}); err != nil {
		t.Fatalf("generate failed: %v", err)
	}
}
