package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestPRReviewRequiresPreviewFeature(t *testing.T) {
	application := &App{Analyzer: &fakeAnalyzer{}}
	req := DefaultRequest()
	req.Mode = ModePRReview

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), report.DependencySurfacePRReviewPreviewFeature) {
		t.Fatalf("expected pr-review preview feature error, got %v", err)
	}
}

func TestBuildPRReviewArtifactSeparatesSections(t *testing.T) {
	base := report.Report{
		Dependencies: []report.DependencyReport{
			prReviewTestDependency("lib", "npm", "1.0.0", 100, 90, false),
			prReviewTestDependency("down", "npm", "2.0.0", 100, 90, false),
			prReviewTestDependency("removed", "npm", "1.0.0", 50, 95, false),
			prReviewTestDependency("channel", "npm", "release-a", 100, 90, false),
			{Name: "unknown-version", Language: "js-ts", EstimatedUnusedBytes: 10, UsedPercent: 90},
		},
	}
	head := report.Report{
		Dependencies: []report.DependencyReport{
			prReviewTestDependency("lib", "npm", "1.2.0", 4096, 50, true),
			prReviewTestDependency("down", "npm", "1.0.0", 100, 90, false),
			prReviewTestDependency("added", "npm", "1.0.0", 20, 80, false),
			prReviewTestDependency("channel", "npm", "release-b", 100, 90, false),
			{Name: "unknown-version", Language: "js-ts", EstimatedUnusedBytes: 11, UsedPercent: 89},
		},
	}
	head.Dependencies[0].Vulnerabilities = []report.VulnerabilityFinding{{
		AdvisoryID:    "GHSA-test",
		Package:       "lib",
		Severity:      report.VulnerabilityPriorityHigh,
		Priority:      report.VulnerabilityPriorityCritical,
		PriorityScore: 95,
		Reachable:     true,
		Evidence:      []string{"imported by app"},
	}}

	baseSHA := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)
	generatedAt := time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC)
	req := PRReviewRequest{MaterialWasteBytes: 1024, MaxRows: 20}
	req.Thresholds.ReachableVulnerabilityPriority = report.VulnerabilityPriorityCritical
	artifact := buildPRReviewArtifact(prReviewArtifactInput{
		repoPath:   "/repo",
		baseSHA:    baseSHA,
		headSHA:    headSHA,
		baseReport: base,
		headReport: head,
		req:        req,
		now:        generatedAt,
	})

	if artifact.Summary.Added != 1 ||
		artifact.Summary.Removed != 1 ||
		artifact.Summary.Upgraded != 1 ||
		artifact.Summary.Downgraded != 1 ||
		artifact.Summary.PolicyChanged != 1 ||
		artifact.Summary.NewlyReachable != 1 ||
		artifact.Summary.MateriallyWorsened != 1 {
		t.Fatalf("unexpected pr-review summary: %#v", artifact.Summary)
	}
	if artifact.Summary.VersionChanged != 1 {
		t.Fatalf("expected non-orderable identity versions to produce one version-changed row, got %#v", artifact.Summary)
	}
	if artifact.Summary.RegressionCount < 4 {
		t.Fatalf("expected downgrade, policy, vulnerability, and waste regressions, got %#v", artifact.Summary)
	}

	markdown, err := formatPRReviewArtifact(artifact, prReviewFormatMarkdown, 1)
	if err != nil {
		t.Fatalf("format pr-review markdown: %v", err)
	}
	assertContainsAll(t, markdown, []string{
		"## Lopper PR Review",
		"Added Dependencies",
		"Downgraded Dependencies",
		"Version Changed Dependencies",
		"Newly Reachable Vulnerabilities",
		"GHSA-test",
		"critical",
		"version ordering was not inferred",
		"static dependency-surface analysis",
	})

	jsonOutput, err := formatPRReviewArtifact(artifact, prReviewFormatJSON, 20)
	if err != nil {
		t.Fatalf("format pr-review json: %v", err)
	}
	assertContainsAll(t, jsonOutput, []string{prReviewSchemaVersion, "GHSA-test", prReviewCategoryMateriallyWorsened})
}

func TestBuildPRReviewArtifactAppliesReachableVulnerabilityPriorityThreshold(t *testing.T) {
	baseDependency := prReviewTestDependency("lib", "npm", "1.0.0", 100, 90, false)
	headDependency := baseDependency
	headDependency.Vulnerabilities = []report.VulnerabilityFinding{
		{
			AdvisoryID: "GHSA-low",
			Package:    "lib",
			Severity:   report.VulnerabilityPriorityLow,
			Priority:   report.VulnerabilityPriorityLow,
			Reachable:  true,
		},
		{
			AdvisoryID: "GHSA-critical",
			Package:    "lib",
			Severity:   report.VulnerabilityPriorityCritical,
			Priority:   report.VulnerabilityPriorityCritical,
			Reachable:  true,
		},
	}
	req := PRReviewRequest{}
	req.Thresholds.ReachableVulnerabilityPriority = report.VulnerabilityPriorityHigh

	artifact := buildPRReviewArtifact(prReviewArtifactInput{
		baseReport: report.Report{Dependencies: []report.DependencyReport{baseDependency}},
		headReport: report.Report{Dependencies: []report.DependencyReport{headDependency}},
		req:        req,
	})
	rows := prReviewTestSectionRows(t, artifact, prReviewCategoryNewlyReachable)
	if len(rows) != 2 {
		t.Fatalf("expected both newly reachable vulnerabilities to remain visible, got %#v", rows)
	}
	regressionsByAdvisory := make(map[string]bool, len(rows))
	for _, row := range rows {
		regressionsByAdvisory[row.AdvisoryID] = row.Regression
	}
	if regressionsByAdvisory["GHSA-low"] {
		t.Fatalf("expected low-priority vulnerability below the high threshold not to regress, got %#v", rows)
	}
	if !regressionsByAdvisory["GHSA-critical"] {
		t.Fatalf("expected critical vulnerability to regress at the high threshold, got %#v", rows)
	}
	if artifact.Summary.NewlyReachable != 2 || artifact.Summary.RegressionCount != 1 {
		t.Fatalf("unexpected thresholded pr-review summary: %#v", artifact.Summary)
	}
}

func TestValidatePRReviewSHAs(t *testing.T) {
	baseSHA := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)
	if _, _, err := validatePRReviewSHAs(baseSHA, headSHA); err != nil {
		t.Fatalf("expected full SHAs to validate: %v", err)
	}
	if _, _, err := validatePRReviewSHAs("main", headSHA); err == nil {
		t.Fatalf("expected branch name base to be rejected")
	}
	if _, _, err := validatePRReviewSHAs(baseSHA, baseSHA); err == nil {
		t.Fatalf("expected identical SHAs to be rejected")
	}
	if _, _, err := validatePRReviewSHAs(baseSHA, "head"); err == nil {
		t.Fatalf("expected branch name head to be rejected")
	}
}

func TestPRReviewFormatHelpers(t *testing.T) {
	for _, value := range []string{"", "md", "pr-comment", "markdown"} {
		got, err := parsePRReviewFormat(value)
		if err != nil || got != prReviewFormatMarkdown {
			t.Fatalf("expected %q to resolve markdown, got %q err=%v", value, got, err)
		}
	}
	if got, err := parsePRReviewFormat("json"); err != nil || got != prReviewFormatJSON {
		t.Fatalf("expected json format, got %q err=%v", got, err)
	}
	if _, err := parsePRReviewFormat("xml"); err == nil {
		t.Fatalf("expected unknown pr-review format error")
	}
	if _, err := formatPRReviewArtifact(prReviewArtifact{}, "xml", 1); err == nil {
		t.Fatalf("expected unknown pr-review artifact format error")
	}
}

func TestFormatPRReviewArtifactMarkdownReportsOverflowAndWarnings(t *testing.T) {
	artifact := prReviewArtifact{
		BaseSHA:       strings.Repeat("a", 40),
		HeadSHA:       strings.Repeat("b", 40),
		AnalysisMode:  "static dependency-surface analysis",
		MergeBaseMode: "disabled",
		FullArtifact:  "See JSON artifact for full output.",
		Summary:       prReviewSummary{Added: 2},
		Sections: []prReviewSection{{
			ID:    prReviewCategoryAdded,
			Title: "Added Dependencies",
			Rows: []prReviewRow{
				{Dependency: "first", Language: "js-ts", HeadVersion: "1.0.0", EvidenceConfidence: "high", Evidence: []string{"lockfile"}},
				{Dependency: "second", Language: "js-ts", HeadVersion: "2.0.0", EvidenceConfidence: "high", Evidence: []string{"manifest"}},
			},
		}},
		Warnings: []string{"warning with | pipe"},
	}

	output, err := formatPRReviewArtifact(artifact, prReviewFormatMarkdown, 1)
	if err != nil {
		t.Fatalf("format markdown artifact with overflow: %v", err)
	}
	assertContainsAll(t, output, []string{
		"1 additional rows omitted from this section.",
		"1 rows were omitted from Markdown output. See JSON artifact for full output.",
		"### Notes",
		"warning with \\| pipe",
	})
}

func TestPRReviewRowSortingHelpers(t *testing.T) {
	rows := []prReviewRow{
		{Dependency: "zeta", Language: "go", Priority: "low", WasteDeltaBytes: 100},
		{Dependency: "alpha", Language: "js-ts", Priority: "critical", Regression: true},
		{Dependency: "beta", Language: "js-ts", Priority: "high", Regression: true},
		{Dependency: "delta", Language: "go", Priority: "medium", WasteDeltaBytes: 200},
	}
	sortPRReviewRows(rows)
	if rows[0].Dependency != "alpha" || rows[1].Dependency != "beta" || rows[2].Dependency != "delta" || rows[3].Dependency != "zeta" {
		t.Fatalf("unexpected sorted pr-review rows: %#v", rows)
	}
	tieRows := []prReviewRow{
		{Dependency: "zeta", Language: "go"},
		{Dependency: "alpha", Language: "go"},
		{Dependency: "beta", Language: "js-ts"},
	}
	sortPRReviewRows(tieRows)
	if tieRows[0].Dependency != "alpha" || tieRows[1].Dependency != "zeta" || tieRows[2].Dependency != "beta" {
		t.Fatalf("unexpected tied pr-review row order: %#v", tieRows)
	}
	wasteRows := []prReviewRow{
		{Dependency: "small", Language: "go", Priority: "medium", WasteDeltaBytes: 1},
		{Dependency: "large", Language: "go", Priority: "medium", WasteDeltaBytes: 2},
	}
	sortPRReviewRows(wasteRows)
	if wasteRows[0].Dependency != "large" {
		t.Fatalf("expected larger waste delta first, got %#v", wasteRows)
	}
}

func TestPRReviewVersionCategoryClassifiesOrderedAndUnorderedVersions(t *testing.T) {
	if got := prReviewVersionCategory("1.0.0", "2.0.0"); got != prReviewCategoryUpgraded {
		t.Fatalf("expected ordered increase to be an upgrade, got %q", got)
	}
	if got := prReviewVersionCategory("2.0.0", "1.0.0"); got != prReviewCategoryDowngraded {
		t.Fatalf("expected ordered decrease to be a downgrade, got %q", got)
	}
	if got := prReviewVersionCategory("release-a", "release-b"); got != prReviewCategoryVersionChanged {
		t.Fatalf("expected unordered versions to fall back to version-changed, got %q", got)
	}
	if got := prReviewVersionCategory("1.0.0", "1.0.0"); got != prReviewCategoryVersionChanged {
		t.Fatalf("expected equal versions to avoid upgrade/downgrade labels, got %q", got)
	}
}

func TestFindPRReviewDependencyByOrdinalUsesVersionlessKeyOrdering(t *testing.T) {
	dependencies := []report.DependencyReport{
		prReviewTestDependency("dup", "npm", "2.0.0", 20, 80, false),
		prReviewTestDependency("dup", "npm", "1.0.0", 10, 90, false),
		prReviewTestDependency("other", "npm", "1.0.0", 5, 95, false),
	}
	key := report.DependencyVersionlessKey(dependencies[0])

	first, ok := findPRReviewDependencyByOrdinal(dependencies, key, 0)
	if !ok || first.Identity.Version != "1.0.0" {
		t.Fatalf("expected ordinal 0 to return the sorted first duplicate, got %#v ok=%t", first, ok)
	}
	second, ok := findPRReviewDependencyByOrdinal(dependencies, key, 1)
	if !ok || second.Identity.Version != "2.0.0" {
		t.Fatalf("expected ordinal 1 to return the sorted second duplicate, got %#v ok=%t", second, ok)
	}
	if _, ok := findPRReviewDependencyByOrdinal(dependencies, key, 2); ok {
		t.Fatal("expected out-of-range ordinal lookup to fail")
	}
	if _, ok := findPRReviewDependencyByOrdinal(dependencies, "", 0); ok {
		t.Fatal("expected empty key lookup to fail")
	}
}

func TestPRReviewRowFallbackHelpers(t *testing.T) {
	fallback := prReviewRowForDependency(prReviewCategoryAdded, report.DependencyReport{Name: "unknown", Language: "go"})
	if fallback.IdentityConfidence != "unknown" || fallback.EvidenceConfidence != "unknown" || strings.Join(fallback.Evidence, ",") != "dependency identity unknown" {
		t.Fatalf("unexpected fallback row: %#v", fallback)
	}
	reachableConfidence := dependencyEvidenceConfidence(report.DependencyReport{ReachabilityConfidence: &report.ReachabilityConfidence{Score: 0.875}})
	if reachableConfidence != "0.88" {
		t.Fatalf("unexpected reachability confidence evidence: %q", reachableConfidence)
	}
	identityEvidence := dependencyIdentityEvidence(report.DependencyReport{Identity: &report.DependencyIdentity{
		Evidence:      []string{"lockfile"},
		VersionStatus: "declared",
		PURLStatus:    "unknown",
	}})
	if strings.Join(identityEvidence, ",") != "lockfile,version status: declared,purl status: unknown" {
		t.Fatalf("unexpected identity status evidence: %#v", identityEvidence)
	}
	dependencies := []report.DependencyReport{{Name: "same", Language: "go"}, {Name: "same", Language: "js-ts"}}
	if got := findPRReviewDependency(dependencies, report.VulnerabilityDelta{Name: "same", Language: "python"}); got.Language != "go" {
		t.Fatalf("expected name-only dependency fallback, got %#v", got)
	}
	if got := findPRReviewDependency(nil, report.VulnerabilityDelta{Name: "missing", Language: "python"}); got.Name != "missing" || got.Language != "python" {
		t.Fatalf("expected synthesized dependency fallback, got %#v", got)
	}
	identityFallback := dependencyIdentityEvidence(report.DependencyReport{Identity: &report.DependencyIdentity{}})
	if strings.Join(identityFallback, ",") != "dependency identity evidence unavailable" {
		t.Fatalf("unexpected identity fallback evidence: %#v", identityFallback)
	}
	duplicateEvidence := compactPRReviewEvidence([]string{" a ", "", "a", "b"})
	if strings.Join(duplicateEvidence, ",") != "a,b" {
		t.Fatalf("unexpected compact evidence: %#v", duplicateEvidence)
	}
}

func TestPRReviewVersionComparisonHelpers(t *testing.T) {
	if got, ok := report.CompareSemanticVersions("release", "v1"); ok || got != 0 {
		t.Fatalf("expected invalid base version not to compare, got %d %t", got, ok)
	}
	if got, ok := report.CompareSemanticVersions("v1.0.0", "release"); ok || got != 0 {
		t.Fatalf("expected invalid head version not to compare, got %d %t", got, ok)
	}
	if got, ok := report.CompareSemanticVersions("v1.0.0", "v1.0.0+build"); !ok || got != 0 {
		t.Fatalf("expected build metadata to compare equal in precedence, got %d %t", got, ok)
	}
	if got, ok := report.CompareSemanticVersions("v2.0.0", "v1.0.0"); !ok || got != 1 {
		t.Fatalf("expected downgrade comparison, got %d %t", got, ok)
	}
	if got, ok := report.CompareSemanticVersions("v1.2.3-rc.1", "v1.2.3"); !ok || got != -1 {
		t.Fatalf("expected prerelease-to-final to compare as upgrade, got %d %t", got, ok)
	}
	if got, ok := report.CompareSemanticVersions("v1..0", "v1.0.1"); ok || got != 0 {
		t.Fatalf("expected invalid non-semver version to remain unordered, got %d %t", got, ok)
	}
	if got, ok := report.CompareSemanticVersions("1.2.0", "v1.2.0"); !ok || got != 0 {
		t.Fatalf("expected optional v prefix to compare equal, got %d %t", got, ok)
	}
}

func TestBuildPRReviewArtifactClassifiesPrereleaseUpgradeAndInvalidVersionChange(t *testing.T) {
	baseReport := report.Report{Dependencies: []report.DependencyReport{
		prReviewTestDependency("pre", "npm", "1.2.3-rc.1", 10, 90, false),
		prReviewTestDependency("invalid", "npm", "release-2026", 10, 90, false),
	}}
	headReport := report.Report{Dependencies: []report.DependencyReport{
		prReviewTestDependency("pre", "npm", "1.2.3", 10, 90, false),
		prReviewTestDependency("invalid", "npm", "release-2027", 10, 90, false),
	}}

	artifact := buildPRReviewArtifact(prReviewArtifactInput{
		baseSHA:    strings.Repeat("a", 40),
		headSHA:    strings.Repeat("b", 40),
		baseReport: baseReport,
		headReport: headReport,
	})

	upgraded := prReviewTestSectionRows(t, artifact, prReviewCategoryUpgraded)
	if len(upgraded) != 1 || upgraded[0].Dependency != "pre" {
		t.Fatalf("expected prerelease-to-final dependency upgrade, got %#v", upgraded)
	}
	versionChanged := prReviewTestSectionRows(t, artifact, prReviewCategoryVersionChanged)
	if len(versionChanged) != 1 || versionChanged[0].Dependency != "invalid" {
		t.Fatalf("expected incomparable versions to remain version-changed, got %#v", versionChanged)
	}
}

func TestPRReviewScalarHelpers(t *testing.T) {
	if got := formatSignedFloat(-1.25); got != "-1.2" {
		t.Fatalf("unexpected negative signed float: %q", got)
	}
	if got := formatSignedFloat(1.25); got != "+1.2" {
		t.Fatalf("unexpected positive signed float: %q", got)
	}
	if got := vulnerabilityPriorityRank(""); got != 1 {
		t.Fatalf("unexpected fallback vulnerability priority rank: %d", got)
	}
	if got := firstNonBlankString(" ", "\t"); got != "" {
		t.Fatalf("expected blank firstNonBlankString fallback, got %q", got)
	}
	if _, ok := report.CompareSemanticVersions("1..0", "1.0.0"); ok {
		t.Fatalf("expected malformed semver not to compare")
	}
	baseLicense := report.DependencyReport{License: &report.DependencyLicense{Raw: "MIT"}}
	headLicense := report.DependencyReport{License: &report.DependencyLicense{Raw: "Apache-2.0"}}
	if change, regression := dependencyPolicyChange(baseLicense, headLicense); change != "license MIT -> Apache-2.0" || regression {
		t.Fatalf("unexpected raw license policy change: %q %t", change, regression)
	}
}

func TestFormatPRReviewMarkdownOverflowAndWarnings(t *testing.T) {
	artifact := prReviewArtifact{
		BaseSHA:       strings.Repeat("a", 40),
		HeadSHA:       strings.Repeat("b", 40),
		AnalysisMode:  "static dependency-surface analysis",
		MergeBaseMode: "disabled",
		FullArtifact:  "Full JSON artifact was written separately.",
		Summary:       prReviewSummary{Added: 2, RegressionCount: 1},
		Warnings:      []string{"watch | pipes"},
		Sections: []prReviewSection{{
			Title: "Added Dependencies",
			Rows: []prReviewRow{
				{Dependency: "pipe|dep", Language: "go", BaseVersion: "", HeadVersion: "1.0.0", WasteDeltaBytes: 12, UsedPercentDelta: 1.2, EvidenceConfidence: "high", AdvisoryID: "GHSA|pipe", Priority: "medium", Evidence: []string{"lock|file"}},
				{Dependency: "hidden", Language: "go", HeadVersion: "1.0.1", EvidenceConfidence: "low"},
			},
		}},
	}

	markdown := formatPRReviewMarkdown(artifact, 1)
	assertContainsAll(t, markdown, []string{
		"| Advisory | Priority |",
		"`pipe\\|dep`",
		"| high | GHSA\\|pipe | medium | lock\\|file |",
		"+12",
		"+1.2",
		"1 additional rows omitted",
		"1 rows were omitted from Markdown output",
		"Full JSON artifact was written separately.",
		"watch \\| pipes",
	})
}

func TestExecutePRReviewValidationErrors(t *testing.T) {
	features := mustResolveAppTestFeatures(t, report.DependencySurfacePRReviewPreviewFeature)
	baseSHA := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)
	req := DefaultRequest()
	req.Mode = ModePRReview
	req.PRReview.Features = features
	req.PRReview.BaseSHA = baseSHA
	req.PRReview.HeadSHA = headSHA
	req.PRReview.Format = "xml"
	if _, err := (&App{Analyzer: &fakeAnalyzer{}}).Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "unknown pr-review format") {
		t.Fatalf("expected format error, got %v", err)
	}
	req.PRReview.Format = prReviewFormatMarkdown
	req.PRReview.BaseSHA = "main"
	if _, err := (&App{Analyzer: &fakeAnalyzer{}}).Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "--base") {
		t.Fatalf("expected base SHA error, got %v", err)
	}
	req.PRReview.BaseSHA = baseSHA
	if _, err := (&App{}).Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "analyzer is not configured") {
		t.Fatalf("expected analyzer error, got %v", err)
	}
	req.PRReview.HeadSHA = strings.Repeat("c", 40)
	if _, err := (&App{Analyzer: &fakeAnalyzer{}}).Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "verify base commit") {
		t.Fatalf("expected analyse revision error, got %v", err)
	}
}

func TestExecutePRReviewRejectsConfigDrivenAdvisoriesWithoutPreviewFeature(t *testing.T) {
	req := DefaultRequest()
	req.Mode = ModePRReview
	req.PRReview.BaseSHA = strings.Repeat("a", 40)
	req.PRReview.HeadSHA = strings.Repeat("b", 40)
	req.PRReview.Features = mustResolveAppTestFeatures(t, report.DependencySurfacePRReviewPreviewFeature)
	req.PRReview.AdvisorySourcePath = "advisories.json"

	_, err := (&App{Analyzer: &fakeAnalyzer{}}).Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), report.ReachabilityVulnerabilityPrioritizationPreviewFeature) {
		t.Fatalf("expected vulnerability preview feature error, got %v", err)
	}
}

func TestExecutePRReviewRejectsConfigDrivenExceptionsWithoutPreviewFeature(t *testing.T) {
	req := DefaultRequest()
	req.Mode = ModePRReview
	req.PRReview.BaseSHA = strings.Repeat("a", 40)
	req.PRReview.HeadSHA = strings.Repeat("b", 40)
	req.PRReview.Features = mustResolveAppTestFeatures(t, report.DependencySurfacePRReviewPreviewFeature)
	req.PRReview.VulnerabilityExceptions = []report.VulnerabilityException{{VulnerabilityID: "GHSA-test", Package: "lib"}}

	_, err := (&App{Analyzer: &fakeAnalyzer{}}).Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), report.VulnerabilityExceptionsVEXPreviewFeature) {
		t.Fatalf("expected exception preview feature error, got %v", err)
	}
}

func TestAnalysePRReviewRevisionsVerifyHeadError(t *testing.T) {
	baseSHA := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)
	stubPRReviewGit(t, func(ctx context.Context, args ...string) (*exec.Cmd, error) {
		if strings.Contains(strings.Join(args, " "), "rev-parse") {
			sha := strings.TrimSuffix(args[len(args)-1], "^{commit}")
			if sha == headSHA {
				return exec.CommandContext(ctx, "/bin/echo", "-n", strings.Repeat("c", 40)), nil
			}
			return exec.CommandContext(ctx, "/bin/echo", "-n", sha), nil
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0"), nil
	})

	_, _, _, err := (&App{Analyzer: &fakeAnalyzer{}}).analysePRReviewRevisions(context.Background(), "/repo", PRReviewRequest{}, baseSHA, headSHA)
	if err == nil || !strings.Contains(err.Error(), "verify head commit") {
		t.Fatalf("expected verify head error, got %v", err)
	}
}

func TestPRReviewGitHelperErrors(t *testing.T) {
	restorePRReviewGitHooks := func() {
		execPRReviewGitCommandFn = gitexec.CommandContext
		resolvePRReviewGitPathFn = gitexec.ResolveBinaryPath
	}
	t.Cleanup(restorePRReviewGitHooks)

	resolvePRReviewGitPathFn = func() (string, error) {
		return "", errors.New("missing git")
	}
	if _, err := runPRReviewGit(context.Background(), "/repo", "status"); err == nil || !strings.Contains(err.Error(), "missing git") {
		t.Fatalf("expected resolve git error, got %v", err)
	}

	resolvePRReviewGitPathFn = func() (string, error) {
		return gitexec.ExecutablePrimary, nil
	}
	execPRReviewGitCommandFn = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return nil, errors.New("construct failed")
	}
	if _, err := runPRReviewGit(context.Background(), "/repo", "status"); err == nil || !strings.Contains(err.Error(), "construct git command") {
		t.Fatalf("expected construct git command error, got %v", err)
	}

	execPRReviewGitCommandFn = func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf boom >&2; exit 7"), nil
	}
	if _, err := runPRReviewGit(context.Background(), "/repo", "status"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected git command output error, got %v", err)
	}

	execPRReviewGitCommandFn = func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf '"+strings.Repeat("b", 40)+"'"), nil
	}
	if _, err := verifyPRReviewCommit(context.Background(), "/repo", strings.Repeat("a", 40)); err == nil || !strings.Contains(err.Error(), "resolved") {
		t.Fatalf("expected verify mismatch error, got %v", err)
	}
}

func TestResolvePRReviewRepositoryScopeErrors(t *testing.T) {
	t.Run("no repository ancestor", func(t *testing.T) {
		_, _, err := resolvePRReviewRepositoryScope(context.Background(), t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "resolve pr-review repository scope") {
			t.Fatalf("expected repository scope error, got %v", err)
		}
	})

	t.Run("prefix resolution fails", func(t *testing.T) {
		originalResolve := resolvePRReviewGitPathFn
		originalExec := execPRReviewGitCommandFn
		resolvePRReviewGitPathFn = func() (string, error) {
			return gitexec.ExecutablePrimary, nil
		}
		execPRReviewGitCommandFn = func(ctx context.Context, _ string, args ...string) (*exec.Cmd, error) {
			if args[len(args)-1] == "--show-toplevel" {
				return exec.CommandContext(ctx, "/bin/echo", "-n", args[1]), nil
			}
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf prefix-failed >&2; exit 1"), nil
		}
		t.Cleanup(func() {
			resolvePRReviewGitPathFn = originalResolve
			execPRReviewGitCommandFn = originalExec
		})

		_, _, err := resolvePRReviewRepositoryScope(context.Background(), "/repo")
		if err == nil || !strings.Contains(err.Error(), "prefix-failed") {
			t.Fatalf("expected prefix resolution error, got %v", err)
		}
	})
}

func TestAnalysePRReviewRevisionsCreateWorkspaceError(t *testing.T) {
	repoPath, baseSHA, headSHA := createPRReviewGitRepo(t)
	originalCreate := createPRReviewTempRootFn
	createPRReviewTempRootFn = func(string, string) (string, error) {
		return "", errors.New("no temp")
	}
	t.Cleanup(func() {
		createPRReviewTempRootFn = originalCreate
	})

	_, _, _, err := (&App{Analyzer: &fakeAnalyzer{}}).analysePRReviewRevisions(context.Background(), repoPath, PRReviewRequest{}, baseSHA, headSHA)
	if err == nil || !strings.Contains(err.Error(), "create pr-review workspace") {
		t.Fatalf("expected workspace creation error, got %v", err)
	}
}

func TestAnalysePRReviewRevisionsWorktreeErrors(t *testing.T) {
	runPRReviewWorktreeErrorCases(t, []prReviewWorktreeErrorCase{
		{name: "add/base", command: "worktree add", counterKey: "base", failureNumber: 1, stderr: "worktree-failed", want: "create base worktree"},
		{name: "add/head", command: "worktree add", counterKey: "head", failureNumber: 2, stderr: "worktree-failed", want: "create head worktree"},
		{name: "remove/head", command: "worktree remove", counterKey: "remove-head", failureNumber: 1, stderr: "remove-failed", want: "remove head worktree"},
		{name: "remove/base", command: "worktree remove", counterKey: "remove-base", failureNumber: 2, stderr: "remove-failed", want: "remove base worktree"},
	})
}

type prReviewWorktreeErrorCase struct {
	name          string
	command       string
	counterKey    string
	failureNumber int
	stderr        string
	want          string
}

func runPRReviewWorktreeErrorCases(t *testing.T, cases []prReviewWorktreeErrorCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertPRReviewWorktreeCommandError(t, tc)
		})
	}
}

func assertPRReviewWorktreeCommandError(t *testing.T, tc prReviewWorktreeErrorCase) {
	t.Helper()

	stubPRReviewGit(t, func(ctx context.Context, args ...string) (*exec.Cmd, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "rev-parse") {
			return prReviewEchoSHA(ctx, args), nil
		}
		if strings.Contains(joined, tc.command) && incrementPRReviewTestCounter(t, tc.counterKey) == tc.failureNumber {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "printf "+tc.stderr+" >&2; exit 1"), nil
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0"), nil
	})
	assertPRReviewRevisionError(t, tc.want)
}

func prReviewEchoSHA(ctx context.Context, args []string) *exec.Cmd {
	sha := strings.TrimSuffix(args[len(args)-1], "^{commit}")
	return exec.CommandContext(ctx, "/bin/echo", "-n", sha)
}

func assertPRReviewRevisionError(t *testing.T, want string) {
	t.Helper()

	baseSHA := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)
	originalChangedFiles := resolvePRReviewChangedFilesFn
	resolvePRReviewChangedFilesFn = func(string, string, string) ([]string, error) {
		return []string{"src/app.txt"}, nil
	}
	t.Cleanup(func() {
		resolvePRReviewChangedFilesFn = originalChangedFiles
	})
	_, _, _, err := (&App{Analyzer: &fakeAnalyzer{}}).analysePRReviewRevisions(context.Background(), "/repo", PRReviewRequest{}, baseSHA, headSHA)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected %s error, got %v", want, err)
	}
}

func TestExecutePRReviewAnalysesExplicitSHAWorktrees(t *testing.T) {
	repoPath, baseSHA, headSHA := createPRReviewGitRepo(t)
	outputPath := filepath.Join(t.TempDir(), "review.json")
	features := mustResolveAppTestFeatures(t, report.DependencySurfacePRReviewPreviewFeature)
	analyzer := &pathAwarePRReviewAnalyzer{}
	application := &App{Analyzer: analyzer}
	req := newExplicitSHAReviewRequest(repoPath, baseSHA, headSHA, outputPath, features)

	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute pr-review: %v", err)
	}
	assertContainsAll(t, output, []string{"pr review report written", outputPath})
	fileContent := string(mustReadAppTestFile(t, outputPath))
	assertContainsAll(t, fileContent, []string{prReviewSchemaVersion, baseSHA, headSHA, "upgraded"})
	assertPRReviewAnalysisCalls(t, analyzer.requests)
}

func TestAnalysePRReviewRevisionsUsesExplicitChangedPackageRangeForBothSides(t *testing.T) {
	repoPath := t.TempDir()
	testutil.RunGit(t, repoPath, "init")
	testutil.RunGit(t, repoPath, "config", "user.email", "pr-review-test@example.com")
	testutil.RunGit(t, repoPath, "config", "user.name", "PR Review Test")

	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages", "a", "file.txt"), "base\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages", "b", "file.txt"), "base\n")
	testutil.RunGit(t, repoPath, "add", ".")
	testutil.RunGit(t, repoPath, "commit", "-m", "base")

	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages", "a", "file.txt"), "base change\n")
	testutil.RunGit(t, repoPath, "add", ".")
	testutil.RunGit(t, repoPath, "commit", "-m", "change a")
	baseSHA := prReviewGitOutput(t, repoPath, "rev-parse", "HEAD")

	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages", "b", "file.txt"), "head change\n")
	testutil.RunGit(t, repoPath, "add", ".")
	testutil.RunGit(t, repoPath, "commit", "-m", "change b")
	headSHA := prReviewGitOutput(t, repoPath, "rev-parse", "HEAD")

	analyzer := &pathAwarePRReviewAnalyzer{}
	req := PRReviewRequest{ScopeMode: ScopeModeChangedPackages}
	_, _, _, err := (&App{Analyzer: analyzer}).analysePRReviewRevisions(context.Background(), repoPath, req, baseSHA, headSHA)
	if err != nil {
		t.Fatalf("analyse pr-review revisions with explicit changed range: %v", err)
	}
	if len(analyzer.requests) != 2 {
		t.Fatalf("expected base and head analysis requests, got %#v", analyzer.requests)
	}
	for _, request := range analyzer.requests {
		if !request.ChangedFilesExplicit || strings.Join(request.ChangedFiles, ",") != "packages/b/file.txt" {
			t.Fatalf("expected explicit base..head changed files on both analyses, got %#v", request)
		}
	}
}

func TestAnalysePRReviewRevisionsPreservesSubdirectoryScope(t *testing.T) {
	repoRoot := t.TempDir()
	testutil.RunGit(t, repoRoot, "init")
	testutil.RunGit(t, repoRoot, "config", "user.email", "pr-review-test@example.com")
	testutil.RunGit(t, repoRoot, "config", "user.name", "PR Review Test")

	apiPath := filepath.Join(repoRoot, "services", "api")
	testutil.MustWriteFile(t, filepath.Join(apiPath, "app.txt"), "base\n")
	testutil.MustWriteFile(t, filepath.Join(repoRoot, "services", "web", "app.txt"), "base\n")
	testutil.RunGit(t, repoRoot, "add", ".")
	testutil.RunGit(t, repoRoot, "commit", "-m", "base")
	baseSHA := prReviewGitOutput(t, repoRoot, "rev-parse", "HEAD")

	testutil.MustWriteFile(t, filepath.Join(apiPath, "app.txt"), "head\n")
	testutil.MustWriteFile(t, filepath.Join(repoRoot, "services", "web", "app.txt"), "unrelated head\n")
	testutil.RunGit(t, repoRoot, "add", ".")
	testutil.RunGit(t, repoRoot, "commit", "-m", "head")
	headSHA := prReviewGitOutput(t, repoRoot, "rev-parse", "HEAD")

	analyzer := &pathAwarePRReviewAnalyzer{}
	_, _, _, err := (&App{Analyzer: analyzer}).analysePRReviewRevisions(context.Background(), apiPath, PRReviewRequest{ScopeMode: ScopeModeChangedPackages}, baseSHA, headSHA)
	if err != nil {
		t.Fatalf("analyse subdirectory-scoped pr-review revisions: %v", err)
	}
	if len(analyzer.requests) != 2 {
		t.Fatalf("expected base and head analysis requests, got %#v", analyzer.requests)
	}
	wantSuffixes := []string{filepath.Join("base", "services", "api"), filepath.Join("head", "services", "api")}
	for index, request := range analyzer.requests {
		if !strings.HasSuffix(request.RepoPath, wantSuffixes[index]) {
			t.Fatalf("expected analysis path scoped to %q, got %q", wantSuffixes[index], request.RepoPath)
		}
		if !request.ChangedFilesExplicit || strings.Join(request.ChangedFiles, ",") != "app.txt" {
			t.Fatalf("expected only package-relative changed files, got %#v", request)
		}
	}
}

func TestAnalysePRReviewRevisionsTreatsMissingSubdirectoryScopeAsEmpty(t *testing.T) {
	tests := []struct {
		name         string
		baseHasScope bool
		headHasScope bool
	}{
		{name: "scope added", headHasScope: true},
		{name: "scope removed", baseHasScope: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runMissingSubdirectoryScopeTest(t, tc.baseHasScope, tc.headHasScope)
		})
	}
}

func runMissingSubdirectoryScopeTest(t *testing.T, baseHasScope, headHasScope bool) {
	t.Helper()
	repoRoot := t.TempDir()
	testutil.RunGit(t, repoRoot, "init")
	testutil.RunGit(t, repoRoot, "config", "user.email", "pr-review-test@example.com")
	testutil.RunGit(t, repoRoot, "config", "user.name", "PR Review Test")

	apiPath := filepath.Join(repoRoot, "services", "api")
	setPRReviewScopeState(t, apiPath, "base\n", baseHasScope)
	testutil.MustWriteFile(t, filepath.Join(repoRoot, "README.md"), "base\n")
	testutil.RunGit(t, repoRoot, "add", ".")
	testutil.RunGit(t, repoRoot, "commit", "-m", "base")
	baseSHA := prReviewGitOutput(t, repoRoot, "rev-parse", "HEAD")

	setPRReviewScopeState(t, apiPath, "head\n", headHasScope)
	testutil.MustWriteFile(t, filepath.Join(repoRoot, "README.md"), "head\n")
	testutil.RunGit(t, repoRoot, "add", "-A")
	testutil.RunGit(t, repoRoot, "commit", "-m", "head")
	headSHA := prReviewGitOutput(t, repoRoot, "rev-parse", "HEAD")

	analyzer := &existingPathPRReviewAnalyzer{}
	baseReport, headReport, _, err := (&App{Analyzer: analyzer}).analysePRReviewRevisions(context.Background(), apiPath, PRReviewRequest{}, baseSHA, headSHA)
	if err != nil {
		t.Fatalf("analyse revisions with missing scope: %v", err)
	}
	assertPRReviewDependencyPresence(t, baseReport.Dependencies, baseHasScope)
	assertPRReviewDependencyPresence(t, headReport.Dependencies, headHasScope)
	if baseReport.RepoPath != apiPath || headReport.RepoPath != apiPath {
		t.Fatalf("expected caller repo path %q, got base=%q head=%q", apiPath, baseReport.RepoPath, headReport.RepoPath)
	}
	if len(analyzer.requests) != 1 {
		t.Fatalf("expected only the existing scope to be analysed, got %#v", analyzer.requests)
	}
}

func setPRReviewScopeState(t *testing.T, apiPath, contents string, present bool) {
	t.Helper()
	if present {
		testutil.MustWriteFile(t, filepath.Join(apiPath, "app.txt"), contents)
		return
	}
	if err := os.RemoveAll(apiPath); err != nil {
		t.Fatalf("remove scope: %v", err)
	}
}

func assertPRReviewDependencyPresence(t *testing.T, dependencies []report.DependencyReport, want bool) {
	t.Helper()
	wantCount := 0
	if want {
		wantCount = 1
	}
	if len(dependencies) != wantCount {
		t.Fatalf("expected dependency presence %t, got %#v", want, dependencies)
	}
}

func TestAnalysePRReviewScopeHandlesNonDirectoryAndInspectionErrors(t *testing.T) {
	t.Run("non-directory scope is empty", func(t *testing.T) {
		scopePath := filepath.Join(t.TempDir(), "api")
		testutil.MustWriteFile(t, scopePath, "not a directory\n")
		analyzer := &existingPathPRReviewAnalyzer{}

		reportData, err := (&App{Analyzer: analyzer}).analysePRReviewScope(context.Background(), scopePath, "/caller/api", PRReviewRequest{})
		if err != nil {
			t.Fatalf("analyse non-directory scope: %v", err)
		}
		if reportData.RepoPath != "/caller/api" || len(reportData.Dependencies) != 0 {
			t.Fatalf("expected empty caller-scoped report, got %#v", reportData)
		}
		if len(analyzer.requests) != 0 {
			t.Fatalf("expected non-directory scope not to be analysed, got %#v", analyzer.requests)
		}
	})

	t.Run("inspection error is preserved", func(t *testing.T) {
		_, err := (&App{Analyzer: &existingPathPRReviewAnalyzer{}}).analysePRReviewScope(context.Background(), "\x00", "/caller/api", PRReviewRequest{})
		if err == nil || !strings.Contains(err.Error(), "inspect pr-review repository scope") {
			t.Fatalf("expected scoped path inspection error, got %v", err)
		}
	})
}

func TestExecutePRReviewAllowsPreviewGatedPolicyPathsAndAppliesPolicies(t *testing.T) {
	repoPath, baseSHA, headSHA := createPRReviewGitRepo(t)
	advisoryPath := filepath.Join(t.TempDir(), "advisories.json")
	testutil.MustWriteFile(t, advisoryPath, `{"advisories":[{"id":"GHSA-lib","package":"lib","ecosystem":"npm","severity":"high","source":"fixture"}]}`)
	requiredFeatures := []string{
		report.DependencySurfacePRReviewPreviewFeature,
		report.ReachabilityVulnerabilityPrioritizationPreviewFeature,
		report.VulnerabilityExceptionsVEXPreviewFeature,
	}
	features := mustResolveAppTestFeatures(t, requiredFeatures...)
	analyzer := &pathAwarePRReviewAnalyzer{
		baseReport: report.Report{Dependencies: []report.DependencyReport{prReviewTestDependency("lib", "npm", "1.0.0", 100, 90, false)}},
		headReport: report.Report{Dependencies: []report.DependencyReport{prReviewTestDependency("lib", "npm", "1.1.0", 100, 90, false)}},
	}
	req := DefaultRequest()
	req.Mode = ModePRReview
	req.RepoPath = repoPath
	req.PRReview.BaseSHA = baseSHA
	req.PRReview.HeadSHA = headSHA
	req.PRReview.Format = prReviewFormatJSON
	req.PRReview.Features = features
	req.PRReview.AdvisorySourcePath = advisoryPath
	req.PRReview.VulnerabilityExceptions = []report.VulnerabilityException{{
		VulnerabilityID: "GHSA-lib",
		Package:         "lib",
		Status:          "not_affected",
	}}

	output, err := (&App{Analyzer: analyzer}).Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute pr-review with preview-gated policy paths: %v", err)
	}
	assertContainsAll(t, output, []string{prReviewSchemaVersion})
	if strings.Contains(output, "GHSA-lib") {
		t.Fatalf("expected vulnerability exception to suppress advisory from artifact, got %s", output)
	}
}

func TestExecutePRReviewUsesCallerRepositoryForRepositoryScopedVulnerabilityExceptions(t *testing.T) {
	repoPath, baseSHA, headSHA := createPRReviewGitRepo(t)
	advisoryPath := filepath.Join(t.TempDir(), "advisories.json")
	testutil.MustWriteFile(t, advisoryPath, `{"advisories":[{"id":"GHSA-lib","package":"lib","ecosystem":"npm","severity":"high","source":"fixture"}]}`)
	features := mustResolveAppTestFeatures(t, report.DependencySurfacePRReviewPreviewFeature, report.ReachabilityVulnerabilityPrioritizationPreviewFeature, report.VulnerabilityExceptionsVEXPreviewFeature)
	analyzer := &pathAwarePRReviewAnalyzer{
		baseReport: report.Report{Dependencies: []report.DependencyReport{prReviewTestDependency("lib", "npm", "1.0.0", 100, 90, false)}},
		headReport: report.Report{Dependencies: []report.DependencyReport{{
			Name:     "lib",
			Language: "js-ts",
			Identity: &report.DependencyIdentity{
				Ecosystem: "npm",
				Name:      "lib",
				Version:   "1.1.0",
				PURL:      "pkg:npm/lib@1.1.0",
			},
			UsedImports: []report.ImportUse{{
				Name:      "lib",
				Module:    "lib",
				Locations: []report.Location{{File: "src/app.txt", Line: 1}},
			}},
		}}},
	}
	req := newExplicitSHAReviewRequest(repoPath, baseSHA, headSHA, "", features)
	req.PRReview.AdvisorySourcePath = advisoryPath
	req.PRReview.VulnerabilityExceptions = []report.VulnerabilityException{{
		VulnerabilityID: "GHSA-lib",
		Repository:      repoPath,
		Path:            "src",
		PURL:            "pkg:npm/lib@1.1.0",
		Status:          "not_affected",
	}}

	output, err := (&App{Analyzer: analyzer}).Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute pr-review with repository-scoped vulnerability exception: %v", err)
	}
	if strings.Contains(output, "GHSA-lib") {
		t.Fatalf("expected repository-scoped vulnerability exception to suppress advisory from artifact, got %s", output)
	}
	assertPRReviewAnalysisCalls(t, analyzer.requests)
	for _, call := range analyzer.requests {
		if base := filepath.Base(call.RepoPath); base != "base" && base != "head" {
			t.Fatalf("expected detached worktree analysis paths, got %#v", analyzer.requests)
		}
	}
}

func TestAnalysePRReviewWorktreeAnnotatesAdvisorySource(t *testing.T) {
	repoPath := t.TempDir()
	advisoryPath := filepath.Join(t.TempDir(), "advisories.json")
	testutil.MustWriteFile(t, advisoryPath, `{"advisories":[{"id":"GHSA-lib","package":"lib","ecosystem":"npm","severity":"high","source":"fixture"}]}`)
	analyzer := &fakeAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{
		Name:     "lib",
		Language: "js-ts",
		Identity: &report.DependencyIdentity{
			Ecosystem: "npm",
			Name:      "lib",
		},
	}}}}
	application := &App{Analyzer: analyzer}

	reportData, err := application.analysePRReviewWorktree(context.Background(), repoPath, repoPath, PRReviewRequest{AdvisorySourcePath: advisoryPath})
	if err != nil {
		t.Fatalf("analyse pr-review worktree: %v", err)
	}
	if len(reportData.Dependencies) != 1 || len(reportData.Dependencies[0].Vulnerabilities) != 1 {
		t.Fatalf("expected advisory finding, got %#v", reportData.Dependencies)
	}
	if reportData.Summary == nil || reportData.Summary.Vulnerabilities == nil || reportData.Summary.Vulnerabilities.TotalFindings != 1 {
		t.Fatalf("expected summary to include advisory finding, got %#v", reportData.Summary)
	}
}

func TestBuildPRReviewArtifactClassifiesCargoAliasVersionChangeAsUpgrade(t *testing.T) {
	baseReport := report.Report{Dependencies: []report.DependencyReport{{
		Name:     "demo",
		Language: "rust",
		Identity: &report.DependencyIdentity{Ecosystem: "crates.io", Name: "demo", Version: "1.0.0", PURL: "pkg:crates.io/demo@1.0.0"},
	}}}
	headReport := report.Report{Dependencies: []report.DependencyReport{{
		Name:     "demo",
		Language: "rust",
		Identity: &report.DependencyIdentity{Ecosystem: "cargo", Name: "demo", Version: "1.1.0", PURL: "pkg:cargo/demo@1.1.0"},
	}}}

	artifact := buildPRReviewArtifact(prReviewArtifactInput{
		baseSHA:    strings.Repeat("a", 40),
		headSHA:    strings.Repeat("b", 40),
		baseReport: baseReport,
		headReport: headReport,
	})

	if rows := prReviewTestSectionRows(t, artifact, prReviewCategoryAdded); len(rows) != 0 {
		t.Fatalf("expected cargo/crates.io alias version change to avoid added churn, got %#v", rows)
	}
	if rows := prReviewTestSectionRows(t, artifact, prReviewCategoryRemoved); len(rows) != 0 {
		t.Fatalf("expected cargo/crates.io alias version change to avoid removed churn, got %#v", rows)
	}
	upgraded := prReviewTestSectionRows(t, artifact, prReviewCategoryUpgraded)
	if len(upgraded) != 1 || upgraded[0].Dependency != "demo" || upgraded[0].BaseVersion != "1.0.0" || upgraded[0].HeadVersion != "1.1.0" {
		t.Fatalf("expected cargo/crates.io alias version change to classify as upgrade, got %#v", upgraded)
	}
}

func TestAnalysePRReviewWorktreeErrorBranches(t *testing.T) {
	if _, err := (&App{Analyzer: &fakeAnalyzer{err: errors.New("analysis failed")}}).analysePRReviewWorktree(context.Background(), t.TempDir(), t.TempDir(), PRReviewRequest{}); err == nil || !strings.Contains(err.Error(), "analysis failed") {
		t.Fatalf("expected analyzer error, got %v", err)
	}
	badAdvisoryPath := filepath.Join(t.TempDir(), "bad-advisories.json")
	testutil.MustWriteFile(t, badAdvisoryPath, "{")
	_, err := (&App{Analyzer: &fakeAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{Name: "lib"}}}}}).analysePRReviewWorktree(context.Background(), t.TempDir(), t.TempDir(), PRReviewRequest{AdvisorySourcePath: badAdvisoryPath})
	if err == nil || !strings.Contains(err.Error(), "parse advisory source") {
		t.Fatalf("expected advisory load error, got %v", err)
	}
}

func TestAnalysePRReviewRevisionsAnalysisErrors(t *testing.T) {
	repoPath, baseSHA, headSHA := createPRReviewGitRepo(t)
	_, _, _, err := (&App{Analyzer: &fakeAnalyzer{err: errors.New("base failed")}}).analysePRReviewRevisions(context.Background(), repoPath, PRReviewRequest{}, baseSHA, headSHA)
	if err == nil || !strings.Contains(err.Error(), "analyse base commit") {
		t.Fatalf("expected base analysis error, got %v", err)
	}
	_, _, _, err = (&App{Analyzer: &sequencePRReviewAnalyzer{reports: []report.Report{{Dependencies: []report.DependencyReport{{Name: "base"}}}}, err: errors.New("head failed")}}).analysePRReviewRevisions(context.Background(), repoPath, PRReviewRequest{}, baseSHA, headSHA)
	if err == nil || !strings.Contains(err.Error(), "analyse head commit") {
		t.Fatalf("expected head analysis error, got %v", err)
	}
}

func TestExecutePRReviewOutputError(t *testing.T) {
	repoPath, baseSHA, headSHA := createPRReviewGitRepo(t)
	outputParent := filepath.Join(t.TempDir(), "file")
	testutil.MustWriteFile(t, outputParent, "x")
	req := DefaultRequest()
	req.Mode = ModePRReview
	req.RepoPath = repoPath
	req.PRReview.BaseSHA = baseSHA
	req.PRReview.HeadSHA = headSHA
	req.PRReview.OutputPath = filepath.Join(outputParent, "review.md")
	req.PRReview.Features = mustResolveAppTestFeatures(t, report.DependencySurfacePRReviewPreviewFeature)
	_, err := (&App{Analyzer: &pathAwarePRReviewAnalyzer{}}).Execute(context.Background(), req)
	if err == nil {
		t.Fatalf("expected output path error")
	}
}

func TestExecutePRReviewReturnsRegressionError(t *testing.T) {
	repoPath, baseSHA, headSHA := createPRReviewGitRepo(t)
	features := mustResolveAppTestFeatures(t, report.DependencySurfacePRReviewPreviewFeature)
	analyzer := &pathAwarePRReviewAnalyzer{
		baseReport: report.Report{Dependencies: []report.DependencyReport{prReviewTestDependency("lib", "npm", "2.0.0", 100, 90, false)}},
		headReport: report.Report{Dependencies: []report.DependencyReport{prReviewTestDependency("lib", "npm", "1.0.0", 100, 90, false)}},
	}
	req := DefaultRequest()
	req.Mode = ModePRReview
	req.RepoPath = repoPath
	req.PRReview.BaseSHA = baseSHA
	req.PRReview.HeadSHA = headSHA
	req.PRReview.Features = features
	req.PRReview.FailOnRegression = true

	output, err := (&App{Analyzer: analyzer}).Execute(context.Background(), req)
	if !errors.Is(err, ErrPRReviewRegressions) {
		t.Fatalf("expected regression error, got output=%q err=%v", output, err)
	}
	if !strings.Contains(output, "Downgraded Dependencies") {
		t.Fatalf("expected markdown output with downgrade, got %q", output)
	}
}

func TestExecutePRReviewReturnsRegressionErrorForAddedMaterialWaste(t *testing.T) {
	repoPath, baseSHA, headSHA := createPRReviewGitRepo(t)
	features := mustResolveAppTestFeatures(t, report.DependencySurfacePRReviewPreviewFeature)
	analyzer := &pathAwarePRReviewAnalyzer{
		baseReport: report.Report{},
		headReport: report.Report{Dependencies: []report.DependencyReport{
			prReviewTestDependency("wasteful", "npm", "1.0.0", 2048, 0, false),
		}},
	}
	req := DefaultRequest()
	req.Mode = ModePRReview
	req.RepoPath = repoPath
	req.PRReview.BaseSHA = baseSHA
	req.PRReview.HeadSHA = headSHA
	req.PRReview.Features = features
	req.PRReview.FailOnRegression = true
	req.PRReview.MaterialWasteBytes = 1024

	output, err := (&App{Analyzer: analyzer}).Execute(context.Background(), req)
	if !errors.Is(err, ErrPRReviewRegressions) {
		t.Fatalf("expected regression error, got output=%q err=%v", output, err)
	}
	if !strings.Contains(output, "Materially Worsened Dependencies") || !strings.Contains(output, "wasteful") {
		t.Fatalf("expected markdown output with added material waste, got %q", output)
	}
}

func TestBuildPRReviewArtifactFlagsAddedDeniedDependencyAsRegression(t *testing.T) {
	artifact := buildPRReviewArtifact(prReviewArtifactInput{
		baseSHA:    strings.Repeat("a", 40),
		headSHA:    strings.Repeat("b", 40),
		baseReport: report.Report{},
		headReport: report.Report{Dependencies: []report.DependencyReport{
			prReviewTestDependency("added-denied", "npm", "1.0.0", 10, 90, true),
		}},
		req: PRReviewRequest{MaterialWasteBytes: 1024, MaxRows: 20},
	})
	if artifact.Summary.Added != 1 || artifact.Summary.RegressionCount != 1 {
		t.Fatalf("expected denied added dependency to count as a regression, got %#v", artifact.Summary)
	}
}

func TestBuildPRReviewArtifactSkipsZeroWasteDeltaAtZeroThreshold(t *testing.T) {
	base := prReviewTestDependency("same", "npm", "1.0.0", 100, 90, false)
	head := prReviewTestDependency("same", "npm", "1.0.0", 100, 90, false)
	artifact := buildPRReviewArtifact(prReviewArtifactInput{
		baseSHA:    strings.Repeat("a", 40),
		headSHA:    strings.Repeat("b", 40),
		baseReport: report.Report{Dependencies: []report.DependencyReport{base}},
		headReport: report.Report{Dependencies: []report.DependencyReport{head}},
		req:        PRReviewRequest{MaterialWasteBytes: 0, MaxRows: 20},
	})
	if artifact.Summary.MateriallyWorsened != 0 {
		t.Fatalf("expected zero waste delta not to count as material regression, got %#v", artifact.Summary)
	}
}

func TestBuildPRReviewArtifactPreservesDuplicateVersionlessInstancesDeterministically(t *testing.T) {
	baseStable := prReviewTestDependency("dup", "npm", "1.0.0", 100, 90, false)
	baseChanged := prReviewTestDependency("dup", "npm", "2.0.0", 110, 89, false)
	headStable := prReviewTestDependency("dup", "npm", "1.0.0", 100, 90, false)
	headChanged := prReviewTestDependency("dup", "npm", "2.1.0", 110, 89, true)
	headAdded := prReviewTestDependency("dup", "npm", "3.0.0", 120, 88, false)

	build := func(baseDeps, headDeps []report.DependencyReport) prReviewArtifact {
		return buildPRReviewArtifact(prReviewArtifactInput{
			baseSHA:    strings.Repeat("a", 40),
			headSHA:    strings.Repeat("b", 40),
			baseReport: report.Report{Dependencies: baseDeps},
			headReport: report.Report{Dependencies: headDeps},
			req:        PRReviewRequest{MaterialWasteBytes: 0, MaxRows: 20},
		})
	}

	first := build([]report.DependencyReport{baseChanged, baseStable}, []report.DependencyReport{headAdded, headStable, headChanged})
	second := build([]report.DependencyReport{baseStable, baseChanged}, []report.DependencyReport{headChanged, headAdded, headStable})

	if first.Summary.Added != 1 || first.Summary.Upgraded != 1 || first.Summary.PolicyChanged != 1 {
		t.Fatalf("expected duplicate versionless instances to retain one add, one upgrade, and one policy change, got %#v", first.Summary)
	}
	if first.Summary != second.Summary {
		t.Fatalf("expected duplicate pairing summary to be order-independent, got %#v vs %#v", first.Summary, second.Summary)
	}

	firstAdded := prReviewTestSectionRows(t, first, prReviewCategoryAdded)
	secondAdded := prReviewTestSectionRows(t, second, prReviewCategoryAdded)
	if len(firstAdded) != 1 || firstAdded[0].HeadVersion != "3.0.0" || len(secondAdded) != 1 || secondAdded[0].HeadVersion != "3.0.0" {
		t.Fatalf("expected added duplicate instance to survive pairing, got %#v and %#v", firstAdded, secondAdded)
	}

	firstUpgraded := prReviewTestSectionRows(t, first, prReviewCategoryUpgraded)
	secondUpgraded := prReviewTestSectionRows(t, second, prReviewCategoryUpgraded)
	if len(firstUpgraded) != 1 || firstUpgraded[0].BaseVersion != "2.0.0" || firstUpgraded[0].HeadVersion != "2.1.0" {
		t.Fatalf("unexpected upgraded duplicate pairing rows: %#v", firstUpgraded)
	}
	if len(secondUpgraded) != 1 || secondUpgraded[0].BaseVersion != "2.0.0" || secondUpgraded[0].HeadVersion != "2.1.0" {
		t.Fatalf("unexpected reordered upgraded duplicate pairing rows: %#v", secondUpgraded)
	}

	firstPolicy := prReviewTestSectionRows(t, first, prReviewCategoryPolicyChanged)
	secondPolicy := prReviewTestSectionRows(t, second, prReviewCategoryPolicyChanged)
	if len(firstPolicy) != 1 || firstPolicy[0].PolicyChange != "license denied false -> true" || !firstPolicy[0].Regression {
		t.Fatalf("unexpected policy row for duplicate pairing: %#v", firstPolicy)
	}
	if len(secondPolicy) != 1 || secondPolicy[0].PolicyChange != firstPolicy[0].PolicyChange || secondPolicy[0].BaseVersion != firstPolicy[0].BaseVersion || secondPolicy[0].HeadVersion != firstPolicy[0].HeadVersion {
		t.Fatalf("expected policy row to be order-independent, got %#v vs %#v", firstPolicy, secondPolicy)
	}
}

func TestBuildPRReviewArtifactClassifiesDuplicateVersionChangeAgainstUnmatchedRow(t *testing.T) {
	baseStable := prReviewTestDependency("dup", "npm", "1.0.0", 100, 90, false)
	baseChanged := prReviewTestDependency("dup", "npm", "2.0.0", 110, 89, false)
	headStable := prReviewTestDependency("dup", "npm", "1.0.0", 100, 90, false)
	headChanged := prReviewTestDependency("dup", "npm", "1.1.0", 110, 89, false)

	artifact := buildPRReviewArtifact(prReviewArtifactInput{
		baseSHA:    strings.Repeat("a", 40),
		headSHA:    strings.Repeat("b", 40),
		baseReport: report.Report{Dependencies: []report.DependencyReport{baseStable, baseChanged}},
		headReport: report.Report{Dependencies: []report.DependencyReport{headStable, headChanged}},
		req:        PRReviewRequest{MaxRows: 20},
	})

	if artifact.Summary.Added != 0 || artifact.Summary.Removed != 0 || artifact.Summary.Downgraded != 1 || artifact.Summary.RegressionCount != 1 {
		t.Fatalf("expected unmatched duplicate version change to be one downgrade, got %#v", artifact.Summary)
	}
	downgraded := prReviewTestSectionRows(t, artifact, prReviewCategoryDowngraded)
	if len(downgraded) != 1 || downgraded[0].BaseVersion != "2.0.0" || downgraded[0].HeadVersion != "1.1.0" {
		t.Fatalf("unexpected duplicate downgrade row: %#v", downgraded)
	}
}

func TestAnalysePRReviewRevisionsCleanupError(t *testing.T) {
	repoPath, baseSHA, headSHA := createPRReviewGitRepo(t)
	originalRemove := removePRReviewTempRootFn
	removePRReviewTempRootFn = func(string) error {
		return errors.New("cleanup failed")
	}
	t.Cleanup(func() {
		removePRReviewTempRootFn = originalRemove
	})

	_, _, _, err := (&App{Analyzer: &pathAwarePRReviewAnalyzer{}}).analysePRReviewRevisions(context.Background(), repoPath, PRReviewRequest{}, baseSHA, headSHA)
	if err == nil || !strings.Contains(err.Error(), "remove pr-review workspace") {
		t.Fatalf("expected cleanup error, got %v", err)
	}
}

func TestRecordPRReviewCleanupError(t *testing.T) {
	t.Run("joins primary and cleanup errors", func(t *testing.T) {
		primaryErr := errors.New("primary failed")
		cleanupErr := errors.New("cleanup failed")
		resultErr := primaryErr

		recordPRReviewCleanupError(&resultErr, cleanupErr, "remove pr-review workspace")

		if !errors.Is(resultErr, primaryErr) {
			t.Fatalf("expected primary error in chain, got %v", resultErr)
		}
		if !errors.Is(resultErr, cleanupErr) {
			t.Fatalf("expected cleanup error in chain, got %v", resultErr)
		}
		if !strings.Contains(resultErr.Error(), "remove pr-review workspace") {
			t.Fatalf("expected cleanup operation context in result, got %v", resultErr)
		}
	})

	t.Run("records cleanup error when primary is nil", func(t *testing.T) {
		cleanupErr := errors.New("cleanup failed")
		var resultErr error

		recordPRReviewCleanupError(&resultErr, cleanupErr, "remove pr-review workspace")

		if !errors.Is(resultErr, cleanupErr) {
			t.Fatalf("expected cleanup error in chain, got %v", resultErr)
		}
		if !strings.Contains(resultErr.Error(), "remove pr-review workspace") {
			t.Fatalf("expected cleanup operation context in result, got %v", resultErr)
		}
	})

	t.Run("leaves primary error unchanged without cleanup error", func(t *testing.T) {
		primaryErr := errors.New("primary failed")
		resultErr := primaryErr

		recordPRReviewCleanupError(&resultErr, nil, "remove pr-review workspace")

		if !errors.Is(resultErr, primaryErr) {
			t.Fatalf("expected primary error to remain in chain, got %v", resultErr)
		}
		if strings.Contains(resultErr.Error(), "remove pr-review workspace") {
			t.Fatalf("expected no cleanup operation context without cleanup error, got %v", resultErr)
		}
	})
}

func prReviewTestSectionRows(t *testing.T, artifact prReviewArtifact, id string) []prReviewRow {
	t.Helper()
	for _, section := range artifact.Sections {
		if section.ID == id {
			return section.Rows
		}
	}
	t.Fatalf("missing pr-review section %q in %#v", id, artifact.Sections)
	return nil
}

func prReviewTestDependency(name, ecosystem, version string, waste int64, used float64, denied bool) report.DependencyReport {
	return report.DependencyReport{
		Name:                 name,
		Language:             "js-ts",
		EstimatedUnusedBytes: waste,
		UsedPercent:          used,
		UsedExportsCount:     int(used),
		TotalExportsCount:    100,
		Identity: &report.DependencyIdentity{
			Ecosystem:  ecosystem,
			Name:       name,
			Version:    version,
			PURL:       "pkg:" + ecosystem + "/" + name + "@" + version,
			Confidence: "high",
			Evidence:   []string{"package lock"},
		},
		License: &report.DependencyLicense{SPDX: "GPL-3.0-only", Denied: denied},
	}
}

type pathAwarePRReviewAnalyzer struct {
	requests   []analysis.Request
	baseReport report.Report
	headReport report.Report
}

type existingPathPRReviewAnalyzer struct {
	requests []analysis.Request
}

type sequencePRReviewAnalyzer struct {
	reports []report.Report
	err     error
	calls   int
}

func (s *sequencePRReviewAnalyzer) Analyse(_ context.Context, _ analysis.Request) (report.Report, error) {
	if s.calls < len(s.reports) {
		reportData := s.reports[s.calls]
		s.calls++
		return reportData, nil
	}
	s.calls++
	return report.Report{}, s.err
}

func stubPRReviewGit(t *testing.T, command func(context.Context, ...string) (*exec.Cmd, error)) {
	t.Helper()

	originalResolve := resolvePRReviewGitPathFn
	originalExec := execPRReviewGitCommandFn
	resolvePRReviewGitPathFn = func() (string, error) {
		return gitexec.ExecutablePrimary, nil
	}
	execPRReviewGitCommandFn = func(ctx context.Context, _ string, args ...string) (*exec.Cmd, error) {
		switch args[len(args)-1] {
		case "--show-toplevel":
			return exec.CommandContext(ctx, "/bin/echo", "-n", args[1]), nil
		case "--show-prefix":
			return exec.CommandContext(ctx, "/bin/echo", "-n"), nil
		}
		return command(ctx, args...)
	}
	t.Cleanup(func() {
		resolvePRReviewGitPathFn = originalResolve
		execPRReviewGitCommandFn = originalExec
	})
}

var prReviewTestCounters = map[string]int{}

func incrementPRReviewTestCounter(t *testing.T, key string) int {
	t.Helper()

	prReviewTestCounters[key]++
	value := prReviewTestCounters[key]
	t.Cleanup(func() {
		delete(prReviewTestCounters, key)
	})
	return value
}

func (p *pathAwarePRReviewAnalyzer) Analyse(_ context.Context, req analysis.Request) (report.Report, error) {
	p.requests = append(p.requests, req)
	switch filepath.Base(req.RepoPath) {
	case "base":
		if len(p.baseReport.Dependencies) > 0 {
			return p.baseReport, nil
		}
		return report.Report{Dependencies: []report.DependencyReport{
			prReviewTestDependency("lib", "npm", "1.0.0", 100, 90, false),
		}}, nil
	case "head":
		if len(p.headReport.Dependencies) > 0 {
			return p.headReport, nil
		}
		return report.Report{Dependencies: []report.DependencyReport{
			prReviewTestDependency("lib", "npm", "1.1.0", 100, 90, false),
		}}, nil
	default:
		return report.Report{}, nil
	}
}

func (a *existingPathPRReviewAnalyzer) Analyse(_ context.Context, req analysis.Request) (report.Report, error) {
	a.requests = append(a.requests, req)
	if _, err := os.Stat(req.RepoPath); err != nil {
		return report.Report{}, err
	}
	return report.Report{Dependencies: []report.DependencyReport{{Name: "present"}}}, nil
}

func newExplicitSHAReviewRequest(repoPath, baseSHA, headSHA, outputPath string, features featureflags.Set) Request {
	req := DefaultRequest()
	req.Mode = ModePRReview
	req.RepoPath = repoPath
	req.PRReview.BaseSHA = baseSHA
	req.PRReview.HeadSHA = headSHA
	req.PRReview.Format = prReviewFormatJSON
	req.PRReview.OutputPath = outputPath
	req.PRReview.TopN = 7
	req.PRReview.ScopeMode = ScopeModeChangedPackages
	req.PRReview.IncludePatterns = []string{"src/**"}
	req.PRReview.ExcludePatterns = []string{"vendor/**"}
	req.PRReview.Thresholds.LicenseDenyList = []string{"GPL-3.0-only"}
	req.PRReview.Thresholds.LowConfidenceWarningPercent = 31
	req.PRReview.Thresholds.MinUsagePercentForRecommendations = 62
	req.PRReview.Thresholds.RemovalCandidateWeightUsage = 0.1
	req.PRReview.Thresholds.RemovalCandidateWeightImpact = 0.2
	req.PRReview.Thresholds.RemovalCandidateWeightConfidence = 0.7
	req.PRReview.Thresholds.LicenseIncludeRegistryProvenance = true
	req.PRReview.Features = features
	return req
}

func assertPRReviewAnalysisCalls(t *testing.T, calls []analysis.Request) {
	t.Helper()

	if len(calls) != 2 {
		t.Fatalf("expected base and head analysis calls, got %#v", calls)
	}
	for _, call := range calls {
		assertPRReviewAnalysisCall(t, call)
	}
}

func assertPRReviewAnalysisCall(t *testing.T, call analysis.Request) {
	t.Helper()

	if call.Cache == nil || call.Cache.Enabled || !call.Cache.ReadOnly {
		t.Fatalf("expected pr-review analysis cache to be disabled/read-only, got %#v", call.Cache)
	}
	if call.TopN != 7 || call.ScopeMode != ScopeModeChangedPackages ||
		strings.Join(call.IncludePatterns, ",") != "src/**" ||
		strings.Join(call.ExcludePatterns, ",") != "vendor/**" {
		t.Fatalf("expected analysis request options to be forwarded, got %#v", call)
	}
	if strings.Join(call.LicenseDenyList, ",") != "GPL-3.0-only" || !call.IncludeRegistryProvenance {
		t.Fatalf("expected license policy to be forwarded, got %#v", call)
	}
	if call.LowConfidenceWarningPercent == nil || *call.LowConfidenceWarningPercent != 31 ||
		call.MinUsagePercentForRecommendations == nil || *call.MinUsagePercentForRecommendations != 62 {
		t.Fatalf("expected threshold policy to be forwarded, got %#v", call)
	}
	if call.RemovalCandidateWeights == nil ||
		call.RemovalCandidateWeights.Usage != 0.1 ||
		call.RemovalCandidateWeights.Impact != 0.2 ||
		call.RemovalCandidateWeights.Confidence != 0.7 {
		t.Fatalf("expected removal weights to be forwarded, got %#v", call.RemovalCandidateWeights)
	}
	if !call.ChangedFilesExplicit || strings.Join(call.ChangedFiles, ",") != "src/app.txt" {
		t.Fatalf("expected explicit pr-review changed range to be forwarded, got %#v", call)
	}
}

func createPRReviewGitRepo(t *testing.T) (string, string, string) {
	t.Helper()

	repoPath := t.TempDir()
	testutil.RunGit(t, repoPath, "init")
	testutil.RunGit(t, repoPath, "config", "user.email", "pr-review-test@example.com")
	testutil.RunGit(t, repoPath, "config", "user.name", "PR Review Test")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "src", "app.txt"), "base\n")
	testutil.RunGit(t, repoPath, "add", ".")
	testutil.RunGit(t, repoPath, "commit", "-m", "base")
	baseSHA := prReviewGitOutput(t, repoPath, "rev-parse", "HEAD")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "src", "app.txt"), "head\n")
	testutil.RunGit(t, repoPath, "add", ".")
	testutil.RunGit(t, repoPath, "commit", "-m", "head")
	headSHA := prReviewGitOutput(t, repoPath, "rev-parse", "HEAD")
	return repoPath, baseSHA, headSHA
}

func prReviewGitOutput(t *testing.T, repoPath string, args ...string) string {
	t.Helper()

	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("resolve git path: %v", err)
	}
	command, err := gitexec.CommandContext(context.Background(), gitPath, append([]string{"-C", repoPath}, args...)...)
	if err != nil {
		t.Fatalf("construct git %s: %v", strings.Join(args, " "), err)
	}
	command.Env = gitexec.SanitizedEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func mustResolveAppTestFeatures(t *testing.T, enabled ...string) featureflags.Set {
	t.Helper()

	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  enabled,
	})
	if err != nil {
		t.Fatalf("resolve app test features: %v", err)
	}
	return features
}

func mustReadAppTestFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
