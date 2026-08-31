#!/usr/bin/env sh
set -eu

repo_root=$(unset CDPATH; cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"

run_check() {
	name=$1
	shift
	printf '==> %s\n' "$name"
	if ! "$@"; then
		printf 'Automation integrity check failed: %s\n' "$name" >&2
		exit 1
	fi
}

run_check "GitHub Actions SHA pinning" ./scripts/check-github-actions-pinning.sh
run_check "GitHub Actions approved runners" ruby scripts/check-github-actions-runners.rb
run_check "automation examples" ./scripts/check-automation-examples.sh
run_check "release automation" sh ./scripts/check-release-automation.sh
run_check "managed output" ./scripts/check-managed-output.sh

printf 'Automation integrity checks passed.\n'
