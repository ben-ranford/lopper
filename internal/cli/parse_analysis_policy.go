package cli

import (
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

type resolvedAnalysisPolicy struct {
	thresholds              thresholds.Values
	scope                   thresholds.PathScope
	policySources           []string
	policyTrace             []report.PolicyMergeTrace
	advisorySourcePath      string
	vulnerabilityExceptions []report.VulnerabilityException
	configPath              string
	features                featureflags.Set
	notifications           notify.Config
}

func resolveAnalysisPolicy(visited map[string]bool, flags analyseFlagValues) (resolvedAnalysisPolicy, error) {
	policy, err := resolveAnalysisPolicyCore(visited, flags)
	if err != nil {
		return resolvedAnalysisPolicy{}, err
	}
	resolvedNotifications, err := resolveAnalyseNotifications(visited, flags, policy.configPath)
	if err != nil {
		return resolvedAnalysisPolicy{}, err
	}
	policy.notifications = resolvedNotifications
	return policy, nil
}

func resolveAnalysisPolicyCore(visited map[string]bool, flags analyseFlagValues) (resolvedAnalysisPolicy, error) {
	resolvedThresholds, resolvedScope, policySources, policyTrace, advisorySourcePath, vulnerabilityExceptions, configFeatures, resolvedConfigPath, err := resolveAnalyseThresholds(flags, visited)
	if err != nil {
		return resolvedAnalysisPolicy{}, err
	}
	resolvedFeatures, err := resolveAnalyseFeatures(visited, flags, configFeatures)
	if err != nil {
		return resolvedAnalysisPolicy{}, err
	}
	return resolvedAnalysisPolicy{
		thresholds:              resolvedThresholds,
		scope:                   resolvedScope,
		policySources:           policySources,
		policyTrace:             policyTrace,
		advisorySourcePath:      advisorySourcePath,
		vulnerabilityExceptions: vulnerabilityExceptions,
		configPath:              resolvedConfigPath,
		features:                resolvedFeatures,
	}, nil
}
