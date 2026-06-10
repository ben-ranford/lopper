package jvm

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
	pomXMLName         = "pom.xml"
	buildGradleName    = "build.gradle"
	buildGradleKTSName = "build.gradle.kts"
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

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, err := workspace.NormalizeRepoPath(req.RepoPath)
	if err != nil {
		return report.Report{}, err
	}

	result := report.Report{
		GeneratedAt: a.Clock(),
		RepoPath:    repoPath,
	}

	declaredDependencies, depPrefixes, depAliases, declarationWarnings := collectDeclaredDependencies(repoPath)
	result.Warnings = append(result.Warnings, declarationWarnings...)
	scanResult, err := scanRepo(ctx, repoPath, depPrefixes, depAliases)
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
