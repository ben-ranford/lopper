package dart

import (
	"context"
	"time"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

func NewAdapter() *Adapter {
	return &Adapter{Clock: time.Now}
}

func (a *Adapter) ID() string {
	return "dart"
}

func (a *Adapter) Aliases() []string {
	return []string{"flutter", "pub"}
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, result, err := a.newReport(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	scan, err := a.scanRepo(ctx, repoPath, &result)
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
	repoPath, err := workspace.NormalizeRepoPath(rawRepoPath)
	if err != nil {
		return "", report.Report{}, err
	}

	return repoPath, report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}, nil
}

func (a *Adapter) scanRepo(ctx context.Context, repoPath string, result *report.Report) (scanResult, error) {
	manifests, warnings, err := collectManifestData(repoPath)
	if err != nil {
		return scanResult{}, err
	}
	result.Warnings = append(result.Warnings, warnings...)

	scan, err := scanRepo(ctx, repoPath, manifests)
	if err != nil {
		return scanResult{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)
	return scan, nil
}
