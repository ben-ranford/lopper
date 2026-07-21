package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func TestParseArgsPRReview(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, ".lopper.yml"), "scope:\n  include: [\"src/**\"]\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "advisories.json"), "{}")
	baseSHA := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)
	req := mustParseArgs(t, []string{
		"pr-review",
		"--repo", repo,
		"--base", baseSHA,
		"--head", headSHA,
		"--format", "json",
		"--output", "review.json",
		"-o", "review.json",
		"--language", "all",
		"--top", "10",
		"--scope-mode", "repo",
		"--config", ".lopper.yml",
		"--advisory-source", "advisories.json",
		"--license-deny", "gpl-3.0-only,agpl-3.0-only",
		"--include", "src/**",
		"--exclude", "vendor/**",
		"--material-waste-bytes", "0",
		"--max-rows", "7",
		"--fail-on-regression",
		"--enable-feature", report.DependencySurfacePRReviewPreviewFeature,
	})
	assertParsedPRReviewRequest(t, req, expectedPRReviewParse{
		repo:                   repo,
		baseSHA:                baseSHA,
		headSHA:                headSHA,
		format:                 "json",
		outputPath:             "review.json",
		language:               "all",
		topN:                   10,
		scopeMode:              app.ScopeModeRepo,
		configPath:             filepath.Join(repo, ".lopper.yml"),
		advisorySourcePath:     "advisories.json",
		licenseDenyList:        "AGPL-3.0-ONLY,GPL-3.0-ONLY",
		lowConfidenceWarning:   thresholds.DefaultLowConfidenceWarningPercent,
		includePatterns:        "src/**",
		excludePatterns:        "vendor/**",
		failOnRegression:       true,
		materialWasteBytes:     0,
		maxRows:                7,
		requiredPreviewFeature: report.DependencySurfacePRReviewPreviewFeature,
	})
}

func TestParseArgsPRReviewValidation(t *testing.T) {
	baseSHA := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing base", args: []string{"pr-review", "--head", headSHA}, want: "requires --base"},
		{name: "missing head", args: []string{"pr-review", "--base", baseSHA}, want: "requires --head"},
		{name: "top zero", args: []string{"pr-review", "--base", baseSHA, "--head", headSHA, "--top", "0"}, want: "--top must be > 0"},
		{name: "negative material", args: []string{"pr-review", "--base", baseSHA, "--head", headSHA, "--material-waste-bytes", "-1"}, want: "--material-waste-bytes must be >= 0"},
		{name: "max rows zero", args: []string{"pr-review", "--base", baseSHA, "--head", headSHA, "--max-rows", "0"}, want: "--max-rows must be > 0"},
		{name: "missing flag value", args: []string{"pr-review", "--base"}, want: "flag needs an argument"},
		{name: "invalid scope mode", args: []string{"pr-review", "--base", baseSHA, "--head", headSHA, "--scope-mode", "bad"}, want: "invalid --scope-mode"},
		{name: "output mismatch", args: []string{"pr-review", "--base", baseSHA, "--head", headSHA, "--output", "one.md", "-o", "two.md"}, want: "must match"},
		{name: "unknown feature", args: []string{"pr-review", "--base", baseSHA, "--head", headSHA, "--enable-feature", "missing-feature"}, want: "unknown feature"},
		{name: "positional", args: []string{"pr-review", "--base", baseSHA, "--head", headSHA, "extra"}, want: "unexpected arguments"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := expectParseArgsError(t, tc.args, "expected pr-review validation error")
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error to contain %q, got %v", tc.want, err)
			}
		})
	}
}

func TestParseArgsPRReviewLoadsPolicyConfig(t *testing.T) {
	repo := t.TempDir()
	configPath := filepath.Join(repo, ".lopper.yml")
	advisoryPath := filepath.Join(repo, "advisories.json")
	testutil.MustWriteFile(t, advisoryPath, "{}")
	testutil.MustWriteFile(t, configPath, `
scope:
  include: ["src/**"]
  exclude: ["vendor/**"]
features:
  enable: ["dependency-surface-pr-review-preview"]
advisories:
  source: advisories.json
  exceptions:
    - vulnerability_id: GHSA-test
      package: lib
      owner: security
      reason: accepted for fixture
      expires: "2026-08-01"
thresholds:
  low_confidence_warning_percent: 27
  min_usage_percent_for_recommendations: 52
  reachable_vulnerability_priority: high
  removal_candidate_weight_usage: 0.2
  removal_candidate_weight_impact: 0.5
  removal_candidate_weight_confidence: 0.3
  license_include_registry_provenance: true
  license_deny: ["GPL-3.0-only"]
`)

	baseSHA := strings.Repeat("a", 40)
	headSHA := strings.Repeat("b", 40)
	req := mustParseArgs(t, []string{
		"pr-review",
		"--repo", repo,
		"--base", baseSHA,
		"--head", headSHA,
		"--config", configPath,
	})

	if req.PRReview.ConfigPath != configPath || req.PRReview.AdvisorySourcePath != advisoryPath {
		t.Fatalf("expected config policy paths to resolve, got %#v", req.PRReview)
	}
	if got := strings.Join(req.PRReview.IncludePatterns, ","); got != "src/**" {
		t.Fatalf("expected config include patterns, got %q", got)
	}
	if got := strings.Join(req.PRReview.ExcludePatterns, ","); got != "vendor/**" {
		t.Fatalf("expected config exclude patterns, got %q", got)
	}
	if got := strings.Join(req.PRReview.Thresholds.LicenseDenyList, ","); got != "GPL-3.0-ONLY" {
		t.Fatalf("expected config deny list, got %q", got)
	}
	if len(req.PRReview.VulnerabilityExceptions) != 1 {
		t.Fatalf("expected config vulnerability exceptions, got %#v", req.PRReview.VulnerabilityExceptions)
	}
	if req.PRReview.Thresholds.LowConfidenceWarningPercent != 27 || req.PRReview.Thresholds.MinUsagePercentForRecommendations != 52 {
		t.Fatalf("expected config thresholds, got %#v", req.PRReview.Thresholds)
	}
	if !req.PRReview.Thresholds.LicenseIncludeRegistryProvenance {
		t.Fatalf("expected config registry provenance policy")
	}
	if !req.PRReview.Features.Enabled(report.DependencySurfacePRReviewPreviewFeature) {
		t.Fatalf("expected config feature flag to be enabled")
	}
}

func TestParseArgsPRReviewIgnoresUnusedNotificationInputs(t *testing.T) {
	tests := []struct {
		name          string
		notifications string
		envTrigger    string
	}{
		{name: "config", notifications: "notifications:\n  slack:\n    on: definitely-not-valid\n"},
		{name: "environment", envTrigger: "definitely-not-valid"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			configPath := filepath.Join(repo, ".lopper.yml")
			testutil.MustWriteFile(t, configPath, "features:\n  enable: [\"dependency-surface-pr-review-preview\"]\n"+tc.notifications)
			t.Setenv(notify.EnvOn, tc.envTrigger)

			_, err := ParseArgs([]string{
				"pr-review",
				"--repo", repo,
				"--base", strings.Repeat("a", 40),
				"--head", strings.Repeat("b", 40),
				"--config", configPath,
			})
			if err != nil {
				t.Fatalf("pr-review must ignore unused notification inputs: %v", err)
			}
		})
	}
}

func TestResolvePRReviewPolicyCLIOverridesConfig(t *testing.T) {
	repo := t.TempDir()
	configPath := filepath.Join(repo, ".lopper.yml")
	configAdvisoryPath := filepath.Join(repo, "config-advisories.json")
	overrideAdvisoryPath := filepath.Join(repo, "cli-advisories.json")
	testutil.MustWriteFile(t, configAdvisoryPath, "{}")
	testutil.MustWriteFile(t, overrideAdvisoryPath, "{}")
	testutil.MustWriteFile(t, configPath, `
scope:
  include: ["src/**"]
  exclude: ["vendor/**"]
advisories:
  source: config-advisories.json
  exceptions:
    - vulnerability_id: GHSA-test
      package: lib
      owner: security
      reason: accepted for fixture
      expires: "2026-08-01"
thresholds:
  low_confidence_warning_percent: 27
  min_usage_percent_for_recommendations: 52
  reachable_vulnerability_priority: medium
  removal_candidate_weight_usage: 0.2
  removal_candidate_weight_impact: 0.5
  removal_candidate_weight_confidence: 0.3
  license_include_registry_provenance: false
  license_deny: ["GPL-3.0-only"]
`)

	visited := map[string]bool{
		"advisory-source":                   true,
		"license-deny":                      true,
		"include":                           true,
		"exclude":                           true,
		"enable-feature":                    true,
		"threshold-low-confidence-warning":  true,
		"threshold-min-usage-percent":       true,
		"threshold-reachable-vuln-priority": true,
		"score-weight-usage":                true,
		"score-weight-impact":               true,
		"score-weight-confidence":           true,
		"license-provenance-registry":       true,
	}
	policy, err := resolveAnalysisPolicy(visited, analyseFlagValues{
		repoPath:                       stringPtr(repo),
		configPath:                     stringPtr(configPath),
		advisorySourcePath:             stringPtr(overrideAdvisoryPath),
		thresholdLowConfidenceWarning:  intPtr(31),
		thresholdMinUsagePercent:       intPtr(60),
		thresholdReachableVulnPriority: stringPtr(report.VulnerabilityPriorityCritical),
		scoreWeightUsage:               float64Ptr(0.4),
		scoreWeightImpact:              float64Ptr(0.1),
		scoreWeightConfidence:          float64Ptr(0.5),
		licenseDeny:                    stringPtr("MIT"),
		licenseIncludeRegistryProv:     boolPtr(true),
		enableFeatures:                 newPatternListFlag([]string{report.DependencySurfacePRReviewPreviewFeature}),
	})
	if err != nil {
		t.Fatalf("resolve pr-review policy: %v", err)
	}
	assertResolvedPRReviewPolicy(t, policy, expectedResolvedPRReviewPolicy{
		configPath:                        configPath,
		advisorySourcePath:                overrideAdvisoryPath,
		includePatterns:                   "src/**",
		licenseDenyList:                   "MIT",
		vulnerabilityExceptions:           1,
		lowConfidenceWarningPercent:       31,
		minUsagePercentForRecommendations: 60,
		reachablePriority:                 report.VulnerabilityPriorityCritical,
		weightUsage:                       0.4,
		weightImpact:                      0.1,
		weightConfidence:                  0.5,
		includeRegistryProvenance:         true,
		requiredFeature:                   report.DependencySurfacePRReviewPreviewFeature,
	})
	assertPolicyTraceSources(t, policy.policyTrace, map[string]string{
		"advisories.source":                         "cli",
		"thresholds.low_confidence_warning_percent": "cli",
		"license.include_registry_provenance":       "cli",
	})
}

type expectedPRReviewParse struct {
	repo                   string
	baseSHA                string
	headSHA                string
	format                 string
	outputPath             string
	language               string
	topN                   int
	scopeMode              string
	configPath             string
	advisorySourcePath     string
	licenseDenyList        string
	lowConfidenceWarning   int
	includePatterns        string
	excludePatterns        string
	failOnRegression       bool
	materialWasteBytes     int64
	maxRows                int
	requiredPreviewFeature string
}

func assertParsedPRReviewRequest(t *testing.T, req app.Request, want expectedPRReviewParse) {
	t.Helper()

	if req.Mode != app.ModePRReview {
		t.Fatalf(modeMismatchFmt, app.ModePRReview, req.Mode)
	}
	assertParsedPRReviewCoreFields(t, req, want)
	if req.PRReview.ConfigPath != want.configPath || req.PRReview.AdvisorySourcePath != want.advisorySourcePath {
		t.Fatalf("unexpected pr-review policy paths: %#v", req.PRReview)
	}
	if got := strings.Join(req.PRReview.Thresholds.LicenseDenyList, ","); got != want.licenseDenyList {
		t.Fatalf("unexpected pr-review license deny list: %q", got)
	}
	if req.PRReview.Thresholds.LowConfidenceWarningPercent != want.lowConfidenceWarning {
		t.Fatalf("expected default low-confidence threshold, got %#v", req.PRReview.Thresholds)
	}
	if got := strings.Join(req.PRReview.IncludePatterns, ","); got != want.includePatterns {
		t.Fatalf("unexpected pr-review include patterns: %q", got)
	}
	if got := strings.Join(req.PRReview.ExcludePatterns, ","); got != want.excludePatterns {
		t.Fatalf("unexpected pr-review exclude patterns: %q", got)
	}
	if req.PRReview.FailOnRegression != want.failOnRegression ||
		req.PRReview.MaterialWasteBytes != want.materialWasteBytes ||
		req.PRReview.MaxRows != want.maxRows {
		t.Fatalf("unexpected pr-review thresholds: %#v", req.PRReview)
	}
	if !req.PRReview.Features.Enabled(want.requiredPreviewFeature) {
		t.Fatalf("expected pr-review preview feature to be enabled")
	}
}

func assertParsedPRReviewCoreFields(t *testing.T, req app.Request, want expectedPRReviewParse) {
	t.Helper()
	if req.RepoPath != want.repo || req.PRReview.BaseSHA != want.baseSHA || req.PRReview.HeadSHA != want.headSHA {
		t.Fatalf("unexpected pr-review revision fields: %#v", req)
	}
	if req.PRReview.Format != want.format || req.PRReview.OutputPath != want.outputPath {
		t.Fatalf("unexpected pr-review output fields: %#v", req.PRReview)
	}
	if req.PRReview.Language != want.language || req.PRReview.TopN != want.topN || req.PRReview.ScopeMode != want.scopeMode {
		t.Fatalf("unexpected pr-review analysis fields: %#v", req.PRReview)
	}
}

type expectedResolvedPRReviewPolicy struct {
	configPath                        string
	advisorySourcePath                string
	includePatterns                   string
	licenseDenyList                   string
	vulnerabilityExceptions           int
	lowConfidenceWarningPercent       int
	minUsagePercentForRecommendations int
	reachablePriority                 string
	weightUsage                       float64
	weightImpact                      float64
	weightConfidence                  float64
	includeRegistryProvenance         bool
	requiredFeature                   string
}

func assertResolvedPRReviewPolicy(t *testing.T, policy resolvedAnalysisPolicy, want expectedResolvedPRReviewPolicy) {
	t.Helper()

	if policy.configPath != want.configPath {
		t.Fatalf("expected config path %q, got %q", want.configPath, policy.configPath)
	}
	if policy.advisorySourcePath != want.advisorySourcePath {
		t.Fatalf("expected advisory source override %q, got %q", want.advisorySourcePath, policy.advisorySourcePath)
	}
	if got := strings.Join(policy.scope.Include, ","); got != want.includePatterns {
		t.Fatalf("expected config include policy, got %q", got)
	}
	if got := strings.Join(policy.thresholds.LicenseDenyList, ","); got != want.licenseDenyList {
		t.Fatalf("expected license deny override, got %q", got)
	}
	if len(policy.vulnerabilityExceptions) != want.vulnerabilityExceptions {
		t.Fatalf("expected config vulnerability exceptions to remain available, got %#v", policy.vulnerabilityExceptions)
	}
	if policy.thresholds.LowConfidenceWarningPercent != want.lowConfidenceWarningPercent ||
		policy.thresholds.MinUsagePercentForRecommendations != want.minUsagePercentForRecommendations {
		t.Fatalf("expected CLI threshold overrides, got %#v", policy.thresholds)
	}
	if policy.thresholds.ReachableVulnerabilityPriority != want.reachablePriority {
		t.Fatalf("expected reachable vulnerability priority override, got %#v", policy.thresholds)
	}
	if policy.thresholds.RemovalCandidateWeightUsage != want.weightUsage ||
		policy.thresholds.RemovalCandidateWeightImpact != want.weightImpact ||
		policy.thresholds.RemovalCandidateWeightConfidence != want.weightConfidence {
		t.Fatalf("expected removal weight overrides, got %#v", policy.thresholds)
	}
	if policy.thresholds.LicenseIncludeRegistryProvenance != want.includeRegistryProvenance {
		t.Fatalf("expected registry provenance override")
	}
	if !policy.features.Enabled(want.requiredFeature) {
		t.Fatalf("expected CLI enable-feature to add the pr-review preview feature")
	}
}

func assertPolicyTraceSources(t *testing.T, trace []report.PolicyMergeTrace, want map[string]string) {
	t.Helper()

	for field, source := range want {
		if policyTraceSource(trace, field) != source {
			t.Fatalf("expected %s trace to resolve to %s, got %#v", field, source, trace)
		}
	}
}

func stringPtr(value string) *string { return &value }

func intPtr(value int) *int { return &value }

func float64Ptr(value float64) *float64 { return &value }

func boolPtr(value bool) *bool { return &value }

func policyTraceSource(trace []report.PolicyMergeTrace, field string) string {
	for _, item := range trace {
		if item.Field == field {
			return item.Source
		}
	}
	return ""
}
