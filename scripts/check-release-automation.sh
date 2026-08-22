#!/usr/bin/env sh
set -eu

repo_root=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

command -v ruby >/dev/null 2>&1 || {
	printf 'ruby not found in PATH (required to parse release workflow YAML and release-please JSON).\n' >&2
	exit 1
}

ruby -e 'require "yaml"; ARGV.each { |path| YAML.load_file(path) }' \
	.github/workflows/release.yml \
	.github/workflows/release-orchestration.yml \
	.github/workflows/release-source-ci.yml \
	.github/workflows/rolling.yml

ruby -rjson -e 'ARGV.each { |path| JSON.parse(File.read(path)) }' \
	release-please-config.json \
	.release-please-manifest.json

for script in \
	scripts/generate-manpage.sh \
	scripts/release-image-tags.sh; do
	if [ ! -f "$script" ]; then
		printf 'Missing release automation script: %s\n' "$script" >&2
		exit 1
	fi
	first_line=$(sed -n "1p" "$script")
	case "$first_line" in
		*"bash"*) shell=bash ;;
		*) shell="sh" ;;
	esac
	if ! command -v "$shell" >/dev/null 2>&1; then
		printf '%s not found in PATH (required to syntax-check %s).\n' "$shell" "$script" >&2
		exit 1
	fi
	"$shell" -n "$script"
done

printf 'Release automation integrity checks passed.\n'
