package app

import (
	"strings"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

type analysisRequestPolicy struct {
	saveBaseline            bool
	thresholds              thresholds.Values
	advisorySourcePath      string
	advisorySourceTrustRoot string
	vulnerabilityExceptions []report.VulnerabilityException
	policySources           []string
	policyTrace             []report.PolicyMergeTrace
}

type preparedAnalysisPolicy struct {
	request                 analysis.Request
	effectiveThresholds     report.EffectiveThresholds
	removalCandidateWeights report.RemovalCandidateWeights
	licensePolicy           report.LicensePolicy
	vulnerabilityPolicy     report.VulnerabilityPolicy
	policySources           []string
	policyTrace             []report.PolicyMergeTrace
}

func prepareAnalysisPolicy(base analysis.Request, policy analysisRequestPolicy) preparedAnalysisPolicy {
	lowConfidence := policy.thresholds.LowConfidenceWarningPercent
	minUsage := policy.thresholds.MinUsagePercentForRecommendations
	weights := thresholds.RemovalCandidateWeights(policy.thresholds)
	base.LowConfidenceWarningPercent = &lowConfidence
	base.MinUsagePercentForRecommendations = &minUsage
	base.RemovalCandidateWeights = &weights
	base.LicenseDenyList = append([]string{}, policy.thresholds.LicenseDenyList...)
	base.IncludeRegistryProvenance = policy.thresholds.LicenseIncludeRegistryProvenance
	base.VulnerabilityExceptions = append([]report.VulnerabilityException{}, policy.vulnerabilityExceptions...)
	base.RequireCompleteCoverage = requiresCompleteCoverage(policy)

	return preparedAnalysisPolicy{
		request: base,
		effectiveThresholds: report.EffectiveThresholds{
			FailOnIncreasePercent:             policy.thresholds.FailOnIncreasePercent,
			LowConfidenceWarningPercent:       policy.thresholds.LowConfidenceWarningPercent,
			MinUsagePercentForRecommendations: policy.thresholds.MinUsagePercentForRecommendations,
			MaxUncertainImportCount:           policy.thresholds.MaxUncertainImportCount,
			ReachableVulnerabilityPriority:    policy.thresholds.ReachableVulnerabilityPriority,
		},
		removalCandidateWeights: weights,
		licensePolicy: report.LicensePolicy{
			Deny:                      report.SortedDenyList(policy.thresholds.LicenseDenyList),
			FailOnDenied:              policy.thresholds.LicenseFailOnDeny,
			IncludeRegistryProvenance: policy.thresholds.LicenseIncludeRegistryProvenance,
		},
		vulnerabilityPolicy: report.VulnerabilityPolicy{
			AdvisorySourcePath:         policy.advisorySourcePath,
			ReachablePriorityThreshold: policy.thresholds.ReachableVulnerabilityPriority,
		},
		policySources: append([]string{}, policy.policySources...),
		policyTrace:   append([]report.PolicyMergeTrace{}, policy.policyTrace...),
	}
}

func requiresCompleteCoverage(policy analysisRequestPolicy) bool {
	if policy.saveBaseline {
		return true
	}
	if policy.thresholds.FailOnIncreasePercent >= 0 {
		return true
	}
	if policy.thresholds.LicenseFailOnDeny && len(policy.thresholds.LicenseDenyList) > 0 {
		return true
	}
	if strings.TrimSpace(policy.advisorySourcePath) == "" {
		return false
	}
	threshold := report.NormalizeVulnerabilityPriorityThreshold(policy.thresholds.ReachableVulnerabilityPriority)
	return threshold != "" && threshold != report.VulnerabilityPriorityOff
}
