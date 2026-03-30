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

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, result, err := shared.NewReport(req.RepoPath, a.Clock)
	if err != nil {
		return report.Report{}, err
	}

	manifestPaths, depLookup, renamedAliases, warnings, err := collectManifestData(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, warnings...)

	scan, err := scanRepo(ctx, repoPath, manifestPaths, depLookup, renamedAliases)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, dependencyWarnings := buildRequestedRustDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, dependencyWarnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}
