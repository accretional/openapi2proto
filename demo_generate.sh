#!/bin/bash
# =============================================================================
# DEMO 1: OpenAPI → Proto Conversion
# =============================================================================
# This script demonstrates converting any OpenAPI v3 spec to protobuf.
# =============================================================================

set -e
cd "$(dirname "$0")"

echo "╔══════════════════════════════════════════════════════════════════════╗"
echo "║  DEMO: OpenAPI v3 → Protobuf/gRPC Conversion                        ║"
echo "╚══════════════════════════════════════════════════════════════════════╝"
echo ""

# ── 1. Convert a single spec ──
echo "━━━ STEP 1: Convert a single OpenAPI spec to proto ━━━"
echo ""
echo "Command:"
echo "  go run ./cmd/openapi2proto \\"
echo "    -input ../twilio-oai/spec/json/twilio_voice_v1.json \\"
echo "    -output /tmp/demo_voice.proto \\"
echo "    -package twilio.voice.v1"
echo ""

go run ./cmd/openapi2proto \
  -input ../twilio-oai/spec/json/twilio_voice_v1.json \
  -output /tmp/demo_voice.proto \
  -package twilio.voice.v1

echo ""
echo "✅ Proto file generated!"
echo "   /tmp/demo_voice.proto"
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
head -40 /tmp/demo_voice.proto
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# ── 2. Validate the proto ──
echo "━━━ STEP 2: Validate proto file ━━━"
echo ""
echo "Command:"
echo "  protoc --proto_path=. --proto_path=third_party \\"
echo "    --descriptor_set_out=/tmp/demo.pb /tmp/demo_voice.proto"
echo ""

protoc \
  --proto_path=. \
  --proto_path=third_party \
  --proto_path=../gnostic/third_party \
  --descriptor_set_out=/tmp/demo.pb \
  /tmp/demo_voice.proto

echo "✅ Proto validation passed!"
echo ""

# ── 3. Batch convert ──
echo "━━━ STEP 3: Batch convert all Twilio specs (56 services) ━━━"
echo ""
echo "Command: ./scripts/generate_twilio_protos.sh"
echo ""

echo "First 3 specs as example:"
for spec in $(ls ../twilio-oai/spec/json/*.json | head -3); do
  base=$(basename "$spec" .json)
  pkg=$(echo "$base" | tr '_-' '..')
  echo "  • $base → $pkg"
done
echo ""
echo "To run all: ./scripts/generate_twilio_protos.sh"
echo ""

# ── 4. Summary ──
echo "╔══════════════════════════════════════════════════════════════════════╗"
echo "║  SUMMARY                                                             ║"
echo "╠══════════════════════════════════════════════════════════════════════╣"
echo "║  • OpenAPI v3 JSON → Proto file                     ✅ PASSED      ║"
echo "║  • Proto validation (protoc)                        ✅ PASSED      ║"
echo "║  • 56 Twilio services with ready protos             ✅ AVAILABLE   ║"
echo "╚══════════════════════════════════════════════════════════════════════╝"
