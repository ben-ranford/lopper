package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestExecuteDashboardJSON(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			"./api": {
				Dependencies: []report.DependencyReport{
					{
						Name: sharedDependencyName,
						Recommendations: []report.Recommendation{
							{Code: "remove-unused-dependency"},
						},
						RiskCues: []report.RiskCue{
							{Code: "cve-2026-1234", Severity: "critical", Message: "critical vulnerability"},
						},
					},
					{Name: "api-only"},
				},
				Summary: &report.Summary{DeniedLicenseCount: 1},
			},
			"./web": {
				Dependencies: []report.DependencyReport{
					{Name: sharedDependencyName},
					{Name: "web-only"},
				},
			},
			"./worker": {
				Dependencies: []report.DependencyReport{
					{Name: sharedDependencyName},
					{Name: "worker-only"},
				},
			},
		},
		errs: map[string]error{},
	}

	application := &App{
		Analyzer: analyzer,
	}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Repos = []DashboardRepo{
		{Name: "api", Path: "./api"},
		{Name: "web", Path: "./web"},
		{Name: "worker", Path: "./worker"},
	}
	req.Dashboard.Format = "json"
	req.Dashboard.TopN = 10

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard: %v", err)
	}

	reportData := dashboard.Report{}
	if err := json.Unmarshal([]byte(output), &reportData); err != nil {
		t.Fatalf("unmarshal dashboard output: %v", err)
	}

	if reportData.Summary.TotalRepos != 3 {
		t.Fatalf("expected three repos, got %+v", reportData.Summary)
	}
	if reportData.Summary.TotalDeps != 6 {
		t.Fatalf("expected six dependencies total, got %+v", reportData.Summary)
	}
	if reportData.Summary.TotalWasteCandidates != 1 {
		t.Fatalf("expected one waste candidate, got %+v", reportData.Summary)
	}
	if reportData.Summary.CrossRepoDuplicates != 1 {
		t.Fatalf("expected one cross-repo duplicate dependency, got %+v", reportData.Summary)
	}
	if reportData.Summary.CriticalCVEs != 1 {
		t.Fatalf("expected one critical CVE signal, got %+v", reportData.Summary)
	}
	if len(reportData.CrossRepoDeps) != 1 || reportData.CrossRepoDeps[0].Name != sharedDependencyName {
		t.Fatalf("unexpected cross-repo duplicate payload: %#v", reportData.CrossRepoDeps)
	}
}

func TestExecuteDashboardOutputFile(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			singleRepoPath: {
				Dependencies: []report.DependencyReport{{Name: "dep"}},
			},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	outputPath := filepath.Join(t.TempDir(), "reports", "org.csv")

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "csv"
	req.Dashboard.Repos = []DashboardRepo{{Path: singleRepoPath}}
	req.Dashboard.OutputPath = outputPath

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard with output path: %v", err)
	}
	if !strings.Contains(output, outputPath) {
		t.Fatalf("expected output path confirmation, got %q", output)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read written dashboard report: %v", err)
	}
	if !strings.Contains(string(data), "total_repos") {
		t.Fatalf("expected CSV report content, got %q", string(data))
	}
}

func TestExecuteDashboardPropagatesAnalysisErrors(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{},
		errs: map[string]error{
			singleRepoPath: errors.New("analysis failed"),
		},
	}
	application := &App{Analyzer: analyzer}
	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "json"
	req.Dashboard.Repos = []DashboardRepo{{Path: singleRepoPath}}

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard with per-repo error should still format output: %v", err)
	}
	if !strings.Contains(output, "analysis failed") {
		t.Fatalf("expected dashboard output to include per-repo error, got %q", output)
	}
}

func TestExecuteDashboardRequiresAnalyzer(t *testing.T) {
	application := &App{}
	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Repos = []DashboardRepo{{Path: singleRepoPath}}

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "dashboard analyzer is not configured") {
		t.Fatalf("expected analyzer configuration error, got %v", err)
	}
}

func TestExecuteDashboardOutputMkdirError(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			singleRepoPath: {Dependencies: []report.DependencyReport{{Name: "dep"}}},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	tmpDir := t.TempDir()
	occupiedPath := filepath.Join(tmpDir, "occupied")
	if err := os.WriteFile(occupiedPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed occupied file: %v", err)
	}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "json"
	req.Dashboard.Repos = []DashboardRepo{{Path: singleRepoPath}}
	req.Dashboard.OutputPath = filepath.Join(occupiedPath, "report.json")

	_, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected mkdir error for output path under regular file")
	}
}

func TestExecuteDashboardOutputWriteError(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			singleRepoPath: {Dependencies: []report.DependencyReport{{Name: "dep"}}},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	outputDir := filepath.Join(t.TempDir(), "reports")
	if err := os.MkdirAll(outputDir, 0o750); err != nil {
		t.Fatalf("create output dir: %v", err)
	}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "json"
	req.Dashboard.Repos = []DashboardRepo{{Path: singleRepoPath}}
	req.Dashboard.OutputPath = outputDir

	_, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected write error when output path points to a directory")
	}
}

func TestExecuteDashboardInvalidFormatReturnsError(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			singleRepoPath: {Dependencies: []report.DependencyReport{{Name: "dep"}}},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "invalid-format"
	req.Dashboard.Repos = []DashboardRepo{{Path: singleRepoPath}}

	_, err := application.Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected invalid format error")
	}
}

func TestExecuteDashboardAppliesBaselineComparison(t *testing.T) {
	tmp := t.TempDir()
	baselineStore := filepath.Join(tmp, "baselines")
	baseline := dashboard.Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []dashboard.RepoResult{
			{Name: "api", Path: singleRepoPath, DependencyCount: 1, WasteCandidateCount: 0, WasteCandidatePercent: 0, CriticalCVEs: 0, DeniedLicenseCount: 0},
		},
		Summary: dashboard.Summary{TotalRepos: 1, TotalDeps: 1, TotalWasteCandidates: 0, CrossRepoDuplicates: 0, CriticalCVEs: 0},
	}
	if _, err := dashboard.SaveSnapshot(baselineStore, "label:baseline", baseline, time.Date(2026, time.March, 11, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("save dashboard baseline snapshot: %v", err)
	}

	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			singleRepoPath: {Dependencies: []report.DependencyReport{{Name: "dep"}, {Name: "dep-2"}}},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "json"
	req.Dashboard.Repos = []DashboardRepo{{Name: "api", Path: singleRepoPath}}
	req.Dashboard.BaselineStorePath = baselineStore
	req.Dashboard.BaselineKey = "label:baseline"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard baseline compare: %v", err)
	}
	if !strings.Contains(output, "baseline_comparison") {
		t.Fatalf("expected baseline comparison in dashboard output, got %q", output)
	}
}
