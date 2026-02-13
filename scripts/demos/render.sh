#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required to render demos" >&2
  exit 1
fi

mkdir -p docs/demos/assets .artifacts/demos

shopt -s nullglob
tapes=(docs/demos/*.tape)
if [ "${#tapes[@]}" -eq 0 ]; then
  echo "no VHS tapes found in docs/demos" >&2
  exit 1
fi

for tape in "${tapes[@]}"; do
  echo "Rendering ${tape}"
  docker run --rm \
    -v "$ROOT_DIR:/vhs" \
    -w /vhs \
    ghcr.io/charmbracelet/vhs:latest \
    "$tape"
done
