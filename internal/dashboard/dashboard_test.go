package dashboard

import (
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestAggregateCrossRepoDuplicatesDistinguishesSameInferredRepoNames(t *testing.T) {
	reportData := Aggregate(time.Date(2026, time.March, 10, 1, 2, 3, 0, time.UTC), []RepoAnalysis{
		{
			Input: RepoInput{Name: "api", Path: "./platform/api"},
			Report: report.Report{
				Dependencies: []report.DependencyReport{{Name: "shared"}},
			},
		},
		{
			Input: RepoInput{Name: "api", Path: "./services/api"},
			Report: report.Report{
				Dependencies: []report.DependencyReport{{Name: "shared"}},
			},
		},
		{
			Input: RepoInput{Name: "worker", Path: "./worker"},
			Report: report.Report{
				Dependencies: []report.DependencyReport{{Name: "shared"}},
			},
		},
	})

	if reportData.Summary.CrossRepoDuplicates != 1 {
		t.Fatalf("expected same-name repos to count as distinct duplicate participants, got %+v", reportData.Summary)
	}
	if len(reportData.CrossRepoDeps) != 1 {
		t.Fatalf("expected one cross-repo dependency, got %#v", reportData.CrossRepoDeps)
	}

	dependency := reportData.CrossRepoDeps[0]
	wantRepos := []string{"api (./platform/api)", "api (./services/api)", "worker"}
	if dependency.Name != "shared" || dependency.Count != 3 || !reflect.DeepEqual(dependency.Repositories, wantRepos) {
		t.Fatalf("unexpected cross-repo dependency payload: %#v", dependency)
	}
}

func TestAggregateCopiesRemoteRevisionMetadata(t *testing.T) {
	reportData := Aggregate(time.Date(2026, time.March, 10, 1, 2, 3, 0, time.UTC), []RepoAnalysis{
		{
			Input: RepoInput{
				Name:           "api",
				Path:           "/cache/api",
				RepoURL:        "https://github.com/example/api.git",
				Revision:       RepoRevision{Branch: "release/2.0"},
				ResolvedCommit: "0123456789abcdef0123456789abcdef01234567",
				Language:       "go",
			},
			Report: report.Report{
				Dependencies: []report.DependencyReport{{Name: "dep"}},
			},
		},
	})

	if len(reportData.Repos) != 1 {
		t.Fatalf("expected one repo result, got %#v", reportData.Repos)
	}
	got := reportData.Repos[0]
	if got.RepoURL != "https://github.com/example/api.git" || got.ResolvedCommit != "0123456789abcdef0123456789abcdef01234567" || got.Revision == nil || got.Revision.Branch != "release/2.0" {
		t.Fatalf("expected remote revision metadata to be copied, got %#v", got)
	}
}

func TestFormatReport(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{
				Name:            testRepoA,
				Path:            "./a",
				RepoURL:         "https://github.com/example/a.git",
				Revision:        &RepoRevision{Tag: "v1.0.0"},
				ResolvedCommit:  "0123456789abcdef0123456789abcdef01234567",
				DependencyCount: 1,
			},
		},
		Summary: Summary{TotalRepos: 1, TotalDeps: 1},
	}

	jsonOutput, err := FormatReport(reportData, FormatJSON)
	if err != nil || !strings.Contains(jsonOutput, "\"total_repos\": 1") || !strings.Contains(jsonOutput, "\"resolved_commit\": \"0123456789abcdef0123456789abcdef01234567\"") {
		t.Fatalf("expected json output, err=%v output=%q", err, jsonOutput)
	}

	csvOutput, err := FormatReport(reportData, FormatCSV)
	if err != nil || !strings.Contains(csvOutput, "total_repos,1") || !strings.Contains(csvOutput, "tag:v1.0.0,0123456789abcdef0123456789abcdef01234567") {
		t.Fatalf("expected csv output, err=%v output=%q", err, csvOutput)
	}

	htmlOutput, err := FormatReport(reportData, FormatHTML)
	if err != nil || !strings.Contains(strings.ToLower(htmlOutput), "<html") || !strings.Contains(htmlOutput, "0123456789abcdef0123456789abcdef01234567") {
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

func TestFormatReportCSVSanitizesCrossRepoAndRepoFormulaPrefixes(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{
				Name:                  "+repo",
				Path:                  "@path",
				Language:              "go",
				DependencyCount:       1,
				WasteCandidateCount:   0,
				WasteCandidatePercent: 0,
			},
			{
				Name:                  "\trepo",
				Path:                  "\rpath",
				Language:              "python",
				DependencyCount:       1,
				WasteCandidateCount:   0,
				WasteCandidatePercent: 0,
			},
		},
		Summary: Summary{
			TotalRepos:           2,
			TotalDeps:            2,
			TotalWasteCandidates: 0,
			CrossRepoDuplicates:  1,
			CriticalCVEs:         0,
		},
		CrossRepoDeps: []CrossRepoDependency{
			{
				Name:         "-shared",
				Count:        3,
				Repositories: []string{"repo-a", "-repo-b", "@repo-c", "\trepo-d"},
			},
		},
	}

	csvOutput, err := FormatReport(reportData, FormatCSV)
	if err != nil {
		t.Fatalf("format csv with formula-like values: %v", err)
	}

	rows := mustReadDashboardCSVRows(t, csvOutput)
	repoRow := dashboardCSVRowAfterHeader(rows, "repo_name")
	crossRepoRow := dashboardCSVRowAfterHeader(rows, "dependency_name")

	if len(repoRow) != 13 || repoRow[0] != "'+repo" || repoRow[1] != "'@path" || repoRow[5] != "go" {
		t.Fatalf("expected sanitized repo csv row, got %#v", repoRow)
	}
	if len(crossRepoRow) != 3 || crossRepoRow[0] != "'-shared" || !strings.Contains(crossRepoRow[2], "'\trepo-d") {
		t.Fatalf("expected sanitized cross-repo csv row, got %#v", crossRepoRow)
	}

	if !dashboardCSVContainsRepoRow(rows, "'\trepo", "'\rpath") {
		t.Fatalf("expected additional sanitized repo row, got %#v", rows)
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
	dependencies := buildCrossRepoDependencies(map[string]map[string]crossRepoRepository{
		"zeta":  crossRepoTestRepos(testRepoA, testRepoB, testRepoC),
		"alpha": crossRepoTestRepos(testRepoD, "repo-e", "repo-f"),
		"omega": crossRepoTestRepos(testRepoA, testRepoB, testRepoC, testRepoD),
		"skip":  crossRepoTestRepos(testRepoA, testRepoB),
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

func crossRepoTestRepos(labels ...string) map[string]crossRepoRepository {
	repos := make(map[string]crossRepoRepository, len(labels))
	for _, label := range labels {
		repos[label] = crossRepoRepository{Label: label}
	}
	return repos
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

func TestDashboardCSVHelpersPropagateWriteErrors(t *testing.T) {
	reportData := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{Name: testRepoA, Path: "./a", Language: "go", DependencyCount: 1},
		},
		Summary: Summary{TotalRepos: 1, TotalDeps: 1},
		CrossRepoDeps: []CrossRepoDependency{
			{Name: "shared", Count: 3, Repositories: []string{"api", "web", "worker"}},
		},
	}

	if writeDashboardSummaryCSV(poisonedDashboardCSVWriter(t, &failingDashboardWriter{}).Write, reportData) == nil {
		t.Fatalf("expected summary CSV writer error")
	}

	if writeDashboardRepoRowsCSV(poisonedDashboardCSVWriter(t, &failingDashboardWriter{}).Write, reportData.Repos) == nil {
		t.Fatalf("expected repo row CSV writer error")
	}

	if writeDashboardCrossRepoRowsCSV(poisonedDashboardCSVWriter(t, &failingDashboardWriter{}).Write, reportData.CrossRepoDeps) == nil {
		t.Fatalf("expected cross-repo CSV writer error")
	}
	if writeDashboardBaselineRowsCSV(poisonedDashboardCSVWriter(t, &failingDashboardWriter{}).Write, &BaselineComparison{BaselineKey: "base"}) == nil {
		t.Fatalf("expected baseline CSV writer error")
	}
}

func TestDashboardBaselineSnapshotAndComparison(t *testing.T) {
	tmp := t.TempDir()
	baseline := Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{Name: "api", Path: "./api", DependencyCount: 1, WasteCandidateCount: 0, WasteCandidatePercent: 0, CriticalCVEs: 0, DeniedLicenseCount: 0},
		},
		Summary: Summary{TotalRepos: 1, TotalDeps: 1, TotalWasteCandidates: 0, CrossRepoDuplicates: 0, CriticalCVEs: 0},
	}

	snapshotPath, err := SaveSnapshot(filepath.Join(tmp, "baselines"), "label:weekly", baseline, time.Date(2026, time.March, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("save baseline snapshot: %v", err)
	}
	loaded, key, err := LoadWithKey(snapshotPath)
	if err != nil {
		t.Fatalf("load baseline snapshot: %v", err)
	}
	if key != "label:weekly" {
		t.Fatalf("expected loaded key label:weekly, got %q", key)
	}

	current := Report{
		GeneratedAt: time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC),
		Repos: []RepoResult{
			{Name: "api", Path: "./api", DependencyCount: 2, WasteCandidateCount: 1, WasteCandidatePercent: 50, CriticalCVEs: 1, DeniedLicenseCount: 1},
			{Name: "worker", Path: "./worker", DependencyCount: 1, WasteCandidateCount: 0, WasteCandidatePercent: 0, CriticalCVEs: 0, DeniedLicenseCount: 0},
		},
		Summary: Summary{TotalRepos: 2, TotalDeps: 3, TotalWasteCandidates: 1, CrossRepoDuplicates: 1, CriticalCVEs: 1},
	}

	updated, err := ApplyBaselineWithKeys(current, loaded, key, "commit:head")
	if err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	if updated.BaselineComparison == nil {
		t.Fatalf("expected baseline comparison on dashboard report")
	}
	if updated.BaselineComparison.SummaryDelta.TotalReposDelta != 1 {
		t.Fatalf("expected total repos delta 1, got %+v", updated.BaselineComparison.SummaryDelta)
	}
	if len(updated.BaselineComparison.Added) != 1 || updated.BaselineComparison.Added[0].Name != "worker" {
		t.Fatalf("expected worker repo to be marked added, got %#v", updated.BaselineComparison.Added)
	}
}

func poisonedDashboardCSVWriter(t *testing.T, writer *failingDashboardWriter) *csv.Writer {
	t.Helper()

	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write([]string{"seed"}); err != nil {
		t.Fatalf("seed csv writer: %v", err)
	}
	csvWriter.Flush()
	if csvWriter.Error() == nil {
		t.Fatalf("expected seed write to poison csv writer")
	}
	return csvWriter
}

func mustReadDashboardCSVRows(t *testing.T, csvOutput string) [][]string {
	t.Helper()

	reader := csv.NewReader(strings.NewReader(csvOutput))
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read csv output: %v", err)
	}

	return rows
}

func dashboardCSVRowAfterHeader(rows [][]string, header string) []string {
	for i, row := range rows {
		if len(row) > 0 && row[0] == header && i+1 < len(rows) {
			return rows[i+1]
		}
	}

	return nil
}

func dashboardCSVContainsRepoRow(rows [][]string, name, path string) bool {
	for _, row := range rows {
		if len(row) == 13 && row[0] == name && row[1] == path {
			return true
		}
	}

	return false
}

type failingDashboardWriter struct {
	writes int
}

func (w *failingDashboardWriter) Write(_ []byte) (int, error) {
	w.writes++
	return 0, errors.New("boom")
}
