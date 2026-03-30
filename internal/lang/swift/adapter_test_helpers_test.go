package swift

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	swiftNIORepositoryURL       = "https://github.com/apple/swift-nio.git"
	swiftBuildDirName           = ".build"
	swiftMainFileName           = "main.swift"
	swiftNIOID                  = "swift-nio"
	expectedOneDependencyReport = "expected one dependency report, got %d"
	analyseErrorFormat          = "analyse: %v"
)

type swiftFixturePodDependency struct {
	name    string
	version string
}

func alamofireFixtureDependency() swiftFixtureDependency {
	return swiftFixtureDependency{
		identity:    alamofireFixtureName,
		url:         alamofireRepositoryURL,
		version:     alamofireVersion,
		productName: alamofireProductName,
	}
}

func swiftNIOFixtureDependency() swiftFixtureDependency {
	return swiftFixtureDependency{
		identity:     swiftNIOID,
		manifestName: swiftNIOID,
		url:          swiftNIORepositoryURL,
		version:      "2.60.0",
		productName:  "NIO",
	}
}

func alamofirePodFixtureDependency() swiftFixturePodDependency {
	return swiftFixturePodDependency{
		name:    alamofireProductName,
		version: alamofirePodVersion,
	}
}

func mustAnalyseSwiftRequest(t *testing.T, req language.Request) report.Report {
	t.Helper()

	reportData, err := NewAdapter().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf(analyseErrorFormat, err)
	}
	return reportData
}

func mustSingleSwiftDependencyReport(t *testing.T, req language.Request) report.DependencyReport {
	t.Helper()

	reportData := mustAnalyseSwiftRequest(t, req)
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(reportData.Dependencies))
	}
	return reportData.Dependencies[0]
}

func writeSwiftDemoPackage(t *testing.T, repo string, dependencies []swiftFixtureDependency, mainContent string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), buildSwiftManifestContent(dependencies))
	testutil.MustWriteFile(t, filepath.Join(repo, packageResolvedName), buildSwiftResolvedContent(dependencies))
	writeSwiftDemoSourceFile(t, repo, mainContent)
}

func writeSwiftDemoCocoaPodsProject(t *testing.T, repo string, dependencies []swiftFixturePodDependency, mainContent string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), buildPodfileContent(dependencies))
	testutil.MustWriteFile(t, filepath.Join(repo, podLockName), buildPodLockContent(dependencies))
	writeSwiftDemoSourceFile(t, repo, mainContent)
}

func buildPodfileContent(dependencies []swiftFixturePodDependency) string {
	lines := []string{
		`platform :ios, "` + swiftPodfilePlatformVersion + `"`,
		`target "` + swiftDemoPackageName + `" do`,
	}
	for _, dependency := range dependencies {
		lines = append(lines, `  pod "`+dependency.name+`", "`+dependency.version+`"`)
	}
	lines = append(lines, "end")
	return strings.Join(lines, "\n")
}

func buildPodLockContent(dependencies []swiftFixturePodDependency) string {
	lines := []string{"PODS:"}
	for _, dependency := range dependencies {
		lines = append(lines, "  - "+dependency.name+" ("+dependency.version+")")
	}
	lines = append(lines, "DEPENDENCIES:")
	for _, dependency := range dependencies {
		lines = append(lines, "  - "+dependency.name+" ("+dependency.version+")")
	}
	lines = append(lines, `COCOAPODS: 1.13.0`)
	return strings.Join(lines, "\n")
}
