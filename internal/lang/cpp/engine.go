package cpp

import (
	"context"
	"io/fs"
	"path/filepath"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

const (
	compileCommandsFile = "compile_commands.json"
	cmakeListsFile      = "CMakeLists.txt"
	maxDetectFiles      = 2048
)

func (a *Adapter) DetectWithConfidence(ctx context.Context, repoPath string) (language.Detection, error) {
	repoPath = shared.DefaultRepoPath(repoPath)
	detection := language.Detection{}
	roots := make(map[string]struct{})

	if err := shared.ApplyRootSignals(repoPath, cppRootSignals, &detection, roots); err != nil {
		return language.Detection{}, err
	}

	err := shared.WalkRepoFiles(ctx, repoPath, maxDetectFiles, shared.ShouldSkipCommonDir, func(path string, entry fs.DirEntry) error {
		updateDetection(path, &detection, roots)
		return nil
	})
	if err != nil {
		return language.Detection{}, err
	}

	return shared.FinalizeDetection(repoPath, detection, roots), nil
}

func updateDetection(path string, detection *language.Detection, roots map[string]struct{}) {
	switch filepath.Base(path) {
	case compileCommandsFile:
		markDetection(detection, roots, filepath.Dir(path), 20)
	case cmakeListsFile:
		markDetection(detection, roots, filepath.Dir(path), 12)
	case "Makefile", "makefile", "GNUmakefile":
		markDetection(detection, roots, filepath.Dir(path), 10)
	case vcpkgManifestFile, conanManifestFile:
		markDetection(detection, roots, filepath.Dir(path), 12)
	case vcpkgLockFile, conanLockFile:
		markDetection(detection, roots, filepath.Dir(path), 8)
	}

	if isCPPSourceOrHeader(path) {
		markDetection(detection, roots, "", 2)
	}
}

func markDetection(detection *language.Detection, roots map[string]struct{}, root string, confidence int) {
	detection.Matched = true
	detection.Confidence += confidence
	if root != "" {
		roots[root] = struct{}{}
	}
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

var cppRootSignals = []shared.RootSignal{
	{Name: compileCommandsFile, Confidence: 60},
	{Name: cmakeListsFile, Confidence: 45},
	{Name: "Makefile", Confidence: 35},
	{Name: "makefile", Confidence: 35},
	{Name: "GNUmakefile", Confidence: 35},
	{Name: vcpkgManifestFile, Confidence: 35},
	{Name: vcpkgLockFile, Confidence: 20},
	{Name: conanManifestFile, Confidence: 35},
	{Name: conanLockFile, Confidence: 20},
}
