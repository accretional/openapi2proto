#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

while IFS= read -r proto; do
  protoc \
    --proto_path="${ROOT}" \
    --proto_path="${ROOT}/third_party" \
    --proto_path="${ROOT}/../gnostic/third_party" \
    --descriptor_set_out="/tmp/$(basename "${proto}").pb" \
    "${proto}"
done < <(find "${ROOT}/generated/twilio" -type f -name '*.proto' | sort)
