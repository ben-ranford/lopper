package cpp

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	testMainCPPFileName = "main.cpp"
	systemIncludeDir    = "/usr/include"
	fmtCoreHeader       = "fmt/core.h"
	fmtCoreIncludeLine  = "#include <" + fmtCoreHeader + ">\n"
)

func TestDetectCanceledContext(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", testMainCPPFileName), fmtCoreIncludeLine)

	_, err := NewAdapter().Detect(testutil.CanceledContext(), repo)
	if err == nil {
		t.Fatalf("expected detect to return context cancellation error")
	}
	_, err = NewAdapter().DetectWithConfidence(testutil.CanceledContext(), repo)
	if err == nil {
		t.Fatalf("expected DetectWithConfidence to return context cancellation error")
	}
}

func TestLoadCompileContextInvalidJSONWarning(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, compileCommandsFile), "{not valid json")

	ctx, err := loadCompileContext(repo)
	if err != nil {
		t.Fatalf("load compile context: %v", err)
	}
	if !hasWarning(ctx.Warnings, "failed to parse") {
		t.Fatalf("expected parse warning, got %#v", ctx.Warnings)
	}
}

func TestLoadCompileContextNoDatabaseWarning(t *testing.T) {
	repo := t.TempDir()
	ctx, err := loadCompileContext(repo)
	if err != nil {
		t.Fatalf("load compile context: %v", err)
	}
	if !hasWarning(ctx.Warnings, "compile_commands.json not found") {
		t.Fatalf("expected missing compile db warning, got %#v", ctx.Warnings)
	}
}

func TestCompileContextCollectorStagesCompileDatabaseData(t *testing.T) {
	repo := t.TempDir()
	compileDB := filepath.Join(repo, "build", compileCommandsFile)
	testutil.MustWriteFile(t, compileDB, `[
  {"directory":"..","file":"src/`+testMainCPPFileName+`","arguments":["c++","-I","include","-isystem/usr/include","-c","src/`+testMainCPPFileName+`"]}
]`)

	collector, err := newCompileContextCollector(repo)
	if err != nil {
		t.Fatalf("new compile context collector: %v", err)
	}
	if err := collector.visit(filepath.Join(repo, "README.md")); err != nil {
		t.Fatalf("visit non-compile-db: %v", err)
	}
	if err := collector.visit(compileDB); err != nil {
		t.Fatalf("visit compile db: %v", err)
	}

	ctx := collector.result()
	if !ctx.HasCompileDatabase {
		t.Fatalf("expected compile database to be recorded")
	}
	wantIncludeDirs := []string{systemIncludeDir, filepath.Join(repo, "include")}
	slices.Sort(wantIncludeDirs)
	if !slices.Equal(ctx.IncludeDirs, wantIncludeDirs) {
		t.Fatalf("unexpected include dirs: %#v", ctx.IncludeDirs)
	}
	sourcePath := filepath.Join(repo, "src", testMainCPPFileName)
	sourceIncludeDirs := ctx.SourceIncludeDirs[sourcePath]
	if len(sourceIncludeDirs) != 2 {
		t.Fatalf("expected per-source include dirs, got %#v", ctx.SourceIncludeDirs)
	}
	if sourceIncludeDirs[0].Path != filepath.Join(repo, "include") || sourceIncludeDirs[0].System {
		t.Fatalf("expected first include dir to preserve -I provenance, got %#v", sourceIncludeDirs[0])
	}
	if sourceIncludeDirs[1].Path != systemIncludeDir || !sourceIncludeDirs[1].System {
		t.Fatalf("expected second include dir to preserve -isystem provenance, got %#v", sourceIncludeDirs[1])
	}
	if !slices.Equal(ctx.SourceFiles, []string{filepath.Join(repo, "src", testMainCPPFileName)}) {
		t.Fatalf("unexpected source files: %#v", ctx.SourceFiles)
	}
	if len(ctx.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", ctx.Warnings)
	}
}

func TestDetectWithCompileDatabaseAndCMakeSignals(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "CMakeLists.txt"), "project(demo)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "compile_commands.json"), `[
  {"directory":".","file":"src/`+testMainCPPFileName+`","command":"c++ -Iinclude -c src/`+testMainCPPFileName+`"}
]`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", testMainCPPFileName), fmtCoreIncludeLine+"int main() { return 0; }\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched || detection.Confidence <= 0 || len(detection.Roots) == 0 {
		t.Fatalf("unexpected detection result: %#v", detection)
	}
}

func TestResolveCompilePathAndDirectory(t *testing.T) {
	dbPath := filepath.Join("/tmp", "build", compileCommandsFile)
	if got := resolveCompileDirectory(dbPath, ""); got != filepath.Dir(dbPath) {
		t.Fatalf("expected db parent directory, got %q", got)
	}
	if got := resolveCompileDirectory(dbPath, "obj"); got != filepath.Join(filepath.Dir(dbPath), "obj") {
		t.Fatalf("expected resolved relative directory, got %q", got)
	}
	if resolveCompilePath("/tmp/build", "") != "" {
		t.Fatalf("expected empty path for empty input")
	}
}

func TestExtractIncludeDirsAndAddDedup(t *testing.T) {
	dirs := extractIncludeDirs([]string{"-I", "include", "-Ivendor/include", "-isystem", systemIncludeDir, "-iquote", "headers", "-isystem/opt/include", "-iquotequoted", "-Ivendor/include", "-I", ""}, "/repo")
	want := []string{
		"/opt/include",
		"/repo/headers",
		"/repo/include",
		"/repo/quoted",
		"/repo/vendor/include",
		systemIncludeDir,
	}
	if !slices.Equal(dirs, want) {
		t.Fatalf("unexpected include dirs: got %#v want %#v", dirs, want)
	}

	searchPaths := extractIncludeSearchPaths([]string{"-I", "include", "-isystem", systemIncludeDir, "-Ivendor/include"}, "/repo")
	if len(searchPaths) != 3 {
		t.Fatalf("unexpected search paths: %#v", searchPaths)
	}
	if searchPaths[0].Path != "/repo/include" || searchPaths[0].System || !searchPaths[0].ProvenanceKnown {
		t.Fatalf("expected first -I path with known non-system provenance, got %#v", searchPaths[0])
	}
	if searchPaths[1].Path != systemIncludeDir || !searchPaths[1].System || !searchPaths[1].ProvenanceKnown {
		t.Fatalf("expected second -isystem path with known system provenance, got %#v", searchPaths[1])
	}

	includeDirSet := map[string]struct{}{}
	includeSearchPathSet := map[string]includeSearchPath{}
	recordCompileIncludes(includeDirSet, includeSearchPathSet, []includeSearchPath{
		{},
		{Path: "/repo/include", ProvenanceKnown: true},
		{Path: "/repo/include", System: true, ProvenanceKnown: true},
	})
	if len(includeDirSet) != 1 {
		t.Fatalf("expected blank and duplicate include dirs to be ignored, got %#v", includeDirSet)
	}
	if got := includeSearchPathSet["/repo/include"]; !got.System {
		t.Fatalf("expected duplicate -isystem provenance to upgrade global path metadata, got %#v", got)
	}

	merged := mergeIncludeSearchPaths(
		[]includeSearchPath{{Path: "/first"}, {Path: "/shared"}},
		[]includeSearchPath{{Path: "/shared"}, {Path: "/second"}},
	)
	if len(merged) != 3 || merged[0].Path != "/first" || merged[1].Path != "/shared" || merged[2].Path != "/second" {
		t.Fatalf("expected merge to preserve first-seen order and append new paths, got %#v", merged)
	}
	if got := mergeIncludeSearchPaths(nil, []includeSearchPath{{Path: "/only"}}); len(got) != 1 || got[0].Path != "/only" {
		t.Fatalf("expected empty merge to copy next paths, got %#v", got)
	}
	var added []string
	seen := map[string]struct{}{}
	addIncludeDir("", seen, &added)
	addIncludeDir("/repo/include", seen, &added)
	addIncludeDir("/repo/include", seen, &added)
	if !slices.Equal(added, []string{"/repo/include"}) {
		t.Fatalf("expected addIncludeDir to ignore blank and duplicate paths, got %#v", added)
	}

	sorted := sortedIncludeSearchPaths(map[string]includeSearchPath{
		"/z": {Path: "/z"},
		"/a": {Path: "/a", System: true},
	})
	if len(sorted) != 2 || sorted[0].Path != "/a" || sorted[1].Path != "/z" {
		t.Fatalf("expected sorted include search paths, got %#v", sorted)
	}
	if copied := copySourceIncludeDirs(nil); copied != nil {
		t.Fatalf("expected nil source include dirs to stay nil, got %#v", copied)
	}
}

func TestParseIncludesBranches(t *testing.T) {
	content := []byte(`#include <` + fmtCoreHeader + `>
#include "local/header.hpp"
#include_next <` + fmtCoreHeader + `>
#include SOME_MACRO_HEADER
#include <broken
`)
	includes := parseIncludes(content)
	if len(includes) != 4 {
		t.Fatalf("expected four includes, got %d", len(includes))
	}
	if slices.ContainsFunc(includes, func(include parsedInclude) bool {
		return strings.Contains(include.Path, "include_next")
	}) {
		t.Fatalf("expected #include_next to be ignored, got %#v", includes)
	}
}

func TestMapIncludeToDependencyBranches(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "src", testMainCPPFileName)
	testutil.MustWriteFile(t, source, "#include \"header.hpp\"\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "header.hpp"), "// local")
	catalog := newDependencyCatalog()
	catalog.add("boost-asio", "vcpkg manifest")

	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "", Delimiter: '<'}, nil, catalog); dep != "" || !unresolved {
		t.Fatalf("expected empty header unresolved")
	}
	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "vector", Delimiter: '<'}, nil, catalog); dep != "" || unresolved {
		t.Fatalf("expected std header to be ignored")
	}
	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "header.hpp", Delimiter: '"'}, nil, catalog); dep != "" || unresolved {
		t.Fatalf("expected local quoted header to be ignored")
	}
	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "missing.hpp", Delimiter: '"'}, nil, catalog); dep != "" || !unresolved {
		t.Fatalf("expected unresolved quoted include")
	}
	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "openssl/ssl.h", Delimiter: '<'}, nil, catalog); dep != "openssl" || unresolved {
		t.Fatalf("expected mapped dependency openssl, got dep=%q unresolved=%v", dep, unresolved)
	}
	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "boost/asio.hpp", Delimiter: '<'}, nil, catalog); dep != "boost-asio" || unresolved {
		t.Fatalf("expected declared prefix match boost-asio, got dep=%q unresolved=%v", dep, unresolved)
	}
}

func TestMapIncludeToDependencyKeepsQualifiedThirdPartyStdBasenames(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "src", testMainCPPFileName)
	testutil.MustWriteFile(t, source, "#include <boost/regex.hpp>\n")

	tests := []struct {
		header     string
		declared   string
		dependency string
	}{
		{header: "boost/regex.hpp", declared: "boost-regex", dependency: "boost-regex"},
		{header: "boost/chrono.hpp", declared: "boost-chrono", dependency: "boost-chrono"},
		{header: "absl/types/optional.h", declared: "absl", dependency: "absl"},
	}

	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			catalog := newDependencyCatalog()
			catalog.add(tt.declared, "vcpkg manifest")

			dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: tt.header, Delimiter: '<'}, nil, catalog)
			if dep != tt.dependency || unresolved {
				t.Fatalf("expected mapped dependency %q, got dep=%q unresolved=%v", tt.dependency, dep, unresolved)
			}
		})
	}
}

func TestMapIncludeToDependencyPreservesQualifiedLookalikesOutsideSystemRoots(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "src", testMainCPPFileName)
	vendorRoot := filepath.Join(t.TempDir(), "vendor", "include")
	testutil.MustWriteFile(t, source, "int main() { return 0; }\n")

	for _, header := range []string{
		"asm/vendor/sdk.hpp",
		"asm-generic/vendor/sdk.hpp",
		"asm-generic/bitops/atomic.h",
		"backward/hash_map",
		"bits/types/struct_timespec.h",
		"linux/netfilter_ipv4/ip_tables.h",
		"x86_64-linux-gnu/sys/time.h",
		"parallel/base.h",
	} {
		header := header
		t.Run(header, func(t *testing.T) {
			testutil.MustWriteFile(t, filepath.Join(vendorRoot, filepath.FromSlash(header)), "// external lookalike\n")

			want := dependencyFromIncludePath(header)
			dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: header, Delimiter: '<'}, []string{vendorRoot}, newDependencyCatalog())
			if dep != want || unresolved {
				t.Fatalf("expected external lookalike %s to map to %q, got dep=%q unresolved=%v", header, want, dep, unresolved)
			}
		})
	}
}

func TestMapIncludeToDependencyIgnoresRepoHeaderFromIncludeDir(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "src", testMainCPPFileName)
	includeDir := filepath.Join(repo, "include")
	testutil.MustWriteFile(t, source, "#include <project/header.hpp>\n")
	testutil.MustWriteFile(t, filepath.Join(includeDir, "project", "header.hpp"), "// local")

	dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "project/header.hpp", Delimiter: '<'}, []string{includeDir}, newDependencyCatalog())
	if dep != "" || unresolved {
		t.Fatalf("expected repo include-dir header to be ignored, got dep=%q unresolved=%v", dep, unresolved)
	}
}

func TestDependencyFromIncludePath(t *testing.T) {
	if got := dependencyFromIncludePath("openssl/ssl.h"); got != "openssl" {
		t.Fatalf("expected openssl, got %q", got)
	}
	if got := dependencyFromIncludePath("fmt.h"); got != "fmt" {
		t.Fatalf("expected extension trimmed dependency token, got %q", got)
	}
	if got := dependencyFromIncludePath("bad*token/header.h"); got != "" {
		t.Fatalf("expected invalid token to map empty, got %q", got)
	}
	if dependencyFromIncludePath("/") != "" {
		t.Fatalf("expected slash-only include to map empty")
	}
	if got := dependencyFromIncludePath("../bad path"); got != "" {
		t.Fatalf("expected invalid dependency token to map to empty, got %q", got)
	}
}

func TestIsLikelyStdHeaderQualifiedStandardHeaders(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, testMainCPPFileName)

	for _, header := range []string{
		"sys/socket.h",
		"sys/types.h",
		"linux/if.h",
		"linux/limits.h",
		"bits/wordsize.h",
		"bits/stdc++.h",
		"experimental/filesystem",
		"experimental/optional",
		"tr1/regex",
		"tr1/unordered_map",
		"ext/algorithm",
		"ext/numeric",
		"parallel/algorithm",
		"parallel/algo.h",
		"parallel/algobase.h",
		"parallel/base.h",
		"parallel/basic_iterator.h",
		"parallel/features.h",
		"parallel/find.h",
		"parallel/iterator.h",
		"parallel/parallel.h",
		"parallel/queue.h",
		"parallel/search.h",
		"debug/map",
		"debug/map.h",
		"debug/safe_iterator.h",
		"debug/set.h",
		"debug/vector",
		"ext/alloc_traits.h",
		"ext/pb_ds/assoc_container.hpp",
		"ext/pb_ds/exception.hpp",
		"ext/pb_ds/hash_policy.hpp",
		"ext/pb_ds/list_update_policy.hpp",
		"ext/pb_ds/priority_queue.hpp",
		"ext/pb_ds/tree_policy.hpp",
		"ext/pb_ds/trie_policy.hpp",
		"ext/type_traits.h",
		"backward/strstream",
		"backward/hash_map",
		"backward/hash_set",
		"backward/auto_ptr.h",
		"tr2/type_traits",
		"tr1/math.h",
		"tr1/complex.h",
		"tr1/ctype.h",
		"tr1/inttypes.h",
		"tr1/limits.h",
		"tr1/stdio.h",
		"tr1/stdint.h",
		"tr1/type_traits.h",
		"tr1/unordered_map.h",
		"tr1/unordered_set.h",
		"asm/errno.h",
		"x86_64-linux-gnu/asm/errno.h",
		"x86_64-linux-gnu/sys/time.h",
		"asm-generic/errno.h",
		"asm-generic/bitops/atomic.h",
		"bits/types/struct_timespec.h",
		"linux/netfilter/nfnetlink.h",
		"linux/netfilter_ipv4/ip_tables.h",
		"./vector",
		"./experimental/optional",
		"experimental/./optional",
	} {
		header := header
		t.Run(header, func(t *testing.T) {
			if !isLikelyStdHeader(header) {
				t.Fatalf("expected %s to be std header", header)
			}
			if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: header, Delimiter: '<'}, nil, newDependencyCatalog()); dep != "" || unresolved {
				t.Fatalf("expected %s to be ignored as std, got dep=%q unresolved=%v", header, dep, unresolved)
			}
		})
	}
}

func TestIsLikelyStdHeaderDoesNotSwallowQualifiedThirdPartyHeaders(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, testMainCPPFileName)

	for _, header := range []string{
		"boost/regex.hpp",
		"absl/types/optional.h",
		"thirdparty/custom.hpp",
		"debug/vector.hpp",
		"debug/map.hpp",
		"debug/logger.h",
		"debug/safe_iterator.hpp",
		"debug/safe_iterator_extra.h",
		"debug/set.hpp",
		"debug/set_extra.h",
		"experimental/logger.hpp",
		"experimental/filesystem.hpp",
		"experimental/optional.hpp",
		"tr1/regex.hpp",
		"tr1/unordered_map.hpp",
		"tr1/unordered_map_extra.h",
		"tr1/logger.hpp",
		"tr1/logger.h",
		"tr2/type_traits.hpp",
		"tr2/logger.hpp",
		"ext/algorithm.hpp",
		"ext/numeric.hpp",
		"ext/logger.hpp",
		"ext/pb_ds/custom.hpp",
		"ext/pb_ds/assoc_container_extra.hpp",
		"ext/pb_ds/exception.h",
		"ext/pb_ds/hash_policy_extra.hpp",
		"ext/pb_ds/map.hpp",
		"ext/pb_ds/vector.hpp",
		"parallel/algorithm.h",
		"parallel/algorithm.hpp",
		"parallel/algo.hpp",
		"parallel/algobase.hpp",
		"parallel/base.hpp",
		"parallel/custom_base.h",
		"parallel/find.hpp",
		"parallel/logger.h",
		"parallel/logger.hpp",
		"backward/hash_map.h",
		"backward/strstream.hpp",
		"backward/hash_map.hpp",
		"debug/logger.hpp",
		"backward/logger.hpp",
	} {
		header := header
		t.Run(header, func(t *testing.T) {
			if isLikelyStdHeader(header) {
				t.Fatalf("did not expect %s to be std header", header)
			}
			want := dependencyFromIncludePath(header)
			catalog := newDependencyCatalog()
			catalog.add(want, "test manifest")

			dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: header, Delimiter: '<'}, nil, catalog)
			if dep != want || unresolved {
				t.Fatalf("expected third-party header %s to map to %q, got dep=%q unresolved=%v", header, want, dep, unresolved)
			}
		})
	}
}

func TestIsLikelyStdHeaderRejectsNonCanonicalQualifiedHeaders(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, testMainCPPFileName)

	for _, tc := range []struct {
		name    string
		headers []string
	}{
		{
			name: "nested third-party",
			headers: []string{
				"debug/vendor/vector",
				"debug/vendor/map.h",
				"experimental/vendor/filesystem",
				"experimental/vendor/optional",
				"ext/vendor/algorithm",
				"experimental/acme/vector",
				"ext/vendor/optional",
				"ext/pb_ds/vendor/assoc_container.hpp",
				"ext/pb_ds/vendor/exception.hpp",
				"parallel/vendor/base.h",
				"parallel/vendor/search.h",
				"tr1/vendor/complex.h",
			},
		},
		{
			name: "mixed-case",
			headers: []string{
				"Asm/errno.h",
				"Bits/stdc++.h",
				"Debug/map",
				"Debug/map.h",
				"Experimental/filesystem",
				"Ext/pb_ds/assoc_container.hpp",
				"Linux/if.h",
				"Parallel/algorithm",
				"Parallel/algo.h",
				"Parallel/base.h",
				"Sys/socket.h",
				"TR1/complex.h",
				"TR1/regex",
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			for _, header := range tc.headers {
				assertQualifiedHeaderMapsAsDependency(t, repo, source, header)
			}
		})
	}
}

func TestAnalyseTopNIgnoresRecognizedQualifiedCompilerHeaders(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main.cpp"), `#include <debug/safe_iterator.h>
#include <debug/set.h>
#include <backward/hash_map>
#include <experimental/filesystem>
#include <sys/socket.h>
#include <linux/if.h>
#include <linux/netfilter_ipv4/ip_tables.h>
#include <bits/stdc++.h>
#include <asm/errno.h>
#include <asm-generic/errno.h>
#include <asm-generic/bitops/atomic.h>
#include <parallel/algo.h>
#include <parallel/algobase.h>
#include <parallel/base.h>
#include <parallel/find.h>
#include <parallel/queue.h>
#include <ext/pb_ds/assoc_container.hpp>
#include <ext/pb_ds/exception.hpp>
#include <tr1/complex.h>
#include <tr1/ctype.h>
#include <tr1/inttypes.h>
#include <tr1/limits.h>
#include <tr1/stdio.h>
#include <tr1/stdint.h>
#include <tr1/unordered_map.h>
#include <tr1/unordered_set.h>
#include <x86_64-linux-gnu/sys/time.h>
#include <linux/netfilter/nfnetlink.h>
#include <bits/types/struct_timespec.h>
#include <experimental/./optional>
int main() { return 0; }
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}
	for _, dependency := range reportData.Dependencies {
		switch dependency.Name {
		case "asm", "asm-generic", "bits", "debug", "experimental", "ext", "linux", "parallel", "sys", "tr1":
			t.Fatalf("expected GNU compiler header to be ignored, got dependency row %#v", dependency)
		}
	}
}

func TestMapIncludeToDependencyUsesCompileDatabaseProvenance(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "src", testMainCPPFileName)
	vendorRoot := filepath.Join(t.TempDir(), "z-vendor", "include")
	systemRoot := filepath.Join(t.TempDir(), "a-system", "include")
	sdkRoot := filepath.Join(t.TempDir(), "sdk", "usr", "include")
	testutil.MustWriteFile(t, source, `#include <debug/map>
#include <sys/types.h>
int main() { return 0; }
`)
	testutil.MustWriteFile(t, filepath.Join(vendorRoot, "debug", "map"), "// vendor debug header\n")
	testutil.MustWriteFile(t, filepath.Join(systemRoot, "debug", "map"), "// compiler debug header\n")
	testutil.MustWriteFile(t, filepath.Join(sdkRoot, "sys", "types.h"), "// sdk system header\n")

	compileInfo := compileContext{
		SourceFiles: []string{source},
		SourceIncludeDirs: map[string][]includeSearchPath{
			source: {
				{Path: vendorRoot, ProvenanceKnown: true},
				{Path: systemRoot, System: true, ProvenanceKnown: true},
				{Path: sdkRoot, System: true, ProvenanceKnown: true},
			},
		},
	}
	result, err := scanRepo(context.Background(), repo, compileInfo, newDependencyCatalog())
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	dependencies, _ := buildTopCPPDependencies(10, result, report.DefaultRemovalCandidateWeights())
	assertDependencyExportCounts(t, dependencies, map[string]int{
		"debug": 1,
	})
	for _, file := range result.Files {
		for _, include := range file.Includes {
			if include.Dependency == "sys" {
				t.Fatalf("expected -isystem sys/types.h to be suppressed, got %#v", result.Files)
			}
		}
	}
}

func TestShouldSuppressQualifiedStdHeaderHonorsKnownNonSystemProvenance(t *testing.T) {
	if !shouldSuppressQualifiedStdHeader("vector", includeResolution{}) {
		t.Fatalf("expected unqualified standard header suppression to stay unconditional")
	}
	if !shouldSuppressQualifiedStdHeader("debug/map", includeResolution{}) {
		t.Fatalf("expected unresolved qualified compiler header to stay suppressed")
	}
	if shouldSuppressQualifiedStdHeader("debug/map", includeResolution{
		Path:            "/usr/local/include/debug/map",
		Resolved:        true,
		ProvenanceKnown: true,
	}) {
		t.Fatalf("expected known ordinary -I provenance to keep system-looking path reportable")
	}
	if !shouldSuppressQualifiedStdHeader("sys/types.h", includeResolution{
		Path:            "/opt/aarch64-sdk/usr/include/sys/types.h",
		Resolved:        true,
		System:          true,
		ProvenanceKnown: true,
	}) {
		t.Fatalf("expected known -isystem provenance to suppress custom SDK system header")
	}
	if !shouldSuppressQualifiedStdHeader("debug/map", includeResolution{
		Path:     "/usr/local/include/debug/map",
		Resolved: true,
	}) {
		t.Fatalf("expected legacy system-path heuristic to suppress when provenance is unknown")
	}
	if isLikelyMultiarchIncludePrefix("vendor") {
		t.Fatalf("did not expect ordinary vendor prefix to be treated as multiarch")
	}
	if isKnownOSCompilerQualifiedHeader("x86_64-linux-gnu") {
		t.Fatalf("did not expect bare multiarch prefix to be treated as an OS header")
	}
	if isKnownOSCompilerQualifiedHeader("bits") {
		t.Fatalf("did not expect bare bits namespace to be treated as an OS header")
	}
	if isKnownNestedCompilerQualifiedStdHeader("ext", "pb_ds", "custom.h") {
		t.Fatalf("did not expect non-hpp pb_ds header to be treated as compiler header")
	}
	if isKnownNestedCompilerQualifiedStdHeader("ext", "pb_ds", ".hpp") {
		t.Fatalf("did not expect empty pb_ds hpp stem to be treated as compiler header")
	}
	if isKnownCompilerQualifiedStdHeaderLeaf("vendor", "map") {
		t.Fatalf("did not expect unknown namespace to be treated as compiler header")
	}
	if isKnownCompilerQualifiedStdHeaderLeaf("debug", ".h") {
		t.Fatalf("did not expect empty .h stem to be treated as compiler header")
	}
	if isKnownCompilerQualifiedStdHeaderLeaf("debug", "map.hpp") {
		t.Fatalf("did not expect unsupported .hpp leaf to be treated as debug compiler header")
	}
	if isKnownCompilerQualifiedStdHeaderStem("debug", "vendor_map") {
		t.Fatalf("did not expect unknown debug stem to be treated as compiler header")
	}
	if isKnownCompilerQualifiedStdHHeader("experimental", "optional") {
		t.Fatalf("did not expect experimental .h namespace to be accepted by exact h-header allowlists")
	}
	for _, path := range []string{
		"",
		"/workspace/vendor/debug/map",
		"/opt/homebrew/include/c++/v1/vector",
		"/opt/local/include/sys/types.h",
		"/mingw/include/sys/types.h",
		"/mingw64/include/sys/types.h",
		"/toolchains/llvm/lib/clang/18/include/stdint.h",
		"/toolchains/gcc/lib/gcc/x86_64-linux-gnu/13/include/stddef.h",
		"/kits/msvc/include/vector",
	} {
		got := isLikelySystemIncludePath(path)
		want := path != "" && !strings.Contains(path, "/workspace/vendor/")
		if got != want {
			t.Fatalf("unexpected system include path classification for %q: got %v want %v", path, got, want)
		}
	}
}

func TestAnalyseTopNReportsNonCanonicalQualifiedLookalikes(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main.cpp"), `#include <debug/vendor/map.h>
#include <experimental/vendor/filesystem>
#include <ext/pb_ds/map.hpp>
#include <ext/pb_ds/vendor/assoc_container.hpp>
#include <parallel/algo.hpp>
#include <parallel/vendor/base.h>
#include <Debug/map.h>
#include <Experimental/filesystem>
#include <Ext/pb_ds/assoc_container.hpp>
#include <Parallel/base.h>
#include <TR1/complex.h>
int main() { return 0; }
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}

	assertDependencyExportCounts(t, reportData.Dependencies, map[string]int{
		"debug":        2,
		"experimental": 2,
		"ext":          3,
		"parallel":     3,
		"tr1":          1,
	})
}

func TestBuildRequestedDependenciesNoTarget(t *testing.T) {
	deps, warnings := buildRequestedCPPDependencies(language.Request{}, scanResult{})
	if len(deps) != 0 {
		t.Fatalf("expected no dependencies without dependency/topN target")
	}
	if !hasWarning(warnings, "no dependency or top-N target provided") {
		t.Fatalf("expected missing target warning, got %#v", warnings)
	}
}

func TestBuildDependencyReportEmptyAndHelpers(t *testing.T) {
	dep, warnings := buildDependencyReport("fmt", scanResult{}, true)
	if dep.Name != "fmt" || dep.TotalExportsCount != 0 {
		t.Fatalf("unexpected empty dependency report: %#v", dep)
	}
	if !hasWarning(warnings, "no mapped include usage") {
		t.Fatalf("expected no mapped usage warning, got %#v", warnings)
	}

	symbols := buildTopUsedSymbols(map[string]int{"a": 1, "b": 3, "c": 2, "d": 1, "e": 1, "f": 1})
	if len(symbols) != 5 {
		t.Fatalf("expected top symbols to be capped at 5, got %d", len(symbols))
	}

	flattened := flattenImportUses(map[string]*report.ImportUse{"a": {Name: "a"}, "b": {Name: "b"}}, []string{"b", "a", "c"})
	if len(flattened) != 2 || flattened[0].Name != "b" || flattened[1].Name != "a" {
		t.Fatalf("unexpected flattened import ordering: %#v", flattened)
	}
}

func TestCollectDependencyUsageSummaryApply(t *testing.T) {
	summary := collectDependencyUsage("fmt", []fileScan{{
		Path: testMainCPPFileName,
		Includes: []includeRecord{
			{Dependency: "fmt", Header: fmtCoreHeader, Location: report.Location{File: testMainCPPFileName, Line: 1, Column: 1}},
			{Dependency: "FMT", Header: fmtCoreHeader, Location: report.Location{File: testMainCPPFileName, Line: 2, Column: 1}},
			{Dependency: "fmt", Header: "fmt/format.h", Location: report.Location{File: testMainCPPFileName, Line: 3, Column: 1}},
		},
	}})

	var dep report.DependencyReport
	summary.apply(&dep)

	if dep.TotalExportsCount != 2 || dep.UsedExportsCount != 2 || dep.UsedPercent != 100 {
		t.Fatalf("unexpected dependency usage summary: %#v", dep)
	}
	if len(dep.TopUsedSymbols) != 2 || dep.TopUsedSymbols[0].Name != fmtCoreHeader || dep.TopUsedSymbols[0].Count != 2 {
		t.Fatalf("unexpected top used symbols: %#v", dep.TopUsedSymbols)
	}
	if len(dep.UsedImports) != 2 || dep.UsedImports[0].Name != fmtCoreHeader || len(dep.UsedImports[0].Locations) != 2 {
		t.Fatalf("unexpected flattened usage imports: %#v", dep.UsedImports)
	}
}

func TestSourceAndPathHelpers(t *testing.T) {
	if !isCPPSourceOrHeader("x.hpp") || isCPPSourceOrHeader("x.txt") {
		t.Fatalf("unexpected source/header detection")
	}
}

func TestAnalyseWithCanceledContext(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", testMainCPPFileName), fmtCoreIncludeLine)

	_, err := loadCompileContext("")
	if err == nil {
		t.Fatalf("expected loadCompileContext to fail with empty repo path")
	}

	_, err = NewAdapter().Analyse(testutil.CanceledContext(), language.Request{RepoPath: repo, TopN: 1})
	if err == nil {
		t.Fatalf("expected analyse with canceled context to return error")
	}
}

func TestScanRepoWithOutsideCompileSourceWarning(t *testing.T) {
	repo := t.TempDir()
	compileInfo := compileContext{
		HasCompileDatabase: true,
		SourceFiles:        []string{"/tmp/not-in-repo.cpp"},
	}
	result, err := scanRepo(context.Background(), repo, compileInfo, newDependencyCatalog())
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if !hasWarning(result.Warnings, "outside repo boundary") {
		t.Fatalf("expected outside repo warning, got %#v", result.Warnings)
	}
	if !hasWarning(result.Warnings, "falling back to repo scan") {
		t.Fatalf("expected fallback warning, got %#v", result.Warnings)
	}
}

func TestScanRepoFallsBackWhenCompileSourcesEscapeRepo(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", testMainCPPFileName), fmtCoreIncludeLine+"int main() { return 0; }\n")
	compileInfo := compileContext{
		HasCompileDatabase: true,
		SourceFiles:        []string{"/tmp/not-in-repo.cpp"},
	}

	result, err := scanRepo(context.Background(), repo, compileInfo, newDependencyCatalog())
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != filepath.Join("src", testMainCPPFileName) {
		t.Fatalf("expected repo source fallback to scan in-repo file, got %#v", result.Files)
	}
}

func TestScanRepoCanceledAndMissingFileErrors(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", testMainCPPFileName), fmtCoreIncludeLine)

	existingSource := compileContext{
		SourceFiles: []string{filepath.Join(repo, "src", testMainCPPFileName)},
	}
	_, err := scanRepo(testutil.CanceledContext(), repo, existingSource, newDependencyCatalog())
	if err == nil {
		t.Fatalf("expected canceled context error from scanRepo")
	}

	missingSource := compileContext{
		SourceFiles: []string{filepath.Join(repo, "src", "missing.cpp")},
	}
	result, err := scanRepo(context.Background(), repo, missingSource, newDependencyCatalog())
	if err != nil {
		t.Fatalf("expected missing compile-db source to fall back, got err=%v", err)
	}
	if !hasWarning(result.Warnings, "missing from repo") {
		t.Fatalf("expected missing compile-db warning, got %#v", result.Warnings)
	}
	if !hasWarning(result.Warnings, "falling back to repo scan") {
		t.Fatalf("expected fallback warning, got %#v", result.Warnings)
	}
}

func TestRelOrBaseFallbackAndHeaderDetectionBranches(t *testing.T) {
	if relOrBase(".", "./internal/lang/cpp/adapter.go") == "" {
		t.Fatalf("expected relative path from relOrBase")
	}
	if got := relOrBase(".", "\x00"); got != "\x00" {
		t.Fatalf("expected relOrBase to fallback to base on invalid path, got %q", got)
	}
	if isLikelyStdHeader("") {
		t.Fatalf("did not expect empty header to be std header")
	}
	if isLikelyStdHeader("/") {
		t.Fatalf("did not expect slash-only header to be std header")
	}
}

func TestScanRepoNoSources(t *testing.T) {
	repo := t.TempDir()
	result, err := scanRepo(context.Background(), repo, compileContext{}, newDependencyCatalog())
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if !hasWarning(result.Warnings, "no C/C++ source files found") {
		t.Fatalf("expected no-source warning, got %#v", result.Warnings)
	}
}

func TestAnalyseTopNWithUnresolvedWarnings(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main.cpp"), `#include <openssl/ssl.h>
#include SOME_HEADER
#include "missing_header.hpp"
int main() { return 0; }
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in top-N report")
	}
	if !hasWarning(reportData.Warnings, "compile_commands.json not found") {
		t.Fatalf("expected compile_commands warning, got %#v", reportData.Warnings)
	}
	if !hasWarning(reportData.Warnings, "include mapping unresolved") {
		t.Fatalf("expected unresolved mapping warning, got %#v", reportData.Warnings)
	}
}

func hasWarning(warnings []string, needle string) bool {
	return slices.ContainsFunc(warnings, func(warning string) bool {
		return strings.Contains(strings.ToLower(warning), strings.ToLower(needle))
	})
}

func assertQualifiedHeaderMapsAsDependency(t *testing.T, repo, source, header string) {
	t.Helper()
	if isLikelyStdHeader(header) {
		t.Fatalf("did not expect %s to be std header", header)
	}
	want := dependencyFromIncludePath(header)
	catalog := newDependencyCatalog()
	catalog.add(want, "test manifest")

	dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: header, Delimiter: '<'}, nil, catalog)
	if dep != want || unresolved {
		t.Fatalf("expected %s to map to %q, got dep=%q unresolved=%v", header, want, dep, unresolved)
	}
}

func assertDependencyExportCounts(t *testing.T, dependencies []report.DependencyReport, want map[string]int) {
	t.Helper()
	for name, expected := range want {
		if got := dependencyExportCount(dependencies, name); got != expected {
			t.Fatalf("expected %s to be reported %d time(s), got %d with deps %#v", name, expected, got, dependencies)
		}
	}
}

func dependencyExportCount(dependencies []report.DependencyReport, name string) int {
	for _, dependency := range dependencies {
		if dependency.Name == name {
			return dependency.TotalExportsCount
		}
	}
	return 0
}
