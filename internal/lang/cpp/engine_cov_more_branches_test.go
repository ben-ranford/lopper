package cpp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const cppMainSourcePath = "src/main.cpp"

func TestCPPDetectAndAnalyseMoreBranches(t *testing.T) {
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	testutil.MustWriteFile(t, repoFile, "x")
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), repoFile); err == nil {
		t.Fatalf("expected detect-with-confidence error for non-directory repo path")
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
		t.Fatalf("expected analyse to fail on invalid repo path")
	}
}

func TestCPPLoadCompileContextCapAndHelpers(t *testing.T) {
	repo := t.TempDir()
	for i := 0; i <= maxCompileDatabases; i++ {
		dir := filepath.Join(repo, fmt.Sprintf("build-%02d", i))
		testutil.MustWriteFile(t, filepath.Join(dir, compileCommandsFile), `[
  {"directory":".","file":"`+cppMainSourcePath+`","command":"c++ -Iinclude -c `+cppMainSourcePath+`"}
]`)
	}

	compileInfo, err := loadCompileContext(repo)
	if err != nil {
		t.Fatalf("loadCompileContext: %v", err)
	}
	if !compileInfo.HasCompileDatabase {
		t.Fatalf("expected compile database discovery")
	}
	if len(compileInfo.IncludeDirs) == 0 || len(compileInfo.SourceFiles) == 0 {
		t.Fatalf("expected include dirs and source files from command parsing, got %#v", compileInfo)
	}

	result := &scanResult{UnresolvedSamples: []string{"a", "b", "c", "d", "e"}}
	appendSampleWarnings(result, []string{"f"})
	if len(result.UnresolvedSamples) != maxWarningSamples {
		t.Fatalf("expected unresolved sample cap to hold, got %#v", result.UnresolvedSamples)
	}

	if got := relOrBase(repo, filepath.Join(repo, "src", testMainCPPFileName)); got != filepath.Join("src", testMainCPPFileName) {
		t.Fatalf("expected relative path from relOrBase, got %q", got)
	}
	if got := relOrBase("\x00", filepath.Join(repo, "src", testMainCPPFileName)); got != testMainCPPFileName {
		t.Fatalf("expected relOrBase to fall back to base name, got %q", got)
	}
	if got := dependencyFromIncludePath("."); got != "" {
		t.Fatalf("expected dot include path to map empty, got %q", got)
	}
	if _, ok := parseIncludeLine("#include", 1); ok {
		t.Fatalf("expected bare include directive to be ignored")
	}
	if _, ok := parseIncludeLine("#include <>", 1); ok {
		t.Fatalf("expected empty delimited include to be ignored")
	}
	if _, ok := parseIncludeLine("#define VALUE 1", 1); ok {
		t.Fatalf("expected non-include preprocessor line to be ignored")
	}
}

func TestCPPRequestedDependencyMoreBranches(t *testing.T) {
	catalog := newDependencyCatalog()
	catalog.add("fmt", "vcpkg manifest")
	scan := scanResult{
		Catalog: catalog,
		Files: []fileScan{{
			Path: cppMainSourcePath,
			Includes: []includeRecord{
				{Dependency: "fmt", Header: "fmt/core.h", Location: report.Location{File: cppMainSourcePath, Line: 1, Column: 1}},
				{Dependency: "zlib", Header: "zlib.h", Location: report.Location{File: cppMainSourcePath, Line: 2, Column: 1}},
			},
		}},
	}

	dependencies, warnings := buildRequestedCPPDependencies(language.Request{Dependency: "FMT"}, scan)
	if len(warnings) != 0 || len(dependencies) != 1 || dependencies[0].Name != "fmt" {
		t.Fatalf("unexpected dependency selection result: deps=%#v warnings=%#v", dependencies, warnings)
	}
}

func TestCPPAdditionalCoverageBranches(t *testing.T) {
	t.Run("makefile detection and normalize failure", testCPPMakefileDetectionAndNormalizeFailure)
	t.Run("compile database read errors bubble out", testCPPCompileDatabaseReadErrorsBubbleOut)
	t.Run("include mapping fallback branches", testCPPIncludeMappingFallbackBranches)
	t.Run("dependency usage warning branches", testCPPDependencyUsageWarningBranches)
}

func testCPPMakefileDetectionAndNormalizeFailure(t *testing.T) {
	detection := language.Detection{}
	roots := map[string]struct{}{}
	makefilePath := filepath.Join(t.TempDir(), "Makefile")
	updateDetection(makefilePath, &detection, roots)
	if !detection.Matched || detection.Confidence == 0 || len(roots) != 1 {
		t.Fatalf("expected Makefile detection signal, got detection=%#v roots=%#v", detection, roots)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})
	deadDir := filepath.Join(t.TempDir(), "dead")
	if err := os.MkdirAll(deadDir, 0o755); err != nil {
		t.Fatalf("mkdir dead dir: %v", err)
	}
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir dead dir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove dead dir: %v", err)
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{}); err == nil {
		t.Fatalf("expected analyse to fail when cwd cannot be resolved")
	}
}

func TestCPPUpdateDetectionRecognizesManifestAndSourceSignals(t *testing.T) {
	repo := t.TempDir()
	detection := language.Detection{}
	roots := map[string]struct{}{}

	updateDetection(filepath.Join(repo, "native", vcpkgManifestFile), &detection, roots)
	updateDetection(filepath.Join(repo, "native", vcpkgLockFile), &detection, roots)
	updateDetection(filepath.Join(repo, "include", "widget.hpp"), &detection, roots)

	if !detection.Matched {
		t.Fatalf("expected detection to be marked as matched")
	}
	if detection.Confidence != 22 {
		t.Fatalf("expected manifest and header confidence to accumulate, got %d", detection.Confidence)
	}
	if len(roots) != 1 {
		t.Fatalf("expected manifest root to be recorded once, got %#v", roots)
	}
	if _, ok := roots[filepath.Join(repo, "native")]; !ok {
		t.Fatalf("expected manifest directory root to be recorded, got %#v", roots)
	}
}

func testCPPCompileDatabaseReadErrorsBubbleOut(t *testing.T) {
	repo := t.TempDir()
	compileDB := filepath.Join(repo, compileCommandsFile)
	if err := os.WriteFile(compileDB, []byte("[]"), 0o000); err != nil {
		t.Fatalf("write unreadable compile db: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(compileDB, 0o600); err != nil {
			t.Fatalf("restore compile db perms: %v", err)
		}
	})
	if _, err := loadCompileContext(repo); err == nil {
		t.Fatalf("expected unreadable compile database to fail load")
	}
	if _, err := collectCompileDatabase(compileDB, repo, map[string]struct{}{}, map[string]struct{}{}); err == nil {
		t.Fatalf("expected unreadable compile database to fail direct collection")
	}
}

func testCPPIncludeMappingFallbackBranches(t *testing.T) {
	if dirs := extractIncludeDirs([]string{" ", "-I"}, "/repo"); len(dirs) != 0 {
		t.Fatalf("expected blank args and missing include values to be ignored, got %#v", dirs)
	}

	if dep, unresolved := mapIncludeToDependency("/repo", "/repo/main.cpp", parsedInclude{Path: ".", Delimiter: '<'}, nil, newDependencyCatalog()); dep != "" || !unresolved {
		t.Fatalf("expected dot include to stay unresolved, got dep=%q unresolved=%v", dep, unresolved)
	}
	if got := dependencyFromIncludePath("./"); got != "" {
		t.Fatalf("expected dot-slash include path to map empty, got %q", got)
	}
	if got := dependencyFromIncludePath(".h"); got != "" {
		t.Fatalf("expected extension-only include path to map empty, got %q", got)
	}
	if isLikelyStdHeader("   ") {
		t.Fatalf("expected blank std header candidate to be rejected")
	}

	custom := &report.RemovalCandidateWeights{Usage: 1, Impact: 2, Confidence: 3}
	got := resolveRemovalCandidateWeights(custom)
	if got == report.DefaultRemovalCandidateWeights() {
		t.Fatalf("expected non-nil weights to normalize instead of using defaults")
	}
}

func testCPPDependencyUsageWarningBranches(t *testing.T) {
	catalog := newDependencyCatalog()
	catalog.add("fmt", "vcpkg manifest")
	catalog.add("fmt", "conan.lock")

	if got := buildDependencyUsageWarnings("fmt", catalog, true, 1, true); len(got) != 0 {
		t.Fatalf("expected used dependency to have no warning, got %#v", got)
	}
	if got := buildDependencyUsageWarnings("fmt", catalog, true, 0, false); len(got) != 0 {
		t.Fatalf("expected warn-on-no-usage=false to suppress warnings, got %#v", got)
	}
	if got := buildDependencyUsageWarnings("openssl", catalog, false, 0, true); len(got) != 1 || got[0] != "no mapped include usage found for dependency openssl" {
		t.Fatalf("unexpected undeclared no-usage warning: %#v", got)
	}
	if got := buildDependencyUsageWarnings("fmt", newDependencyCatalog(), true, 0, true); len(got) != 1 || got[0] != "no mapped include usage found for dependency fmt" {
		t.Fatalf("unexpected declared warning without sources: %#v", got)
	}
	if got := buildDependencyUsageWarnings("fmt", catalog, true, 0, true); len(got) != 1 || got[0] != "dependency fmt is declared in conan.lock + vcpkg manifest but has no mapped include usage" {
		t.Fatalf("unexpected declared no-usage warning: %#v", got)
	}
}

func TestCPPBuildTopDependenciesWarnsWhenNoDataIsAvailable(t *testing.T) {
	dependencies, warnings := buildTopCPPDependencies(5, scanResult{}, report.DefaultRemovalCandidateWeights())
	if len(dependencies) != 0 {
		t.Fatalf("expected no dependencies without scan data, got %#v", dependencies)
	}
	if len(warnings) != 1 || warnings[0] != "no dependency data available for top-N ranking" {
		t.Fatalf("unexpected no-data warning: %#v", warnings)
	}
}
