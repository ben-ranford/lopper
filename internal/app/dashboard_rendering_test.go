package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
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

func TestExecuteDashboardRemediationQueueRequiresPreviewFeature(t *testing.T) {
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			singleRepoPath: {
				Dependencies: []report.DependencyReport{{
					Name: "vuln-lib",
					Vulnerabilities: []report.VulnerabilityFinding{{
						AdvisoryID: "GHSA-dashboard",
						Package:    "vuln-lib",
						Severity:   report.VulnerabilityPriorityHigh,
						Priority:   report.VulnerabilityPriorityHigh,
					}},
				}},
			},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "json"
	req.Dashboard.Repos = []DashboardRepo{{Name: "repo", Path: singleRepoPath}}

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard without remediation feature: %v", err)
	}
	disabledReport := dashboard.Report{}
	if err := json.Unmarshal([]byte(output), &disabledReport); err != nil {
		t.Fatalf("unmarshal disabled dashboard output: %v", err)
	}
	if len(disabledReport.RemediationItems) != 0 {
		t.Fatalf("expected remediation queue to be disabled by default, got %#v", disabledReport.RemediationItems)
	}

	req.Dashboard.Features = enabledDashboardRemediationQueueFeatures(t)
	output, err = application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard with remediation feature: %v", err)
	}
	enabledReport := dashboard.Report{}
	if err := json.Unmarshal([]byte(output), &enabledReport); err != nil {
		t.Fatalf("unmarshal enabled dashboard output: %v", err)
	}
	if len(enabledReport.RemediationItems) != 1 || enabledReport.RemediationItems[0].Category != "vulnerability" {
		t.Fatalf("expected vulnerability remediation item when feature is enabled, got %#v", enabledReport.RemediationItems)
	}
}

func TestExecuteDashboardPortfolioRoutesChildReposWithOwnCodeowners(t *testing.T) {
	tmp := t.TempDir()
	callerRepo := filepath.Join(tmp, "caller")
	repoA := filepath.Join(tmp, "services", "api")
	repoB := filepath.Join(tmp, "services", "web")

	testutil.MustWriteFile(t, filepath.Join(callerRepo, ".github", "CODEOWNERS"), "* @caller\n")
	testutil.MustWriteFile(t, filepath.Join(repoA, ".github", "CODEOWNERS"), "* @team-api\n")
	testutil.MustWriteFile(t, filepath.Join(repoB, "CODEOWNERS"), "* @team-web\n")

	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			repoA: {Dependencies: []report.DependencyReport{{
				Name: "api-lib",
				Vulnerabilities: []report.VulnerabilityFinding{{
					AdvisoryID: "GHSA-api",
					Package:    "api-lib",
					Severity:   report.VulnerabilityPriorityHigh,
					Priority:   report.VulnerabilityPriorityHigh,
				}},
			}}},
			repoB: {Dependencies: []report.DependencyReport{{
				Name: "web-lib",
				Vulnerabilities: []report.VulnerabilityFinding{{
					AdvisoryID: "GHSA-web",
					Package:    "web-lib",
					Severity:   report.VulnerabilityPriorityHigh,
					Priority:   report.VulnerabilityPriorityHigh,
				}},
			}}},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.RepoPath = callerRepo
	req.Dashboard.Format = "json"
	req.Dashboard.Repos = []DashboardRepo{
		{Name: "api", Path: repoA},
		{Name: "web", Path: repoB},
	}
	req.Dashboard.Features = mustResolveAppTestFeatures(t, DashboardRemediationQueuePreviewFeature, DashboardRemediationRoutingSummariesPreviewFeature)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard with child repo codeowners: %v", err)
	}

	reportData := dashboard.Report{}
	if err := json.Unmarshal([]byte(output), &reportData); err != nil {
		t.Fatalf("unmarshal dashboard output: %v", err)
	}
	if len(reportData.RemediationItems) != 2 {
		t.Fatalf("expected two routed remediation items, got %#v", reportData.RemediationItems)
	}

	routedByRepo := make(map[string]dashboard.RemediationItem, len(reportData.RemediationItems))
	for _, item := range reportData.RemediationItems {
		routedByRepo[item.Repo] = item
		if item.Owner == "@caller" {
			t.Fatalf("expected no caller CODEOWNERS leakage, got %#v", item)
		}
	}

	if got := routedByRepo["api"]; got.Owner != "@team-api" || got.RoutingSource != ".github/CODEOWNERS" || got.Due != "" || got.Status != "open" {
		t.Fatalf("expected api item to use api repo CODEOWNERS, got %#v", got)
	}
	if got := routedByRepo["web"]; got.Owner != "@team-web" || got.RoutingSource != "CODEOWNERS" || got.Due != "" || got.Status != "open" {
		t.Fatalf("expected web item to use web repo CODEOWNERS, got %#v", got)
	}
}

func TestExecuteDashboardSingleRepoRoutingStillUsesAnalysedRepoRoot(t *testing.T) {
	tmp := t.TempDir()
	callerRepo := filepath.Join(tmp, "caller")
	repoPath := filepath.Join(tmp, "repo")

	testutil.MustWriteFile(t, filepath.Join(callerRepo, ".github", "CODEOWNERS"), "* @caller\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, ".github", "CODEOWNERS"), "* @single-owner\n")

	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			repoPath: {Dependencies: []report.DependencyReport{{
				Name: "single-lib",
				Vulnerabilities: []report.VulnerabilityFinding{{
					AdvisoryID: "GHSA-single",
					Package:    "single-lib",
					Severity:   report.VulnerabilityPriorityHigh,
					Priority:   report.VulnerabilityPriorityHigh,
				}},
			}}},
		},
		errs: map[string]error{},
	}
	application := &App{Analyzer: analyzer}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.RepoPath = callerRepo
	req.Dashboard.Format = "json"
	req.Dashboard.Repos = []DashboardRepo{{Name: "repo", Path: repoPath}}
	req.Dashboard.Features = mustResolveAppTestFeatures(t, DashboardRemediationQueuePreviewFeature, DashboardRemediationRoutingSummariesPreviewFeature)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute single-repo dashboard routing: %v", err)
	}

	reportData := dashboard.Report{}
	if err := json.Unmarshal([]byte(output), &reportData); err != nil {
		t.Fatalf("unmarshal dashboard output: %v", err)
	}
	if len(reportData.RemediationItems) != 1 {
		t.Fatalf("expected one routed remediation item, got %#v", reportData.RemediationItems)
	}
	item := reportData.RemediationItems[0]
	if item.Owner != "@single-owner" || item.RoutingSource != ".github/CODEOWNERS" || item.Status != "open" {
		t.Fatalf("expected single repo routing from analysed repo root, got %#v", item)
	}
}

func TestExecuteDashboardJSONDistinguishesSameBasenameRepos(t *testing.T) {
	const (
		platformAPI = "./platform/api"
		servicesAPI = "./services/api"
		worker      = "./worker"
	)
	analyzer := &mapAnalyzer{
		reports: map[string]report.Report{
			platformAPI: {Dependencies: []report.DependencyReport{{Name: sharedDependencyName}}},
			servicesAPI: {Dependencies: []report.DependencyReport{{Name: sharedDependencyName}}},
			worker:      {Dependencies: []report.DependencyReport{{Name: sharedDependencyName}}},
		},
		errs: map[string]error{},
	}

	application := &App{Analyzer: analyzer}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Repos = []DashboardRepo{
		{Path: platformAPI},
		{Path: servicesAPI},
		{Path: worker},
	}
	req.Dashboard.Format = "json"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard: %v", err)
	}

	reportData := dashboard.Report{}
	if err := json.Unmarshal([]byte(output), &reportData); err != nil {
		t.Fatalf("unmarshal dashboard output: %v", err)
	}

	if reportData.Summary.CrossRepoDuplicates != 1 {
		t.Fatalf("expected same-basename repos to count as distinct duplicate participants, got %+v", reportData.Summary)
	}
	if len(reportData.CrossRepoDeps) != 1 {
		t.Fatalf("expected one cross-repo dependency, got %#v", reportData.CrossRepoDeps)
	}
	wantRepos := []string{"api (./platform/api)", "api (./services/api)", "worker"}
	if reportData.CrossRepoDeps[0].Count != 3 || !reflect.DeepEqual(reportData.CrossRepoDeps[0].Repositories, wantRepos) {
		t.Fatalf("unexpected cross-repo duplicate payload: %#v", reportData.CrossRepoDeps[0])
	}
}

func enabledDashboardRemediationQueueFeatures(t *testing.T) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:        "LOP-FEAT-0017",
		Name:        DashboardRemediationQueuePreviewFeature,
		Description: "Dashboard remediation queue preview",
		Lifecycle:   featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new remediation queue feature registry: %v", err)
	}
	features, err := registry.Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{DashboardRemediationQueuePreviewFeature},
	})
	if err != nil {
		t.Fatalf("resolve remediation queue feature: %v", err)
	}
	return features
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

func TestExecuteDashboardBaselineDoesNotExposeRemediationQueueWhenPreviewDisabled(t *testing.T) {
	tmp := t.TempDir()
	baselineStore := filepath.Join(tmp, "baselines")
	baseline := dashboard.Report{
		GeneratedAt: time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		RemediationItems: []dashboard.RemediationItem{{
			ID:              "rqi-baseline-only",
			Repo:            "api",
			Dependency:      "vuln-lib",
			Category:        "vulnerability",
			Severity:        report.VulnerabilityPriorityHigh,
			Priority:        report.VulnerabilityPriorityHigh,
			SuggestedAction: "Upgrade vuln-lib.",
		}},
	}
	if _, err := dashboard.SaveSnapshot(baselineStore, "label:baseline", baseline, time.Date(2026, time.March, 11, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("save dashboard baseline snapshot: %v", err)
	}

	application := &App{Analyzer: &mapAnalyzer{
		reports: map[string]report.Report{
			singleRepoPath: {Dependencies: []report.DependencyReport{{Name: "dep"}}},
		},
		errs: map[string]error{},
	}}

	req := DefaultRequest()
	req.Mode = ModeDashboard
	req.Dashboard.Format = "csv"
	req.Dashboard.Repos = []DashboardRepo{{Name: "api", Path: singleRepoPath}}
	req.Dashboard.BaselineStorePath = baselineStore
	req.Dashboard.BaselineKey = "label:baseline"

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute dashboard baseline compare: %v", err)
	}
	if !strings.Contains(output, "baseline_key,label:baseline") {
		t.Fatalf("expected ordinary baseline comparison to remain enabled, got %q", output)
	}
	for _, forbidden := range []string{"rqi-baseline-only", "remediation_id,kind", "remediation_id,baseline_status"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("expected remediation queue data to stay hidden when preview is disabled; found %q in %q", forbidden, output)
		}
	}
}
