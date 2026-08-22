#!/usr/bin/env sh
set -eu

repo_root=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

command -v ruby >/dev/null 2>&1 || {
	printf 'ruby not found in PATH (required to parse automation example YAML).\n' >&2
	exit 1
}

example=examples/lefthook.yml
if [ ! -f "$example" ]; then
	printf 'Missing %s checked-in automation example.\n' "$example" >&2
	exit 1
fi

ruby -e 'require "yaml"; ARGV.each { |path| YAML.load_file(path) }' "$example"

for required in \
	'make automation-integrity' \
	'--format json' \
	'git diff --exit-code'; do
	if ! grep -F -- "$required" "$example" >/dev/null; then
		printf '%s must preserve the automation example contract: missing %s\n' "$example" "$required" >&2
		exit 1
	fi
done

printf 'Automation examples preserve JSON and mutation-guard contracts.\n'
