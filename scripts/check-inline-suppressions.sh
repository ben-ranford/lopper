#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

requested_base_ref="${SUPPRESSION_BASE:-origin/main}"
marker_no_prefix="no"
marker_ts_prefix="ts"
marker_eslint_prefix="eslint"
marker_coverage_prefix="coverage"
# Build marker names from pieces so this gate does not match its own source.
marker_pattern="(^|[[:space:]])((//|/\\*+|#)[[:space:]]*(@?(${marker_no_prefix}(sec|sonar|lint|qa)|${marker_eslint_prefix}-disable(-next-line|-line)?|${marker_ts_prefix}-(ignore|expect-error)|pragma:[[:space:]]*${marker_no_prefix}[[:space:]]+cover|${marker_coverage_prefix}:[[:space:]]*ignore)))([^[:alnum:]_-]|$)"
source_file_pattern="(^\\.githooks/|.*\\.(go|sh|bash|zsh|ksh|py|rb|php|js|jsx|cjs|mjs|ts|tsx|java|kt|kts|swift|rs|c|cc|cpp|cxx|h|hpp|hh|cs|ya?ml)$)"
diff_scope=""
gh_bin="${GH_BIN:-gh}"
tracking_mode="${SUPPRESSION_TRACKING_MODE:-detect}"
tracking_output="${SUPPRESSION_TRACKING_OUTPUT:-}"

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

trim_value() {
	local value="$1"
	value="${value#"${value%%[![:space:]]*}"}"
	value="${value%"${value##*[![:space:]]}"}"
	printf '%s\n' "$value"
}

extract_metadata_field() {
	local content="$1"
	local key_pattern="$2"
	local value=""

	if [[ "$content" =~ (^|[[:space:];,])(${key_pattern})[[:space:]]*[:=][[:space:]]*([^;]+) ]]; then
		value="$(trim_value "${BASH_REMATCH[3]}")"
	fi
	printf '%s\n' "$value"
}

fingerprint_for_match() {
	local file="$1"
	local content="$2"

	printf '%s\n%s' "$file" "$content" | shasum -a 256 | awk '{ print $1 }'
}

source_url_for_match() {
	local file="$1"
	local line="$2"
	local repo="${SUPPRESSION_GITHUB_REPOSITORY:-${GITHUB_REPOSITORY:-}}"
	local server="${GITHUB_SERVER_URL:-https://github.com}"
	local sha="${GITHUB_SHA:-}"

	if [[ -n "$repo" && -n "$sha" ]]; then
		printf '%s/%s/blob/%s/%s#L%s\n' "$server" "$repo" "$sha" "$file" "$line"
		return 0
	fi

	printf '%s:%s\n' "$file" "$line"
}

write_tracking_body() {
	local body_file="$1"
	local file="$2"
	local line="$3"
	local content="$4"
	local rationale="$5"
	local owner="$6"
	local removal_condition="$7"
	local fingerprint="$8"
	local location_url

	location_url="$(source_url_for_match "$file" "$line")"
	{
		printf '<!-- lopper-inline-suppression:%s -->\n\n' "$fingerprint"
		printf '## Inline analysis suppression tracking\n\n'
		printf '%s\n' "- Location: \`${file}:${line}\`"
		printf '%s\n' "- Source: ${location_url}"
		printf '%s\n' "- Rationale: ${rationale}"
		printf '%s\n' "- Owner: ${owner}"
		printf '%s\n\n' "- Removal condition: ${removal_condition}"
		printf 'Source line:\n\n'
		printf '```text\n'
		printf '%s\n' "$content"
		printf '```\n'
	} >"$body_file"
}

ensure_tracking_issue() {
	local file="$1"
	local line="$2"
	local content="$3"
	local rationale
	local owner
	local removal_condition
	local fingerprint
	local body_file
	local title
	local existing_issue
	local created_issue
	local repo="${SUPPRESSION_GITHUB_REPOSITORY:-${GITHUB_REPOSITORY:-}}"

	rationale="$(extract_metadata_field "$content" "rationale|reason")"
	owner="$(extract_metadata_field "$content" "owner")"
	removal_condition="$(extract_metadata_field "$content" "remove-when|removal-condition|removal")"

	if [[ -z "$rationale" || -z "$owner" || -z "$removal_condition" ]]; then
		echo "Missing inline suppression tracking metadata for ${file}:${line}." >&2
		echo "$content" >&2
		echo "Add same-line metadata: rationale=<why this exception is needed>; owner=<GitHub handle or team>; remove-when=<specific removal condition>." >&2
		return 1
	fi

	if ! command -v "$gh_bin" >/dev/null 2>&1; then
		echo "Unable to track inline suppression ${file}:${line}: GitHub CLI '$gh_bin' was not found." >&2
		echo "Install gh or set GH_BIN; CI must provide GH_TOKEN/GITHUB_TOKEN with issues:write." >&2
		return 1
	fi

	fingerprint="$(fingerprint_for_match "$file" "$content")"
	body_file="$(create_temp_file)"
	write_tracking_body "$body_file" "$file" "$line" "$content" "$rationale" "$owner" "$removal_condition" "$fingerprint"
	title="ci: track inline suppression in ${file}:${line}"

	if [[ -n "$repo" ]]; then
		existing_issue="$("$gh_bin" issue list --repo "$repo" --state open --search "lopper-inline-suppression:${fingerprint}" --json number --jq '.[0].number' 2>/dev/null)" || {
			rm -f "$body_file"
			echo "Unable to search GitHub tracking issues for ${file}:${line}." >&2
			echo "Ensure gh is authenticated and CI grants issues:write; new inline suppressions fail closed when tracking cannot be verified." >&2
			return 1
		}
	else
		existing_issue="$("$gh_bin" issue list --state open --search "lopper-inline-suppression:${fingerprint}" --json number --jq '.[0].number' 2>/dev/null)" || {
			rm -f "$body_file"
			echo "Unable to search GitHub tracking issues for ${file}:${line}." >&2
			echo "Ensure gh is authenticated and CI grants issues:write; new inline suppressions fail closed when tracking cannot be verified." >&2
			return 1
		}
	fi

	if [[ -n "$existing_issue" ]]; then
		if [[ -n "$repo" ]]; then
			"$gh_bin" issue comment "$existing_issue" --repo "$repo" --body-file "$body_file" >/dev/null || {
				rm -f "$body_file"
				echo "Unable to update GitHub tracking issue #${existing_issue} for ${file}:${line}." >&2
				echo "Ensure gh is authenticated and CI grants issues:write; new inline suppressions fail closed when tracking cannot be updated." >&2
				return 1
			}
		else
			"$gh_bin" issue comment "$existing_issue" --body-file "$body_file" >/dev/null || {
				rm -f "$body_file"
				echo "Unable to update GitHub tracking issue #${existing_issue} for ${file}:${line}." >&2
				echo "Ensure gh is authenticated and CI grants issues:write; new inline suppressions fail closed when tracking cannot be updated." >&2
				return 1
			}
		fi
		rm -f "$body_file"
		echo "Updated GitHub tracking issue #${existing_issue} for inline suppression ${file}:${line}."
		return 0
	fi

	if [[ -n "$repo" ]]; then
		created_issue="$("$gh_bin" issue create --repo "$repo" --title "$title" --body-file "$body_file" 2>/dev/null)" || {
			rm -f "$body_file"
			echo "Unable to create GitHub tracking issue for inline suppression ${file}:${line}." >&2
			echo "Ensure gh is authenticated and CI grants issues:write; new inline suppressions fail closed when tracking cannot be created." >&2
			return 1
		}
	else
		created_issue="$("$gh_bin" issue create --title "$title" --body-file "$body_file" 2>/dev/null)" || {
			rm -f "$body_file"
			echo "Unable to create GitHub tracking issue for inline suppression ${file}:${line}." >&2
			echo "Ensure gh is authenticated and CI grants issues:write; new inline suppressions fail closed when tracking cannot be created." >&2
			return 1
		}
	fi

	rm -f "$body_file"
	echo "Opened GitHub tracking issue for inline suppression ${file}:${line}: ${created_issue}"
	return 0
}

json_escape() {
	local value="$1"
	value="${value//\\/\\\\}"
	value="${value//\"/\\\"}"
	value="${value//$'\t'/\\t}"
	value="${value//$'\r'/\\r}"
	value="${value//$'\n'/\\n}"
	printf '%s' "$value"
}

write_tracking_records() {
	local output_file="$1"
	local records_file="$2"
	local output_dir
	local tmp_output
	local seen_file
	local first=1
	local file
	local line
	local content
	local rationale
	local owner
	local removal_condition
	local fingerprint
	local location_url

	output_dir="$(dirname "$output_file")"
	mkdir -p "$output_dir"
	tmp_output="$(create_temp_file)"
	seen_file="$(create_temp_file)"
	: >"$seen_file"

	{
		printf '{\n'
		printf '  "schema": "lopper-inline-suppressions-v1",\n'
		printf '  "suppressions": [\n'
	} >"$tmp_output"

	while IFS=: read -r file line content; do
		rationale="$(extract_metadata_field "$content" "rationale|reason")"
		owner="$(extract_metadata_field "$content" "owner")"
		removal_condition="$(extract_metadata_field "$content" "remove-when|removal-condition|removal")"

		if [[ -z "$rationale" || -z "$owner" || -z "$removal_condition" ]]; then
			rm -f "$tmp_output" "$seen_file"
			echo "Missing inline suppression tracking metadata for ${file}:${line}." >&2
			echo "$content" >&2
			echo "Add same-line metadata: rationale=<why this exception is needed>; owner=<GitHub handle or team>; remove-when=<specific removal condition>." >&2
			return 1
		fi

		fingerprint="$(fingerprint_for_match "$file" "$content")"
		if grep -Fqx "$fingerprint" "$seen_file"; then
			continue
		fi
		printf '%s\n' "$fingerprint" >>"$seen_file"
		location_url="$(source_url_for_match "$file" "$line")"

		if [[ "$first" -eq 0 ]]; then
			printf ',\n' >>"$tmp_output"
		fi
		first=0
		{
			printf '    {\n'
			printf '      "fingerprint": "%s",\n' "$(json_escape "$fingerprint")"
			printf '      "file": "%s",\n' "$(json_escape "$file")"
			printf '      "line": %s,\n' "$line"
			printf '      "source": "%s",\n' "$(json_escape "$location_url")"
			printf '      "content": "%s",\n' "$(json_escape "$content")"
			printf '      "rationale": "%s",\n' "$(json_escape "$rationale")"
			printf '      "owner": "%s",\n' "$(json_escape "$owner")"
			printf '      "remove_when": "%s"\n' "$(json_escape "$removal_condition")"
			printf '    }'
		} >>"$tmp_output"
	done <"$records_file"

	{
		printf '\n'
		printf '  ]\n'
		printf '}\n'
	} >>"$tmp_output"

	mv "$tmp_output" "$output_file"
	rm -f "$seen_file"
}

track_records_with_gh() {
	local records_file="$1"
	local seen_file
	local file
	local line
	local content
	local fingerprint

	seen_file="$(create_temp_file)"
	: >"$seen_file"
	while IFS=: read -r file line content; do
		fingerprint="$(fingerprint_for_match "$file" "$content")"
		if grep -Fqx "$fingerprint" "$seen_file"; then
			continue
		fi
		printf '%s\n' "$fingerprint" >>"$seen_file"
		if ! ensure_tracking_issue "$file" "$line" "$content"; then
			rm -f "$seen_file"
			return 1
		fi
	done <"$records_file"
	rm -f "$seen_file"
}

if ! git diff --cached --quiet --exit-code -- .; then
	diff_scope="staged changes"
	diff_args=(git diff --cached --unified=0 --no-color --diff-filter=AM --relative --)
elif ! git diff --quiet --exit-code -- .; then
	diff_scope="working tree changes"
	diff_args=(git diff --unified=0 --no-color --diff-filter=AM --relative --)
else
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
fi

tmp_matches="$(create_temp_file)"
trap 'rm -f "$tmp_matches"' EXIT INT TERM

set +e
"${diff_args[@]}" | awk -v pattern="$marker_pattern" -v file_pattern="$source_file_pattern" '
BEGIN {
	file = ""
	line = 0
	found = 0
	check_file = 0
}
/^\+\+\+ b\// {
	file = substr($0, 7)
	check_file = (file ~ file_pattern)
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
	# Use POSIX tolower() instead of gawk IGNORECASE so this works with BSD awk and mawk.
	if (check_file && tolower(content) ~ pattern) {
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
	echo "Inline suppression markers require tracking metadata in $diff_scope." >&2
	cat "$tmp_matches" >&2
	echo "Each new suppression must include same-line rationale, owner, and remove-when metadata." >&2
	case "$tracking_mode" in
		detect)
			if [[ -n "$tracking_output" ]]; then
				write_tracking_records "$tracking_output" "$tmp_matches"
			else
				validation_output="$(create_temp_file)"
				write_tracking_records "$validation_output" "$tmp_matches"
				rm -f "$validation_output"
			fi
			echo "Inline suppression metadata passed ($diff_scope)"
			exit 0
			;;
		track)
			track_records_with_gh "$tmp_matches"
			echo "Inline suppression tracking passed ($diff_scope)"
			exit 0
			;;
		*)
			echo "Unsupported SUPPRESSION_TRACKING_MODE '$tracking_mode'; expected 'detect' or 'track'." >&2
			exit 1
			;;
	esac
fi

echo "Inline suppression check passed ($diff_scope)"
