package rust

import (
	"context"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

type Adapter struct {
	language.AdapterLifecycle
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle(rustAdapterID, []string{"rs", "cargo"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Result, error) {
	repoPath, result, err := shared.NewReport(req.RepoPath, a.Clock)
	if err != nil {
		return report.Report{}, err
	}

	manifestPaths, sourceFallbackRoot, excludedSourceRoots, depLookup, renamedAliases, warnings, coverageGaps, err := collectManifestDataWithCoverage(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, warnings...)

	scan, err := scanRepoWithFallback(ctx, repoPath, manifestPaths, sourceFallbackRoot, excludedSourceRoots, depLookup, renamedAliases)
	if err != nil {
		return report.Report{}, err
	}
	scan.CoverageGaps = append(scan.CoverageGaps, coverageGaps...)
	result.Warnings = append(result.Warnings, scan.Warnings...)
	result.CoverageGaps = append(result.CoverageGaps, scan.CoverageGaps...)

	dependencies, dependencyWarnings := buildRequestedRustDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, dependencyWarnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}
