package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/app"
	"github.com/ben-ranford/lopper/internal/report"
)

func parsePRReview(args []string, req app.Request) (app.Request, error) {
	normalizedArgs, err := normalizeArgs(args)
	if err != nil {
		return req, err
	}
	args = normalizedArgs

	fs := flag.NewFlagSet("pr-review", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	includePatterns := newPatternListFlag(req.PRReview.IncludePatterns)
	excludePatterns := newPatternListFlag(req.PRReview.ExcludePatterns)
	enableFeatures := newPatternListFlag(nil)
	disableFeatures := newPatternListFlag(nil)

	repoFlag := fs.String("repo", req.RepoPath, "repository path")
	baseFlag := fs.String("base", req.PRReview.BaseSHA, "base commit SHA")
	headFlag := fs.String("head", req.PRReview.HeadSHA, "head commit SHA")
	formatFlag := fs.String("format", req.PRReview.Format, "output format")
	outputFlag := fs.String("output", req.PRReview.OutputPath, "output file path")
	outputShortFlag := fs.String("o", req.PRReview.OutputPath, "output file path")
	languageFlag := fs.String("language", req.PRReview.Language, "language adapter")
	topFlag := fs.Int("top", req.PRReview.TopN, "top N dependencies per revision")
	scopeModeFlag := fs.String("scope-mode", req.PRReview.ScopeMode, "analysis scope mode")
	configFlag := fs.String("config", req.PRReview.ConfigPath, "config file path")
	advisorySourceFlag := fs.String("advisory-source", req.PRReview.AdvisorySourcePath, "local vulnerability advisory source file")
	thresholdLowConfidenceWarningFlag := fs.Int("threshold-low-confidence-warning", req.PRReview.Thresholds.LowConfidenceWarningPercent, "low-confidence warning threshold")
	thresholdMinUsagePercentFlag := fs.Int("threshold-min-usage-percent", req.PRReview.Thresholds.MinUsagePercentForRecommendations, "minimum usage percent threshold for recommendation generation")
	thresholdReachableVulnPriorityFlag := fs.String("threshold-reachable-vuln-priority", req.PRReview.Thresholds.ReachableVulnerabilityPriority, "reachable vulnerability priority threshold (off, low, medium, high, critical)")
	scoreWeightUsageFlag := fs.Float64("score-weight-usage", req.PRReview.Thresholds.RemovalCandidateWeightUsage, "relative weight for removal-candidate usage signal")
	scoreWeightImpactFlag := fs.Float64("score-weight-impact", req.PRReview.Thresholds.RemovalCandidateWeightImpact, "relative weight for removal-candidate impact signal")
	scoreWeightConfidenceFlag := fs.Float64("score-weight-confidence", req.PRReview.Thresholds.RemovalCandidateWeightConfidence, "relative weight for removal-candidate confidence signal")
	licenseDenyFlag := fs.String("license-deny", strings.Join(req.PRReview.Thresholds.LicenseDenyList, ","), "comma-separated SPDX identifiers to deny")
	licenseIncludeRegistryProvFlag := fs.Bool("license-provenance-registry", req.PRReview.Thresholds.LicenseIncludeRegistryProvenance, "opt-in registry provenance heuristics for JS/TS dependencies")
	failOnRegressionFlag := fs.Bool("fail-on-regression", req.PRReview.FailOnRegression, "fail when new PR regressions are detected")
	materialWasteBytesFlag := fs.Int64("material-waste-bytes", req.PRReview.MaterialWasteBytes, "estimated unused byte delta required for a material waste regression")
	maxRowsFlag := fs.Int("max-rows", req.PRReview.MaxRows, "maximum Markdown rows per section")
	fs.Var(enableFeatures, "enable-feature", "comma-separated feature flag names to enable (repeatable)")
	fs.Var(disableFeatures, "disable-feature", "comma-separated feature flag names to disable (repeatable)")
	fs.Var(includePatterns, "include", "comma-separated include path globs (repeatable)")
	fs.Var(excludePatterns, "exclude", "comma-separated exclude path globs (repeatable)")

	if err := parseFlagSet(fs, args); err != nil {
		return req, err
	}
	if fs.NArg() > 0 {
		return req, fmt.Errorf("unexpected arguments for pr-review")
	}
	if strings.TrimSpace(*baseFlag) == "" {
		return req, fmt.Errorf("pr-review requires --base")
	}
	if strings.TrimSpace(*headFlag) == "" {
		return req, fmt.Errorf("pr-review requires --head")
	}
	if *topFlag <= 0 {
		return req, fmt.Errorf("--top must be > 0")
	}
	if *materialWasteBytesFlag < 0 {
		return req, fmt.Errorf("--material-waste-bytes must be >= 0")
	}
	if *maxRowsFlag <= 0 {
		return req, fmt.Errorf("--max-rows must be > 0")
	}
	scopeMode, err := parseScopeMode(*scopeModeFlag)
	if err != nil {
		return req, err
	}
	outputPath, err := resolveOutputPath(*outputFlag, *outputShortFlag)
	if err != nil {
		return req, err
	}
	visited := visitedFlags(fs)
	resolvedPolicy, err := resolveAnalysisPolicyCore(visited, analyseFlagValues{
		repoPath:                       repoFlag,
		configPath:                     configFlag,
		advisorySourcePath:             advisorySourceFlag,
		thresholdLowConfidenceWarning:  thresholdLowConfidenceWarningFlag,
		thresholdMinUsagePercent:       thresholdMinUsagePercentFlag,
		thresholdReachableVulnPriority: thresholdReachableVulnPriorityFlag,
		scoreWeightUsage:               scoreWeightUsageFlag,
		scoreWeightImpact:              scoreWeightImpactFlag,
		scoreWeightConfidence:          scoreWeightConfidenceFlag,
		licenseDeny:                    licenseDenyFlag,
		licenseIncludeRegistryProv:     licenseIncludeRegistryProvFlag,
		enableFeatures:                 enableFeatures,
		disableFeatures:                disableFeatures,
	})
	if err != nil {
		return req, err
	}

	req.Mode = app.ModePRReview
	req.RepoPath = strings.TrimSpace(*repoFlag)
	req.PRReview = app.PRReviewRequest{
		BaseSHA:                 strings.TrimSpace(*baseFlag),
		HeadSHA:                 strings.TrimSpace(*headFlag),
		Format:                  strings.TrimSpace(*formatFlag),
		OutputPath:              outputPath,
		Language:                strings.TrimSpace(*languageFlag),
		TopN:                    *topFlag,
		ScopeMode:               scopeMode,
		ConfigPath:              resolvedPolicy.configPath,
		AdvisorySourcePath:      resolvedPolicy.advisorySourcePath,
		PolicySources:           append([]string{}, resolvedPolicy.policySources...),
		PolicyTrace:             append([]report.PolicyMergeTrace{}, resolvedPolicy.policyTrace...),
		VulnerabilityExceptions: append([]report.VulnerabilityException{}, resolvedPolicy.vulnerabilityExceptions...),
		IncludePatterns:         resolveScopePatterns(visited, "include", includePatterns.Values(), resolvedPolicy.scope.Include),
		ExcludePatterns:         resolveScopePatterns(visited, "exclude", excludePatterns.Values(), resolvedPolicy.scope.Exclude),
		FailOnRegression:        *failOnRegressionFlag,
		MaterialWasteBytes:      *materialWasteBytesFlag,
		MaxRows:                 *maxRowsFlag,
		Features:                resolvedPolicy.features,
		Thresholds:              resolvedPolicy.thresholds,
	}
	return req, nil
}
