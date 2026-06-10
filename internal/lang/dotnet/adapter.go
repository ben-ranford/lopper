package dotnet

import (
	"context"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

const (
	centralPackagesFile = "Directory.Packages.props"
	csharpProjectExt    = ".csproj"
	fsharpProjectExt    = ".fsproj"
	solutionFileExt     = ".sln"
	csharpSourceExt     = ".cs"
	fsharpSourceExt     = ".fs"
	maxDetectFiles      = 1024
	maxScanFiles        = 4096
)

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("dotnet", []string{"csharp", "cs", "fsharp", "fs"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	result := report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}

	scan, err := scanRepo(ctx, repoPath)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scan.Warnings...)

	dependencies, warnings := buildRequestedDotNetDependencies(req, scan)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	if len(scan.DeclaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no .NET package dependencies discovered from project manifests")
	}
	return result, nil
}

type importBinding = shared.ImportRecord

type fileScan struct {
	Path    string
	Imports []importBinding
	Usage   map[string]int
}

type scanResult struct {
	Files                  []fileScan
	DeclaredDependencies   []string
	Warnings               []string
	AmbiguousByDependency  map[string]int
	UndeclaredByDependency map[string]int
	SkippedGeneratedFiles  int
	SkippedFileLimit       bool
}
