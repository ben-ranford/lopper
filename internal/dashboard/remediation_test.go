package dashboard

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestAggregateWithRemediationQueueBuildsOrderedStableItems(t *testing.T) {
	const duplicateDependency = "shared-lib"
	analyses := []RepoAnalysis{
		{
			Input: RepoInput{Name: "api", Path: "./api"},
			Report: report.Report{
				Dependencies: []report.DependencyReport{
					{
						Name: "vuln-lib",
						Vulnerabilities: []report.VulnerabilityFinding{
							{
								AdvisoryID:   "GHSA-dashboard",
								Package:      "vuln-lib",
								Severity:     report.VulnerabilityPriorityHigh,
								Priority:     report.VulnerabilityPriorityCritical,
								FixedVersion: "2.0.0",
								Source:       "local-advisory",
								Reachable:    true,
								Evidence:     []string{"imported by api"},
							},
						},
					},
					{
						Name: "waste-lib",
						Recommendations: []report.Recommendation{
							{Code: "remove-unused-dependency", Priority: report.VulnerabilityPriorityMedium, Message: "Remove waste-lib."},
						},
					},
					{Name: duplicateDependency},
				},
			},
		},
		{
			Input:  RepoInput{Name: "web", Path: "./web"},
			Report: report.Report{Dependencies: []report.DependencyReport{{Name: duplicateDependency}}},
		},
		{
			Input:  RepoInput{Name: "worker", Path: "./worker"},
			Report: report.Report{Dependencies: []report.DependencyReport{{Name: duplicateDependency}}},
		},
		{
			Input: RepoInput{Name: "broken", Path: "./broken"},
			Err:   errors.New("analysis failed"),
		},
	}

	first := AggregateWithOptions(time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC), analyses, AggregateOptions{IncludeRemediationQueue: true})
	second := AggregateWithOptions(time.Date(2026, time.March, 11, 0, 0, 0, 0, time.UTC), analyses, AggregateOptions{IncludeRemediationQueue: true})

	if len(first.RemediationItems) != 4 {
		t.Fatalf("expected four remediation items, got %#v", first.RemediationItems)
	}
	if remediationItemRank(first.RemediationItems[0]) < remediationItemRank(first.RemediationItems[1]) {
		t.Fatalf("expected remediation items sorted by descending priority, got %#v", first.RemediationItems)
	}
	if !reflect.DeepEqual(remediationIDs(first.RemediationItems), remediationIDs(second.RemediationItems)) {
		t.Fatalf("expected stable remediation IDs, first=%#v second=%#v", remediationIDs(first.RemediationItems), remediationIDs(second.RemediationItems))
	}

	assertRemediationCategory(t, first.RemediationItems, remediationCategoryRepoError, "")
	assertRemediationCategory(t, first.RemediationItems, remediationCategoryVulnerability, "vuln-lib")
	assertRemediationCategory(t, first.RemediationItems, remediationCategoryWaste, "waste-lib")
	duplicate := assertRemediationCategory(t, first.RemediationItems, remediationCategoryDuplicateDependency, duplicateDependency)
	if !strings.Contains(duplicate.Repo, "api") || !strings.Contains(duplicate.Repo, "web") || !strings.Contains(duplicate.Repo, "worker") {
		t.Fatalf("expected duplicate item repo list to include all repos, got %#v", duplicate)
	}
}

func TestAggregateWithRemediationQueueDedupeDuplicateDependencyItems(t *testing.T) {
	generatedAt := time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC)
	analyses := []RepoAnalysis{{
		Input: RepoInput{Name: "api", Path: "./api"},
		Report: report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "denied-lib", License: &report.DependencyLicense{SPDX: "GPL-3.0", Denied: true, Evidence: []string{"policy"}}},
				{Name: "denied-lib", License: &report.DependencyLicense{SPDX: "GPL-3.0", Denied: true, Evidence: []string{"duplicate row"}}},
			},
		},
	}}
	reportData := AggregateWithOptions(generatedAt, analyses, AggregateOptions{IncludeRemediationQueue: true})

	count := 0
	var item RemediationItem
	for _, candidate := range reportData.RemediationItems {
		if candidate.Category == remediationCategoryLicense && candidate.Dependency == "denied-lib" {
			count++
			item = candidate
		}
	}
	if count != 1 {
		t.Fatalf("expected duplicate dependency license items to dedupe, got %#v", reportData.RemediationItems)
	}
	if !reflect.DeepEqual(item.Evidence, []string{"license: GPL-3.0", "policy", "duplicate row"}) {
		t.Fatalf("expected duplicate evidence to merge, got %#v", item)
	}
}

func TestDashboardBaselineComparisonRemediationItems(t *testing.T) {
	regressedID := stableRemediationID("api", "vuln-lib", "vulnerability", "GHSA-regressed")
	existingID := stableRemediationID("api", "old-lib", "waste", "remove-unused-dependency")
	removedID := stableRemediationID("api", "removed-lib", "license")
	newID := stableRemediationID("api", "new-lib", "risk")

	current := Report{
		RemediationItems: []RemediationItem{
			{ID: regressedID, Repo: "api", Dependency: "vuln-lib", Category: remediationCategoryVulnerability, Priority: report.VulnerabilityPriorityCritical, Severity: report.VulnerabilityPriorityCritical, SuggestedAction: "upgrade"},
			{ID: existingID, Repo: "api", Dependency: "old-lib", Category: remediationCategoryWaste, Priority: report.VulnerabilityPriorityLow, Severity: report.VulnerabilityPriorityLow, SuggestedAction: "remove"},
			{ID: newID, Repo: "api", Dependency: "new-lib", Category: remediationCategoryRisk, Priority: report.VulnerabilityPriorityMedium, Severity: report.VulnerabilityPriorityMedium, SuggestedAction: "triage"},
		},
	}
	baseline := Report{
		RemediationItems: []RemediationItem{
			{ID: regressedID, Repo: "api", Dependency: "vuln-lib", Category: remediationCategoryVulnerability, Priority: report.VulnerabilityPriorityHigh, Severity: report.VulnerabilityPriorityHigh, SuggestedAction: "upgrade"},
			{ID: existingID, Repo: "api", Dependency: "old-lib", Category: remediationCategoryWaste, Priority: report.VulnerabilityPriorityLow, Severity: report.VulnerabilityPriorityLow, SuggestedAction: "remove"},
			{ID: removedID, Repo: "api", Dependency: "removed-lib", Category: remediationCategoryLicense, Priority: report.VulnerabilityPriorityHigh, Severity: report.VulnerabilityPriorityHigh, SuggestedAction: "replace"},
		},
	}

	comparison := ComputeBaselineComparison(current, baseline)
	if len(comparison.NewRemediationItems) != 1 || comparison.NewRemediationItems[0].ID != newID {
		t.Fatalf("expected new remediation item, got %#v", comparison.NewRemediationItems)
	}
	if len(comparison.RegressedRemediationItems) != 1 || comparison.RegressedRemediationItems[0].ID != regressedID {
		t.Fatalf("expected regressed remediation item, got %#v", comparison.RegressedRemediationItems)
	}
	if len(comparison.ExistingRemediationItems) != 1 || comparison.ExistingRemediationItems[0].ID != existingID {
		t.Fatalf("expected existing remediation item, got %#v", comparison.ExistingRemediationItems)
	}
	if len(comparison.RemovedRemediationItems) != 1 || comparison.RemovedRemediationItems[0].ID != removedID {
		t.Fatalf("expected removed remediation item, got %#v", comparison.RemovedRemediationItems)
	}

	updated, err := ApplyBaselineWithKeys(current, baseline, "label:baseline", "commit:head")
	if err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	statuses := map[string]string{}
	for _, item := range updated.RemediationItems {
		statuses[item.ID] = item.BaselineStatus
	}
	if statuses[newID] != string(RemediationItemNew) || statuses[regressedID] != string(RemediationItemRegressed) || statuses[existingID] != string(RemediationItemExisting) {
		t.Fatalf("expected current remediation items to be annotated by baseline status, got %#v", statuses)
	}
}

func TestFormatReportRemediationQueueCSVAndHTML(t *testing.T) {
	item := RemediationItem{
		ID:              "rqi-test",
		Repo:            "<api>",
		RepoPath:        "./api",
		Dependency:      "vuln-lib",
		Category:        remediationCategoryVulnerability,
		Severity:        report.VulnerabilityPriorityHigh,
		Priority:        report.VulnerabilityPriorityCritical,
		Evidence:        []string{"<evidence>"},
		SuggestedAction: "Upgrade <vuln-lib>.",
		BaselineStatus:  string(RemediationItemNew),
	}
	reportData := Report{
		GeneratedAt:      time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC),
		Summary:          Summary{TotalRepos: 1},
		Repos:            []RepoResult{{Name: "api", Path: "./api"}},
		RemediationItems: []RemediationItem{item},
		BaselineComparison: &BaselineComparison{
			BaselineKey:           "label:baseline",
			RemediationItemDeltas: []RemediationItemDelta{remediationItemDelta(RemediationItemNew, item, RemediationItem{})},
		},
	}

	csvOutput, err := FormatReport(reportData, FormatCSV)
	if err != nil {
		t.Fatalf("format remediation csv: %v", err)
	}
	for _, want := range []string{"remediation_id,baseline_status,repo,repo_path,dependency,category,severity,priority,evidence,suggested_action", "rqi-test,new,<api>,./api,vuln-lib,vulnerability,high,critical,<evidence>,Upgrade <vuln-lib>.", "remediation_id,kind,repo,repo_path,dependency,category"} {
		if !strings.Contains(csvOutput, want) {
			t.Fatalf("expected csv output to contain %q, got %q", want, csvOutput)
		}
	}

	htmlOutput, err := FormatReport(reportData, FormatHTML)
	if err != nil {
		t.Fatalf("format remediation html: %v", err)
	}
	for _, want := range []string{"Remediation Queue", "Remediation Queue Baseline", "&lt;api&gt;", "Upgrade &lt;vuln-lib&gt;."} {
		if !strings.Contains(htmlOutput, want) {
			t.Fatalf("expected html output to contain %q, got %q", want, htmlOutput)
		}
	}
}

func TestRemediationHelperBranchCoverage(t *testing.T) {
	loadDelta := 3
	analysis := RepoAnalysis{
		Input: RepoInput{Name: "api", Path: "./api"},
		Report: report.Report{
			Dependencies: []report.DependencyReport{
				{
					Name: "",
					Vulnerabilities: []report.VulnerabilityFinding{{
						AdvisoryID: "GHSA-skipped",
						Severity:   report.VulnerabilityPriorityHigh,
					}},
				},
				{
					Name: "fallback-vuln",
					Vulnerabilities: []report.VulnerabilityFinding{{
						Severity: "moderate",
					}},
				},
				{
					Name:    "permitted-lib",
					License: &report.DependencyLicense{SPDX: "MIT", Denied: false},
				},
				{
					Name:    "custom-license-lib",
					License: &report.DependencyLicense{Raw: "Custom", Denied: true, Source: "policy", Evidence: []string{"approval needed"}},
				},
				{
					Name: "recommend-lib",
					Recommendations: []report.Recommendation{
						{Code: "", Message: ""},
						{Code: "review-ownership", Rationale: "low ownership", ConfidenceReasonCodes: []string{"stale"}},
					},
				},
				{
					Name: "risk-lib",
					RiskCues: []report.RiskCue{
						{Code: "", Message: ""},
						{Code: "CVE-signal", Message: "Investigate", ConfidenceReasonCodes: []string{"low-confidence"}},
					},
				},
			},
			BaselineComparison: &report.BaselineComparison{
				RuntimeRegressions: []report.DependencyDelta{
					{
						Name:     "runtime-lib",
						Language: "python",
						RuntimeDelta: &report.RuntimeDelta{
							NewRuntimeLoads:       true,
							RuntimeOnlyRegression: true,
							LoadCountDelta:        &loadDelta,
						},
					},
					{Name: "   "},
				},
			},
		},
	}
	items := repoRemediationItems(analysis, map[string]int{"api": 1})

	fallback := assertRemediationCategory(t, items, remediationCategoryVulnerability, "fallback-vuln")
	if fallback.Priority != report.VulnerabilityPriorityMedium || !strings.Contains(fallback.SuggestedAction, "the advisory") {
		t.Fatalf("expected fallback vulnerability item to normalize moderate priority and action, got %#v", fallback)
	}
	license := assertRemediationCategory(t, items, remediationCategoryLicense, "custom-license-lib")
	if !reflect.DeepEqual(license.Evidence, []string{"license: Custom", "source: policy", "approval needed"}) {
		t.Fatalf("expected license evidence to include source and policy evidence, got %#v", license.Evidence)
	}
	recommendation := assertRemediationCategory(t, items, remediationCategoryRecommendation, "recommend-lib")
	if !strings.Contains(recommendation.SuggestedAction, "review-ownership") || !reflect.DeepEqual(recommendation.Evidence, []string{"recommendation: review-ownership", "low ownership", "stale"}) {
		t.Fatalf("expected default recommendation action and evidence, got %#v", recommendation)
	}
	risk := assertRemediationCategory(t, items, remediationCategoryRisk, "risk-lib")
	if risk.Priority != report.VulnerabilityPriorityLow || !reflect.DeepEqual(risk.Evidence, []string{"risk: CVE-signal", "Investigate", "low-confidence"}) {
		t.Fatalf("expected default risk severity and evidence, got %#v", risk)
	}
	runtime := assertRemediationCategory(t, items, remediationCategoryRuntimeRegression, "runtime-lib")
	for _, want := range []string{"runtime regression detected", "language: python", "new runtime loads", "runtime-only regression", "load_count_delta: +3"} {
		if !containsString(runtime.Evidence, want) {
			t.Fatalf("expected runtime evidence %q in %#v", want, runtime.Evidence)
		}
	}

	if item, ok := licenseRemediationItem("repo", "repo", "./repo", "nil-license", nil); ok || item.ID != "" {
		t.Fatalf("expected nil license to produce no item, got %#v", item)
	}
	if len(runtimeRegressionEvidence(report.DependencyDelta{})) != 1 {
		t.Fatalf("expected bare runtime regression evidence to keep only the base marker")
	}
	if got := crossRepoRemediationItems([]CrossRepoDependency{{Name: "   "}, {Name: "shared", Count: 3, Repositories: []string{"api", "web", "worker"}}}); len(got) != 1 {
		t.Fatalf("expected blank cross-repo dependency to be skipped, got %#v", got)
	}
}

func TestRemediationOrderingAndNormalizationBranches(t *testing.T) {
	items := []RemediationItem{
		{ID: "z", Repo: "web", Dependency: "dep", Category: remediationCategoryDuplicateDependency, Priority: "unknown"},
		{ID: "a", Repo: "api", Dependency: "dep", Category: remediationCategoryVulnerability, Priority: report.VulnerabilityPriorityHigh},
		{ID: "b", Repo: "api", Dependency: "dep", Category: remediationCategoryRisk, Priority: report.VulnerabilityPriorityHigh},
		{ID: "c", Repo: "api", Dependency: "zzz", Category: remediationCategoryRisk, Priority: report.VulnerabilityPriorityHigh},
	}
	sortRemediationItems(items)
	if got := remediationIDs(items); !reflect.DeepEqual(got, []string{"a", "b", "c", "z"}) {
		t.Fatalf("unexpected remediation sort order: %#v", got)
	}

	deltas := []RemediationItemDelta{
		{ID: "z", Kind: RemediationItemExisting, Repo: "web", Dependency: "dep", Priority: report.VulnerabilityPriorityLow},
		{ID: "c", Kind: RemediationItemRegressed, Repo: "api", Dependency: "zzz", Priority: report.VulnerabilityPriorityHigh},
		{ID: "b", Kind: RemediationItemNew, Repo: "api", Dependency: "dep", Priority: report.VulnerabilityPriorityHigh},
		{ID: "a", Kind: RemediationItemNew, Repo: "api", Dependency: "dep", Priority: report.VulnerabilityPriorityHigh},
	}
	sortRemediationItemDeltas(deltas)
	if got := []string{deltas[0].ID, deltas[1].ID, deltas[2].ID, deltas[3].ID}; !reflect.DeepEqual(got, []string{"a", "b", "c", "z"}) {
		t.Fatalf("unexpected remediation delta sort order: %#v", got)
	}

	for _, category := range []string{
		remediationCategoryRepoError,
		remediationCategoryVulnerability,
		remediationCategoryLicense,
		remediationCategoryRuntimeRegression,
		remediationCategoryRisk,
		remediationCategoryWaste,
		remediationCategoryRecommendation,
		remediationCategoryDuplicateDependency,
		"other",
	} {
		if categoryRank(category) < 0 {
			t.Fatalf("category %q returned invalid rank", category)
		}
	}
	for _, level := range []string{report.VulnerabilityPriorityCritical, report.VulnerabilityPriorityHigh, report.VulnerabilityPriorityMedium, report.VulnerabilityPriorityLow, "other"} {
		if remediationLevelRank(level) < 0 {
			t.Fatalf("level %q returned invalid rank", level)
		}
	}
	if normalizeRemediationLevel("  MODERATE ") != report.VulnerabilityPriorityMedium {
		t.Fatalf("expected moderate to normalize to medium")
	}
	if got := firstNonBlank(" ", "\t"); got != "" {
		t.Fatalf("expected blank values to return empty string, got %q", got)
	}
}

func TestRemediationDedupeAndSnapshotNormalizationBranches(t *testing.T) {
	items := []RemediationItem{
		{ID: "same", Repo: " api ", Dependency: " dep ", Category: remediationCategoryRisk, Priority: report.VulnerabilityPriorityLow, Evidence: []string{"first", " "}, SuggestedAction: " triage "},
		{ID: "same", Repo: "api", Dependency: "dep", Category: remediationCategoryRisk, Priority: report.VulnerabilityPriorityCritical, Evidence: []string{"first", "second"}, SuggestedAction: "escalate"},
		{ID: " ", Repo: "ignored", Priority: report.VulnerabilityPriorityCritical},
		{ID: "tie-b", Repo: "api", Dependency: "dep", Category: remediationCategoryRisk, Priority: report.VulnerabilityPriorityHigh},
		{ID: "tie-a", Repo: "api", Dependency: "dep", Category: remediationCategoryRisk, Priority: report.VulnerabilityPriorityHigh},
	}

	deduped := dedupeAndSortRemediationItems(items)
	if got := remediationIDs(deduped); !reflect.DeepEqual(got, []string{"same", "tie-a", "tie-b"}) {
		t.Fatalf("unexpected dedupe order: %#v", got)
	}
	if deduped[0].Priority != report.VulnerabilityPriorityCritical || !reflect.DeepEqual(deduped[0].Evidence, []string{"first", "second"}) {
		t.Fatalf("expected duplicate to keep higher priority and merged evidence, got %#v", deduped[0])
	}

	byID := remediationItemsByID(items)
	if len(byID) != 3 || byID["same"].Priority != report.VulnerabilityPriorityCritical {
		t.Fatalf("expected remediationItemsByID to dedupe and skip blank IDs, got %#v", byID)
	}

	snapshot := normalizeSnapshotReport(Report{
		Repos:            []RepoResult{{Name: "web", Path: "./web", DependencyCount: 2}, {Name: "api", Path: "./api", DependencyCount: 1}},
		RemediationItems: items,
	})
	if snapshot.Summary.TotalRepos != 2 || snapshot.Summary.TotalDeps != 3 {
		t.Fatalf("expected empty summary to be computed during snapshot normalization, got %#v", snapshot.Summary)
	}
	if got := []string{snapshot.Repos[0].Name, snapshot.Repos[1].Name}; !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Fatalf("expected repos sorted during snapshot normalization, got %#v", got)
	}
}

func remediationIDs(items []RemediationItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func assertRemediationCategory(t *testing.T, items []RemediationItem, category, dependency string) RemediationItem {
	t.Helper()
	for _, item := range items {
		if item.Category == category && (dependency == "" || item.Dependency == dependency) {
			return item
		}
	}
	t.Fatalf("expected remediation item category=%q dependency=%q in %#v", category, dependency, items)
	return RemediationItem{}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
