#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

required_assets=(
  docs/demos/assets/baseline-gate.gif
  docs/demos/assets/quickstart-top.gif
  docs/demos/assets/single-dependency.gif
)

for asset in "${required_assets[@]}"; do
  if [[ ! -f "$asset" ]]; then
    echo "missing demo asset: $asset" >&2
    echo "run: make demos" >&2
    exit 1
  fi
done

manifest="docs/demos/assets/.sources.sha256"
if [[ ! -f "$manifest" ]]; then
  echo "missing demo source manifest: $manifest" >&2
  echo "run: make demos" >&2
  exit 1
fi

shopt -s nullglob
sources=(scripts/demos/render.sh docs/demos/*.tape docs/demos/fixtures/*)
if [[ "${#sources[@]}" -eq 0 ]]; then
  echo "no demo sources found" >&2
  exit 1
fi

tmp_manifest="$(mktemp)"
trap 'rm -f "$tmp_manifest"' EXIT
printf '%s\n' "${sources[@]}" | LC_ALL=C sort | xargs shasum -a 256 > "$tmp_manifest"

if ! cmp -s "$manifest" "$tmp_manifest"; then
  echo "demo sources changed without refreshed assets" >&2
  echo "run: make demos" >&2
  diff -u "$manifest" "$tmp_manifest" || true
  exit 1
fi

echo "demo assets check passed"
