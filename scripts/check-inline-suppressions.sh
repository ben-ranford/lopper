#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

requested_base_ref="${SUPPRESSION_BASE:-origin/main}"
marker_prefix="no"
# Build marker names from pieces so this gate does not match its own source.
marker_pattern="(^|[^[:alnum:]_])(${marker_prefix}sec|${marker_prefix}sonar)([^[:alnum:]_]|$)"
diff_scope=""

create_temp_file() {
	local template="${TMPDIR:-/tmp}/inline-suppressions.XXXXXX"
	local temp_file=""

	if temp_file="$(mktemp "$template" 2>/dev/null)"; then
		printf '%s\n' "$temp_file"
		return 0
	fi
	if temp_file="$(mktemp -t inline-suppressions 2>/dev/null)"; then
		printf '%s\n' "$temp_file"
		return 0
	fi

	echo "unable to create temporary file for suppression check" >&2
	return 1
}

if git diff --cached --quiet --exit-code -- .; then
	base_ref="$requested_base_ref"
	used_fallback=0
	if ! git rev-parse --verify -q "$base_ref^{commit}" >/dev/null; then
		echo "Warning: suppression base ref '$base_ref' not found; falling back to 'HEAD~1'. This may miss inline suppressions introduced earlier in this branch." >&2
		base_ref="HEAD~1"
		used_fallback=1
	fi
	if ! git rev-parse --verify -q "$base_ref^{commit}" >/dev/null; then
		echo "No valid suppression base ref found; skipping inline suppression check." >&2
		exit 0
	fi
	if ! base_commit="$(git merge-base "$base_ref" HEAD 2>/dev/null)"; then
		echo "Base ref '$base_ref' is not related to HEAD; skipping inline suppression check." >&2
		exit 0
	fi
	if [[ "$used_fallback" -eq 1 ]]; then
		diff_scope="branch changes vs fallback $base_ref (requested $requested_base_ref)"
	else
		diff_scope="branch changes vs $base_ref"
	fi
	diff_args=(git diff --unified=0 --no-color --diff-filter=AM --relative "$base_commit..HEAD" --)
else
	diff_scope="staged changes"
	diff_args=(git diff --cached --unified=0 --no-color --diff-filter=AM --relative --)
fi

tmp_matches="$(create_temp_file)"
trap 'rm -f "$tmp_matches"' EXIT INT TERM

set +e
"${diff_args[@]}" | awk -v pattern="$marker_pattern" '
BEGIN {
	IGNORECASE = 1
	file = ""
	line = 0
	found = 0
}
/^\+\+\+ b\// {
	file = substr($0, 7)
	next
}
/^@@ / {
	hunk = $0
	sub(/^@@ -[0-9]+(,[0-9]+)? \+/, "", hunk)
	sub(/ .*/, "", hunk)
	split(hunk, parts, ",")
	line = parts[1] + 0
	next
}
/^\+/ && $0 !~ /^\+\+\+/ {
	content = substr($0, 2)
	if (content ~ pattern) {
		printf "%s:%d:%s\n", file, line, content
		found = 1
	}
	line++
	next
}
END {
	exit(found ? 1 : 0)
}
' >"$tmp_matches"
awk_status=$?
set -e

if [[ "$awk_status" -ne 0 ]]; then
	if [[ "$awk_status" -ne 1 || ! -s "$tmp_matches" ]]; then
		exit "$awk_status"
	fi
	echo "Inline suppression markers are not allowed in $diff_scope." >&2
	cat "$tmp_matches" >&2
	echo "Remove the marker and address the underlying issue instead." >&2
	exit 1
fi

echo "Inline suppression check passed ($diff_scope)"
