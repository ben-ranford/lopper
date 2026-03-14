#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

GO_BIN="${GO:-go}"
PROFILE_ROOT="${MEM_PROFILE_DIR:-.artifacts/memory-profiles}"
STAMP="${MEM_PROFILE_STAMP:-$(date -u +"%Y%m%dT%H%M%SZ")}"
RUN_DIR="${PROFILE_ROOT%/}/${STAMP}"
LATEST_POINTER="${PROFILE_ROOT%/}/latest-run-dir.txt"
TEST_PATTERN="${MEM_PROFILE_RUN_PATTERN:-Test}"
COUNT="${MEM_PROFILE_COUNT:-1}"
NODECOUNT="${MEM_PROFILE_NODECOUNT:-20}"
PACKAGES_RAW="${MEM_PROFILE_PACKAGES:-./internal/lang/dotnet ./internal/lang/rust ./internal/analysis ./internal/lang/golang}"

orig_goflags="${GOFLAGS:-}"
export GOFLAGS="${orig_goflags:+$orig_goflags }-buildvcs=false"

mkdir -p "$RUN_DIR"

IFS=' ' read -r -a packages <<<"$PACKAGES_RAW"
if [[ "${#packages[@]}" -eq 0 ]]; then
	echo "MEM_PROFILE_PACKAGES must include at least one package" >&2
	exit 1
fi

module_path="$("$GO_BIN" list -m -f '{{.Path}}')"
generated_at="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
summary_index="$RUN_DIR/summary.md"

{
	echo "# Memory Profiling Run"
	echo
	echo "- Generated (UTC): $generated_at"
	echo "- Watched packages: ${packages[*]}"
	echo
	echo "Compare the package summaries in this directory against the latest successful baseline run on \`main\`."
	echo
} >"$summary_index"

for pkg in "${packages[@]}"; do
	import_path="$("$GO_BIN" list "$pkg")"
	rel_path="${import_path#${module_path}/}"
	pprof_pattern="$(printf '%s' "$import_path" | sed -e 's/[][(){}.^$*+?|\\]/\\&/g')"
	slug="$(printf '%s' "$rel_path" | tr '/.' '__')"
	profile_file="$RUN_DIR/${slug}.mem.pprof"
	summary_file="$RUN_DIR/${slug}.alloc-space.txt"
	log_file="$RUN_DIR/${slug}.test.log"

	echo "==> profiling $pkg"
	"$GO_BIN" test "$pkg" -run "$TEST_PATTERN" -count "$COUNT" -memprofile "$profile_file" 2>&1 | tee "$log_file"
	"$GO_BIN" tool pprof -sample_index=alloc_space -nodecount "$NODECOUNT" -focus "$pprof_pattern" -show "$pprof_pattern" -top "$profile_file" >"$summary_file"

	headline="$(awk '/^Showing nodes accounting for/ {print; exit}' "$summary_file")"

	{
		echo "## \`$rel_path\`"
		echo
		echo "- Import path: \`$import_path\`"
		echo "- Profile: \`${profile_file#$ROOT_DIR/}\`"
		echo "- Summary: \`${summary_file#$ROOT_DIR/}\`"
		echo "- Test log: \`${log_file#$ROOT_DIR/}\`"
		if [[ -n "$headline" ]]; then
			echo "- Snapshot: $headline"
		fi
		echo
		echo '```text'
		sed -n '1,18p' "$summary_file"
		echo '```'
		echo
	} >>"$summary_index"
done

printf '%s\n' "$RUN_DIR" >"$LATEST_POINTER"
printf 'Memory profile artifacts written to %s\n' "$RUN_DIR"
