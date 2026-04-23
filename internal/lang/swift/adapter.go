package swift

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
	adapter.AdapterLifecycle = language.NewAdapterLifecycle(swiftAdapterID, []string{"swiftpm"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, result, err := shared.NewReport(req.RepoPath, a.Clock)
	if err != nil {
		return report.Report{}, err
	}

	catalogOptions := dependencyCatalogOptions{
		EnableCarthage: req.Features.Enabled(swiftCarthagePreviewFlagName),
	}
	catalog, catalogWarnings, err := buildDependencyCatalogWithOptions(repoPath, catalogOptions)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, catalogWarnings...)

	scan, err := scanRepo(ctx, repoPath, catalog)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedSwiftDependencies(req, scan, catalog)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}
