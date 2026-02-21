package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestHelperFunctions(t *testing.T) {
	if !isMultiLanguage(" ALL ") {
		t.Fatalf("expected all language match")
	}
	if isMultiLanguage("js-ts") {
		t.Fatalf("did not expect single-language mode")
	}

	roots := candidateRoots(nil, "/repo")
	if len(roots) != 1 || roots[0] != "/repo" {
		t.Fatalf("unexpected candidate roots: %#v", roots)
	}
	if got := normalizeCandidateRoot("/repo", "sub"); got != filepath.Join("/repo", "sub") {
		t.Fatalf("unexpected normalized root: %q", got)
	}
}

func TestAdjustRelativeLocationsAndLanguage(t *testing.T) {
	deps := []report.DependencyReport{{
		UsedImports:   []report.ImportUse{{Locations: []report.Location{{File: "src/main.js", Line: 1}}}},
		UnusedImports: []report.ImportUse{{Locations: []report.Location{{File: "/abs/file.js", Line: 2}}}},
	}}
	applyLanguageID(deps, "js-ts")
	if deps[0].Language != "js-ts" {
		t.Fatalf("expected language to be applied")
	}
	adjustRelativeLocations("/repo", "/repo/packages/a", deps)
	if deps[0].UsedImports[0].Locations[0].File != filepath.Clean("packages/a/src/main.js") {
		t.Fatalf("expected relative file adjustment, got %q", deps[0].UsedImports[0].Locations[0].File)
	}
	if deps[0].UnusedImports[0].Locations[0].File != "/abs/file.js" {
		t.Fatalf("expected absolute file path unchanged")
	}
}

func TestMergeDependencyCoreFields(t *testing.T) {
	left := report.DependencyReport{
		Language:          "js-ts",
		Name:              "lodash",
		UsedExportsCount:  1,
		TotalExportsCount: 4,
		UsedImports:       []report.ImportUse{{Name: "map", Module: "lodash", Locations: []report.Location{{File: "a.js", Line: 1}}}},
		UnusedImports:     []report.ImportUse{{Name: "filter", Module: "lodash", Locations: []report.Location{{File: "a.js", Line: 2}}}},
		UnusedExports:     []report.SymbolRef{{Name: "chunk", Module: "lodash"}},
		RiskCues:          []report.RiskCue{{Code: "dynamic", Severity: "low", Message: "x"}},
		Recommendations:   []report.Recommendation{{Code: "rec-a", Priority: "high", Message: "x"}},
		TopUsedSymbols:    []report.SymbolUsage{{Name: "map", Count: 2}},
		RuntimeUsage: &report.RuntimeUsage{
			LoadCount:   1,
			Correlation: report.RuntimeCorrelationRuntimeOnly,
			RuntimeOnly: true,
		},
	}
	right := report.DependencyReport{
		Language:          "js-ts",
		Name:              "lodash",
		UsedExportsCount:  2,
		TotalExportsCount: 6,
		UsedImports:       []report.ImportUse{{Name: "map", Module: "lodash", Locations: []report.Location{{File: "b.js", Line: 1}}}},
		UnusedImports:     []report.ImportUse{{Name: "map", Module: "lodash", Locations: []report.Location{{File: "b.js", Line: 3}}}},
		UnusedExports:     []report.SymbolRef{{Name: "uniq", Module: "lodash"}},
		RiskCues:          []report.RiskCue{{Code: "native", Severity: "high", Message: "y"}},
		Recommendations:   []report.Recommendation{{Code: "rec-b", Priority: "low", Message: "y"}},
		TopUsedSymbols:    []report.SymbolUsage{{Name: "map", Count: 1}, {Name: "filter", Count: 1}},
		RuntimeUsage: &report.RuntimeUsage{
			LoadCount:   2,
			Correlation: report.RuntimeCorrelationOverlap,
		},
	}

	merged := mergeDependency(left, right)
	if merged.UsedExportsCount != 3 || merged.TotalExportsCount != 10 {
		t.Fatalf("unexpected merged export counts: %+v", merged)
	}
	if merged.RuntimeUsage == nil || merged.RuntimeUsage.LoadCount != 3 || merged.RuntimeUsage.RuntimeOnly {
		t.Fatalf("unexpected merged runtime usage: %#v", merged.RuntimeUsage)
	}
	if merged.RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected overlap correlation, got %#v", merged.RuntimeUsage)
	}
	if len(merged.RiskCues) != 2 || len(merged.Recommendations) != 2 {
		t.Fatalf("expected merged cues and recommendations")
	}
	if len(merged.TopUsedSymbols) == 0 || merged.TopUsedSymbols[0].Name != "map" {
		t.Fatalf("expected merged top symbols to include map first, got %#v", merged.TopUsedSymbols)
	}
}

func TestMergeDependencyFiltersUsedFromUnused(t *testing.T) {
	left := report.DependencyReport{
		Language:      "js-ts",
		Name:          "lodash",
		UsedImports:   []report.ImportUse{{Name: "map", Module: "lodash", Locations: []report.Location{{File: "a.js", Line: 1}}}},
		UnusedImports: []report.ImportUse{{Name: "filter", Module: "lodash", Locations: []report.Location{{File: "a.js", Line: 2}}}},
	}
	right := report.DependencyReport{
		Language:      "js-ts",
		Name:          "lodash",
		UsedImports:   []report.ImportUse{{Name: "map", Module: "lodash", Locations: []report.Location{{File: "b.js", Line: 1}}}},
		UnusedImports: []report.ImportUse{{Name: "map", Module: "lodash", Locations: []report.Location{{File: "b.js", Line: 3}}}},
	}
	merged := mergeDependency(left, right)
	for _, imp := range merged.UnusedImports {
		if imp.Name == "map" {
			t.Fatalf("expected used import overlaps to be filtered from unused imports")
		}
	}
}

func TestMergeHelpersSortBranches(t *testing.T) {
	refs := mergeSymbolRefs([]report.SymbolRef{{Name: "a", Module: "m"}}, []report.SymbolRef{{Name: "b", Module: "m"}})
	if len(refs) != 2 {
		t.Fatalf("expected merged symbol refs, got %#v", refs)
	}
	cues := mergeRiskCues([]report.RiskCue{{Code: "b", Severity: "low"}}, []report.RiskCue{{Code: "a", Severity: "high"}})
	if len(cues) != 2 || cues[0].Code != "a" {
		t.Fatalf("expected sorted risk cues, got %#v", cues)
	}
	recs := mergeRecommendations([]report.Recommendation{{Code: "a", Priority: "low"}}, []report.Recommendation{{Code: "b", Priority: "high"}})
	if len(recs) != 2 || recs[0].Code != "b" {
		t.Fatalf("expected high-priority recommendation first, got %#v", recs)
	}
}

func TestAnnotateRuntimeTrace(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{{Name: "lodash", UsedImports: []report.ImportUse{{Name: "map", Module: "lodash"}}}},
	}
	annotated, err := annotateRuntimeTraceIfPresent("", rep)
	if err != nil {
		t.Fatalf("annotate without trace: %v", err)
	}
	if annotated.Dependencies[0].RuntimeUsage != nil {
		t.Fatalf("expected no runtime usage without trace file")
	}

	path := filepath.Join(t.TempDir(), "trace.ndjson")
	trace := []byte(`{"module":"lodash/map"}` + "\n")
	if err := os.WriteFile(path, trace, 0o600); err != nil {
		t.Fatalf("write runtime trace: %v", err)
	}
	annotated, err = annotateRuntimeTraceIfPresent(path, rep)
	if err != nil {
		t.Fatalf("annotate with trace: %v", err)
	}
	if annotated.Dependencies[0].RuntimeUsage == nil {
		t.Fatalf("expected runtime usage annotation")
	}
}

func TestServiceAnalyseErrorBranches(t *testing.T) {
	svc := &Service{InitErr: errors.New("init error")}
	if _, err := svc.Analyse(context.Background(), Request{RepoPath: ".", Language: "all"}); err == nil {
		t.Fatalf("expected analyse to fail on init error")
	}

	reg := language.NewRegistry()
	if err := reg.Register(testServiceAdapter{
		id:     "js-ts",
		detect: language.Detection{Matched: true, Confidence: 10},
		err:    errors.New("analyse failed"),
	}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc = &Service{Registry: reg}
	if _, err := svc.Analyse(context.Background(), Request{RepoPath: ".", Language: "js-ts", TopN: 1}); err == nil {
		t.Fatalf("expected analyse error in single-language mode")
	}
}

func TestRunCandidatesAndDuplicateRootsBranches(t *testing.T) {
	candidate := language.Candidate{
		Adapter: testServiceAdapter{
			id:     "ok",
			detect: language.Detection{Matched: true, Confidence: 50},
			analyse: report.Report{
				Dependencies: []report.DependencyReport{{Name: "dep"}},
			},
		},
		Detection: language.Detection{
			Matched:    true,
			Confidence: 50,
			Roots:      []string{".", "."},
		},
	}
	svc := &Service{}
	reports, _, err := svc.runCandidateOnRoots(context.Background(), Request{RepoPath: ".", Language: "all", TopN: 1}, ".", candidate)
	if err != nil {
		t.Fatalf("runCandidateOnRoots: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected duplicate roots to be de-duped, got %d reports", len(reports))
	}

	broken := language.Candidate{
		Adapter: testServiceAdapter{id: "broken", detect: language.Detection{Matched: true}, err: errors.New("boom")},
		Detection: language.Detection{
			Matched: true,
		},
	}
	if _, _, err := svc.runCandidates(context.Background(), Request{RepoPath: ".", Language: "js-ts", TopN: 1}, ".", []language.Candidate{broken}); err == nil {
		t.Fatalf("expected runCandidates error for single-language adapter failure")
	}
}

func TestMergeSortAndPriorityHelperBranches(t *testing.T) {
	imports := mergeImportUses(
		[]report.ImportUse{{Module: "b", Name: "x"}},
		[]report.ImportUse{{Module: "a", Name: "x"}},
	)
	if len(imports) != 2 || imports[0].Module != "a" {
		t.Fatalf("expected import sort by module, got %#v", imports)
	}

	refs := mergeSymbolRefs(
		[]report.SymbolRef{{Module: "z", Name: "a"}},
		[]report.SymbolRef{{Module: "a", Name: "a"}},
	)
	if len(refs) != 2 || refs[0].Module != "a" {
		t.Fatalf("expected symbol ref sort by module, got %#v", refs)
	}

	recs := mergeRecommendations(
		[]report.Recommendation{{Code: "b", Priority: "medium"}},
		[]report.Recommendation{{Code: "a", Priority: "medium"}},
	)
	if len(recs) != 2 || recs[0].Code != "a" {
		t.Fatalf("expected recommendation tie-break sort by code, got %#v", recs)
	}

	if rank := recommendationPriorityRank("unknown"); rank != 3 {
		t.Fatalf("expected unknown priority rank 3, got %d", rank)
	}
}

func TestResolveRemovalCandidateWeights(t *testing.T) {
	defaults := report.DefaultRemovalCandidateWeights()
	if got := resolveRemovalCandidateWeights(nil); got != defaults {
		t.Fatalf("expected default removal candidate weights, got %#v", got)
	}
	custom := &report.RemovalCandidateWeights{Usage: 2, Impact: 3, Confidence: 5}
	got := resolveRemovalCandidateWeights(custom)
	if got.Usage != 0.2 || got.Impact != 0.3 || got.Confidence != 0.5 {
		t.Fatalf("expected normalized custom weights, got %#v", got)
	}
}

func TestRuntimeUsageSignalsAndMergeCorrelation(t *testing.T) {
	cases := []struct {
		name       string
		usage      *report.RuntimeUsage
		hasStatic  bool
		hasRuntime bool
	}{
		{name: "nil", usage: nil, hasStatic: false, hasRuntime: false},
		{
			name:       "static-only correlation",
			usage:      &report.RuntimeUsage{LoadCount: 0, Correlation: report.RuntimeCorrelationStaticOnly},
			hasStatic:  true,
			hasRuntime: false,
		},
		{
			name:       "runtime-only correlation",
			usage:      &report.RuntimeUsage{LoadCount: 2, Correlation: report.RuntimeCorrelationRuntimeOnly},
			hasStatic:  false,
			hasRuntime: true,
		},
		{
			name:       "overlap correlation",
			usage:      &report.RuntimeUsage{LoadCount: 3, Correlation: report.RuntimeCorrelationOverlap},
			hasStatic:  true,
			hasRuntime: true,
		},
		{
			name:       "legacy runtime-only bool",
			usage:      &report.RuntimeUsage{LoadCount: 1, RuntimeOnly: true},
			hasStatic:  false,
			hasRuntime: true,
		},
	}

	for _, tc := range cases {
		gotStatic, gotRuntime := runtimeUsageSignals(tc.usage)
		if gotStatic != tc.hasStatic || gotRuntime != tc.hasRuntime {
			t.Fatalf("%s: expected static/runtime %v/%v got %v/%v", tc.name, tc.hasStatic, tc.hasRuntime, gotStatic, gotRuntime)
		}
	}

	if got := mergeRuntimeCorrelation(true, false); got != report.RuntimeCorrelationStaticOnly {
		t.Fatalf("expected static-only merge correlation, got %q", got)
	}
	if got := mergeRuntimeCorrelation(false, true); got != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected runtime-only merge correlation, got %q", got)
	}
	if got := mergeRuntimeCorrelation(true, true); got != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected overlap merge correlation, got %q", got)
	}
}
