package app

import (
	"context"
	"errors"
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
	lockfileWarnings, err := resolvePreparedLockfileDrift(ctx, req.RepoPath, req.Analyse)
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
	if err := analysis.ValidateTrustedScopedCacheOptions(cacheOptions, req.Analyse.IncludePatterns, req.Analyse.ExcludePatterns); err != nil {
		return preparedAnalyseExecution{}, err
	}
	baseRequest := analysis.Request{
		RepoPath:                 req.RepoPath,
		Repository:               req.Analyse.repository,
		RepositoryView:           req.Analyse.repositoryView,
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

func resolvePreparedLockfileDrift(ctx context.Context, repoPath string, req AnalyseRequest) ([]string, error) {
	if req.lockfileDriftCaptured {
		return append([]string{}, req.lockfileWarnings...), req.lockfileDriftErr
	}
	if req.repositoryView != nil {
		return nil, errors.New("lockfile drift state was not captured before opening the repository view")
	}
	return evaluateLockfileDriftPolicyWithFeatures(ctx, repoPath, req.Thresholds.LockfileDriftPolicy, req.Features)
}

func validateCodemodApplyPreconditions(ctx context.Context, repoPath string, req AnalyseRequest) error {
	if !req.ApplyCodemod || req.AllowDirty {
		return nil
	}
	if req.codemodPreconditionCaptured {
		return req.codemodPrecondition
	}
	if req.repositoryView != nil {
		return errors.New("codemod cleanliness state was not captured before opening the repository view")
	}
	normalizedRepoPath, err := normalizeRepoPathForCodemod(repoPath)
	if err != nil {
		return err
	}
	return ensureCleanWorktreeForCodemod(ctx, normalizedRepoPath, false)
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
	if req.cacheOptions != nil {
		return req.cacheOptions, nil
	}
	cacheOptions := &analysis.CacheOptions{
		Enabled:  req.CacheEnabled,
		Path:     req.CachePath,
		ReadOnly: req.CacheReadOnly,
	}
	if !cacheOptions.Enabled {
		return cacheOptions, nil
	}
	cachePath := strings.TrimSpace(cacheOptions.Path)
	if cachePath == "" || strings.TrimSpace(repoPath) == "" {
		return cacheOptions, nil
	}

	var (
		trustedOptions *analysis.CacheOptions
		err            error
	)
	if req.repository != nil {
		trustedOptions, err = analysis.ResolveTrustedCacheOptionsForRepository(req.repository, cacheOptions)
	} else {
		trustedOptions, err = analysis.ResolveTrustedCacheOptions(repoPath, cacheOptions)
	}
	if err == nil {
		return trustedOptions, nil
	}
	if filepath.IsAbs(cachePath) && analysis.AuthenticatedExternalCacheOptions(trustedOptions, err) {
		return trustedOptions, nil
	}
	return nil, err
}
