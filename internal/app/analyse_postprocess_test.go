package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestApplyAdvisoriesIfNeededBranches(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "skips blank advisory source path", run: testApplyAdvisoriesSkipsBlankSourcePath},
		{name: "uses advisory load path and recomputes summary", run: testApplyAdvisoriesUsesLoadPath},
		{name: "propagates advisory load errors", run: testApplyAdvisoriesPropagatesLoadErrors},
	} {
		t.Run(tc.name, tc.run)
	}
}

func testApplyAdvisoriesSkipsBlankSourcePath(t *testing.T) {
	t.Helper()

	input := report.Report{
		Summary: &report.Summary{DependencyCount: 1},
	}
	got, err := applyAdvisoriesIfNeeded(input, AnalyseRequest{AdvisorySourcePath: "   "})
	if err != nil {
		t.Fatalf("apply advisories with blank source path: %v", err)
	}
	if got.Summary.DependencyCount != input.Summary.DependencyCount {
		t.Fatalf("expected summary to remain unchanged, got %#v", got.Summary)
	}
}

func testApplyAdvisoriesUsesLoadPath(t *testing.T) {
	t.Helper()

	advisoryPath := filepath.Join(t.TempDir(), "advisories.json")
	if err := os.WriteFile(advisoryPath, []byte(`{"advisories":[{"id":"GHSA-lib","package":"lib","ecosystem":"npm","severity":"high","source":"fixture"}]}`), 0o600); err != nil {
		t.Fatalf("write advisory source: %v", err)
	}

	input := report.Report{
		Dependencies: []report.DependencyReport{{
			Name:     "lib",
			Language: "js-ts",
			Identity: &report.DependencyIdentity{Version: "1.0.0"},
		}},
	}
	got, err := applyAdvisoriesIfNeeded(input, AnalyseRequest{
		AdvisorySourcePath: "ignored.json",
		advisoryLoadPath:   advisoryPath,
	})
	if err != nil {
		t.Fatalf("apply advisories: %v", err)
	}
	if len(got.Dependencies) != 1 || len(got.Dependencies[0].Vulnerabilities) != 1 {
		t.Fatalf("expected advisory annotation to add one vulnerability, got %#v", got.Dependencies)
	}
	finding := got.Dependencies[0].Vulnerabilities[0]
	if finding.AdvisoryID != "GHSA-lib" || finding.Source != "fixture" {
		t.Fatalf("unexpected advisory finding: %#v", finding)
	}
	if got.Summary == nil || got.Summary.Vulnerabilities == nil || got.Summary.Vulnerabilities.TotalFindings != 1 {
		t.Fatalf("expected summary recomputation to include one vulnerability, got %#v", got.Summary)
	}
}

func testApplyAdvisoriesPropagatesLoadErrors(t *testing.T) {
	t.Helper()

	_, err := applyAdvisoriesIfNeeded(report.Report{}, AnalyseRequest{
		AdvisorySourcePath: "advisories.json",
		advisoryLoadPath:   filepath.Join(t.TempDir(), "missing.json"),
	})
	if err == nil || !strings.Contains(err.Error(), "read advisory source") {
		t.Fatalf("expected advisory load error, got %v", err)
	}
}

func TestApplyBaselineIfNeededWithRepositoryRejectsUnsafeRelativeStorePath(t *testing.T) {
	repo := t.TempDir()
	repository, err := analysis.ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := analysis.OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	req := AnalyseRequest{
		BaselineStorePath: filepath.Join("..", "outside"),
		BaselineKey:       "label:nightly",
	}
	application := &App{}
	_, err = application.applyBaselineIfNeededWithRepository(report.Report{}, repo, req, view)
	if err == nil || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("expected unsafe relative baseline store rejection, got %v", err)
	}
}

func TestApplyBaselineIfNeededWithRepositoryRejectsUnsafeRelativeBaselinePath(t *testing.T) {
	repo := t.TempDir()
	repository, err := analysis.ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("authorize repository: %v", err)
	}
	view, err := analysis.OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	t.Cleanup(func() {
		if err := view.Close(); err != nil {
			t.Errorf("close repository view: %v", err)
		}
	})

	req := AnalyseRequest{
		BaselinePath: filepath.Join("..", "outside.json"),
	}
	application := &App{}
	_, err = application.applyBaselineIfNeededWithRepository(report.Report{}, repo, req, view)
	if err == nil || !strings.Contains(err.Error(), "repository root") {
		t.Fatalf("expected unsafe relative baseline path rejection, got %v", err)
	}
}

func TestApplyBaselineIfNeededWithRepositoryFallsBackWithoutView(t *testing.T) {
	application := &App{}
	input := report.Report{Summary: &report.Summary{DependencyCount: 1}}
	got, err := application.applyBaselineIfNeededWithRepository(input, ".", AnalyseRequest{}, nil)
	if err != nil {
		t.Fatalf("apply baseline without repository view: %v", err)
	}
	if got.Summary == nil || got.Summary.DependencyCount != 1 {
		t.Fatalf("expected report to remain unchanged without repository view, got %#v", got)
	}
}

func TestApplyAdvisoriesIfNeededFallsBackToAdvisorySourcePath(t *testing.T) {
	repo := t.TempDir()
	advisoryPath := filepath.Join(repo, "advisories.json")
	if err := os.WriteFile(advisoryPath, []byte(`{"advisories":[{"id":"GHSA-lib","package":"lib","ecosystem":"npm","severity":"high","source":"fixture"}]}`), 0o600); err != nil {
		t.Fatalf("write advisory source: %v", err)
	}

	input := report.Report{
		Dependencies: []report.DependencyReport{{
			Name:     "lib",
			Language: "js-ts",
			Identity: &report.DependencyIdentity{Version: "1.0.0"},
		}},
	}
	got, err := applyAdvisoriesIfNeeded(input, AnalyseRequest{
		AdvisorySourcePath: advisoryPath,
	})
	if err != nil {
		t.Fatalf("apply advisories without fallback path: %v", err)
	}
	if len(got.Dependencies) != 1 || len(got.Dependencies[0].Vulnerabilities) != 1 {
		t.Fatalf("expected advisory annotation through retained-view fallback path, got %#v", got.Dependencies)
	}
}

func TestReachableVulnerabilityThresholdHelpers(t *testing.T) {
	if hasReachableVulnerabilityAtOrAbove(report.Report{}, "   ") {
		t.Fatalf("expected blank threshold to be ignored")
	}
	if hasReachableVulnerabilityAtOrAbove(report.Report{}, report.VulnerabilityPriorityOff) {
		t.Fatalf("expected off threshold to be ignored")
	}

	deps := []report.DependencyReport{{
		Name: "lib",
		Vulnerabilities: []report.VulnerabilityFinding{
			{
				AdvisoryID: "GHSA-suppressed",
				Reachable:  true,
				Priority:   report.VulnerabilityPriorityCritical,
				Decision:   &report.VulnerabilityExceptionDecision{Status: "not-affected"},
			},
			{
				AdvisoryID:    "GHSA-unevaluable",
				Reachable:     true,
				VersionStatus: "unevaluable",
				Priority:      report.VulnerabilityPriorityLow,
			},
		},
	}}
	if !dependencyHasReachableVulnerabilityAtOrAbove(deps, report.VulnerabilityPriorityCritical) {
		t.Fatalf("expected unevaluable reachable vulnerability to satisfy threshold")
	}
}
