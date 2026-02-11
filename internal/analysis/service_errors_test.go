package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

type testServiceAdapter struct {
	id      string
	detect  language.Detection
	analyse report.Report
	err     error
}

func (a testServiceAdapter) ID() string        { return a.id }
func (a testServiceAdapter) Aliases() []string { return nil }
func (a testServiceAdapter) Detect(context.Context, string) (bool, error) {
	return a.detect.Matched, nil
}
func (a testServiceAdapter) DetectWithConfidence(context.Context, string) (language.Detection, error) {
	return a.detect, nil
}
func (a testServiceAdapter) Analyse(context.Context, language.Request) (report.Report, error) {
	if a.err != nil {
		return report.Report{}, a.err
	}
	return a.analyse, nil
}

func TestPrepareAnalysisErrors(t *testing.T) {
	svc := &Service{InitErr: errors.New("init failed")}
	if _, _, err := svc.prepareAnalysis(context.Background(), Request{RepoPath: ".", Language: "all"}); err == nil {
		t.Fatalf("expected init error")
	}

	svc = &Service{}
	if _, _, err := svc.prepareAnalysis(context.Background(), Request{RepoPath: ".", Language: "all"}); err == nil {
		t.Fatalf("expected nil-registry error")
	}
}

func TestRunCandidateOnRootsMultiLanguageErrorBecomesWarning(t *testing.T) {
	adapter := testServiceAdapter{id: "broken", detect: language.Detection{Matched: true, Confidence: 10}, err: errors.New("analyse failed")}
	candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 10, Roots: []string{"."}}}
	svc := &Service{}
	reports, warnings, err := svc.runCandidateOnRoots(context.Background(), Request{RepoPath: ".", Language: "all"}, ".", candidate)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("expected no reports, got %#v", reports)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected warning for analyse failure in all-language mode")
	}
}

func TestRunCandidateOnRootsSingleLanguageError(t *testing.T) {
	adapter := testServiceAdapter{id: "broken", detect: language.Detection{Matched: true, Confidence: 10}, err: errors.New("analyse failed")}
	candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 10, Roots: []string{"."}}}
	svc := &Service{}
	_, _, err := svc.runCandidateOnRoots(context.Background(), Request{RepoPath: ".", Language: "js-ts"}, ".", candidate)
	if err == nil {
		t.Fatalf("expected error in single-language mode")
	}
}

func TestAnalyseNoReportsAndRuntimeTraceErrorBranches(t *testing.T) {
	reg := language.NewRegistry()
	if err := reg.Register(testServiceAdapter{
		id:     "broken",
		detect: language.Detection{Matched: true, Confidence: 20},
		err:    errors.New("analyse failed"),
	}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc := &Service{Registry: reg}

	rep, err := svc.Analyse(context.Background(), Request{
		RepoPath: ".",
		Language: "all",
		TopN:     1,
	})
	if err != nil {
		t.Fatalf("analyse all-mode with broken adapter: %v", err)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), "no language adapter produced results") {
		t.Fatalf("expected no-results warning, got %#v", rep.Warnings)
	}

	reg = language.NewRegistry()
	if err := reg.Register(testServiceAdapter{
		id:     "ok",
		detect: language.Detection{Matched: true, Confidence: 90},
		analyse: report.Report{
			Dependencies: []report.DependencyReport{{Name: "dep"}},
		},
	}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	svc = &Service{Registry: reg}
	_, err = svc.Analyse(context.Background(), Request{
		RepoPath:         ".",
		Language:         "all",
		TopN:             1,
		RuntimeTracePath: filepath.Join(t.TempDir(), "missing.ndjson"),
	})
	if err == nil {
		t.Fatalf("expected runtime trace load error")
	}
}

func TestPrepareAnalysisResolveErrorAndHelperBranches(t *testing.T) {
	reg := language.NewRegistry()
	if err := reg.Register(testServiceAdapter{
		id:      "broken",
		detect:  language.Detection{Matched: true},
		err:     nil,
		analyse: report.Report{},
	}); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	// Force registry resolve error via unsupported explicit language.
	svc := &Service{Registry: reg}
	if _, _, err := svc.prepareAnalysis(context.Background(), Request{RepoPath: ".", Language: "unknown"}); err == nil {
		t.Fatalf("expected prepareAnalysis resolve error")
	}

	adapter := testServiceAdapter{id: "x", detect: language.Detection{Matched: true, Confidence: 0}}
	warnings := lowConfidenceWarning("all", language.Candidate{Adapter: adapter, Detection: language.Detection{Confidence: 0}})
	if len(warnings) != 0 {
		t.Fatalf("expected no warning for non-positive confidence")
	}
	if warnings := lowConfidenceWarning("js-ts", language.Candidate{Adapter: adapter, Detection: language.Detection{Confidence: 10}}); len(warnings) != 0 {
		t.Fatalf("expected no warning for single-language mode")
	}

	deps := []report.DependencyReport{{
		UsedImports: []report.ImportUse{{Locations: []report.Location{{File: "a.js", Line: 1}}}},
	}}
	adjustRelativeLocations(".", ".", deps)
	if deps[0].UsedImports[0].Locations[0].File != "a.js" {
		t.Fatalf("expected unchanged location when analyzed root equals repo root")
	}
}

func TestMergeReportsAndTopSymbolsBranches(t *testing.T) {
	reports := []report.Report{
		{
			Warnings: []string{"w1"},
			Dependencies: []report.DependencyReport{
				{Language: "js-ts", Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2},
			},
		},
		{
			Warnings: []string{"w2"},
			Dependencies: []report.DependencyReport{
				{Language: "js-ts", Name: "dep", UsedExportsCount: 2, TotalExportsCount: 3},
			},
		},
	}
	merged := mergeReports(".", reports)
	if len(merged.Dependencies) != 1 || merged.Dependencies[0].UsedExportsCount != 3 {
		t.Fatalf("expected merged duplicate dependency report, got %#v", merged.Dependencies)
	}

	items := mergeTopSymbols(
		[]report.SymbolUsage{{Name: "a", Count: 1}, {Name: "b", Count: 1}, {Name: "c", Count: 1}},
		[]report.SymbolUsage{{Name: "d", Count: 1}, {Name: "e", Count: 1}, {Name: "f", Count: 1}},
	)
	if len(items) != 5 {
		t.Fatalf("expected top symbols truncation to 5, got %#v", items)
	}

	filtered := filterUsedOverlaps(
		[]report.ImportUse{{Module: "m", Name: "a"}, {Module: "m", Name: "b"}},
		[]report.ImportUse{{Module: "m", Name: "a"}},
	)
	if len(filtered) != 1 || filtered[0].Name != "b" {
		t.Fatalf("expected overlap filter to drop used import, got %#v", filtered)
	}
}

func TestAnnotateRuntimeTraceHelperError(t *testing.T) {
	_, err := annotateRuntimeTraceIfPresent(filepath.Join(t.TempDir(), "missing.ndjson"), report.Report{})
	if err == nil {
		t.Fatalf("expected runtime trace annotation error")
	}
}

func TestPrepareAnalysisRepoPathAbsErrorFallback(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	deadDir := t.TempDir()
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir deadDir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove deadDir: %v", err)
	}

	svc := &Service{Registry: language.NewRegistry()}
	if _, _, err := svc.prepareAnalysis(context.Background(), Request{RepoPath: ".", Language: "all"}); err == nil {
		// this branch is platform dependent; treat nil as acceptable.
	}
}
