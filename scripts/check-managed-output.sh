#!/usr/bin/env sh
set -eu

repo_root=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

command -v python3 >/dev/null 2>&1 || {
	printf 'python3 not found in PATH (required for managed-output Python syntax checks).\n' >&2
	exit 1
}
command -v node >/dev/null 2>&1 || {
	printf 'node not found in PATH (required for managed-output JavaScript syntax checks).\n' >&2
	exit 1
}
command -v ruby >/dev/null 2>&1 || {
	printf 'ruby not found in PATH (required for managed-output JSON/YAML checks).\n' >&2
	exit 1
}

find scripts .githooks -type f \( -name '*.sh' -o -path '.githooks/*' \) -exec sh -c '
	failed=0
	for script do
		first_line=$(sed -n "1p" "$script")
		case "$first_line" in
			*"bash"*) shell=bash ;;
			*) shell=sh ;;
		esac
		if ! command -v "$shell" >/dev/null 2>&1; then
			printf "%s not found in PATH (required to syntax-check %s).\n" "$shell" "$script" >&2
			failed=1
			continue
		fi
		if ! "$shell" -n "$script"; then
			failed=1
		fi
	done
	exit "$failed"
' sh {} +

python3 - \
	scripts/read_package_version.py \
	scripts/vscode_release_notes.py \
	scripts/runtime/sitecustomize.py <<'PYTHON'
import ast
import pathlib
import sys

for path in sys.argv[1:]:
	ast.parse(pathlib.Path(path).read_text(encoding="utf-8"), filename=path)
PYTHON

node --check scripts/queue_me_controller.js
node --check scripts/runtime/require-hook.cjs
node --check scripts/runtime/loader.mjs

ruby -rjson -e 'ARGV.each { |path| JSON.parse(File.read(path)) }' \
	internal/featureflags/features.json \
	internal/featureflags/release_locks.json \
	renovate.json

ruby -e 'require "yaml"; ARGV.each { |path| YAML.load_file(path) }' \
	.golangci.yml \
	.gostyle.yml \
	action.yml

printf 'Managed output checks passed.\n'
