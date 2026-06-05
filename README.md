# openapi2proto

**Universal OpenAPI v3 → Protobuf/gRPC Converter**

`openapi2proto` is a **general-purpose** tool that converts any OpenAPI v3 spec to Protocol Buffer (`.proto`) files with gRPC service definitions. Built on Google's `gnostic` parser, it works with **any REST API** — not just Twilio.

## ✨ Works With Any API

- ✅ **Twilio** (56 services, 3000+ messages) — included as examples
- ✅ **Stripe** — Payment APIs
- ✅ **GitHub** — Repository, Issues, PRs
- ✅ **Your API** — Any OpenAPI v3 spec

`openapi2proto` is built around `gnostic`'s OpenAPI parser and is completely **API-agnostic**.

## 🚀 Quick Demo

```bash
# 1. Full demo (proto generation + validation + stats)
./demo.sh

# 2. With live API testing
#    Option A: Create .env file
echo "TWILIO_ACCOUNT_SID=ACxxx" > .env
echo "TWILIO_AUTH_TOKEN=xxx" >> .env
./demo.sh --test

#    Option B: Export directly
export TWILIO_ACCOUNT_SID=ACxxx
export TWILIO_AUTH_TOKEN=xxx
./demo.sh --test

# 3. Just API testing
./demo_test.sh
```

## 📋 Features

- Parses OpenAPI v3 JSON/YAML with `github.com/google/gnostic/openapiv3`
- Emits `.proto` services grouped by first tag or as a single service
- Generates request/response wrapper messages for HTTP operations
- Lifts component schemas and inline schemas into protobuf messages
- Emits `google.api.http` annotations so the generated gRPC surface can be
  transcoded back to REST when needed
- Handles local `#/components/...` refs for schemas, parameters, responses,
  request bodies, and headers

Current limitations:

- OpenAPI v3 only
- External `$ref` targets are not resolved
- `oneOf` and `anyOf` currently fall back to `google.protobuf.Struct`
- Canonical success response only; non-2xx error schemas are not modeled yet

Generate one spec:

```bash
cd openapi2proto
go run ./cmd/openapi2proto \
  -input ../twilio-oai/spec/json/twilio_voice_v1.json \
  -output ./generated/twilio/voice/v1/twilio_voice_v1.proto \
  -package twilio.voice.v1
```

Generate all checked-in Twilio specs:

```bash
cd openapi2proto
./scripts/generate_twilio_protos.sh
```

Validate a generated file:

```bash
protoc \
  --proto_path=. \
  --proto_path=third_party \
  --proto_path=../gnostic/third_party \
  --descriptor_set_out=/tmp/out.pb \
  generated/twilio/voice/v1/twilio_voice_v1.proto
```

Validate every generated Twilio proto:

```bash
cd openapi2proto
./scripts/validate_generated_protos.sh
```
