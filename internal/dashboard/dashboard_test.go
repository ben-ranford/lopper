package dashboard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

const (
	testDashboardConfigFile = "lopper-org.yml"
	testRepoA               = "repo-a"
	testRepoB               = "repo-b"
	testRepoC               = "repo-c"
	testRepoD               = "repo-d"
)

func TestParseFormat(t *testing.T) {
	tests := []struct {
		input string
		want  Format
	}{
		{input: "", want: FormatJSON},
		{input: "json", want: FormatJSON},
		{input: "csv", want: FormatCSV},
		{input: "html", want: FormatHTML},
	}
	for _, tc := range tests {
		got, err := ParseFormat(tc.input)
		if err != nil {
			t.Fatalf("parse format %q: %v", tc.input, err)
		}
		if got != tc.want {
			t.Fatalf("parse format %q expected %q, got %q", tc.input, tc.want, got)
		}
	}

	_, err := ParseFormat("xml")
	if err == nil || !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("expected unknown dashboard format error, got %v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, testDashboardConfigFile)
	content := "dashboard:\n  repos:\n    - path: ./api\n      name: API\n      language: go\n  baseline_store: /ci/baselines\n  output: json\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.ConfigDir != tmpDir {
		t.Fatalf("expected config dir %q, got %q", tmpDir, loaded.ConfigDir)
	}
	if loaded.Dashboard.Output != "json" || loaded.Dashboard.BaselineStore != "/ci/baselines" {
		t.Fatalf("unexpected dashboard config payload: %#v", loaded.Dashboard)
	}
	if len(loaded.Dashboard.Repos) != 1 || loaded.Dashboard.Repos[0].Path != "./api" {
		t.Fatalf("unexpected dashboard config repos: %#v", loaded.Dashboard.Repos)
	}
}

func TestLoadConfigRequiresRepos(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, testDashboardConfigFile)
	content := "dashboard:\n  output: html\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "at least one repo") {
		t.Fatalf("expected config repo validation error, got %v", err)
	}
}

func TestAggregate(t *testing.T) {
	reportA := report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name: "shared",
				Recommendations: []report.Recommendation{
					{Code: "remove-unused-dependency"},
				},
				RiskCues: []report.RiskCue{
					{Code: "cve-2026-1111", Severity: "critical", Message: "cve issue"},
				},
			},
			{Name: "a-only"},
		},
		Summary: &report.Summary{DeniedLicenseCount: 2},
	}
	reportB := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "shared"},
			{Name: "b-only"},
		},
	}
	reportC := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "shared"},
			{Name: "c-only"},
		},
	}

	data := Aggregate(time.Date(2026, time.March, 10, 1, 2, 3, 0, time.UTC), []RepoAnalysis{
		{Input: RepoInput{Name: testRepoA, Path: "./a"}, Report: reportA},
		{Input: RepoInput{Name: testRepoB, Path: "./b"}, Report: reportB},
		{Input: RepoInput{Name: testRepoC, Path: "./c"}, Report: reportC},
		{Input: RepoInput{Name: testRepoD, Path: "./d"}, Err: errors.New("failed")},
	})

	if data.Summary.TotalRepos != 4 {
		t.Fatalf("expected four repos total, got %+v", data.Summary)
	}
	if data.Summary.TotalDeps != 6 {
		t.Fatalf("expected six dependencies, got %+v", data.Summary)
	}
	if data.Summary.TotalWasteCandidates != 1 {
		t.Fatalf("expected one waste candidate, got %+v", data.Summary)
	}
	if data.Summary.CrossRepoDuplicates != 1 {
		t.Fatalf("expected one cross-repo duplicate, got %+v", data.Summary)
	}
	if data.Summary.CriticalCVEs != 1 {
		t.Fatalf("expected one critical CVE signal, got %+v", data.Summary)
	}
	if len(data.CrossRepoDeps) != 1 || data.CrossRepoDeps[0].Name != "shared" || data.CrossRepoDeps[0].Count != 3 {
		t.Fatalf("unexpected cross-repo dependency set: %#v", data.CrossRepoDeps)
	}
	if len(data.SourceWarnings) != 1 || !strings.Contains(data.SourceWarnings[0], "failed") {
		t.Fatalf("expected source warning from failed repo analysis, got %#v", data.SourceWarnings)
	}
}

func TestFormatReport(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{Name: testRepoA, Path: "./a", DependencyCount: 1},
		},
		Summary: Summary{TotalRepos: 1, TotalDeps: 1},
	}

	jsonOutput, err := FormatReport(reportData, FormatJSON)
	if err != nil || !strings.Contains(jsonOutput, "\"total_repos\": 1") {
		t.Fatalf("expected json output, err=%v output=%q", err, jsonOutput)
	}

	csvOutput, err := FormatReport(reportData, FormatCSV)
	if err != nil || !strings.Contains(csvOutput, "total_repos,1") {
		t.Fatalf("expected csv output, err=%v output=%q", err, csvOutput)
	}

	htmlOutput, err := FormatReport(reportData, FormatHTML)
	if err != nil || !strings.Contains(strings.ToLower(htmlOutput), "<html") {
		t.Fatalf("expected html output, err=%v output=%q", err, htmlOutput)
	}
}

func TestFormatReportCSVIncludesCrossRepoRows(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{Name: testRepoA, Path: "./a", DependencyCount: 1},
		},
		Summary: Summary{
			TotalRepos:           1,
			TotalDeps:            1,
			CrossRepoDuplicates:  1,
			TotalWasteCandidates: 0,
		},
		CrossRepoDeps: []CrossRepoDependency{
			{Name: "shared-lib", Count: 3, Repositories: []string{"api", "web", "worker"}},
		},
	}

	csvOutput, err := FormatReport(reportData, FormatCSV)
	if err != nil {
		t.Fatalf("format csv with cross-repo rows: %v", err)
	}
	if !strings.Contains(csvOutput, "dependency_name,repo_count,repositories") {
		t.Fatalf("expected cross-repo header row, got %q", csvOutput)
	}
	if !strings.Contains(csvOutput, "shared-lib,3,api|web|worker") {
		t.Fatalf("expected cross-repo dependency row, got %q", csvOutput)
	}
}

func TestFormatReportUnknownFormat(t *testing.T) {
	_, err := FormatReport(Report{}, Format("xml"))
	if err == nil || !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("expected unknown format error, got %v", err)
	}
}

func TestLoadConfigRequiresPath(t *testing.T) {
	_, err := LoadConfig("   ")
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("expected config path required error, got %v", err)
	}
}

func TestScanRiskSignalsPrioritizesSeverityAndCountsCVEs(t *testing.T) {
	severity, criticalCVEs := scanRiskSignals([]report.DependencyReport{
		{
			RiskCues: []report.RiskCue{
				{Severity: "low", Code: "style"},
				{Severity: "medium", Code: "perf"},
				{Severity: "high", Code: "vuln-high"},
				{Severity: "critical", Code: "cve-2026-9999", Message: "critical vulnerability"},
			},
		},
	})

	if severity != "critical" {
		t.Fatalf("expected critical top severity, got %q", severity)
	}
	if criticalCVEs != 1 {
		t.Fatalf("expected one critical CVE signal, got %d", criticalCVEs)
	}
}

func TestRiskSeverityRankUnknown(t *testing.T) {
	if got := riskSeverityRank("unknown"); got != 0 {
		t.Fatalf("expected unknown severity to rank as 0, got %d", got)
	}
}

func TestCountDeniedLicensesFallbackFromDependencies(t *testing.T) {
	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{License: &report.DependencyLicense{Denied: true}},
			{License: &report.DependencyLicense{Denied: false}},
		},
	}
	if got := countDeniedLicenses(reportData); got != 1 {
		t.Fatalf("expected one denied license from dependency list, got %d", got)
	}
}

func TestBuildCrossRepoDependenciesSortOrder(t *testing.T) {
	dependencies := buildCrossRepoDependencies(map[string]map[string]struct{}{
		"zeta":  {testRepoA: {}, testRepoB: {}, testRepoC: {}},
		"alpha": {testRepoD: {}, "repo-e": {}, "repo-f": {}},
		"omega": {testRepoA: {}, testRepoB: {}, testRepoC: {}, testRepoD: {}},
		"skip":  {testRepoA: {}, testRepoB: {}},
	})
	if len(dependencies) != 3 {
		t.Fatalf("expected three cross-repo dependencies, got %#v", dependencies)
	}
	if dependencies[0].Name != "omega" || dependencies[0].Count != 4 {
		t.Fatalf("expected highest-count dependency first, got %#v", dependencies[0])
	}
	if dependencies[1].Name != "alpha" || dependencies[2].Name != "zeta" {
		t.Fatalf("expected alphabetical order for equal counts, got %#v", dependencies)
	}
}

func TestFormatHTMLIncludesCrossRepoSectionAndEscapes(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{
				Name:                "<api>",
				Path:                "./svc&api",
				Language:            "go",
				DependencyCount:     3,
				WasteCandidateCount: 1,
				TopRiskSeverity:     "critical",
				Error:               "<error>",
			},
		},
		Summary: Summary{TotalRepos: 1, TotalDeps: 3, CrossRepoDuplicates: 1},
		CrossRepoDeps: []CrossRepoDependency{
			{Name: "shared<&>", Count: 3, Repositories: []string{"api", "web", "worker"}},
		},
	}

	htmlOutput := formatHTML(reportData)
	if !strings.Contains(htmlOutput, "Cross-Repo Duplicate Dependencies") {
		t.Fatalf("expected cross-repo section in HTML output, got %q", htmlOutput)
	}
	if !strings.Contains(htmlOutput, "&lt;api&gt;") || !strings.Contains(htmlOutput, "./svc&amp;api") || !strings.Contains(htmlOutput, "&lt;error&gt;") {
		t.Fatalf("expected escaped HTML values in output, got %q", htmlOutput)
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, testDashboardConfigFile)
	if err := os.WriteFile(path, []byte("dashboard: ["), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatalf("expected YAML parse error for invalid config")
	}
}
