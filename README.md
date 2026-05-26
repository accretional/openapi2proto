# openapi2proto

Converts any OpenAPI v3 specification into Protocol Buffer (`.proto`) files
with gRPC service definitions and optional Go service stubs that proxy gRPC
calls to the underlying REST API.

Built on Google's [gnostic](https://github.com/google/gnostic) parser.
API-agnostic — works with any OpenAPI v3 spec.

## What it does

- Parses OpenAPI v3 JSON or YAML specs
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

## Limitations

- OpenAPI v3 only (not v2)
- External `$ref` targets are not resolved
- `oneOf` and `anyOf` fall back to `google.protobuf.Struct`
- Only the canonical success response is modeled; non-2xx error schemas are not emitted

## Projects using openapi2proto

- [proto-cloudflare](https://github.com/accretional/proto-cloudflare) — gRPC proxy for the Cloudflare API (~220 services)
- [proto-openai](https://github.com/accretional/proto-openai) — gRPC proxy for the OpenAI API (35 services, 242 RPCs)
