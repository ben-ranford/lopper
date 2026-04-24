package dart

import (
	"context"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

var dartAliases = []string{"flutter", "pub"}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("dart", dartAliases, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, result, err := a.newReport(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	previewEnabled := req.Features.Enabled(dartSourceAttributionPreviewFeature)
	scan, err := a.scanRepoWithOptions(ctx, repoPath, &result, previewEnabled)
	if err != nil {
		return report.Report{}, err
	}

	dependencies, dependencyWarnings := buildRequestedDartDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, dependencyWarnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}

func (a *Adapter) newReport(rawRepoPath string) (string, report.Report, error) {
	return shared.NewReport(rawRepoPath, a.Clock)
}

func (a *Adapter) scanRepo(ctx context.Context, repoPath string, result *report.Report) (scanResult, error) {
	return a.scanRepoWithOptions(ctx, repoPath, result, false)
}

func (a *Adapter) scanRepoWithOptions(ctx context.Context, repoPath string, result *report.Report, includeLocalPathImports bool) (scanResult, error) {
	manifests, warnings, err := collectManifestData(repoPath)
	if err != nil {
		return scanResult{}, err
	}
	result.Warnings = append(result.Warnings, warnings...)

	scan, err := scanRepoWithOptions(ctx, repoPath, manifests, includeLocalPathImports)
	if err != nil {
		return scanResult{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)
	return scan, nil
}
