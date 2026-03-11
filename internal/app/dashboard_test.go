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

const (
	sharedDependencyName = "shared-lib"
	singleRepoPath       = "./repo"
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

func TestResolveDashboardRequestRequiresRepos(t *testing.T) {
	_, err := resolveDashboardRequest(DashboardRequest{})
	if err == nil || !strings.Contains(err.Error(), "requires at least one repo") {
		t.Fatalf("expected missing repos error, got %v", err)
	}
}

func TestResolveDashboardRequestAppliesDefaultLanguageAndOutputTrim(t *testing.T) {
	resolved, err := resolveDashboardRequest(DashboardRequest{
		Repos:           []DashboardRepo{{Path: "./api"}},
		DefaultLanguage: "go",
		Format:          "json",
		OutputPath:      " ./out/report.json ",
	})
	if err != nil {
		t.Fatalf("resolve dashboard request: %v", err)
	}
	if len(resolved.repos) != 1 {
		t.Fatalf("expected one resolved repo, got %#v", resolved.repos)
	}
	if resolved.repos[0].Language != "go" {
		t.Fatalf("expected default language to be applied, got %#v", resolved.repos[0])
	}
	if resolved.outputPath != "./out/report.json" {
		t.Fatalf("expected output path to be trimmed, got %q", resolved.outputPath)
	}
}

func TestReposFromDashboardConfigMissingPath(t *testing.T) {
	_, err := reposFromDashboardConfig(dashboard.LoadedConfig{
		ConfigDir: t.TempDir(),
		Dashboard: dashboard.ConfigDashboard{
			Repos: []dashboard.ConfigRepo{
				{Name: "broken-repo"},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "missing path") {
		t.Fatalf("expected missing path error, got %v", err)
	}
}

func TestInferDashboardRepoNameRootPath(t *testing.T) {
	if got := inferDashboardRepoName(string(filepath.Separator)); got != string(filepath.Separator) {
		t.Fatalf("expected root path repo name fallback, got %q", got)
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

func TestLoadDashboardConfigError(t *testing.T) {
	_, hasConfig, err := loadDashboardConfig(filepath.Join(t.TempDir(), "missing.yml"))
	if err == nil {
		t.Fatalf("expected config load error for missing file")
	}
	if hasConfig {
		t.Fatalf("expected hasConfig=false when load fails")
	}
}

func TestLoadDashboardConfigEmptyPath(t *testing.T) {
	loaded, hasConfig, err := loadDashboardConfig("   ")
	if err != nil {
		t.Fatalf("expected empty config path to be a no-op, got %v", err)
	}
	if hasConfig {
		t.Fatalf("expected hasConfig=false for empty config path")
	}
	if loaded.Path != "" || loaded.ConfigDir != "" || len(loaded.Dashboard.Repos) != 0 {
		t.Fatalf("expected empty loaded config, got %#v", loaded)
	}
}

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
