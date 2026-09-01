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
write_memory_bench_status() {
	status_code="$1";
	printf "%s\n" "$status_code" > "$MEMORY_BENCH_STATUS";
};
write_invalid_memory_summary() {
	summary_error="$1";
	printf "## Memory Benchmarks\n\nThresholds: bytes/op <= +%s%%, allocs/op <= +%s%%\n\nBase benchmarks: unavailable\nHead benchmarks: unavailable\n\nInput errors:\n- %s\n\nComparison status: invalid\nResult: benchmark input could not be read for a safe memory comparison.\n" "$MEMORY_BENCH_MAX_BYTES_PCT" "$MEMORY_BENCH_MAX_ALLOCS_PCT" "$summary_error" > "$MEMORY_BENCH_SUMMARY";
	write_memory_bench_status "2";
};
write_harness_change_requires_approval_summary() {
	summary_error="$1";
	printf "## Memory Benchmarks\n\nThresholds: bytes/op <= +%s%%, allocs/op <= +%s%%\n\nBase benchmarks: unavailable\nHead benchmarks: unavailable\n\nInput errors:\n- %s\n\nComparison status: invalid\nResult: benchmark harness changed; add the memory-approved label to acknowledge the unmatched base definition.\n" "$MEMORY_BENCH_MAX_BYTES_PCT" "$MEMORY_BENCH_MAX_ALLOCS_PCT" "$summary_error" > "$MEMORY_BENCH_SUMMARY";
	write_memory_bench_status "1";
};
fail_invalid_memory_gate() {
	diagnostic="$1";
	summary_error="$diagnostic";
	printf "Memory benchmark gate invalid: %s\n" "$diagnostic" >&2;
	if [ "$#" -gt 1 ]; then
		summary_error="$2";
	fi;
	write_invalid_memory_summary "$summary_error";
	exit 2;
};
report_harness_change_requires_approval() {
	diagnostic="$1";
	summary_error="$diagnostic";
	printf "Memory benchmark approval required: %s\n" "$diagnostic" >&2;
	if [ "$#" -gt 1 ]; then
		summary_error="$2";
	fi;
	write_harness_change_requires_approval_summary "$summary_error";
	exit 0;
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
benchmark_harness_append_file() {
	fingerprint_kind="$1";
	fingerprint_file="$2";
	if ! fingerprint_blob=$(git hash-object -- "$fingerprint_dir/$fingerprint_file" 2>/dev/null); then
		return 1;
	fi;
	printf "%s\t%s\t%s\n" "$fingerprint_kind" "$fingerprint_file" "$fingerprint_blob" >> "$fingerprint_manifest_tmp";
};
benchmark_harness_fingerprint() {
	fingerprint_pkg="$1";
	fingerprint_go_files_tmp=$(mktemp) || return 1;
	fingerprint_files_tmp=$(mktemp) || { rm -f "$fingerprint_go_files_tmp"; return 1; };
	fingerprint_kind_files_tmp=$(mktemp) || { rm -f "$fingerprint_go_files_tmp" "$fingerprint_files_tmp"; return 1; };
	fingerprint_manifest_tmp=$(mktemp) || { rm -f "$fingerprint_go_files_tmp" "$fingerprint_files_tmp" "$fingerprint_kind_files_tmp"; return 1; };
	if ! fingerprint_dir=$(GOFLAGS=-buildvcs=false run_validated_go "benchmark harness directory resolution for '$fingerprint_pkg'" list -f '{{.Dir}}' "$fingerprint_pkg" 2>/dev/null); then
		rm -f "$fingerprint_go_files_tmp" "$fingerprint_files_tmp" "$fingerprint_kind_files_tmp" "$fingerprint_manifest_tmp";
		return 1;
	fi;
	if ! GOFLAGS=-buildvcs=false run_validated_go "benchmark harness file resolution for '$fingerprint_pkg'" list -test -f '{{range .TestGoFiles}}{{printf "test\t%s\n" .}}{{end}}{{range .XTestGoFiles}}{{printf "xtest\t%s\n" .}}{{end}}' "$fingerprint_pkg" > "$fingerprint_go_files_tmp" 2>/dev/null; then
		rm -f "$fingerprint_go_files_tmp" "$fingerprint_files_tmp" "$fingerprint_kind_files_tmp" "$fingerprint_manifest_tmp";
		return 1;
	fi;
	if ! LC_ALL=C sort -u -o "$fingerprint_go_files_tmp" "$fingerprint_go_files_tmp"; then
		rm -f "$fingerprint_go_files_tmp" "$fingerprint_files_tmp" "$fingerprint_kind_files_tmp" "$fingerprint_manifest_tmp";
		return 1;
	fi;
	: > "$fingerprint_files_tmp";
	for fingerprint_kind in test xtest; do
		awk -F "$(printf '\t')" -v kind="$fingerprint_kind" '$1 == kind { print $2 }' "$fingerprint_go_files_tmp" > "$fingerprint_kind_files_tmp";
		if ! "$benchmark_harness_selector_bin" "$fingerprint_dir" "$fingerprint_kind" < "$fingerprint_kind_files_tmp" >> "$fingerprint_files_tmp"; then
			rm -f "$fingerprint_go_files_tmp" "$fingerprint_files_tmp" "$fingerprint_kind_files_tmp" "$fingerprint_manifest_tmp";
			return 1;
		fi;
	done;
	if ! LC_ALL=C sort -u -o "$fingerprint_files_tmp" "$fingerprint_files_tmp"; then
		rm -f "$fingerprint_go_files_tmp" "$fingerprint_files_tmp" "$fingerprint_kind_files_tmp" "$fingerprint_manifest_tmp";
		return 1;
	fi;
	: > "$fingerprint_manifest_tmp";
	fingerprint_failed=0;
	while IFS=$(printf '\t') read -r fingerprint_kind fingerprint_file; do
		[ -n "$fingerprint_file" ] || continue;
		case "$fingerprint_kind" in
			test|xtest) ;;
			test-embed|xtest-embed) ;;
			*) continue ;;
		esac;
		if ! benchmark_harness_append_file "$fingerprint_kind" "$fingerprint_file"; then
			fingerprint_failed=1;
			break;
		fi;
	done < "$fingerprint_files_tmp";
	LC_ALL=C sort -u -o "$fingerprint_manifest_tmp" "$fingerprint_manifest_tmp" || fingerprint_failed=1;
	if [ "$fingerprint_failed" -ne 0 ] || ! fingerprint_value=$(git hash-object -- "$fingerprint_manifest_tmp" 2>/dev/null); then
		rm -f "$fingerprint_go_files_tmp" "$fingerprint_files_tmp" "$fingerprint_kind_files_tmp" "$fingerprint_manifest_tmp";
		return 1;
	fi;
	rm -f "$fingerprint_go_files_tmp" "$fingerprint_files_tmp" "$fingerprint_kind_files_tmp" "$fingerprint_manifest_tmp";
	printf "git-hash-object:%s\n" "$fingerprint_value";
};
format_benchmark_definition() {
	invocation_pkg="$1";
	invocation_selection="$2";
	invocation_fingerprint="$3";
	printf "package=%s selection=%s -run '^$' GO_TEST_LDFLAGS_ARGS=%s flags=-benchmem -count=%s -benchtime=%s harness-files=benchmark-test-go-files,benchmark-test-embed-files harness-fingerprint=%s invocation=GOFLAGS=-buildvcs=false GOTOOLCHAIN=%s %s test %s -run '^$' -bench '%s' -benchmem -count=%s -benchtime=%s '%s'" "$invocation_pkg" "$invocation_selection" "$GO_TEST_LDFLAGS_ARGS" "$BENCH_COUNT" "$BENCH_TIME" "$invocation_fingerprint" "$requested_go_toolchain" "$go_bin_path" "$GO_TEST_LDFLAGS_ARGS" "$invocation_selection" "$BENCH_COUNT" "$BENCH_TIME" "$invocation_pkg";
};
if ! base_commit=$(git rev-parse --verify -q --end-of-options "$base_ref^{commit}"); then
	echo "Memory benchmark base ref '$base_ref' is missing or invalid; failing closed.";
	fail_invalid_memory_gate "base benchmark input could not be read: requested base ref '$base_ref' is missing or invalid.";
fi;
if ! git merge-base --is-ancestor "$base_commit" HEAD >/dev/null 2>&1; then
	echo "Memory benchmark base ref '$base_ref' is not an ancestor of HEAD; failing closed.";
	fail_invalid_memory_gate "base benchmark input could not be read: requested base ref '$base_ref' is not an ancestor of HEAD.";
fi;
base_ref="$base_commit";
bench_dir=$(mktemp -d);
base_tree="$bench_dir/base";
base_output_tmp=$(mktemp);
head_output_tmp=$(mktemp);
bench_packages_tmp=$(mktemp);
bench_definitions_tmp=$(mktemp);
# shellcheck disable=SC2317,SC2329 # cleanup is invoked by trap.
cleanup() { (unset GIT_INDEX_FILE; git worktree remove --force "$base_tree" >/dev/null 2>&1 || true); rm -rf "$bench_dir"; rm -f "$base_output_tmp" "$head_output_tmp" "$bench_packages_tmp" "$bench_definitions_tmp"; };
trap cleanup EXIT INT TERM;
benchmark_harness_selector_bin="$bench_dir/benchharness";
benchmark_harness_selector_src="$bench_dir/benchharness.go";
cat > "$benchmark_harness_selector_src" <<'GOEOF';
package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type harnessDecl struct {
	file string
	decl ast.Decl
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: benchharness <package-dir> <test|xtest>")
		os.Exit(2)
	}

	manifest, err := benchmarkHarnessManifest(os.Args[1], os.Args[2], os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve benchmark harness files: %v\n", err)
		os.Exit(2)
	}
	for _, line := range manifest {
		fmt.Println(line)
	}
}

func benchmarkHarnessManifest(dir, kind string, stdin *os.File) ([]string, error) {
	files, err := readFileList(stdin)
	if err != nil {
		return nil, err
	}
	parsed := make(map[string]*ast.File, len(files))
	decls := make(map[string][]harnessDecl)
	roots := make([]harnessDecl, 0)
	modInfo := loadModuleInfo(dir)
	for _, rel := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		file, err := parser.ParseFile(token.NewFileSet(), abs, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", rel, err)
		}
		parsed[rel] = file
		for _, decl := range file.Decls {
			for _, name := range declaredNames(decl) {
				decls[name] = append(decls[name], harnessDecl{file: rel, decl: decl})
			}
			if rootDeclarationCanAffectBenchmark(decl, modInfo) {
				roots = append(roots, harnessDecl{file: rel, decl: decl})
			}
		}
	}

	selected, seenNames := selectedBenchmarkHarnessFiles(roots, decls)
	manifest := make([]string, 0, len(selected))
	seen := make(map[string]struct{})
	for _, rel := range files {
		if _, ok := selected[rel]; !ok {
			continue
		}
		appendManifestLine(&manifest, seen, kind, rel)
		embedFiles, err := embeddedFilesForSelectedFile(dir, parsed[rel], seenNames)
		if err != nil {
			return nil, fmt.Errorf("resolve embeds in %s: %w", rel, err)
		}
		for _, embedFile := range embedFiles {
			appendManifestLine(&manifest, seen, kind+"-embed", embedFile)
		}
	}
	sort.Strings(manifest)
	return manifest, nil
}

func readFileList(stdin *os.File) ([]string, error) {
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	files := make([]string, 0)
	for scanner.Scan() {
		if rel := strings.TrimSpace(scanner.Text()); rel != "" {
			files = append(files, filepath.ToSlash(rel))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return files, nil
}

func appendManifestLine(manifest *[]string, seen map[string]struct{}, kind, rel string) {
	line := kind + "\t" + filepath.ToSlash(rel)
	if _, ok := seen[line]; ok {
		return
	}
	seen[line] = struct{}{}
	*manifest = append(*manifest, line)
}

// selectedBenchmarkHarnessFiles walks the reachability graph from roots and
// returns the selected files plus seenNames: every declared name actually
// pulled in, at the name (not whole-declaration) granularity. A root
// decl's own names are all considered seen -- the criteria that make a
// decl a root (an import, an initialized var, a benchmark/init/TestMain
// function, a method) apply to the whole declaration, not to one name
// within it -- but a decl reached only because something else referenced
// one of its names contributes just that name, not its siblings. This
// lets embeddedFilesForSelectedFile scope go:embed comments to the
// specific spec that was actually reached, rather than every spec sharing
// its enclosing declaration.
func selectedBenchmarkHarnessFiles(roots []harnessDecl, decls map[string][]harnessDecl) (map[string]struct{}, map[string]struct{}) {
	selected := make(map[string]struct{})
	seenDecls := make(map[ast.Decl]struct{})
	seenNames := make(map[string]struct{})
	rootDecls := make(map[ast.Decl]struct{}, len(roots))
	for _, root := range roots {
		rootDecls[root.decl] = struct{}{}
	}
	queue := append([]harnessDecl(nil), roots...)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if _, ok := seenDecls[current.decl]; ok {
			continue
		}
		seenDecls[current.decl] = struct{}{}
		selected[current.file] = struct{}{}
		selfDeclared := declaredNamesSet(current.decl)
		if _, isRoot := rootDecls[current.decl]; isRoot {
			for name := range selfDeclared {
				seenNames[name] = struct{}{}
			}
		}
		for name := range referencedPackageNames(current.decl) {
			if _, isOwnDeclaration := selfDeclared[name]; isOwnDeclaration {
				// referencedPackageNames walks the whole decl generically and
				// cannot tell a declaration site from a use site, so a
				// sibling in the same var/const/type group -- e.g. another
				// name in the same var(...) block -- would otherwise look
				// like this decl referencing itself.
				continue
			}
			for _, next := range decls[name] {
				seenNames[name] = struct{}{}
				if _, ok := seenDecls[next.decl]; !ok {
					queue = append(queue, next)
				}
			}
		}
	}
	return selected, seenNames
}

func declaredNamesSet(decl ast.Decl) map[string]struct{} {
	names := declaredNames(decl)
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

func declaredNames(decl ast.Decl) []string {
	switch typed := decl.(type) {
	case *ast.FuncDecl:
		if typed.Name == nil {
			return nil
		}
		return []string{typed.Name.Name}
	case *ast.GenDecl:
		if !declarationTokenCanAffectBenchmark(typed.Tok) {
			return nil
		}
		names := make([]string, 0, len(typed.Specs))
		for _, spec := range typed.Specs {
			switch typedSpec := spec.(type) {
			case *ast.ValueSpec:
				for _, name := range typedSpec.Names {
					names = append(names, name.Name)
				}
			case *ast.TypeSpec:
				names = append(names, typedSpec.Name.Name)
			}
		}
		return names
	default:
		return nil
	}
}

func declarationTokenCanAffectBenchmark(tok token.Token) bool {
	return tok == token.CONST || tok == token.TYPE || tok == token.VAR
}

func rootDeclarationCanAffectBenchmark(decl ast.Decl, modInfo moduleInfo) bool {
	switch typed := decl.(type) {
	case *ast.FuncDecl:
		return rootFunctionCanAffectBenchmark(typed)
	case *ast.GenDecl:
		return rootGenDeclCanAffectBenchmark(typed, modInfo)
	default:
		return false
	}
}

func rootFunctionCanAffectBenchmark(decl *ast.FuncDecl) bool {
	if decl.Name == nil {
		return false
	}
	if decl.Recv != nil {
		return true
	}
	name := decl.Name.Name
	return name == "init" || name == "TestMain" || isGoTestEntrypoint(name, "Benchmark")
}

func rootGenDeclCanAffectBenchmark(decl *ast.GenDecl, modInfo moduleInfo) bool {
	switch decl.Tok {
	case token.IMPORT:
		return genDeclHasBenchmarkImport(decl, modInfo)
	case token.VAR:
		return genDeclHasInitializedVar(decl)
	default:
		return false
	}
}

func genDeclHasBenchmarkImport(decl *ast.GenDecl, modInfo moduleInfo) bool {
	for _, spec := range decl.Specs {
		importSpec, ok := spec.(*ast.ImportSpec)
		if ok && importCanAffectBenchmark(importSpec, modInfo) {
			return true
		}
	}
	return false
}

// importCanAffectBenchmark reports whether spec can affect benchmark setup.
// A blank import always counts (it exists purely for its init() side
// effect). A named import counts when it's tracked by the module: the
// current module itself (or one of its subpackages), or any module listed
// in go.mod's require directives, regardless of import-path shape -- Go
// modules place no format requirement on a path, so a bare, dotless name is
// a fully valid external module reference via a replace directive. Every
// other named import is standard library: it ships with the Go toolchain
// rather than being an independently versioned dependency of this repo, so
// it isn't tracked here.
func importCanAffectBenchmark(spec *ast.ImportSpec, modInfo moduleInfo) bool {
	path := importPath(spec)
	if path == "" || path == "embed" {
		return false
	}
	if spec.Name != nil && spec.Name.Name == "_" {
		return true
	}
	return modInfo.tracks(path)
}

func importPath(spec *ast.ImportSpec) string {
	if spec == nil || spec.Path == nil {
		return ""
	}
	value, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return ""
	}
	return value
}

// moduleInfo carries the current module's own path and the set of module
// paths its go.mod declares as required, used to distinguish a tracked
// dependency (first-party or third-party, any path shape) from a standard
// library import, which is never declared in go.mod.
type moduleInfo struct {
	path     string
	required map[string]struct{}
}

func (m moduleInfo) tracks(importPath string) bool {
	if m.path != "" && (importPath == m.path || strings.HasPrefix(importPath, m.path+"/")) {
		return true
	}
	for required := range m.required {
		if importPath == required || strings.HasPrefix(importPath, required+"/") {
			return true
		}
	}
	return false
}

func loadModuleInfo(dir string) moduleInfo {
	current := filepath.Clean(dir)
	for {
		content, err := os.ReadFile(filepath.Join(current, "go.mod"))
		if err == nil {
			return parseModuleInfo(content)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return moduleInfo{required: map[string]struct{}{}}
		}
		current = parent
	}
}

func parseModuleInfo(content []byte) moduleInfo {
	info := moduleInfo{required: make(map[string]struct{})}
	inRequireBlock := false
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		trimmed := strings.TrimSpace(stripGoModLineComment(scanner.Text()))
		if trimmed == "" {
			continue
		}
		if inRequireBlock {
			if trimmed == ")" {
				inRequireBlock = false
				continue
			}
			addRequiredModulePath(info.required, trimmed)
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "module":
			if len(fields) >= 2 {
				info.path = unquoteGoModToken(fields[1])
			}
		case "require":
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "require"))
			if rest == "(" {
				inRequireBlock = true
				continue
			}
			addRequiredModulePath(info.required, rest)
		}
	}
	return info
}

func addRequiredModulePath(required map[string]struct{}, spec string) {
	fields := strings.Fields(spec)
	if len(fields) == 0 {
		return
	}
	required[unquoteGoModToken(fields[0])] = struct{}{}
}

// unquoteGoModToken unquotes a go.mod module-path token if it's quoted.
// go.mod's grammar permits a module or require directive's path to be a
// double-quoted or raw ("`"-delimited) Go string literal instead of a bare
// token; without unquoting, a quoted path (e.g. require "example.com/dep"
// v1.0.0) would never match the unquoted path an *ast.ImportSpec carries,
// so a tracked import using that form would be missed.
func unquoteGoModToken(token string) string {
	if unquoted, err := strconv.Unquote(token); err == nil {
		return unquoted
	}
	return token
}

func stripGoModLineComment(line string) string {
	if idx := strings.Index(line, "//"); idx >= 0 {
		return line[:idx]
	}
	return line
}

func genDeclHasInitializedVar(decl *ast.GenDecl) bool {
	for _, spec := range decl.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if ok && len(valueSpec.Values) > 0 {
			return true
		}
	}
	return false
}

func referencedPackageNames(node ast.Node) map[string]struct{} {
	names := make(map[string]struct{})
	ast.Inspect(node, func(n ast.Node) bool {
		switch typed := n.(type) {
		case *ast.Ident:
			if typed.Name != "_" {
				names[typed.Name] = struct{}{}
			}
		case *ast.SelectorExpr:
			if typed.Sel != nil {
				names[typed.Sel.Name] = struct{}{}
			}
		}
		return true
	})
	return names
}

func isGoTestEntrypoint(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	suffix := name[len(prefix):]
	if suffix == "" {
		return true
	}
	first, _ := utf8.DecodeRuneInString(suffix)
	return !unicode.IsLower(first)
}

// embeddedFilesForSelectedFile resolves the go:embed patterns attached to
// this file's *selected* declarations only, not every //go:embed comment
// anywhere in the file. A benchmark and an ordinary test can share one
// _test.go file; fingerprinting every embed in the whole file would make an
// ordinary test's fixture, unreachable from any benchmark, invalidate the
// harness fingerprint when it changes.
func embeddedFilesForSelectedFile(dir string, file *ast.File, seenNames map[string]struct{}) ([]string, error) {
	if file == nil {
		return nil, nil
	}
	seen := make(map[string]struct{})
	files := make([]string, 0)
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.VAR {
			continue
		}
		for _, group := range embedDocCommentGroupsForSelectedSpecs(genDecl, seenNames) {
			for _, comment := range group.List {
				text := strings.TrimSpace(comment.Text)
				if !strings.HasPrefix(text, "//go:embed") {
					continue
				}
				patterns, err := parseEmbedPatterns(strings.TrimSpace(strings.TrimPrefix(text, "//go:embed")))
				if err != nil {
					return nil, err
				}
				for _, pattern := range patterns {
					matches, err := resolveEmbedPattern(dir, pattern)
					if err != nil {
						return nil, err
					}
					for _, match := range matches {
						if _, ok := seen[match]; ok {
							continue
						}
						seen[match] = struct{}{}
						files = append(files, match)
					}
				}
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

// embedDocCommentGroupsForSelectedSpecs returns the doc comment for each of
// decl's specs whose own declared name was actually reached (present in
// seenNames), covering both a single ungrouped var declaration (Go attaches
// its doc comment to the GenDecl itself, not the lone ValueSpec) and a
// grouped var block (Go attaches each spec's own doc comment to that
// ValueSpec directly). Scoping by spec, not by whether the whole decl was
// selected, matters specifically for a grouped block: a benchmark
// referencing only one of several embed vars in the same var(...) group
// must not pull in the other vars' embed fixtures too.
func embedDocCommentGroupsForSelectedSpecs(decl *ast.GenDecl, seenNames map[string]struct{}) []*ast.CommentGroup {
	groups := make([]*ast.CommentGroup, 0, len(decl.Specs))
	for _, spec := range decl.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		doc := valueSpec.Doc
		if doc == nil && len(decl.Specs) == 1 {
			doc = decl.Doc
		}
		if doc == nil || !valueSpecNameIsSeen(valueSpec, seenNames) {
			continue
		}
		groups = append(groups, doc)
	}
	return groups
}

func valueSpecNameIsSeen(spec *ast.ValueSpec, seenNames map[string]struct{}) bool {
	for _, name := range spec.Names {
		if _, ok := seenNames[name.Name]; ok {
			return true
		}
	}
	return false
}

func parseEmbedPatterns(text string) ([]string, error) {
	patterns := make([]string, 0)
	for {
		text = strings.TrimLeftFunc(text, unicode.IsSpace)
		if text == "" {
			return patterns, nil
		}
		switch text[0] {
		case '`':
			end := strings.IndexByte(text[1:], '`')
			if end < 0 {
				return nil, fmt.Errorf("unterminated raw string literal in go:embed directive")
			}
			lit := text[:end+2]
			value, err := strconv.Unquote(lit)
			if err != nil {
				return nil, err
			}
			patterns = append(patterns, value)
			text = text[end+2:]
		case '"':
			end := 1
			escaped := false
			for end < len(text) {
				ch := text[end]
				if escaped {
					escaped = false
				} else if ch == '\\' {
					escaped = true
				} else if ch == '"' {
					break
				}
				end++
			}
			if end >= len(text) {
				return nil, fmt.Errorf("unterminated string literal in go:embed directive")
			}
			lit := text[:end+1]
			value, err := strconv.Unquote(lit)
			if err != nil {
				return nil, err
			}
			patterns = append(patterns, value)
			text = text[end+1:]
		default:
			end := strings.IndexFunc(text, unicode.IsSpace)
			if end < 0 {
				patterns = append(patterns, text)
				text = ""
			} else {
				patterns = append(patterns, text[:end])
				text = text[end:]
			}
		}
	}
}

func resolveEmbedPattern(dir, pattern string) ([]string, error) {
	includeHidden := false
	if strings.HasPrefix(pattern, "all:") {
		includeHidden = true
		pattern = strings.TrimPrefix(pattern, "all:")
	}
	pattern = filepath.ToSlash(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("empty go:embed pattern")
	}
	if hasPathMeta(pattern) {
		return resolveEmbedGlob(dir, pattern, includeHidden)
	}
	abs := filepath.Join(dir, filepath.FromSlash(pattern))
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return embeddedFilesUnderDir(dir, abs, includeHidden)
	}
	if info.Mode().IsRegular() {
		return []string{filepath.ToSlash(pattern)}, nil
	}
	return nil, nil
}

func resolveEmbedGlob(dir, pattern string, includeHidden bool) ([]string, error) {
	files := make([]string, 0)
	err := filepath.WalkDir(dir, func(abs string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if abs == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, abs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		matched, err := path.Match(pattern, rel)
		if err != nil {
			return err
		}
		if !matched {
			return nil
		}
		if entry.IsDir() {
			nested, err := embeddedFilesUnderDir(dir, abs, includeHidden)
			if err != nil {
				return err
			}
			files = append(files, nested...)
			return filepath.SkipDir
		}
		if entry.Type().IsRegular() {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return uniqueStrings(files), nil
}

func embeddedFilesUnderDir(root, dir string, includeHidden bool) ([]string, error) {
	files := make([]string, 0)
	err := filepath.WalkDir(dir, func(abs string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if abs != dir && !includeHidden && hiddenEmbedName(entry.Name()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func hasPathMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

func hiddenEmbedName(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:0]
	var last string
	for i, value := range values {
		if i > 0 && value == last {
			continue
		}
		out = append(out, value)
		last = value
	}
	return out
}
GOEOF
GOFLAGS=-buildvcs=false run_validated_go "benchmark harness selector build" build -o "$benchmark_harness_selector_bin" "$benchmark_harness_selector_src";
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
		if [ "$MEMORY_BENCH_ENFORCE" = "0" ]; then
			report_harness_change_requires_approval "base benchmark definition for package '$bench_pkg' does not match the resolved head harness fingerprint.";
		fi;
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
write_memory_bench_status "$status";
if [ "$MEMORY_BENCH_ENFORCE" = "0" ] && [ "$status" -eq 1 ]; then
	exit 0;
fi;
exit "$status"
