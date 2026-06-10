package js

import (
	"context"
	"fmt"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("js-ts", []string{"js", "ts", "javascript", "typescript"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Result, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	result := report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}

	scanResult, err := ScanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.UsageUncertainty = summarizeUsageUncertainty(scanResult)
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	switch {
	case req.Dependency != "":
		resolvedRoots := resolveDependencyRootsFromScan(repoPath, req.Dependency, scanResult)
		dependencyRootPath := firstResolvedDependencyRoot(resolvedRoots)
		depReport, warnings := buildDependencyReport(dependencyReportOptions{
			RepoPath:                          repoPath,
			Dependency:                        req.Dependency,
			DependencyRootPath:                dependencyRootPath,
			ScanResult:                        scanResult,
			RuntimeProfile:                    req.RuntimeProfile,
			MinUsagePercentForRecommendations: resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations),
			SuggestOnly:                       req.SuggestOnly,
			IncludeRegistryProvenance:         req.IncludeRegistryProvenance,
		})
		result.Dependencies = []report.DependencyReport{depReport}
		result.Warnings = append(result.Warnings, warnings...)
		if len(resolvedRoots) > 1 {
			result.Warnings = append(result.Warnings, fmt.Sprintf("dependency resolves to multiple node_modules roots: %s", req.Dependency))
		}
		result.Summary = report.ComputeSummary(result.Dependencies)
	case req.TopN > 0:
		deps, warnings := buildTopDependencies(repoPath, scanResult, req.TopN, req.RuntimeProfile, resolveMinUsageRecommendationThreshold(req.MinUsagePercentForRecommendations), resolveRemovalCandidateWeights(req.RemovalCandidateWeights), req.IncludeRegistryProvenance)
		result.Dependencies = deps
		result.Warnings = append(result.Warnings, warnings...)
		if len(deps) == 0 {
			result.Warnings = append(result.Warnings, "no dependency data available for top-N ranking")
		}
		result.Summary = report.ComputeSummary(result.Dependencies)
	default:
		result.Warnings = append(result.Warnings, "no dependency or top-N target provided")
	}

	return result, nil
}
