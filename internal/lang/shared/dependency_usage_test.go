package shared

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestFirstContentColumn(t *testing.T) {
	if got := FirstContentColumn("\t  abc"); got != 4 {
		t.Fatalf("expected first content column 4, got %d", got)
	}
	if got := FirstContentColumn("   "); got != 1 {
		t.Fatalf("expected fallback column 1, got %d", got)
	}
}

func TestMapSliceAndMapFileUsages(t *testing.T) {
	numbers := []int{1, 2, 3}
	mapped := MapSlice(numbers, func(v int) string { return strings.Repeat("x", v) })
	if !slices.Equal(mapped, []string{"x", "xx", "xxx"}) {
		t.Fatalf("unexpected mapped values: %#v", mapped)
	}

	type raw struct {
		imports []ImportRecord
		usage   map[string]int
	}
	files := []raw{{imports: []ImportRecord{{Dependency: "alpha"}}, usage: map[string]int{"a": 1}}}
	usages := MapFileUsages(files, func(v raw) []ImportRecord { return v.imports }, func(v raw) map[string]int { return v.usage })
	if len(usages) != 1 || len(usages[0].Imports) != 1 || usages[0].Imports[0].Dependency != "alpha" {
		t.Fatalf("unexpected file usages: %#v", usages)
	}
}

func TestCountUsage(t *testing.T) {
	imports := []ImportRecord{{Local: "foo"}, {Local: "bar"}, {Local: "baz", Wildcard: true}}
	content := []byte("foo(); foo(); bar(); baz();")
	usage := CountUsage(content, imports)
	if usage["foo"] != 1 {
		t.Fatalf("expected foo usage 1, got %d", usage["foo"])
	}
	if usage["bar"] != 0 {
		t.Fatalf("expected bar usage 0, got %d", usage["bar"])
	}
	if _, ok := usage["baz"]; ok {
		t.Fatalf("expected wildcard import to be skipped")
	}
}

func TestUsagePatternCacheReusesCompiledRegex(t *testing.T) {
	first := usagePattern("foo")
	second := usagePattern("foo")
	if first != second {
		t.Fatalf("expected cached regex instance reuse")
	}
}

func TestBuildDependencyStats(t *testing.T) {
	files := []FileUsage{
		{
			Imports: []ImportRecord{
				{Dependency: "Alpha", Module: "alpha", Name: "map", Local: "map", Location: report.Location{File: "a.js", Line: 1}},
				{Dependency: "alpha", Module: "alpha", Name: "filter", Local: "filter", Location: report.Location{File: "a.js", Line: 2}},
				{Dependency: "alpha", Module: "alpha", Name: "*", Local: "pkg", Wildcard: true, Location: report.Location{File: "a.js", Line: 3}},
				{Dependency: "beta", Module: "beta", Name: "other", Local: "other", Location: report.Location{File: "b.js", Line: 1}},
			},
			Usage: map[string]int{"map": 2, "filter": 0},
		},
	}
	stats := BuildDependencyStats("alpha", files, strings.ToLower)
	if !stats.HasImports {
		t.Fatalf("expected HasImports=true")
	}
	if stats.TotalCount != 3 {
		t.Fatalf("expected TotalCount=3, got %d", stats.TotalCount)
	}
	if stats.UsedCount != 2 {
		t.Fatalf("expected UsedCount=2, got %d", stats.UsedCount)
	}
	if stats.WildcardImports != 1 {
		t.Fatalf("expected WildcardImports=1, got %d", stats.WildcardImports)
	}
	if len(stats.UsedImports) == 0 || len(stats.UnusedImports) == 0 {
		t.Fatalf("expected both used and unused imports, got used=%#v unused=%#v", stats.UsedImports, stats.UnusedImports)
	}
	if !HasWildcardImport(stats.UsedImports) {
		t.Fatalf("expected wildcard import in used imports")
	}
}

func TestListDependenciesAndTopReports(t *testing.T) {
	files := []FileUsage{
		{Imports: []ImportRecord{{Dependency: "Alpha"}, {Dependency: "beta"}}},
		{Imports: []ImportRecord{{Dependency: "alpha"}, {Dependency: "gamma"}}},
	}
	deps := ListDependencies(files, strings.ToLower)
	if !slices.Equal(deps, []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("unexpected dependencies: %#v", deps)
	}

	reports, warnings := BuildTopReports(2, deps, func(dep string) (report.DependencyReport, []string) {
		score := 10.0
		switch dep {
		case "alpha":
			score = 20
		case "beta":
			score = 80
		case "gamma":
			score = 40
		}
		return report.DependencyReport{Name: dep, UsedPercent: 100 - score, TotalExportsCount: 100}, []string{dep + "-warn"}
	})
	if len(reports) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(reports))
	}
	if reports[0].Name != "beta" {
		t.Fatalf("expected highest waste report first, got %q", reports[0].Name)
	}
	if len(warnings) != 3 {
		t.Fatalf("expected one warning per dependency, got %d", len(warnings))
	}
}

func TestBuildRequestedDependenciesDependencyTarget(t *testing.T) {
	dependencyCalled := false
	topCalled := false
	reports, warnings := BuildRequestedDependencies(
		language.Request{Dependency: "Alpha"},
		1,
		strings.ToLower,
		func(dependency string, scan int) (report.DependencyReport, []string) {
			dependencyCalled = true
			if scan != 1 {
				t.Fatalf("unexpected scan value: %d", scan)
			}
			return report.DependencyReport{Name: dependency}, []string{"dep-warning"}
		},
		func(_, _ int) ([]report.DependencyReport, []string) {
			topCalled = true
			return nil, nil
		},
	)
	if !dependencyCalled || topCalled {
		t.Fatalf("expected only dependency builder to run (dependency=%v top=%v)", dependencyCalled, topCalled)
	}
	if len(reports) != 1 || reports[0].Name != "alpha" {
		t.Fatalf("unexpected dependency reports: %#v", reports)
	}
	if !slices.Equal(warnings, []string{"dep-warning"}) {
		t.Fatalf("unexpected dependency warnings: %#v", warnings)
	}
}

func TestBuildRequestedDependenciesTopNTarget(t *testing.T) {
	dependencyCalled := false
	topCalled := false
	reports, warnings := BuildRequestedDependencies(
		language.Request{TopN: 3},
		2,
		strings.ToLower,
		func(string, int) (report.DependencyReport, []string) {
			dependencyCalled = true
			return report.DependencyReport{}, nil
		},
		func(topN, scan int) ([]report.DependencyReport, []string) {
			topCalled = true
			if topN != 3 || scan != 2 {
				t.Fatalf("unexpected topN/scan values: %d/%d", topN, scan)
			}
			return []report.DependencyReport{{Name: "top"}}, []string{"top-warning"}
		},
	)
	if dependencyCalled || !topCalled {
		t.Fatalf("expected only top builder to run (dependency=%v top=%v)", dependencyCalled, topCalled)
	}
	if len(reports) != 1 || reports[0].Name != "top" {
		t.Fatalf("unexpected top-N reports: %#v", reports)
	}
	if !slices.Equal(warnings, []string{"top-warning"}) {
		t.Fatalf("unexpected top-N warnings: %#v", warnings)
	}
}

func TestBuildRequestedDependenciesMissingTarget(t *testing.T) {
	dependencyCalled := false
	topCalled := false
	reports, warnings := BuildRequestedDependencies(
		language.Request{},
		0,
		strings.ToLower,
		func(string, int) (report.DependencyReport, []string) {
			dependencyCalled = true
			return report.DependencyReport{}, nil
		},
		func(_, _ int) ([]report.DependencyReport, []string) {
			topCalled = true
			return nil, nil
		},
	)
	if dependencyCalled || topCalled {
		t.Fatalf("expected no builders to run (dependency=%v top=%v)", dependencyCalled, topCalled)
	}
	if reports != nil {
		t.Fatalf("expected nil reports when no target provided, got %#v", reports)
	}
	if !slices.Equal(warnings, []string{"no dependency or top-N target provided"}) {
		t.Fatalf("unexpected no-target warnings: %#v", warnings)
	}
}

func TestSortReportsByWasteAndHelpers(t *testing.T) {
	reports := []report.DependencyReport{
		{Name: "unknown", TotalExportsCount: 0},
		{Name: "b", UsedPercent: 20, TotalExportsCount: 10},
		{Name: "a", UsedPercent: 20, TotalExportsCount: 10},
	}
	SortReportsByWaste(reports)
	if reports[0].Name != "a" {
		t.Fatalf("expected alpha tie-break on name, got %q", reports[0].Name)
	}

	if score, ok := WasteScore(report.DependencyReport{TotalExportsCount: 0}); ok || score != -1 {
		t.Fatalf("expected unknown waste score for empty exports")
	}
	if score, ok := WasteScore(report.DependencyReport{TotalExportsCount: 10, UsedPercent: 30}); !ok || score != 70 {
		t.Fatalf("unexpected waste score: score=%v ok=%v", score, ok)
	}

	keys := SortedKeys(map[string]struct{}{"z": {}, "a": {}})
	if !slices.Equal(keys, []string{"a", "z"}) {
		t.Fatalf("unexpected sorted keys: %#v", keys)
	}
	if got := SortedKeys(nil); got != nil {
		t.Fatalf("expected nil sorted keys for nil map, got %#v", got)
	}

	if used := calculateUsedPercent(0, 0); used != 0 {
		t.Fatalf("expected used percent 0 for zero totals, got %f", used)
	}

	dest := map[string]*report.ImportUse{}
	addImport(dest, report.ImportUse{Name: "map", Module: "lodash", Locations: []report.Location{{File: "a.js", Line: 1}}})
	addImport(dest, report.ImportUse{Name: "map", Module: "lodash", Locations: []report.Location{{File: "b.js", Line: 2}}})
	if len(dest["lodash:map"].Locations) != 2 {
		t.Fatalf("expected duplicate import locations to merge, got %#v", dest["lodash:map"])
	}
}

func TestSortReportsByWasteWithCustomWeights(t *testing.T) {
	reports := []report.DependencyReport{
		{Name: "high-impact", UsedExportsCount: 50, TotalExportsCount: 100, UsedPercent: 50},
		{Name: "high-usage-waste", UsedExportsCount: 1, TotalExportsCount: 10, UsedPercent: 10},
	}
	SortReportsByWaste(reports, report.RemovalCandidateWeights{
		Usage:      1,
		Impact:     0,
		Confidence: 0,
	})
	if reports[0].Name != "high-usage-waste" {
		t.Fatalf("expected usage-only scoring to rank high usage waste first, got %q", reports[0].Name)
	}
}

func TestDetectionHelpers(t *testing.T) {
	if got := DefaultRepoPath(""); got != "." {
		t.Fatalf("expected empty repo path to default to '.', got %q", got)
	}
	if got := DefaultRepoPath("repo"); got != "repo" {
		t.Fatalf("expected non-empty repo path to pass through, got %q", got)
	}
	if got := NormalizeDependencyID(" Example.Dependency "); got != "example.dependency" {
		t.Fatalf("unexpected normalized dependency ID: %q", got)
	}

	detection := FinalizeDetection("repo", language.Detection{Matched: true, Confidence: 20}, map[string]struct{}{})
	if detection.Confidence != 35 {
		t.Fatalf("expected floor confidence 35, got %d", detection.Confidence)
	}
	if !slices.Equal(detection.Roots, []string{"repo"}) {
		t.Fatalf("expected fallback root assignment, got %#v", detection.Roots)
	}

	detection = FinalizeDetection("repo", language.Detection{Matched: true, Confidence: 120}, map[string]struct{}{"b": {}, "a": {}})
	if detection.Confidence != 95 {
		t.Fatalf("expected confidence cap 95, got %d", detection.Confidence)
	}
	if !slices.Equal(detection.Roots, []string{"a", "b"}) {
		t.Fatalf("expected sorted roots, got %#v", detection.Roots)
	}

	matched, err := DetectMatched(context.Background(), "repo", func(context.Context, string) (language.Detection, error) {
		return language.Detection{Matched: true}, nil
	})
	if err != nil || !matched {
		t.Fatalf("expected matched detection from helper, matched=%v err=%v", matched, err)
	}

	boom := errors.New("boom")
	if _, err := DetectMatched(context.Background(), "repo", func(context.Context, string) (language.Detection, error) {
		return language.Detection{}, boom
	}); !errors.Is(err, boom) {
		t.Fatalf("expected detect helper to propagate error, got %v", err)
	}
}

func TestShouldSkipDir(t *testing.T) {
	if !ShouldSkipDir(".git", nil) {
		t.Fatalf("expected baseline skip for .git")
	}
	if !ShouldSkipCommonDir(".git") {
		t.Fatalf("expected common skip to include baseline .git")
	}
	if !ShouldSkipCommonDir(".cache") {
		t.Fatalf("expected common skip for .cache")
	}
	if !ShouldSkipDir(".venv", map[string]bool{".venv": true}) {
		t.Fatalf("expected language-specific skip for .venv")
	}
	if ShouldSkipDir("src", nil) {
		t.Fatalf("did not expect src to be skipped")
	}
}

func TestApplyRootSignals(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "CMakeLists.txt"), []byte("project(x)\n"), 0o600); err != nil {
		t.Fatalf("write cmake: %v", err)
	}

	detection := language.Detection{}
	roots := map[string]struct{}{}
	err := ApplyRootSignals(repo, []RootSignal{
		{Name: "CMakeLists.txt", Confidence: 40},
		{Name: "missing.file", Confidence: 99},
	}, &detection, roots)
	if err != nil {
		t.Fatalf("apply root signals: %v", err)
	}
	if !detection.Matched || detection.Confidence != 40 {
		t.Fatalf("unexpected detection from root signals: %#v", detection)
	}
	if _, ok := roots[repo]; !ok {
		t.Fatalf("expected repo root to be added")
	}
}

func TestWalkRepoFiles(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "ignored.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write ignored.txt: %v", err)
	}

	var visited []string
	err := WalkRepoFiles(context.Background(), repo, 1, ShouldSkipCommonDir, func(path string, entry fs.DirEntry) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo files: %v", err)
	}
	if len(visited) != 1 {
		t.Fatalf("expected max-file cut-off to limit to 1 visit, got %#v", visited)
	}
}

func TestWalkRepoFilesCanceled(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if WalkRepoFiles(testutil.CanceledContext(), repo, 0, nil, func(path string, entry fs.DirEntry) error { return nil }) == nil {
		t.Fatalf("expected context canceled error from WalkRepoFiles")
	}
}

func TestIsPathWithin(t *testing.T) {
	repo := t.TempDir()
	inside := filepath.Join(repo, "src", "a.txt")
	outside := filepath.Join(repo, "..", "outside.txt")

	if !IsPathWithin(repo, inside) {
		t.Fatalf("expected inside path to be within repo")
	}
	if IsPathWithin(repo, outside) {
		t.Fatalf("expected outside path to be outside repo")
	}
	if IsPathWithin("\x00", inside) {
		t.Fatalf("expected invalid root to return false")
	}
}
