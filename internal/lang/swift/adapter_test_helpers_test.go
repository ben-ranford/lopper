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
	alamofireRepositoryURL      = "https://github.com/Alamofire/Alamofire.git"
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

type swiftFixtureCarthageDependency struct {
	kind      string
	source    string
	reference string
}

func alamofireFixtureDependency() swiftFixtureDependency {
	return swiftFixtureDependency{
		identity:    "alamofire",
		url:         alamofireRepositoryURL,
		version:     "5.8.0",
		productName: "Alamofire",
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
		name:    "Alamofire",
		version: "5.8.1",
	}
}

func rxSwiftCarthageFixtureDependency() swiftFixtureCarthageDependency {
	return swiftFixtureCarthageDependency{
		kind:      "github",
		source:    "ReactiveX/RxSwift",
		reference: "6.8.0",
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

func writeSwiftDemoCarthageProject(t *testing.T, repo string, dependencies []swiftFixtureCarthageDependency, mainContent string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), buildCartfileContent(dependencies))
	testutil.MustWriteFile(t, filepath.Join(repo, carthageResolvedName), buildCartfileResolvedContent(dependencies))
	writeSwiftDemoSourceFile(t, repo, mainContent)
}

func writeSwiftDemoSourceFile(t *testing.T, repo string, mainContent string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", "Demo", swiftMainFileName), mainContent)
}

func buildPodfileContent(dependencies []swiftFixturePodDependency) string {
	lines := []string{
		`platform :ios, "16.0"`,
		`target "Demo" do`,
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

func buildCartfileContent(dependencies []swiftFixtureCarthageDependency) string {
	lines := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		kind := strings.TrimSpace(dependency.kind)
		if kind == "" {
			kind = "github"
		}
		lines = append(lines, kind+` "`+dependency.source+`" "`+dependency.reference+`"`)
	}
	return strings.Join(lines, "\n")
}

func buildCartfileResolvedContent(dependencies []swiftFixtureCarthageDependency) string {
	lines := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		kind := strings.TrimSpace(dependency.kind)
		if kind == "" {
			kind = "github"
		}
		reference := strings.TrimSpace(dependency.reference)
		if reference == "" {
			reference = "1.0.0"
		}
		lines = append(lines, kind+` "`+dependency.source+`" "`+reference+`"`)
	}
	return strings.Join(lines, "\n")
}
