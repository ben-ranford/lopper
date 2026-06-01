package app

import (
	"context"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestRunDashboardAnalysesEmptyRepos(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{},
		errs:    map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	results := application.runDashboardAnalyses(context.Background(), DashboardRequest{}, nil)
	if len(results) != 0 {
		t.Fatalf("expected no results for empty repos, got %#v", results)
	}
	if len(analyzer.calls) != 0 {
		t.Fatalf("expected no analyzer calls for empty repos, got %#v", analyzer.calls)
	}
}

func TestRunDashboardAnalysesDefaultsTopNAndScope(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			"./api": {Dependencies: []report.DependencyReport{{Name: "dep"}}},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	repos := []dashboard.RepoInput{
		{Name: "api", Path: "./api", Language: "go"},
	}

	results := application.runDashboardAnalyses(context.Background(), DashboardRequest{}, repos)
	if len(results) != 1 {
		t.Fatalf("expected one analysis result, got %#v", results)
	}
	if len(analyzer.calls) != 1 {
		t.Fatalf("expected one analyzer call, got %#v", analyzer.calls)
	}
	call := analyzer.calls[0]
	if call.TopN != 20 {
		t.Fatalf("expected default TopN=20, got %d", call.TopN)
	}
	if call.ScopeMode != analysis.ScopeModeRepo {
		t.Fatalf("expected ScopeModeRepo, got %q", call.ScopeMode)
	}
	if call.RuntimeProfile != "node-import" {
		t.Fatalf("expected node-import runtime profile, got %q", call.RuntimeProfile)
	}
	if results[0].Input.Path != "./api" {
		t.Fatalf("unexpected result ordering: %#v", results)
	}
}

func TestRunDashboardAnalysesForwardsFeatures(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			"./scripts": {Dependencies: []report.DependencyReport{{Name: "pester"}}},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelRelease,
	})
	if err != nil {
		t.Fatalf("resolve release features: %v", err)
	}
	if !features.Enabled("powershell-adapter-preview") {
		t.Fatalf("expected release default features to include powershell adapter")
	}

	repos := []dashboard.RepoInput{
		{Name: "scripts", Path: "./scripts", Language: "powershell"},
	}
	dashboardReq := DashboardRequest{
		Features: features,
	}
	results := application.runDashboardAnalyses(context.Background(), dashboardReq, repos)
	if len(results) != 1 {
		t.Fatalf("expected one analysis result, got %#v", results)
	}
	if len(analyzer.calls) != 1 {
		t.Fatalf("expected one analyzer call, got %#v", analyzer.calls)
	}
	if !analyzer.calls[0].Features.Enabled("powershell-adapter-preview") {
		t.Fatalf("expected dashboard analysis request to forward powershell feature flag")
	}
}
