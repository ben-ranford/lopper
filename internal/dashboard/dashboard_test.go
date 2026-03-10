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
	path := filepath.Join(tmpDir, "lopper-org.yml")
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
	path := filepath.Join(tmpDir, "lopper-org.yml")
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
		{Input: RepoInput{Name: "repo-a", Path: "./a"}, Report: reportA},
		{Input: RepoInput{Name: "repo-b", Path: "./b"}, Report: reportB},
		{Input: RepoInput{Name: "repo-c", Path: "./c"}, Report: reportC},
		{Input: RepoInput{Name: "repo-d", Path: "./d"}, Err: errors.New("failed")},
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
			{Name: "repo-a", Path: "./a", DependencyCount: 1},
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
