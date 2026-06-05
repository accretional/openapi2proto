# openapi2proto

Converts any OpenAPI specification into Protocol Buffer (`.proto`) files
with gRPC service definitions and optional Go service stubs that proxy gRPC
calls to the underlying REST API.

Built on Google's [gnostic](https://github.com/google/gnostic) parser.
API-agnostic — works with OpenAPI 2.0, 3.0, and 3.1 specs.

## What it does

- Parses OpenAPI 2.0 (Swagger), 3.0, and 3.1 JSON or YAML specs
- Emits `.proto` files with gRPC service definitions grouped by tag (or as a single service)
- Generates request/response wrapper messages for each HTTP operation
- Lifts component schemas and inline schemas into protobuf messages
- Adds `google.api.http` annotations for REST transcoding
- Optionally generates Go service stubs that implement each RPC as a REST call
  through a shared `runtime.Client`

## Usage

```
go run ./cmd/openapi2proto [flags]
```

### Flags

| Flag | Description | Default |
|------|-------------|---------|
| `-input` | Path to OpenAPI v3 spec (JSON or YAML) | required |
| `-output` | Output `.proto` file path | stdout |
| `-package` | Proto package name | derived from input filename |
| `-go_package` | Go package option (`path;alias`) | derived from package |
| `-grouping` | Service grouping: `tag` or `single` | `tag` |
| `-no_http` | Disable `google.api.http` annotations | false |
| `-service_out` | Output path for generated Go service file | (none) |
| `-go_module` | Go module path of the consuming project | (none) |
| `-runtime_import` | Go import path for the runtime package | `github.com/accretional/openapi2proto/runtime` |

### Examples

Generate a proto file:

```bash
go run ./cmd/openapi2proto \
  -input spec.json \
  -output proto/myapi/v1/myapi.proto \
  -package myapi.v1
```

Generate a proto file with a Go service stub:

```bash
go run ./cmd/openapi2proto \
  -input spec.json \
  -output proto/myapi/v1/myapi.proto \
  -package myapi.v1 \
  -go_package "myapi/v1;myapiv1" \
  -service_out service/myapi/server.go \
  -go_module github.com/myorg/myproject \
  -runtime_import github.com/myorg/myproject/internal/runtime
```

### Runtime package

The `runtime/` directory contains a generic HTTP client (`runtime.Client`) used
by generated Go service stubs. Copy it into your project (e.g. `internal/runtime/`)
and point `-runtime_import` at that path.

## Input formats and sanitization

The tool accepts OpenAPI 2.0, 3.0, and 3.1 in JSON or YAML. Input is parsed
into an order-preserving YAML node tree (so generated proto field numbers stay
stable) and normalized for gnostic's strict OpenAPI 3.0 model before generation:

- **OpenAPI 2.0 (Swagger)** is detected via `swagger: "2.0"` and up-converted
  to OpenAPI v3 in memory using [`kin-openapi`](https://github.com/getkin/kin-openapi)
  (`openapi2conv.ToV3`), then fed through the normal v3 pipeline. Non-standard
  tuple-form `items` arrays (e.g. Slack) are collapsed to a single schema first.
- **OpenAPI 3.1 → 3.0 down-conversion**: `openapi: 3.1.x` is rewritten to
  `3.0.3`; `type: [T, "null"]` (and `type: "null"`) becomes `type: T` +
  `nullable: true`; `anyOf`/`oneOf` of a schema and null collapse to a nullable
  schema; `const` becomes a single-value `enum`; numeric
  `exclusiveMinimum`/`exclusiveMaximum` become the 3.0 boolean form; boolean
  schemas are normalized; and 3.1-only JSON-Schema keywords gnostic can't model
  (`examples`, `prefixItems`, `propertyNames`, `unevaluated*`, `dependent*`,
  `$schema`/`$id`, etc.) are dropped.
- **Non-standard property stripping**: unknown sibling keywords are removed
  where gnostic rejects them — on schemas (e.g. ElevenLabs' stray `embed`
  flag), on `info` (e.g. Square's `info.externalDocs`), and `$ref` siblings.

## Limitations

- External `$ref` targets are not resolved
- `oneOf` and `anyOf` fall back to `google.protobuf.Struct`
- Only the canonical success response is modeled; non-2xx error schemas are not emitted
- Field/property order is preserved for v3 specs; v2 specs lose source order
  during the kin-openapi up-conversion (it uses unordered maps internally)

## Projects using openapi2proto

- [proto-cloudflare](https://github.com/accretional/proto-cloudflare) — gRPC proxy for the Cloudflare API (~220 services)
- [proto-openai](https://github.com/accretional/proto-openai) — gRPC proxy for the OpenAI API (35 services, 242 RPCs)
