package ruby

import (
	"context"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

const rubyAdapterID = "ruby"

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle(rubyAdapterID, []string{"rb"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	scan, err := scanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}

	dependencies, warnings := buildRequestedRubyDependencies(req, scan)
	result := report.Report{
		GeneratedAt:  a.Clock(),
		RepoPath:     repoPath,
		Dependencies: dependencies,
		Warnings:     append(scan.Warnings, warnings...),
	}
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result, nil
}
