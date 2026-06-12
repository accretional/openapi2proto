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

// enumDiscoveryDoc is a Google Discovery document exercising enum handling:
// a well-formed enum, an enum with no *_UNSPECIFIED value, an enum with
// confusable aliases, and an enum whose value collides with its field name.
const enumDiscoveryDoc = `{
  "kind": "discovery#restDescription",
  "discoveryVersion": "v1",
  "name": "widgets",
  "version": "v1",
  "rootUrl": "https://widgets.googleapis.com/",
  "servicePath": "",
  "schemas": {
    "Widget": {
      "id": "Widget",
      "type": "object",
      "properties": {
        "executionEnvironment": {
          "type": "string",
          "enum": ["EXECUTION_ENVIRONMENT_UNSPECIFIED", "EXECUTION_ENVIRONMENT_GEN1", "EXECUTION_ENVIRONMENT_GEN2"],
          "enumDescriptions": ["unset", "first gen", "second gen"]
        },
        "mode": {"type": "string", "enum": ["READ", "WRITE"]},
        "engine": {"type": "string", "enum": ["ENGINE_MYSQL", "MYSQL"]},
        "approved": {"type": "string", "enum": ["approved", "unapproved"]}
      }
    }
  },
  "methods": {
    "get": {
      "id": "widgets.get",
      "path": "v1/widget",
      "httpMethod": "GET",
      "response": {"$ref": "Widget"}
    }
  }
}`

func TestGenerateEnums(t *testing.T) {
	doc, err := DecodeOpenAPIFromBytes([]byte(enumDiscoveryDoc))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate("widgets.v1", doc, Config{})
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)

	for _, want := range []string{
		// Exact Discovery value names (so protojson sends the right wire string),
		// with the source *_UNSPECIFIED placed at 0 and enumDescriptions as comments.
		"enum ExecutionEnvironment {",
		"EXECUTION_ENVIRONMENT_UNSPECIFIED = 0;",
		"EXECUTION_ENVIRONMENT_GEN1 = 1;",
		"EXECUTION_ENVIRONMENT_GEN2 = 2;",
		"// second gen",
		"ExecutionEnvironment execution_environment = 1;",
		// No *_UNSPECIFIED in source -> synthesize a zero value.
		"enum Mode {",
		"MODE_UNSPECIFIED = 0;",
		"READ = 1;",
		"WRITE = 2;",
		// Confusable aliases (ENGINE_MYSQL / MYSQL) -> field stays string.
		"string engine = 3;",
		// Enum value "approved" collides with field name "approved" -> string.
		"string approved = 4;",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated proto missing %q\n%s", want, text)
		}
	}
	for _, notWant := range []string{"enum Engine", "enum Approved"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("generated proto unexpectedly contains %q (should fall back to string)\n%s", notWant, text)
		}
	}
}

func TestDecodeOpenAPIFromBytesNormalizesJSONSurrogates(t *testing.T) {
	const raw = `{"openapi":"3.0.1","info":{"title":"Emoji","version":"1.0"},"paths":{},"components":{"schemas":{"demo":{"type":"object","properties":{"body":{"type":"string","example":"Hello! \ud83d\udc4d"}}}}}}`
	if _, err := DecodeOpenAPIFromBytes([]byte(raw)); err != nil {
		t.Fatalf("DecodeOpenAPIFromBytes should normalize JSON surrogate escapes: %v", err)
	}
}
