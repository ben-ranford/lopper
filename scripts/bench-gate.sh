#!/bin/sh
set -eu

if [ "${MEMORY_BENCH_BASE+x}" != x ]; then
	MEMORY_BENCH_BASE="origin/main"
fi
if [ "${GO+x}" != x ]; then
	GO="go"
fi
: "${GO_BIN:=}"
if [ "${GO_TOOLCHAIN+x}" != x ]; then
	GO_TOOLCHAIN="go1.27.0"
fi
if [ "${BENCH_COUNT+x}" != x ]; then
	BENCH_COUNT="3"
fi
if [ "${BENCH_TIME+x}" != x ]; then
	BENCH_TIME="200ms"
fi
if [ "${MEMORY_BENCH_PACKAGES+x}" != x ]; then
	MEMORY_BENCH_PACKAGES="./internal/lang/shared ./internal/report"
fi
if [ "${MEMORY_BENCH_MAX_BYTES_PCT+x}" != x ]; then
	MEMORY_BENCH_MAX_BYTES_PCT="15"
fi
if [ "${MEMORY_BENCH_MAX_ALLOCS_PCT+x}" != x ]; then
	MEMORY_BENCH_MAX_ALLOCS_PCT="10"
fi
if [ "${BENCH_BASE_OUTPUT+x}" != x ]; then
	BENCH_BASE_OUTPUT=".artifacts/bench-base.out"
fi
if [ "${BENCH_HEAD_OUTPUT+x}" != x ]; then
	BENCH_HEAD_OUTPUT=".artifacts/bench-head.out"
fi
if [ "${MEMORY_BENCH_SUMMARY+x}" != x ]; then
	MEMORY_BENCH_SUMMARY=".artifacts/memory-bench-summary.md"
fi
if [ "${MEMORY_BENCH_STATUS+x}" != x ]; then
	MEMORY_BENCH_STATUS=".artifacts/memory-bench-status.txt"
fi
if [ "${MEMORY_BENCH_ENFORCE+x}" != x ]; then
	MEMORY_BENCH_ENFORCE="1"
fi
if [ "${GO_TEST_LDFLAGS+x}" != x ]; then
	GO_TEST_LDFLAGS="-X github.com/ben-ranford/lopper/internal/version.buildChannel=${BUILD_CHANNEL:-dev}"
fi

if [ -n "$GO_TEST_LDFLAGS" ]; then
	GO_TEST_LDFLAGS_ARGS="-ldflags \\\"$GO_TEST_LDFLAGS\\\""
else
	GO_TEST_LDFLAGS_ARGS=""
fi

set -eu;
requested_base_ref="$MEMORY_BENCH_BASE";
base_ref="$requested_base_ref";
requested_go_bin="$GO_BIN";
requested_go_toolchain="$GO_TOOLCHAIN";
for artifact_destination in "$BENCH_BASE_OUTPUT" "$BENCH_HEAD_OUTPUT" "$MEMORY_BENCH_SUMMARY" "$MEMORY_BENCH_STATUS"; do
	if [ -z "$artifact_destination" ]; then
		printf "Memory benchmark gate invalid: benchmark artifact destinations must not be empty.\n" >&2;
		exit 2;
	fi;
done
# shellcheck disable=SC2046 # Each dirname is a single, intentionally unquoted path argument.
mkdir -p $(dirname "$BENCH_BASE_OUTPUT") $(dirname "$BENCH_HEAD_OUTPUT") $(dirname "$MEMORY_BENCH_SUMMARY") $(dirname "$MEMORY_BENCH_STATUS");
write_invalid_memory_summary() {
	summary_error="$1";
	printf "## Memory Benchmarks\n\nThresholds: bytes/op <= +%s%%, allocs/op <= +%s%%\n\nBase benchmarks: unavailable\nHead benchmarks: unavailable\n\nInput errors:\n- %s\n\nComparison status: invalid\nResult: benchmark input could not be read for a safe memory comparison.\n" "$MEMORY_BENCH_MAX_BYTES_PCT" "$MEMORY_BENCH_MAX_ALLOCS_PCT" "$summary_error" > "$MEMORY_BENCH_SUMMARY";
	printf "2\n" > "$MEMORY_BENCH_STATUS";
};
fail_invalid_memory_gate() {
	diagnostic="$1";
	summary_error="$diagnostic";
	if [ "$#" -gt 1 ]; then summary_error="$2"; fi;
	printf "Memory benchmark gate invalid: %s\n" "$diagnostic" >&2;
	write_invalid_memory_summary "$summary_error";
	exit 2;
};
run_configured_go_env() {
	configured_go="$1"
	GOTOOLCHAIN="$requested_go_toolchain" sh -c '
		configured_go="$1"
		# GO is a trusted Make command fragment. Parse it in a child shell so quoted
		# executable paths and wrapper arguments survive the environment boundary.
		# shellcheck disable=SC2086 # The configured command intentionally retains shell quoting.
		eval "set -- $configured_go"
		while [ "$#" -gt 0 ]; do
			case "$1" in
				[A-Za-z_]=*|[A-Za-z_][A-Za-z0-9_]*=*) export "$1"; shift ;;
				*) break ;;
			esac
		done
		[ "$#" -gt 0 ] || exit 127
		exec "$@" env GOROOT GOHOSTOS
	' sh "$configured_go"
};
if [ -z "$requested_go_bin" ]; then
	go_env_output="$(run_configured_go_env "$GO" 2>/dev/null || true)";
	derived_go_root="$(printf '%s\n' "$go_env_output" | sed -n '1p')";
	derived_go_host_os="$(printf '%s\n' "$go_env_output" | sed -n '2p')";
	if [ -z "$derived_go_root" ]; then
		fail_invalid_memory_gate "configured GO command could not resolve GOROOT; set GO_BIN explicitly.";
	fi;
	derived_go_exe="";
	if [ "$derived_go_host_os" = "windows" ]; then derived_go_exe=".exe"; fi;
	requested_go_bin="$derived_go_root/bin/go$derived_go_exe";
fi;
resolve_go_bin_reference() {
	candidate="$1";
	case "$candidate" in
		*/*) resolved="$candidate" ;;
		*) resolved="$(command -v "$candidate" 2>/dev/null || true)" ;;
	esac;
	if [ -z "$resolved" ]; then return 1; fi;
	case "$resolved" in
		/*) ;;
		*) resolved="$PWD/$resolved" ;;
	esac;
	if [ ! -e "$resolved" ]; then return 1; fi;
	printf "%s\n" "$resolved";
};
resolve_go_bin() {
	reference_path="$1";
	resolved_go_bin="";
	resolve_go_bin_error="";
	if [ -z "$reference_path" ] || [ ! -e "$reference_path" ]; then return 1; fi;
	target_path="$reference_path";
	case "$target_path" in
		/*) ;;
		*) target_path="$PWD/$target_path" ;;
	esac;
	symlink_depth=0;
	while [ -L "$target_path" ]; do
		link_parent="$(CDPATH='' cd -P "$(dirname "$target_path")" 2>/dev/null && pwd -P)" || {
			resolve_go_bin_error="could not canonicalize symlink parent '$target_path'.";
			return 2;
		};
		link_target="$(readlink "$target_path" 2>/dev/null)" || {
			resolve_go_bin_error="could not read symbolic link '$target_path'.";
			return 2;
		};
		case "$link_target" in
			/*) target_path="$link_target" ;;
			*) target_path="$link_parent/$link_target" ;;
		esac;
		symlink_depth=$((symlink_depth + 1));
		if [ "$symlink_depth" -gt 40 ]; then
			resolve_go_bin_error="exceeded symlink resolution depth for '$reference_path'.";
			return 2;
		fi;
	done;
	canonical_parent="$(CDPATH='' cd -P "$(dirname "$target_path")" 2>/dev/null && pwd -P)" || {
		resolve_go_bin_error="could not canonicalize parent directory '$target_path'.";
		return 2;
	};
	canonical_name="$(basename "$target_path")" || {
		resolve_go_bin_error="could not determine executable name for '$target_path'.";
		return 2;
	};
	resolved_go_bin="$canonical_parent/$canonical_name";
	return 0;
};
fingerprint_go_bin() {
	go_bin="$1";
	cksum "$go_bin" 2>/dev/null;
};
go_bin_reference_path="$(resolve_go_bin_reference "$requested_go_bin" || true)";
if resolve_go_bin "$go_bin_reference_path"; then
	go_bin_path="$resolved_go_bin";
else
	go_bin_status=$?;
	if [ "$go_bin_status" -eq 1 ]; then
		fail_invalid_memory_gate "configured GO_BIN '$requested_go_bin' is missing or not found." "base benchmark input could not be read: configured GO_BIN '$requested_go_bin' is missing or not found.";
	fi;
	fail_invalid_memory_gate "configured GO_BIN '$requested_go_bin' could not be canonicalized: $resolve_go_bin_error" "base benchmark input could not be read: configured GO_BIN '$requested_go_bin' could not be canonicalized: $resolve_go_bin_error";
fi;
if [ ! -x "$go_bin_path" ]; then
	fail_invalid_memory_gate "configured GO_BIN '$go_bin_path' is not executable." "base benchmark input could not be read: configured GO_BIN '$go_bin_path' is not executable.";
fi;
expected_go_bin_fingerprint="$(fingerprint_go_bin "$go_bin_path" || true)";
if [ -z "$expected_go_bin_fingerprint" ]; then
	fail_invalid_memory_gate "configured GO_BIN '$go_bin_path' could not be fingerprinted." "base benchmark input could not be read: configured GO_BIN '$go_bin_path' could not be fingerprinted.";
fi;
validate_go_bin_identity() {
	phase="$1";
	if resolve_go_bin "$go_bin_reference_path"; then
		current_go_bin_path="$resolved_go_bin";
	else
		fail_invalid_memory_gate "configured GO_BIN '$requested_go_bin' disappeared or became invalid before $phase." "base benchmark input could not be read: configured GO_BIN '$requested_go_bin' disappeared or became invalid before $phase.";
	fi;
	if [ "$current_go_bin_path" != "$go_bin_path" ]; then
		fail_invalid_memory_gate "configured GO_BIN '$requested_go_bin' resolved to '$current_go_bin_path' during $phase after validating '$go_bin_path'." "base benchmark input could not be read: configured GO_BIN '$requested_go_bin' was substituted during $phase.";
	fi;
	if [ ! -x "$current_go_bin_path" ]; then
		fail_invalid_memory_gate "configured GO_BIN '$current_go_bin_path' is not executable during $phase." "base benchmark input could not be read: configured GO_BIN '$current_go_bin_path' is not executable during $phase.";
	fi;
	current_go_bin_fingerprint="$(fingerprint_go_bin "$current_go_bin_path" || true)";
	if [ -z "$current_go_bin_fingerprint" ] || [ "$current_go_bin_fingerprint" != "$expected_go_bin_fingerprint" ]; then
		fail_invalid_memory_gate "configured GO_BIN '$current_go_bin_path' changed in place before $phase." "base benchmark input could not be read: configured GO_BIN '$current_go_bin_path' changed in place before $phase.";
	fi;
};
validate_go_bin_identity "Go toolchain discovery";
expected_go_version="$(GOTOOLCHAIN=$requested_go_toolchain "$go_bin_path" env GOVERSION 2>/dev/null || true)";
if [ -z "$expected_go_version" ]; then
	fail_invalid_memory_gate "configured GO_BIN '$go_bin_path' could not resolve GOVERSION for GOTOOLCHAIN='$requested_go_toolchain'." "base benchmark input could not be read: configured GO_BIN '$go_bin_path' could not resolve GOVERSION for GOTOOLCHAIN='$requested_go_toolchain'.";
fi;
validate_go_toolchain() {
	phase="$1";
	validate_go_bin_identity "$phase";
	resolved_go_version="$(GOTOOLCHAIN=$requested_go_toolchain "$go_bin_path" env GOVERSION 2>/dev/null || true)";
	if [ "$resolved_go_version" != "$expected_go_version" ]; then
		fail_invalid_memory_gate "configured GO_BIN '$go_bin_path' reported GOVERSION '$resolved_go_version' during $phase; expected '$expected_go_version'." "base benchmark input could not be read: configured GO_BIN '$go_bin_path' reported GOVERSION '$resolved_go_version' during $phase; expected '$expected_go_version'.";
	fi;
};
run_validated_go() {
	phase="$1";
	shift;
	validate_go_toolchain "$phase";
	GOTOOLCHAIN=$requested_go_toolchain "$go_bin_path" "$@";
};
run_validated_go_test() {
	phase="$1";
	shift;
	if [ -n "$GO_TEST_LDFLAGS" ]; then
		run_validated_go "$phase" test -ldflags "$GO_TEST_LDFLAGS" "$@";
	else
		run_validated_go "$phase" test "$@";
	fi;
};
validate_go_toolchain "initial validation";
go_version_log="$(GOTOOLCHAIN=$requested_go_toolchain "$go_bin_path" version 2>/dev/null || true)";
reported_go_version="${go_version_log#go version }";
if [ "$reported_go_version" = "$go_version_log" ]; then
	reported_go_version="";
else
	reported_go_version="${reported_go_version% *}";
fi;
if [ -z "$go_version_log" ] || [ "$reported_go_version" != "$expected_go_version" ]; then
	fail_invalid_memory_gate "configured GO_BIN '$go_bin_path' reported version '$reported_go_version' for GOTOOLCHAIN='$requested_go_toolchain'; expected '$expected_go_version'." "base benchmark input could not be read: configured GO_BIN '$go_bin_path' reported version '$reported_go_version' for GOTOOLCHAIN='$requested_go_toolchain'; expected '$expected_go_version'.";
fi;
echo "Memory benchmark GO_BIN: $go_bin_path";
echo "Memory benchmark Go toolchain: $expected_go_version";
benchmark_harness_fingerprint() {
	fingerprint_pkg="$1";
	fingerprint_files_tmp=$(mktemp) || return 1;
	fingerprint_manifest_tmp=$(mktemp) || { rm -f "$fingerprint_files_tmp"; return 1; };
	if ! fingerprint_dir=$(GOFLAGS=-buildvcs=false run_validated_go "benchmark harness directory resolution for '$fingerprint_pkg'" list -f '{{.Dir}}' "$fingerprint_pkg" 2>/dev/null); then
		rm -f "$fingerprint_files_tmp" "$fingerprint_manifest_tmp";
		return 1;
	fi;
	if ! GOFLAGS=-buildvcs=false run_validated_go "benchmark harness file resolution for '$fingerprint_pkg'" list -f '{{range .TestGoFiles}}{{printf "test\t%s\n" .}}{{end}}{{range .TestEmbedFiles}}{{printf "test-embed\t%s\n" .}}{{end}}{{range .XTestGoFiles}}{{printf "xtest\t%s\n" .}}{{end}}{{range .XTestEmbedFiles}}{{printf "xtest-embed\t%s\n" .}}{{end}}' "$fingerprint_pkg" > "$fingerprint_files_tmp" 2>/dev/null; then
		rm -f "$fingerprint_files_tmp" "$fingerprint_manifest_tmp";
		return 1;
	fi;
	if ! LC_ALL=C sort -u -o "$fingerprint_files_tmp" "$fingerprint_files_tmp"; then
		rm -f "$fingerprint_files_tmp" "$fingerprint_manifest_tmp";
		return 1;
	fi;
	: > "$fingerprint_manifest_tmp";
	fingerprint_failed=0;
	while IFS=$(printf '\t') read -r fingerprint_kind fingerprint_file; do
		[ -n "$fingerprint_file" ] || continue;
		if ! fingerprint_blob=$(git hash-object -- "$fingerprint_dir/$fingerprint_file" 2>/dev/null); then
			fingerprint_failed=1;
			break;
		fi;
		if ! printf "%s\t%s\t%s\n" "$fingerprint_kind" "$fingerprint_file" "$fingerprint_blob" >> "$fingerprint_manifest_tmp"; then
			fingerprint_failed=1;
			break;
		fi;
	done < "$fingerprint_files_tmp";
	if [ "$fingerprint_failed" -ne 0 ] || ! fingerprint_value=$(git hash-object -- "$fingerprint_manifest_tmp" 2>/dev/null); then
		rm -f "$fingerprint_files_tmp" "$fingerprint_manifest_tmp";
		return 1;
	fi;
	rm -f "$fingerprint_files_tmp" "$fingerprint_manifest_tmp";
	printf "git-hash-object:%s\n" "$fingerprint_value";
};
format_benchmark_definition() {
	invocation_pkg="$1";
	invocation_selection="$2";
	invocation_fingerprint="$3";
	printf "package=%s selection=%s -run '^$' GO_TEST_LDFLAGS_ARGS=%s flags=-benchmem -count=%s -benchtime=%s harness-files=TestGoFiles,TestEmbedFiles,XTestGoFiles,XTestEmbedFiles harness-fingerprint=%s invocation=GOFLAGS=-buildvcs=false GOTOOLCHAIN=%s %s test %s -run '^$' -bench '%s' -benchmem -count=%s -benchtime=%s '%s'" "$invocation_pkg" "$invocation_selection" "$GO_TEST_LDFLAGS_ARGS" "$BENCH_COUNT" "$BENCH_TIME" "$invocation_fingerprint" "$requested_go_toolchain" "$go_bin_path" "$GO_TEST_LDFLAGS_ARGS" "$invocation_selection" "$BENCH_COUNT" "$BENCH_TIME" "$invocation_pkg";
};
if ! base_commit=$(git rev-parse --verify -q --end-of-options "$base_ref^{commit}"); then
	echo "Memory benchmark base ref '$base_ref' is missing or invalid; failing closed.";
	fail_invalid_memory_gate "base benchmark input could not be read: requested base ref '$base_ref' is missing or invalid.";
fi;
if ! git merge-base --is-ancestor "$base_commit" HEAD >/dev/null 2>&1; then
	echo "Memory benchmark base ref '$base_ref' is not an ancestor of HEAD; failing closed.";
	fail_invalid_memory_gate "base benchmark input could not be read: requested base ref '$base_ref' is not an ancestor of HEAD.";
fi;
bench_dir=$(mktemp -d);
base_tree="$bench_dir/base";
base_output_tmp=$(mktemp);
head_output_tmp=$(mktemp);
bench_packages_tmp=$(mktemp);
bench_definitions_tmp=$(mktemp);
# shellcheck disable=SC2317,SC2329 # cleanup is invoked by trap.
cleanup() { (unset GIT_INDEX_FILE; git worktree remove --force "$base_tree" >/dev/null 2>&1 || true); rm -rf "$bench_dir"; rm -f "$base_output_tmp" "$head_output_tmp" "$bench_packages_tmp" "$bench_definitions_tmp"; };
trap cleanup EXIT INT TERM;
echo "Running memory benchmark delta against $base_ref.";
: > "$base_output_tmp";
: > "$head_output_tmp";
: > "$bench_definitions_tmp";
printf "Memory benchmark GO_BIN: %s\nMemory benchmark Go toolchain: %s\n" "$go_bin_path" "$expected_go_version" >> "$base_output_tmp";
printf "Memory benchmark GO_BIN: %s\nMemory benchmark Go toolchain: %s\n" "$go_bin_path" "$expected_go_version" >> "$head_output_tmp";
if [ -z "$MEMORY_BENCH_PACKAGES" ]; then
	fail_invalid_memory_gate "configured MEMORY_BENCH_PACKAGES must not be empty.";
fi;
# shellcheck disable=SC2086 # MEMORY_BENCH_PACKAGES is a space-delimited package list.
if ! GOFLAGS=-buildvcs=false run_validated_go "head benchmark package resolution" list $MEMORY_BENCH_PACKAGES > "$bench_packages_tmp" 2>&1; then
	cat "$bench_packages_tmp";
	fail_invalid_memory_gate "head benchmark package targets could not be resolved.";
fi;
LC_ALL=C sort -u -o "$bench_packages_tmp" "$bench_packages_tmp";
while IFS= read -r bench_pkg; do
	[ -n "$bench_pkg" ] || continue;
	pkg_bench_tmp=$(mktemp);
	pkg_names_tmp=$(mktemp);
	if ! GOFLAGS=-buildvcs=false run_validated_go_test "head benchmark discovery for '$bench_pkg'" -run '^$' -list '^Benchmark' "$bench_pkg" > "$pkg_bench_tmp" 2>&1; then
		cat "$pkg_bench_tmp";
		rm -f "$pkg_bench_tmp" "$pkg_names_tmp";
		fail_invalid_memory_gate "head benchmark definition could not be resolved from package '$bench_pkg'.";
	fi;
	awk '/^Benchmark/ { print $1 }' "$pkg_bench_tmp" | LC_ALL=C sort -u > "$pkg_names_tmp";
	if [ ! -s "$pkg_names_tmp" ]; then
		rm -f "$pkg_bench_tmp" "$pkg_names_tmp";
		fail_invalid_memory_gate "configured head benchmark package target '$bench_pkg' resolved zero selected benchmarks.";
	fi;
	if ! harness_fingerprint=$(benchmark_harness_fingerprint "$bench_pkg"); then
		rm -f "$pkg_bench_tmp" "$pkg_names_tmp";
		fail_invalid_memory_gate "head benchmark harness fingerprint could not be resolved from package '$bench_pkg'.";
	fi;
	bench_selection=$(awk 'BEGIN { separator = "^(" } { printf "%s%s", separator, $1; separator = "|" } END { print ")$" }' "$pkg_names_tmp");
	printf "%s\t%s\t%s\n" "$bench_pkg" "$bench_selection" "$harness_fingerprint" >> "$bench_definitions_tmp";
	rm -f "$pkg_bench_tmp" "$pkg_names_tmp";
done < "$bench_packages_tmp";
LC_ALL=C sort -u "$bench_definitions_tmp" -o "$bench_definitions_tmp";
echo "Resolved head benchmark definitions:";
while IFS=$(printf '\t') read -r bench_pkg bench_selection harness_fingerprint; do
	definition_metadata=$(format_benchmark_definition "$bench_pkg" "$bench_selection" "$harness_fingerprint");
	printf "  %s\n" "$definition_metadata";
done < "$bench_definitions_tmp";
if ! (unset GIT_INDEX_FILE; git worktree add --detach "$base_tree" "$base_commit" >/dev/null); then
	fail_invalid_memory_gate "base benchmark revision '$base_commit' could not be prepared.";
fi;
while IFS=$(printf '\t') read -r bench_pkg bench_selection harness_fingerprint; do
	if ! base_harness_fingerprint=$(cd "$base_tree" && benchmark_harness_fingerprint "$bench_pkg"); then
		fail_invalid_memory_gate "base benchmark harness fingerprint could not be resolved for package '$bench_pkg'.";
	fi;
	if [ "$base_harness_fingerprint" != "$harness_fingerprint" ]; then
		fail_invalid_memory_gate "base benchmark definition for package '$bench_pkg' does not match the resolved head harness fingerprint.";
	fi;
done < "$bench_definitions_tmp";
while IFS=$(printf '\t') read -r bench_pkg bench_selection harness_fingerprint; do
	definition_metadata=$(format_benchmark_definition "$bench_pkg" "$bench_selection" "$harness_fingerprint");
	printf "Applied base benchmark definition: %s\n" "$definition_metadata";
	printf "Applied base benchmark definition: %s\n" "$definition_metadata" >> "$base_output_tmp";
	bench_run_output_tmp=$(mktemp);
	if ! (cd "$base_tree" && GOFLAGS=-buildvcs=false run_validated_go_test "base benchmark execution for '$bench_pkg'" -run '^$' -bench "$bench_selection" -benchmem -count="$BENCH_COUNT" -benchtime="$BENCH_TIME" "$bench_pkg") > "$bench_run_output_tmp" 2>&1; then
		cat "$bench_run_output_tmp";
		rm -f "$bench_run_output_tmp";
		fail_invalid_memory_gate "base benchmark definition for package '$bench_pkg' with selection '$bench_selection' could not be applied unchanged.";
	fi;
	cat "$bench_run_output_tmp";
	cat "$bench_run_output_tmp" >> "$base_output_tmp";
	rm -f "$bench_run_output_tmp";
done < "$bench_definitions_tmp";
cp "$base_output_tmp" "$BENCH_BASE_OUTPUT";
while IFS=$(printf '\t') read -r bench_pkg bench_selection harness_fingerprint; do
	definition_metadata=$(format_benchmark_definition "$bench_pkg" "$bench_selection" "$harness_fingerprint");
	printf "Applied head benchmark definition: %s\n" "$definition_metadata";
	printf "Applied head benchmark definition: %s\n" "$definition_metadata" >> "$head_output_tmp";
	bench_run_output_tmp=$(mktemp);
	if ! GOFLAGS=-buildvcs=false run_validated_go_test "head benchmark execution for '$bench_pkg'" -run '^$' -bench "$bench_selection" -benchmem -count="$BENCH_COUNT" -benchtime="$BENCH_TIME" "$bench_pkg" > "$bench_run_output_tmp" 2>&1; then
		cat "$bench_run_output_tmp";
		rm -f "$bench_run_output_tmp";
		fail_invalid_memory_gate "head benchmark definition for package '$bench_pkg' with selection '$bench_selection' could not be applied unchanged.";
	fi;
	cat "$bench_run_output_tmp";
	cat "$bench_run_output_tmp" >> "$head_output_tmp";
	rm -f "$bench_run_output_tmp";
done < "$bench_definitions_tmp";
cp "$head_output_tmp" "$BENCH_HEAD_OUTPUT";
benchdelta_bin="$bench_dir/benchdelta";
GOFLAGS=-buildvcs=false run_validated_go "benchdelta helper build" build -o "$benchdelta_bin" ./tools/benchdelta;
set +e;
"$benchdelta_bin" -base "$BENCH_BASE_OUTPUT" -head "$BENCH_HEAD_OUTPUT" -max-bytes-pct "$MEMORY_BENCH_MAX_BYTES_PCT" -max-allocs-pct "$MEMORY_BENCH_MAX_ALLOCS_PCT" -summary-out "$MEMORY_BENCH_SUMMARY";
status=$?;
set -e;
printf "%s\n" "$status" > "$MEMORY_BENCH_STATUS";
if [ "$MEMORY_BENCH_ENFORCE" = "0" ] && [ "$status" -eq 1 ]; then
	exit 0;
fi;
exit "$status"
