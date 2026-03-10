package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/report"
)

type mapAnalyzer struct {
	mu      sync.Mutex
	reports map[string]report.Report
	errs    map[string]error
	calls   []analysis.Request
}

func (m *mapAnalyzer) Analyse(_ context.Context, req analysis.Request) (report.Report, error) {
	m.mu.Lock()
	m.calls = append(m.calls, req)
	m.mu.Unlock()

	if err, ok := m.errs[req.RepoPath]; ok {
		return report.Report{}, err
	}
	if reportData, ok := m.reports[req.RepoPath]; ok {
		return reportData, nil
	}
	return report.Report{}, nil
}

func TestExecuteDashboardJSON(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			"./api": {
				Dependencies: []report.DependencyReport{
					{
						Name: "shared-lib",
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
					{Name: "shared-lib"},
					{Name: "web-only"},
				},
			},
			"./worker": {
				Dependencies: []report.DependencyReport{
					{Name: "shared-lib"},
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
	if len(reportData.CrossRepoDeps) != 1 || reportData.CrossRepoDeps[0].Name != "shared-lib" {
		t.Fatalf("unexpected cross-repo duplicate payload: %#v", reportData.CrossRepoDeps)
	}
}

func TestExecuteDashboardOutputFile(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			"./repo": {
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
	req.Dashboard.Repos = []DashboardRepo{{Path: "./repo"}}
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

func TestResolveDashboardRequestConfigRelativeRepo(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "lopper-org.yml")
	config := "dashboard:\n  repos:\n    - path: ./api\n      name: API Service\n      language: go\n  output: html\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	resolved, err := resolveDashboardRequest(DashboardRequest{
		ConfigPath: configPath,
	})
	if err != nil {
		t.Fatalf("resolve dashboard request from config: %v", err)
	}
	if len(resolved.repos) != 1 {
		t.Fatalf("expected one repo from config, got %#v", resolved.repos)
	}
	if resolved.repos[0].Path != filepath.Join(tmpDir, "api") {
		t.Fatalf("expected config-relative repo path, got %#v", resolved.repos)
	}
	if resolved.format != dashboard.FormatHTML {
		t.Fatalf("expected config output format html, got %q", resolved.format)
	}
}

func TestResolveDashboardRequestConfigRepoURLNotSupported(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "lopper-org.yml")
	config := "dashboard:\n  repos:\n    - repoUrl: https://github.com/org/worker\n  output: json\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := resolveDashboardRequest(DashboardRequest{
		ConfigPath: configPath,
	})
	if err == nil {
		t.Fatalf("expected unsupported repoUrl error")
	}
	if !strings.Contains(err.Error(), "repoUrl") {
		t.Fatalf("expected repoUrl error message, got %v", err)
	}
}

func TestExecuteDashboardPropagatesAnalysisErrors(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{},
		errs: map[string]error{
			"./repo": errors.New("analysis failed"),
		},
	}
	application := &App{Analyzer: analyzer}
	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "json"
	req.Dashboard.Repos = []DashboardRepo{{Path: "./repo"}}

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard with per-repo error should still format output: %v", err)
	}
	if !strings.Contains(output, "analysis failed") {
		t.Fatalf("expected dashboard output to include per-repo error, got %q", output)
	}
}
