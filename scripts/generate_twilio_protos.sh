#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${ROOT}/generated/twilio"

rm -rf "${OUT_DIR}"
mkdir -p "${OUT_DIR}"

while IFS= read -r spec; do
  base="$(basename "${spec}" .json)"
  pkg="$(printf '%s' "${base}" | tr '_-' '..')"
  rel_dir="$(printf '%s' "${pkg}" | tr '.' '/')"
  mkdir -p "${OUT_DIR}/${rel_dir}"
  go run ./cmd/openapi2proto \
    -input "${spec}" \
    -output "${OUT_DIR}/${rel_dir}/${base}.proto" \
    -package "${pkg}"
done < <(find "${ROOT}/../twilio-oai/spec/json" -maxdepth 1 -type f -name '*.json' | sort)
