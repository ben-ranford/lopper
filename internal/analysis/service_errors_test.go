package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	analyseFailedErrMsg = "analyse failed"
	registerAdapterFmt  = "register adapter: %v"
)

type testServiceAdapter struct {
	id        string
	detect    language.Detection
	analyse   report.Report
	analyseFn func(context.Context, language.Request) (report.Report, error)
	err       error
}

func (a *testServiceAdapter) ID() string        { return a.id }
func (a *testServiceAdapter) Aliases() []string { return nil }
func (a *testServiceAdapter) Detect(context.Context, string) (bool, error) {
	return a.detect.Matched, nil
}
func (a *testServiceAdapter) DetectWithConfidence(context.Context, string) (language.Detection, error) {
	return a.detect, nil
}
func (a *testServiceAdapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	if a.analyseFn != nil {
		return a.analyseFn(ctx, req)
	}
	if a.err != nil {
		return report.Report{}, a.err
	}
	return a.analyse, nil
}

func TestPrepareAnalysisErrors(t *testing.T) {
	svc := &Service{InitErr: errors.New("init failed")}
	if _, err := svc.prepareAnalysis(Request{RepoPath: ".", Language: "all"}); err == nil {
		t.Fatalf("expected init error")
	}

	svc = &Service{}
	if _, err := svc.prepareAnalysis(Request{RepoPath: ".", Language: "all"}); err == nil {
		t.Fatalf("expected nil-registry error")
	}
}

func TestRunCandidateOnRootsMultiLanguageErrorBecomesWarning(t *testing.T) {
	adapter := &testServiceAdapter{id: "broken", detect: language.Detection{Matched: true, Confidence: 10}, err: errors.New(analyseFailedErrMsg)}
	candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 10, Roots: []string{"."}}}
	svc := &Service{}
	reports, warnings, _, err := svc.runCandidateOnRoots(context.Background(), Request{RepoPath: ".", Language: "all"}, ".", candidate, nil)
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

func TestRunCandidateOnRootsMultiLanguageCoverageErrorIsFatalWhenRequired(t *testing.T) {
	for _, languageID := range []string{"all", "auto"} {
		t.Run(languageID, func(t *testing.T) {
			adapter := &testServiceAdapter{
				id:     "php",
				detect: language.Detection{Matched: true, Confidence: 90},
				err:    errors.Join(errors.New("read composer.json"), safeio.ErrFileTooLarge),
			}
			candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 90, Roots: []string{"."}}}
			svc := &Service{}

			_, _, _, err := svc.runCandidateOnRoots(context.Background(), Request{
				RepoPath:                ".",
				Language:                languageID,
				RequireCompleteCoverage: true,
			}, ".", candidate, nil)
			if !errors.Is(err, safeio.ErrFileTooLarge) {
				t.Fatalf("expected oversized coverage error to be fatal when coverage is required, got %v", err)
			}
		})
	}
}

func TestRunCandidateOnRootsMultiLanguageAdapterErrorIsFatalWhenCoverageRequired(t *testing.T) {
	for _, languageID := range []string{"all", "auto"} {
		t.Run(languageID, func(t *testing.T) {
			expected := errors.New("composer scan incomplete")
			adapter := &testServiceAdapter{
				id:     "php",
				detect: language.Detection{Matched: true, Confidence: 90},
				err:    expected,
			}
			candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 90, Roots: []string{"."}}}
			svc := &Service{}

			_, _, _, err := svc.runCandidateOnRoots(context.Background(), Request{
				RepoPath:                ".",
				Language:                languageID,
				RequireCompleteCoverage: true,
			}, ".", candidate, nil)
			if !errors.Is(err, expected) {
				t.Fatalf("expected matched adapter error to be fatal when coverage is required, got %v", err)
			}
		})
	}
}

func TestRunCandidateOnRootsMultiLanguageIncompleteCoverageReportIsFatalWhenRequired(t *testing.T) {
	assertIncompleteCoverageReportFatal(t, "all", "expected incomplete coverage report to be fatal when coverage is required")
}

func TestRunCandidateOnRootsExplicitLanguageIncompleteCoverageReportIsFatalWhenRequired(t *testing.T) {
	assertIncompleteCoverageReportFatal(t, "php", "expected explicit-language incomplete coverage report to be fatal when coverage is required")
}

func assertIncompleteCoverageReportFatal(t *testing.T, languageID, wantMessage string) {
	t.Helper()

	adapter := &testServiceAdapter{
		id:     "php",
		detect: language.Detection{Matched: true, Confidence: 90},
		analyse: report.Report{Dependencies: []report.DependencyReport{{
			Name:            "vendor/lib",
			UsageIncomplete: true,
		}}},
	}
	candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 90, Roots: []string{"."}}}
	svc := &Service{}

	_, _, _, err := svc.runCandidateOnRoots(context.Background(), Request{
		RepoPath:                ".",
		Language:                languageID,
		RequireCompleteCoverage: true,
	}, ".", candidate, nil)
	if !errors.Is(err, ErrIncompleteCoverage) {
		t.Fatalf("%s, got %v", wantMessage, err)
	}
	if !strings.Contains(err.Error(), "php:vendor/lib") {
		t.Fatalf("expected incomplete dependency details in error, got %v", err)
	}
}

func TestRunCandidateOnRootsMultiLanguageReportLevelIncompleteCoverageIsFatalWhenRequired(t *testing.T) {
	for _, languageID := range []string{"all", "auto"} {
		t.Run(languageID, func(t *testing.T) {
			adapter := &testServiceAdapter{
				id:      "php",
				detect:  language.Detection{Matched: true, Confidence: 90},
				analyse: report.Report{UsageIncomplete: true},
			}
			candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 90, Roots: []string{"."}}}
			svc := &Service{}

			_, _, _, err := svc.runCandidateOnRoots(context.Background(), Request{
				RepoPath:                ".",
				Language:                languageID,
				RequireCompleteCoverage: true,
			}, ".", candidate, nil)
			if !errors.Is(err, ErrIncompleteCoverage) {
				t.Fatalf("expected report-level incomplete coverage to be fatal when coverage is required, got %v", err)
			}
			if !strings.Contains(err.Error(), "reported incomplete usage coverage") {
				t.Fatalf("expected report-level incomplete coverage details in error, got %v", err)
			}
		})
	}
}

// TestRunCandidateOnRootsCoverageGapIsFatalWhenRequired proves an oversized
// gemspec (or any other CoverageGap-producing skip) is rejected under
// RequireCompleteCoverage even when it never sets UsageIncomplete: the gap
// mechanism records a structured CoverageGap instead, which
// incompleteCoverageReportError must also check.
func TestRunCandidateOnRootsCoverageGapIsFatalWhenRequired(t *testing.T) {
	for _, languageID := range []string{"all", "ruby"} {
		t.Run(languageID, func(t *testing.T) {
			adapter := &testServiceAdapter{
				id:     "ruby",
				detect: language.Detection{Matched: true, Confidence: 90},
				analyse: report.Report{CoverageGaps: []report.CoverageGap{{
					Code: "ruby-oversized-gemspec",
					Path: "big.gemspec",
				}}},
			}
			candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 90, Roots: []string{"."}}}
			svc := &Service{}

			_, _, _, err := svc.runCandidateOnRoots(context.Background(), Request{
				RepoPath:                ".",
				Language:                languageID,
				RequireCompleteCoverage: true,
			}, ".", candidate, nil)
			if !errors.Is(err, ErrIncompleteCoverage) {
				t.Fatalf("expected coverage gap to be fatal when coverage is required, got %v", err)
			}
			if !strings.Contains(err.Error(), "big.gemspec") {
				t.Fatalf("expected coverage gap path in error, got %v", err)
			}
		})
	}
}

func TestRunCandidateOnRootsIncompleteCoverageReportPreservesOrdinaryPartialBehavior(t *testing.T) {
	for _, tt := range []struct {
		name string
		req  Request
	}{
		{
			name: "all language without complete coverage requirement",
			req:  Request{RepoPath: ".", Language: "all"},
		},
		{
			name: "single language without complete coverage requirement",
			req:  Request{RepoPath: ".", Language: "php"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			adapter := &testServiceAdapter{
				id:     "php",
				detect: language.Detection{Matched: true, Confidence: 90},
				analyse: report.Report{Dependencies: []report.DependencyReport{{
					Name:            "vendor/lib",
					UsageIncomplete: true,
				}}},
			}
			candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 90, Roots: []string{"."}}}
			svc := &Service{}

			reports, _, _, err := svc.runCandidateOnRoots(context.Background(), tt.req, ".", candidate, nil)
			if err != nil {
				t.Fatalf("expected ordinary partial report behavior, got %v", err)
			}
			if len(reports) != 1 || len(reports[0].Dependencies) != 1 || !reports[0].Dependencies[0].UsageIncomplete {
				t.Fatalf("expected incomplete report to be preserved, got %#v", reports)
			}
		})
	}
}

func TestRunCandidateOnRootsSingleLanguageError(t *testing.T) {
	adapter := &testServiceAdapter{id: "broken", detect: language.Detection{Matched: true, Confidence: 10}, err: errors.New(analyseFailedErrMsg)}
	candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 10, Roots: []string{"."}}}
	svc := &Service{}
	_, _, _, err := svc.runCandidateOnRoots(context.Background(), Request{RepoPath: ".", Language: "js-ts"}, ".", candidate, nil)
	if err == nil {
		t.Fatalf("expected error in single-language mode")
	}
}

func TestAnalyseNoReportsAndRuntimeTraceErrorBranches(t *testing.T) {
	reg := language.NewRegistry()
	if err := reg.Register(&testServiceAdapter{
		id:     "broken",
		detect: language.Detection{Matched: true, Confidence: 20},
		err:    errors.New(analyseFailedErrMsg),
	}); err != nil {
		t.Fatalf(registerAdapterFmt, err)
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
	if err := reg.Register(&testServiceAdapter{
		id:     "ok",
		detect: language.Detection{Matched: true, Confidence: 90},
		analyse: report.Report{
			Dependencies: []report.DependencyReport{{Name: "dep"}},
		},
	}); err != nil {
		t.Fatalf(registerAdapterFmt, err)
	}
	svc = &Service{Registry: reg}
	rep, err = svc.Analyse(context.Background(), Request{
		RepoPath:         ".",
		Language:         "all",
		TopN:             1,
		RuntimeTracePath: filepath.Join(t.TempDir(), "missing.ndjson"),
	})
	if err != nil {
		t.Fatalf("expected runtime trace fallback warning, got %v", err)
	}
	if len(rep.Warnings) == 0 {
		t.Fatalf("expected missing runtime trace warning")
	}
}

func TestPrepareAnalysisResolveErrorAndHelperBranches(t *testing.T) {
	reg := language.NewRegistry()
	if err := reg.Register(&testServiceAdapter{
		id:      "broken",
		detect:  language.Detection{Matched: true},
		err:     nil,
		analyse: report.Report{},
	}); err != nil {
		t.Fatalf(registerAdapterFmt, err)
	}
	// Force registry resolve error via unsupported explicit language.
	svc := &Service{Registry: reg}
	if _, err := svc.resolveCandidates(context.Background(), ".", Request{Language: "unknown"}); err == nil {
		t.Fatalf("expected prepareAnalysis resolve error")
	}

	adapter := &testServiceAdapter{id: "x", detect: language.Detection{Matched: true, Confidence: 0}}
	lowConfidenceThreshold := resolveLowConfidenceWarningThreshold(nil)
	warnings := lowConfidenceWarning("all", language.Candidate{Adapter: adapter, Detection: language.Detection{Confidence: 0}}, lowConfidenceThreshold)
	if len(warnings) != 0 {
		t.Fatalf("expected no warning for non-positive confidence")
	}
	if warnings := lowConfidenceWarning("js-ts", language.Candidate{Adapter: adapter, Detection: language.Detection{Confidence: 10}}, lowConfidenceThreshold); len(warnings) != 0 {
		t.Fatalf("expected no warning for single-language mode")
	}

	deps := []report.DependencyReport{{
		UsedImports: []report.ImportUse{{Locations: []report.Location{{File: "a.js", Line: 1}}}},
	}}
	adjustRelativeLocations(".", ".", deps)
	if deps[0].UsedImports[0].Locations[0].File != "a.js" {
		t.Fatalf("expected unchanged location when analyzed root equals repo root")
	}

	gaps := []report.CoverageGap{
		{Code: report.CoverageGapRubyOversizedGemspec, Language: "ruby", Path: " foo.gemspec "},
		{Code: report.CoverageGapRubyOversizedGemspec, Language: "ruby", Path: "/absolute/foo.gemspec"},
		{Code: "pathless-gap", Language: "ruby"},
	}
	adjustRelativeCoverageGaps("/repo", "/repo/packages/a", gaps)
	if gaps[0].Path != "packages/a/ foo.gemspec " {
		t.Fatalf("expected relative coverage gap to be rebased without trimming filename whitespace, got %q", gaps[0].Path)
	}
	if gaps[1].Path != "/absolute/foo.gemspec" {
		t.Fatalf("expected absolute coverage gap path to stay absolute, got %q", gaps[1].Path)
	}
	if gaps[2].Path != "" {
		t.Fatalf("expected empty coverage gap path to remain empty, got %q", gaps[2].Path)
	}
}

func TestAdjustRelativeCoverageGapsPreservesUnixLiteralBackslashes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("backslash is a path separator on Windows")
	}

	gaps := []report.CoverageGap{
		{Code: report.CoverageGapRubyOversizedGemspec, Language: "ruby", Path: "a\\b.gemspec"},
		{Code: report.CoverageGapRubyOversizedGemspec, Language: "ruby", Path: "a/b.gemspec"},
	}
	adjustRelativeCoverageGaps("/repo", "/repo/packages/a", gaps)

	if gaps[0].Path != "packages/a/a\\b.gemspec" {
		t.Fatalf("expected literal backslash coverage gap to be preserved while rebasing, got %q", gaps[0].Path)
	}
	if gaps[1].Path != "packages/a/a/b.gemspec" {
		t.Fatalf("expected slash coverage gap to remain distinct while rebasing, got %q", gaps[1].Path)
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

	items := mergeTopSymbols([]report.SymbolUsage{{Name: "a", Count: 1}, {Name: "b", Count: 1}, {Name: "c", Count: 1}}, []report.SymbolUsage{{Name: "d", Count: 1}, {Name: "e", Count: 1}, {Name: "f", Count: 1}})
	if len(items) != 5 {
		t.Fatalf("expected top symbols truncation to 5, got %#v", items)
	}

	filtered := filterUsedOverlaps([]report.ImportUse{{Module: "m", Name: "a"}, {Module: "m", Name: "b"}}, []report.ImportUse{{Module: "m", Name: "a"}})
	if len(filtered) != 1 || filtered[0].Name != "b" {
		t.Fatalf("expected overlap filter to drop used import, got %#v", filtered)
	}
}

func TestAnnotateRuntimeTraceHelperMissingFileFallback(t *testing.T) {
	annotated, err := annotateRuntimeTraceIfPresent(filepath.Join(t.TempDir(), "missing.ndjson"), "js-ts", report.Report{}, false)
	if err != nil {
		t.Fatalf("expected missing runtime trace fallback, got %v", err)
	}
	if len(annotated.Warnings) == 0 {
		t.Fatalf("expected missing runtime trace warning")
	}
}

func TestPrepareAnalysisRepoPathAbsErrorFallback(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})

	deadDir := t.TempDir()
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir deadDir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove deadDir: %v", err)
	}

	svc := &Service{Registry: language.NewRegistry()}
	if _, err := svc.prepareAnalysis(Request{RepoPath: ".", Language: "all"}); err != nil {
		t.Logf("prepareAnalysis returned platform-dependent error: %v", err)
	}
}
