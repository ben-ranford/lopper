package jvm

import (
	"context"
	"errors"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/workspace"
)

type Adapter struct {
	language.AdapterLifecycle
}

var afterJVMAnalyseRootOpen = func(string) error { return nil }

const (
	pomXMLName                   = "pom.xml"
	buildGradleName              = "build.gradle"
	buildGradleKTSName           = "build.gradle.kts"
	maxScannableJVMBuildFile     = shared.GradleManifestByteLimit
	maxScannableJVMSourceFile    = 2 * 1024 * 1024
	maxJVMBuildTraversalEntries  = 8192
	maxJVMBuildFiles             = 2048
	maxJVMBuildWorkItems         = 2048
	maxJVMSourceTraversalEntries = 32768
	maxJVMSourceFiles            = 8192
	maxJVMSourceWorkItems        = 8192
)

var jvmSkippedDirectories = map[string]bool{
	"target":     true,
	".gradle":    true,
	".mvn":       true,
	"out":        true,
	".classpath": true,
	".settings":  true,
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("jvm", []string{"java", "kotlin"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (result report.Result, err error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}
	root, err := safeio.OpenRootNoFollow(repoPath)
	if err != nil {
		return report.Report{}, err
	}
	defer func() {
		err = errors.Join(err, root.Close())
	}()

	if err := afterJVMAnalyseRootOpen(repoPath); err != nil {
		return report.Report{}, err
	}

	result = report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}

	declaredDependencies, depPrefixes, depAliases, declarationWarnings, err := collectDeclaredDependenciesWithinRoot(ctx, repoPath, root)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, declarationWarnings...)
	scanResult, err := scanRepoWithinRoot(ctx, repoPath, root, depPrefixes, depAliases)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	dependencies, warnings := buildRequestedJVMDependencies(req, scanResult)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	if len(declaredDependencies) == 0 {
		result.Warnings = append(result.Warnings, "no JVM dependencies discovered from pom.xml or build.gradle manifests")
	}

	return result, nil
}

func normalizeDependencyID(value string) string {
	return shared.NormalizeDependencyID(value)
}

func shouldSkipDir(name string) bool {
	return shared.ShouldSkipDir(name, jvmSkippedDirectories)
}
