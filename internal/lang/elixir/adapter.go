package elixir

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
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("elixir", []string{"ex", "mix"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}
	declared, err := loadDeclaredDependencies(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	scan, err := scanElixirRepo(ctx, repoPath, declared)
	if err != nil {
		return report.Report{}, err
	}
	dependencies, warnings := buildRequestedDependencies(req, scan)
	return report.Report{
		GeneratedAt:   a.Clock(),
		RepoPath:      repoPath,
		Dependencies:  dependencies,
		Warnings:      warnings,
		Summary:       report.ComputeSummary(dependencies),
		SchemaVersion: report.SchemaVersion,
	}, nil
}
