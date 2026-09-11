#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

MANPAGE_DATE="${MANPAGE_DATE:-1970-01-01}"
tmp_path="$(mktemp "${TMPDIR:-/tmp}/lopper-manpage.XXXXXX")"
trap 'rm -f "$tmp_path"' EXIT

MANPAGE_DATE="$MANPAGE_DATE" ./scripts/generate-manpage.sh "$tmp_path"
if ! cmp -s "$tmp_path" docs/man/lopper.1; then
	echo "docs/man/lopper.1 is stale; run 'make manpage'." >&2
	diff -u docs/man/lopper.1 "$tmp_path" || true
	exit 1
fi
