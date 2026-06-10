package cpp

import (
	"context"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("cpp", []string{"c++", "c", "cc", "cxx"}, adapter.DetectWithConfidence)
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

	compileInfo, err := loadCompileContext(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, compileInfo.Warnings...)

	catalog, catalogWarnings, err := loadDependencyCatalog(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, catalogWarnings...)

	scan, err := scanRepo(ctx, repoPath, compileInfo, catalog)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedCPPDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}
