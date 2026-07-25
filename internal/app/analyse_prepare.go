package app

import (
	"context"
	"path/filepath"
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
	vulnerabilityPolicy     report.VulnerabilityPolicy
	policySources           []string
	policyTrace             []report.PolicyMergeTrace
}

func prepareAnalyseExecution(ctx context.Context, req Request) (preparedAnalyseExecution, error) {
	lockfileWarnings, err := evaluateLockfileDriftPolicyWithFeatures(ctx, req.RepoPath, req.Analyse.Thresholds.LockfileDriftPolicy, req.Analyse.Features)
	if err != nil {
		return preparedAnalyseExecution{}, err
	}
	if err := validateCodemodApplyPreconditions(ctx, req.RepoPath, req.Analyse); err != nil {
		return preparedAnalyseExecution{}, err
	}

	runtimeTracePath, runtimeTracePathExplicit := prepareRuntimeTracePlan(req)
	cacheOptions, err := prepareAnalyseCacheOptions(req.RepoPath, req.Analyse)
	if err != nil {
		return preparedAnalyseExecution{}, err
	}
	baseRequest := analysis.Request{
		RepoPath:                 req.RepoPath,
		Dependency:               req.Analyse.Dependency,
		TopN:                     req.Analyse.TopN,
		ScopeMode:                req.Analyse.ScopeMode,
		SuggestOnly:              req.Analyse.SuggestOnly || req.Analyse.ApplyCodemod,
		Language:                 req.Analyse.Language,
		ConfigPath:               req.Analyse.ConfigPath,
		RuntimeProfile:           req.Analyse.RuntimeProfile,
		RuntimeTracePath:         runtimeTracePath,
		RuntimeTracePathExplicit: runtimeTracePathExplicit,
		RuntimeTestCommand:       strings.TrimSpace(req.Analyse.RuntimeTestCommand),
		IncludePatterns:          req.Analyse.IncludePatterns,
		ExcludePatterns:          req.Analyse.ExcludePatterns,
		Features:                 req.Analyse.Features,
		Cache:                    cacheOptions,
	}
	policy := analysisRequestPolicy{
		thresholds:              req.Analyse.Thresholds,
		advisorySourcePath:      req.Analyse.AdvisorySourcePath,
		vulnerabilityExceptions: req.Analyse.VulnerabilityExceptions,
		policySources:           req.Analyse.PolicySources,
		policyTrace:             req.Analyse.PolicyTrace,
	}
	preparedPolicy := prepareAnalysisPolicy(baseRequest, policy)

	return preparedAnalyseExecution{
		request:                 preparedPolicy.request,
		lockfileWarnings:        lockfileWarnings,
		effectiveThresholds:     preparedPolicy.effectiveThresholds,
		removalCandidateWeights: preparedPolicy.removalCandidateWeights,
		licensePolicy:           preparedPolicy.licensePolicy,
		vulnerabilityPolicy:     preparedPolicy.vulnerabilityPolicy,
		policySources:           preparedPolicy.policySources,
		policyTrace:             preparedPolicy.policyTrace,
	}, nil
}

func validateCodemodApplyPreconditions(ctx context.Context, repoPath string, req AnalyseRequest) error {
	if !req.ApplyCodemod {
		return nil
	}
	normalizedRepoPath, err := normalizeRepoPathForCodemod(repoPath)
	if err != nil {
		return err
	}
	return ensureCleanWorktreeForCodemod(ctx, normalizedRepoPath, req.AllowDirty)
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
		MergeTrace:              append([]report.PolicyMergeTrace{}, prepared.policyTrace...),
		Thresholds:              effectiveThresholds,
		RemovalCandidateWeights: prepared.removalCandidateWeights,
		License:                 licensePolicy,
		Vulnerabilities:         prepared.vulnerabilityPolicy,
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

func prepareAnalyseCacheOptions(repoPath string, req AnalyseRequest) (*analysis.CacheOptions, error) {
	cacheOptions := &analysis.CacheOptions{
		Enabled:    req.CacheEnabled,
		Path:       req.CachePath,
		ReadOnly:   req.CacheReadOnly,
		PinnedPath: req.CachePinnedPath,
	}
	if !cacheOptions.Enabled || strings.TrimSpace(cacheOptions.PinnedPath) != "" {
		return cacheOptions, nil
	}
	cachePath := strings.TrimSpace(cacheOptions.Path)
	if cachePath == "" || strings.TrimSpace(repoPath) == "" {
		return cacheOptions, nil
	}

	trustedOptions, err := analysis.ResolveTrustedCacheOptions(repoPath, cacheOptions)
	if err == nil {
		return trustedOptions, nil
	}
	if filepath.IsAbs(cachePath) {
		return cacheOptions, nil
	}
	return nil, err
}
