package app

import (
	"context"
	"strings"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

type preparedAnalyseExecution struct {
	request                 analysis.Request
	lockfileWarnings        []string
	effectiveThresholds     report.EffectiveThresholds
	removalCandidateWeights report.RemovalCandidateWeights
	licensePolicy           report.LicensePolicy
	policySources           []string
}

func prepareAnalyseExecution(ctx context.Context, req Request) (preparedAnalyseExecution, error) {
	lockfileWarnings, err := evaluateLockfileDriftPolicy(ctx, req.RepoPath, req.Analyse.Thresholds.LockfileDriftPolicy)
	if err != nil {
		return preparedAnalyseExecution{}, err
	}
	if err := validateCodemodApplyPreconditions(ctx, req.RepoPath, req.Analyse); err != nil {
		return preparedAnalyseExecution{}, err
	}

	lowConfidence := req.Analyse.Thresholds.LowConfidenceWarningPercent
	minUsage := req.Analyse.Thresholds.MinUsagePercentForRecommendations
	weights := report.RemovalCandidateWeights{
		Usage:      req.Analyse.Thresholds.RemovalCandidateWeightUsage,
		Impact:     req.Analyse.Thresholds.RemovalCandidateWeightImpact,
		Confidence: req.Analyse.Thresholds.RemovalCandidateWeightConfidence,
	}
	runtimeTracePath, runtimeTracePathExplicit := prepareRuntimeTracePlan(req)
	effectiveThresholds := report.EffectiveThresholds{
		FailOnIncreasePercent:             req.Analyse.Thresholds.FailOnIncreasePercent,
		LowConfidenceWarningPercent:       req.Analyse.Thresholds.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: req.Analyse.Thresholds.MinUsagePercentForRecommendations,
		MaxUncertainImportCount:           req.Analyse.Thresholds.MaxUncertainImportCount,
	}

	return preparedAnalyseExecution{
		request: analysis.Request{
			RepoPath:                          req.RepoPath,
			Dependency:                        req.Analyse.Dependency,
			TopN:                              req.Analyse.TopN,
			ScopeMode:                         req.Analyse.ScopeMode,
			SuggestOnly:                       req.Analyse.SuggestOnly || req.Analyse.ApplyCodemod,
			Language:                          req.Analyse.Language,
			ConfigPath:                        req.Analyse.ConfigPath,
			RuntimeProfile:                    req.Analyse.RuntimeProfile,
			RuntimeTracePath:                  runtimeTracePath,
			RuntimeTracePathExplicit:          runtimeTracePathExplicit,
			RuntimeTestCommand:                strings.TrimSpace(req.Analyse.RuntimeTestCommand),
			IncludePatterns:                   req.Analyse.IncludePatterns,
			ExcludePatterns:                   req.Analyse.ExcludePatterns,
			LowConfidenceWarningPercent:       &lowConfidence,
			MinUsagePercentForRecommendations: &minUsage,
			RemovalCandidateWeights:           &weights,
			LicenseDenyList:                   append([]string{}, req.Analyse.Thresholds.LicenseDenyList...),
			IncludeRegistryProvenance:         req.Analyse.Thresholds.LicenseIncludeRegistryProvenance,
			Cache: &analysis.CacheOptions{
				Enabled:  req.Analyse.CacheEnabled,
				Path:     req.Analyse.CachePath,
				ReadOnly: req.Analyse.CacheReadOnly,
			},
		},
		lockfileWarnings:        lockfileWarnings,
		effectiveThresholds:     effectiveThresholds,
		removalCandidateWeights: weights,
		licensePolicy: report.LicensePolicy{
			Deny:                      report.SortedDenyList(req.Analyse.Thresholds.LicenseDenyList),
			FailOnDenied:              req.Analyse.Thresholds.LicenseFailOnDeny,
			IncludeRegistryProvenance: req.Analyse.Thresholds.LicenseIncludeRegistryProvenance,
		},
		policySources: append([]string{}, req.Analyse.PolicySources...),
	}, nil
}

func decorateAnalyseReport(reportData *report.Report, prepared preparedAnalyseExecution) {
	if reportData == nil {
		return
	}

	effectiveThresholds := prepared.effectiveThresholds
	licensePolicy := prepared.licensePolicy
	licensePolicy.Deny = append([]string{}, prepared.licensePolicy.Deny...)
	reportData.EffectiveThresholds = &effectiveThresholds
	reportData.EffectivePolicy = &report.EffectivePolicy{
		Sources:                 append([]string{}, prepared.policySources...),
		Thresholds:              effectiveThresholds,
		RemovalCandidateWeights: prepared.removalCandidateWeights,
		License:                 licensePolicy,
	}
	reportData.Warnings = append(reportData.Warnings, prepared.lockfileWarnings...)
}

func prepareRuntimeTrace(_ context.Context, req Request) ([]string, string) {
	tracePath, _ := prepareRuntimeTracePlan(req)
	return nil, tracePath
}

func prepareRuntimeTracePlan(req Request) (string, bool) {
	path := strings.TrimSpace(req.Analyse.RuntimeTracePath)
	return path, path != ""
}
