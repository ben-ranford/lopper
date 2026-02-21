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
	fmtCoreIncludeLine  = "#include <fmt/core.h>\n"
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
	dirs := extractIncludeDirs([]string{
		"-I", "include",
		"-Ivendor/include",
		"-isystem", "/usr/include",
		"-iquote", "headers",
		"-isystem/opt/include",
		"-iquotequoted",
		"-Ivendor/include",
		"-I",
		"",
	}, "/repo")
	want := []string{
		"/opt/include",
		"/repo/headers",
		"/repo/include",
		"/repo/quoted",
		"/repo/vendor/include",
		"/usr/include",
	}
	if !slices.Equal(dirs, want) {
		t.Fatalf("unexpected include dirs: got %#v want %#v", dirs, want)
	}
}

func TestParseIncludesBranches(t *testing.T) {
	content := []byte(`#include <fmt/core.h>
#include "local/header.hpp"
#include SOME_MACRO_HEADER
#include <broken
`)
	includes := parseIncludes(content)
	if len(includes) != 4 {
		t.Fatalf("expected four includes, got %d", len(includes))
	}
}

func TestMapIncludeToDependencyBranches(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "src", testMainCPPFileName)
	testutil.MustWriteFile(t, source, "#include \"header.hpp\"\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "header.hpp"), "// local")

	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "", Delimiter: '<'}, nil); dep != "" || !unresolved {
		t.Fatalf("expected empty header unresolved")
	}
	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "vector", Delimiter: '<'}, nil); dep != "" || unresolved {
		t.Fatalf("expected std header to be ignored")
	}
	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "header.hpp", Delimiter: '"'}, nil); dep != "" || unresolved {
		t.Fatalf("expected local quoted header to be ignored")
	}
	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "missing.hpp", Delimiter: '"'}, nil); dep != "" || !unresolved {
		t.Fatalf("expected unresolved quoted include")
	}
	if dep, unresolved := mapIncludeToDependency(repo, source, parsedInclude{Path: "openssl/ssl.h", Delimiter: '<'}, nil); dep != "openssl" || unresolved {
		t.Fatalf("expected mapped dependency openssl, got dep=%q unresolved=%v", dep, unresolved)
	}
}

func TestDependencyFromIncludePathAndStdHeader(t *testing.T) {
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
	if !isLikelyStdHeader("sys/types.h") {
		t.Fatalf("expected sys/types.h to be std header")
	}
	if isLikelyStdHeader("thirdparty/custom.hpp") {
		t.Fatalf("did not expect thirdparty/custom.hpp to be std header")
	}
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
	dep, warnings := buildDependencyReport("fmt", scanResult{})
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

	flattened := flattenImportUses(map[string]*report.ImportUse{
		"a": {Name: "a"},
		"b": {Name: "b"},
	}, []string{"b", "a", "c"})
	if len(flattened) != 2 || flattened[0].Name != "b" || flattened[1].Name != "a" {
		t.Fatalf("unexpected flattened import ordering: %#v", flattened)
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
	result, err := scanRepo(context.Background(), repo, compileContext{
		HasCompileDatabase: true,
		SourceFiles:        []string{"/tmp/not-in-repo.cpp"},
	})
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if !hasWarning(result.Warnings, "outside repo boundary") {
		t.Fatalf("expected outside repo warning, got %#v", result.Warnings)
	}
}

func TestScanRepoCanceledAndMissingFileErrors(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", testMainCPPFileName), fmtCoreIncludeLine)

	_, err := scanRepo(testutil.CanceledContext(), repo, compileContext{
		SourceFiles: []string{filepath.Join(repo, "src", testMainCPPFileName)},
	})
	if err == nil {
		t.Fatalf("expected canceled context error from scanRepo")
	}

	_, err = scanRepo(context.Background(), repo, compileContext{
		SourceFiles: []string{filepath.Join(repo, "src", "missing.cpp")},
	})
	if err == nil {
		t.Fatalf("expected missing source file error from scanRepo")
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
	result, err := scanRepo(context.Background(), repo, compileContext{})
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
