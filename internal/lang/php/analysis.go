package php

import (
	"context"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type analysisPipelineState struct {
	repoPath string
	composer composerData
	scan     scanResult
	warnings []string
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	state := analysisPipelineState{repoPath: repoPath}
	if err := runComposerIngestionStage(&state); err != nil {
		return report.Report{}, err
	}
	if err := runPHPScanStage(ctx, &state); err != nil {
		return report.Report{}, err
	}
	return a.runPHPReportAssemblyStage(req, state), nil
}

func runComposerIngestionStage(state *analysisPipelineState) error {
	composerData, warnings, err := loadComposerData(state.repoPath)
	if err != nil {
		return err
	}
	state.composer = composerData
	state.warnings = append(state.warnings, warnings...)
	return nil
}

func runPHPScanStage(ctx context.Context, state *analysisPipelineState) error {
	scan, err := scanRepo(ctx, state.repoPath, state.composer)
	if err != nil {
		return err
	}
	state.scan = scan
	state.warnings = append(state.warnings, scan.Warnings...)
	return nil
}

func (a *Adapter) runPHPReportAssemblyStage(req language.Request, state analysisPipelineState) report.Report {
	result := report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    state.repoPath,
		Warnings:    append([]string(nil), state.warnings...),
	}

	dependencies, warnings := buildRequestedPHPDependencies(req, state.scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)
	return result
}
