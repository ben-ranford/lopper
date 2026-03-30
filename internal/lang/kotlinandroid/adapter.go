package kotlinandroid

import (
	"context"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

type Adapter struct {
	language.AdapterLifecycle
}

const (
	buildGradleName     = "build.gradle"
	buildGradleKTSName  = "build.gradle.kts"
	settingsGradleName  = "settings.gradle"
	settingsGradleKTS   = "settings.gradle.kts"
	gradleLockfileName  = "gradle.lockfile"
	androidManifestName = "androidmanifest.xml"
)

var kotlinAndroidSkippedDirectories = map[string]bool{
	".gradle":    true,
	"build":      true,
	"out":        true,
	"target":     true,
	".classpath": true,
	".settings":  true,
}

var androidBuildPluginMarkers = []string{
	"com.android.application",
	"com.android.dynamic-feature",
	"com.android.library",
	"com.android.test",
	"org.jetbrains.kotlin.android",
}

func NewAdapter() *Adapter {
	adapter := &Adapter{}
	adapter.AdapterLifecycle = language.NewAdapterLifecycle("kotlin-android", []string{"android-kotlin", "gradle-android", "android"}, adapter.DetectWithConfidence)
	return adapter
}

func (a *Adapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	repoPath, result, err := shared.NewReport(req.RepoPath, a.Clock)
	if err != nil {
		return report.Report{}, err
	}

	descriptors, lookups, declarationWarnings := collectDeclaredDependencies(repoPath)
	result.Warnings = append(result.Warnings, declarationWarnings...)

	scanResult, err := scanRepo(ctx, repoPath, lookups)
	if err != nil {
		return report.Report{}, err
	}
	result.Warnings = append(result.Warnings, scanResult.Warnings...)

	dependencies, warnings := buildRequestedKotlinAndroidDependencies(req, scanResult)
	result.Dependencies = dependencies
	result.Warnings = append(result.Warnings, warnings...)
	result.Summary = report.ComputeSummary(result.Dependencies)

	if len(descriptors) == 0 {
		result.Warnings = append(result.Warnings, "no Kotlin/Android dependencies discovered from Gradle manifests")
	}
	if !lookups.HasLockfile {
		result.Warnings = append(result.Warnings, "gradle.lockfile not found; dependency versions may be incomplete")
	}

	return result, nil
}

func normalizeDependencyID(value string) string {
	return shared.NormalizeDependencyID(value)
}

func shouldSkipDir(name string) bool {
	return shared.ShouldSkipDir(strings.ToLower(name), kotlinAndroidSkippedDirectories)
}
