package analysis

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	programFileName           = "Program.cs"
	packageJSONFileName       = "package.json"
	indexJSFileName           = "index.js"
	buildGradleFileName       = "build.gradle"
	demoPackageJSONContent    = "{\n  \"name\": \"demo\"\n}\n"
	lodashMapUsageJS          = "import { map } from \"lodash\"\nmap([1], (x) => x)\n"
	nodeMainPackageJSON       = "{\n  \"main\": \"index.js\"\n}\n"
	mapExportJSContent        = "export function map() {}\n"
	leftPadDependencyID       = "left-pad"
	newtonsoftDependencyID    = "newtonsoft.json"
	kotlinAndroidLanguageID   = "kotlin-android"
	expectedOneDependencyText = "expected one dependency report, got %d"
)

func TestServiceAnalyseAllLanguages(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, packageJSONFileName), demoPackageJSONContent)
	writeFile(t, filepath.Join(repo, indexJSFileName), lodashMapUsageJS)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", packageJSONFileName), nodeMainPackageJSON)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", indexJSFileName), mapExportJSContent)
	writeFile(t, filepath.Join(repo, "main.py"), "import requests\nrequests.get('https://example.test')\n")
	writeFile(t, filepath.Join(repo, buildGradleFileName), "dependencies { implementation libs.junit.jupiter }\n")
	writeFile(t, filepath.Join(repo, "gradle", "libs.versions.toml"), "[libraries]\njunit-jupiter = { module = \"org.junit.jupiter:junit-jupiter-api\", version = \"5.10.0\" }\n")
	writeFile(t, filepath.Join(repo, "src", "main", "AndroidManifest.xml"), "<manifest package=\"example.demo\"/>\n")
	writeFile(t, filepath.Join(repo, "src", "test", "java", "ExampleTest.java"), "import org.junit.jupiter.api.Test;\nclass ExampleTest {}\n")
	writeFile(t, filepath.Join(repo, "go.mod"), "module example.com/demo\n\nrequire github.com/google/uuid v1.6.0\n")
	writeFile(t, filepath.Join(repo, "main.go"), "package main\n\nimport \"github.com/google/uuid\"\n\nfunc main() { _ = uuid.NewString() }\n")
	writeFile(t, filepath.Join(repo, "composer.json"), "{\n  \"require\": {\n    \"monolog/monolog\": \"^3.0\"\n  }\n}\n")
	writeFile(t, filepath.Join(repo, "composer.lock"), "{\n  \"packages\": [\n    {\n      \"name\": \"monolog/monolog\",\n      \"autoload\": {\n        \"psr-4\": {\n          \"Monolog\\\\\": \"src/Monolog\"\n        }\n      }\n    }\n  ]\n}\n")
	writeFile(t, filepath.Join(repo, "index.php"), "<?php\nuse Monolog\\Logger;\n$logger = new Logger(\"app\");\n")
	writeFile(t, filepath.Join(repo, "Cargo.toml"), "[package]\nname = \"demo\"\nversion = \"0.1.0\"\n\n[dependencies]\nanyhow = \"1.0\"\n")
	writeFile(t, filepath.Join(repo, "src", "lib.rs"), "use anyhow::Result;\npub fn run() -> Result<()> { Ok(()) }\n")
	writeFile(t, filepath.Join(repo, "Gemfile"), "source 'https://rubygems.org'\ngem 'httparty'\n")
	writeFile(t, filepath.Join(repo, "app.rb"), "require 'httparty'\n")
	writeFile(t, filepath.Join(repo, "App.csproj"), "<Project Sdk=\"Microsoft.NET.Sdk\"><ItemGroup><PackageReference Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n")
	writeFile(t, filepath.Join(repo, programFileName), "using JsonConvert = Newtonsoft.Json.JsonConvert;\npublic class Program { public static void Main() { _ = JsonConvert.SerializeObject(new { V = 1 }); } }\n")
	writeFile(t, filepath.Join(repo, "src", "native", "main.cpp"), "#include <openssl/ssl.h>\nint main() { return 0; }\n")
	writeFile(t, filepath.Join(repo, "mix.exs"), "defmodule Demo.MixProject do\n  use Mix.Project\n  def project, do: [app: :demo, version: \"0.1.0\", deps: deps()]\n  defp deps, do: [{:jason, \"~> 1.4\"}]\nend\n")
	writeFile(t, filepath.Join(repo, "mix.lock"), "%{\n  \"jason\": {:hex, :jason, \"1.4.1\", \"checksum\", [:mix], [], \"hexpm\", \"checksum\"}\n}\n")
	writeFile(t, filepath.Join(repo, "lib", "demo.ex"), "defmodule Demo do\n  alias Jason\n  def run(v), do: Jason.decode!(v)\nend\n")
	writeFile(t, filepath.Join(repo, "pubspec.yaml"), "name: demo\ndependencies:\n  http: ^1.0.0\n")
	writeFile(t, filepath.Join(repo, "pubspec.lock"), "packages:\n  http:\n    dependency: \"direct main\"\n    description: {name: http}\n    source: hosted\n    version: \"1.0.0\"\n")
	writeFile(t, filepath.Join(repo, "lib", "main.dart"), "import 'package:http/http.dart' as http;\nvoid main() { http.Client(); }\n")

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath: repo,
		TopN:     10,
		Language: "all",
	})
	if err != nil {
		t.Fatalf("analyse all: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in report")
	}
	languages := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		languages = append(languages, dep.Language)
	}
	expectedLanguages := []string{"js-ts", "python", "cpp", "jvm", kotlinAndroidLanguageID, "go", "php", "ruby", "rust", "dotnet", "elixir", "dart"}
	for _, expectedLanguage := range expectedLanguages {
		if !slices.Contains(languages, expectedLanguage) {
			t.Fatalf("expected language %q in dependencies, got %#v", expectedLanguage, languages)
		}
	}
	if len(reportData.LanguageBreakdown) < len(expectedLanguages) {
		t.Fatalf("expected language breakdown for multiple adapters, got %#v", reportData.LanguageBreakdown)
	}
	if reportData.Scope == nil || reportData.Scope.Mode != ScopeModePackage || len(reportData.Scope.Packages) == 0 {
		t.Fatalf("expected scope metadata with analyzed packages, got %#v", reportData.Scope)
	}
}

func TestServiceAnalyseScopedDependencyIdentityUsesScopedRepoPath(t *testing.T) {
	repo := t.TempDir()
	writeScopedGoIdentityFixture(t, repo)

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:        repo,
		Dependency:      "github.com/google/uuid",
		Language:        "go",
		IncludePatterns: []string{"services/included/**", "services/excluded/**"},
		ExcludePatterns: []string{"services/excluded/**"},
		Features:        mustResolveDependencyIdentityPreviewFeatureSet(t),
	})
	if err != nil {
		t.Fatalf("analyse scoped identity fixture: %v", err)
	}
	if reportData.RepoPath != repo {
		t.Fatalf("expected original repo path preserved, got %q want %q", reportData.RepoPath, repo)
	}
	if reportData.Scope == nil || len(reportData.Scope.Packages) != 1 || reportData.Scope.Packages[0] != "services/included" {
		t.Fatalf("expected scoped analyzed root remapped to included package, got %#v", reportData.Scope)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(reportData.Dependencies))
	}
	assertIdentity(t, findIdentityDependency(t, reportData, "go", "github.com/google/uuid"), report.DependencyIdentity{
		Ecosystem: "golang", Name: "github.com/google/uuid", Version: "v1.6.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:golang/github.com/google/uuid@v1.6.0", PURLStatus: identityStatusResolved, Source: "services/included/go.mod", Confidence: "high",
	})
	for _, warning := range reportData.Warnings {
		if strings.Contains(warning, "v9.9.9") {
			t.Fatalf("expected scoped warnings to exclude excluded manifest version, got %#v", reportData.Warnings)
		}
	}
}

func TestServiceAnalyseDependencyIdentityUnscopedStillUsesFullRepo(t *testing.T) {
	repo := t.TempDir()
	writeScopedGoIdentityFixture(t, repo)

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "github.com/google/uuid",
		Language:   "go",
		Features:   mustResolveDependencyIdentityPreviewFeatureSet(t),
	})
	if err != nil {
		t.Fatalf("analyse unscoped identity fixture: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(reportData.Dependencies))
	}
	dependency := findIdentityDependency(t, reportData, "go", "github.com/google/uuid")
	if dependency.Identity.VersionStatus != identityStatusConflicting || dependency.Identity.PURLStatus != identityPURLUnavailable || dependency.Identity.Version != "" {
		t.Fatalf("expected unscoped full-repo identity conflict, got %#v", dependency.Identity)
	}
	if !slices.Equal(dependency.Identity.Conflicts, []string{
		"v1.6.0 from services/included/go.mod",
		"v9.9.9 from services/excluded/go.mod",
	}) {
		t.Fatalf("unexpected unscoped identity conflicts: %#v", dependency.Identity.Conflicts)
	}
}

func writeScopedGoIdentityFixture(t *testing.T, repo string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, "services", "included", "go.mod"), "module example.com/included\n\nrequire github.com/google/uuid v1.6.0\n")
	writeFile(t, filepath.Join(repo, "services", "included", "main.go"), "package main\n\nimport \"github.com/google/uuid\"\n\nfunc main() { _ = uuid.NewString() }\n")
	writeFile(t, filepath.Join(repo, "services", "excluded", "go.mod"), "module example.com/excluded\n\nrequire github.com/google/uuid v9.9.9\n")
	writeFile(t, filepath.Join(repo, "services", "excluded", "main.go"), "package main\n\nimport \"github.com/google/uuid\"\n\nfunc main() { _ = uuid.NewString() }\n")
}

func TestServiceAnalyseKotlinAndroidPackageScopeAvoidsRootOverlapDoubleCount(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "settings.gradle"), "rootProject.name = 'demo'\ninclude ':app'\n")
	writeFile(t, filepath.Join(repo, buildGradleFileName), "plugins { id 'com.android.application' version '8.5.0' apply false }\n")
	writeFile(t, filepath.Join(repo, "app", buildGradleFileName), "dependencies { implementation 'androidx.core:core-ktx:1.13.1' }\n")
	writeFile(t, filepath.Join(repo, "app", "src", "main", "kotlin", "com", "example", "Main.kt"), `
package com.example

import androidx.core.view.isVisible

class Main {
  fun x(v: android.view.View) {
    v.isVisible = true
  }
}
`)

	service := NewService()
	repoReport, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "core-ktx",
		Language:   kotlinAndroidLanguageID,
		ScopeMode:  ScopeModeRepo,
	})
	if err != nil {
		t.Fatalf("analyse repo scope: %v", err)
	}
	packageReport, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "core-ktx",
		Language:   kotlinAndroidLanguageID,
		ScopeMode:  ScopeModePackage,
	})
	if err != nil {
		t.Fatalf("analyse package scope: %v", err)
	}

	if packageReport.Scope == nil || len(packageReport.Scope.Packages) != 1 || packageReport.Scope.Packages[0] != "app" {
		t.Fatalf("expected package scope to include only app root, got %#v", packageReport.Scope)
	}
	if len(repoReport.Dependencies) != 1 || len(packageReport.Dependencies) != 1 {
		t.Fatalf("expected one dependency in each scope, repo=%d package=%d", len(repoReport.Dependencies), len(packageReport.Dependencies))
	}
	repoDep := repoReport.Dependencies[0]
	packageDep := packageReport.Dependencies[0]
	if packageDep.UsedExportsCount != repoDep.UsedExportsCount {
		t.Fatalf("expected package scope used exports to match repo scope after root pruning, repo=%d package=%d", repoDep.UsedExportsCount, packageDep.UsedExportsCount)
	}
	if len(packageDep.UsedImports) == 0 || len(repoDep.UsedImports) == 0 {
		t.Fatalf("expected import usage evidence in both scopes")
	}
	if len(packageDep.UsedImports[0].Locations) != len(repoDep.UsedImports[0].Locations) {
		t.Fatalf("expected package scope import locations to match repo scope after root pruning, repo=%d package=%d", len(repoDep.UsedImports[0].Locations), len(packageDep.UsedImports[0].Locations))
	}

	lockWarning := "gradle.lockfile not found; dependency versions may be incomplete"
	warningCount := 0
	for _, warning := range packageReport.Warnings {
		if warning == lockWarning {
			warningCount++
		}
	}
	if warningCount != 1 {
		t.Fatalf("expected one missing lockfile warning in package scope, got %d warnings: %#v", warningCount, packageReport.Warnings)
	}
}

func TestServiceAnalyseElixirFixtureLanguage(t *testing.T) {
	repo := filepath.Join("..", "..", "testdata", "elixir", "mix")
	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "jason",
		Language:   "elixir",
	})
	if err != nil {
		t.Fatalf("analyse elixir fixture: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(reportData.Dependencies))
	}
	if reportData.Dependencies[0].Language != "elixir" {
		t.Fatalf("expected language elixir, got %q", reportData.Dependencies[0].Language)
	}
}

func TestServiceAnalyseAllLanguagesElixirFixture(t *testing.T) {
	repo := filepath.Join("..", "..", "testdata", "elixir", "mix")
	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath: repo,
		TopN:     10,
		Language: "all",
	})
	if err != nil {
		t.Fatalf("analyse all fixture: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in all-mode report")
	}
	if reportData.Dependencies[0].Language != "elixir" {
		t.Fatalf("expected elixir language row, got %#v", reportData.Dependencies)
	}
}

func TestServiceAnalyseSwiftCocoaPodsAutoAndAllModes(t *testing.T) {
	t.Run("auto", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, "Podfile"), "platform :ios, '16.0'\ntarget 'Demo' do\n  pod 'Alamofire', '5.8.1'\nend\n")
		writeFile(t, filepath.Join(repo, "Podfile.lock"), "PODS:\n  - Alamofire (5.8.1)\nDEPENDENCIES:\n  - Alamofire (5.8.1)\nCOCOAPODS: 1.13.0\n")
		writeFile(t, filepath.Join(repo, "Sources", "App", "main.swift"), "import Alamofire\nlet value = Session.default\n")

		service := NewService()
		reportData, err := service.Analyse(context.Background(), Request{
			RepoPath:   repo,
			Dependency: "alamofire",
			Language:   "auto",
		})
		if err != nil {
			t.Fatalf("analyse swift CocoaPods auto: %v", err)
		}
		if len(reportData.Dependencies) != 1 {
			t.Fatalf(expectedOneDependencyText, len(reportData.Dependencies))
		}
		if reportData.Dependencies[0].Language != "swift" {
			t.Fatalf("expected swift dependency in auto mode, got %#v", reportData.Dependencies)
		}
	})

	t.Run("all mixed languages", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, packageJSONFileName), demoPackageJSONContent)
		writeFile(t, filepath.Join(repo, indexJSFileName), lodashMapUsageJS)
		writeFile(t, filepath.Join(repo, "node_modules", "lodash", packageJSONFileName), nodeMainPackageJSON)
		writeFile(t, filepath.Join(repo, "node_modules", "lodash", indexJSFileName), mapExportJSContent)
		writeFile(t, filepath.Join(repo, "Podfile"), "platform :ios, '16.0'\ntarget 'Demo' do\n  pod 'Alamofire', '5.8.1'\nend\n")
		writeFile(t, filepath.Join(repo, "Podfile.lock"), "PODS:\n  - Alamofire (5.8.1)\nDEPENDENCIES:\n  - Alamofire (5.8.1)\nCOCOAPODS: 1.13.0\n")
		writeFile(t, filepath.Join(repo, "Sources", "App", "main.swift"), "import Alamofire\nlet value = Session.default\n")

		service := NewService()
		reportData, err := service.Analyse(context.Background(), Request{
			RepoPath: repo,
			TopN:     10,
			Language: "all",
		})
		if err != nil {
			t.Fatalf("analyse all mixed Swift CocoaPods repo: %v", err)
		}
		languages := make([]string, 0, len(reportData.Dependencies))
		for _, dep := range reportData.Dependencies {
			languages = append(languages, dep.Language)
		}
		if !slices.Contains(languages, "swift") {
			t.Fatalf("expected swift results in all-mode report, got %#v", reportData.Dependencies)
		}
		if !slices.Contains(languages, "js-ts") {
			t.Fatalf("expected js-ts results in all-mode report, got %#v", reportData.Dependencies)
		}
	})
}

func TestServiceAnalyseSwiftCarthageAutoModeBehindPreviewFlag(t *testing.T) {
	repo := t.TempDir()
	writeSwiftCarthageAnalysisFixture(t, repo)
	service := NewService()

	withoutFlag, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "rxswift",
		Language:   "auto",
	})
	if err != nil {
		t.Fatalf("analyse swift Carthage auto without flag: %v", err)
	}
	if dep := singleDependencyReport(t, withoutFlag); dep.TotalExportsCount != 0 {
		t.Fatalf("expected preview-off analysis to avoid Carthage attribution, got %#v", dep)
	}

	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "rxswift",
		Language:   "auto",
		Features:   mustResolveSwiftCarthagePreviewSet(t, true),
	})
	if err != nil {
		t.Fatalf("analyse swift Carthage auto with flag: %v", err)
	}
	dep := singleDependencyReport(t, reportData)
	if dep.Language != "swift" || dep.TotalExportsCount == 0 {
		t.Fatalf("expected Carthage-attributed swift dependency in auto mode, got %#v", dep)
	}
}

func TestServiceAnalyseSwiftCarthageAllModeBehindPreviewFlag(t *testing.T) {
	repo := t.TempDir()
	writeJSFixture(t, repo)
	writeSwiftCarthageAnalysisFixture(t, repo)

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath: repo,
		TopN:     10,
		Language: "all",
		Features: mustResolveSwiftCarthagePreviewSet(t, true),
	})
	if err != nil {
		t.Fatalf("analyse all mixed Swift Carthage repo: %v", err)
	}
	assertReportLanguages(t, reportData.Dependencies, "swift", "js-ts")
}

func TestServiceAnalyseDartStableDefaultsKeepLocalPathImportUsage(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "pubspec.yaml"), `name: demo
dependencies:
  local_pkg:
    path: ../local_pkg
`)
	writeFile(t, filepath.Join(repo, "pubspec.lock"), `packages:
  local_pkg:
    dependency: "direct main"
    description: {path: ../local_pkg}
    source: path
    version: "0.0.1"
`)
	writeFile(t, filepath.Join(repo, "lib", "main.dart"), `import 'package:local_pkg/local_pkg.dart' as local;
void main() { local.run(); }
`)

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "local_pkg",
		Language:   "dart",
		Features:   mustResolveStableDefaultsFeatureSet(t),
	})
	if err != nil {
		t.Fatalf("analyse dart with stable defaults: %v", err)
	}
	dep := singleDependencyReport(t, reportData)
	if dep.Language != "dart" {
		t.Fatalf("expected dart language, got %#v", dep)
	}
	if dep.TotalExportsCount == 0 || dep.UsedExportsCount == 0 {
		t.Fatalf("expected local path import usage under stable defaults, got %#v", dep)
	}
	for _, warning := range reportData.Warnings {
		if strings.Contains(strings.ToLower(warning), `no imports found for dependency "local_pkg"`) {
			t.Fatalf("expected local path import attribution under stable defaults, got warnings %#v", reportData.Warnings)
		}
	}
}

func writeSwiftCarthageAnalysisFixture(t *testing.T, repo string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, "Cartfile"), "github \"ReactiveX/RxSwift\" ~> 6.0\n")
	writeFile(t, filepath.Join(repo, "Cartfile.resolved"), "github \"ReactiveX/RxSwift\" \"6.8.0\"\n")
	writeFile(t, filepath.Join(repo, "Sources", "App", "main.swift"), "import RxSwift\nlet value = DisposeBag()\n")
}

func writeJSFixture(t *testing.T, repo string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, packageJSONFileName), demoPackageJSONContent)
	writeFile(t, filepath.Join(repo, indexJSFileName), lodashMapUsageJS)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", packageJSONFileName), nodeMainPackageJSON)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", indexJSFileName), mapExportJSContent)
}

func singleDependencyReport(t *testing.T, reportData report.Report) report.DependencyReport {
	t.Helper()
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(reportData.Dependencies))
	}
	return reportData.Dependencies[0]
}

func assertReportLanguages(t *testing.T, dependencies []report.DependencyReport, expected ...string) {
	t.Helper()
	languages := make([]string, 0, len(dependencies))
	for _, dep := range dependencies {
		languages = append(languages, dep.Language)
	}
	for _, languageID := range expected {
		if !slices.Contains(languages, languageID) {
			t.Fatalf("expected language %q in dependencies, got %#v", languageID, languages)
		}
	}
}

func mustResolveSwiftCarthagePreviewSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()
	options := featureflags.ResolveOptions{Channel: featureflags.ChannelDev}
	if enabled {
		options.Enable = []string{"swift-carthage"}
	}
	resolved, err := featureflags.DefaultRegistry().Resolve(options)
	if err != nil {
		t.Fatalf("resolve swift Carthage preview feature set: %v", err)
	}
	return resolved
}

func mustResolveDependencyIdentityPreviewFeatureSet(t *testing.T) featureflags.Set {
	t.Helper()
	resolved, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{report.DependencyIdentityPreviewFeature},
	})
	if err != nil {
		t.Fatalf("resolve dependency identity preview feature set: %v", err)
	}
	return resolved
}

func mustResolveStableDefaultsFeatureSet(t *testing.T) featureflags.Set {
	t.Helper()
	resolved, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
	})
	if err != nil {
		t.Fatalf("resolve stable defaults feature set: %v", err)
	}
	return resolved
}

func mustResolvePythonRuntimeTraceFeatureSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()
	options := featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
	}
	state := "disabled"
	if enabled {
		state = "enabled"
		options.Enable = []string{pythonRuntimeTraceFeature}
	} else {
		options.Disable = []string{pythonRuntimeTraceFeature}
	}
	resolved, err := featureflags.DefaultRegistry().Resolve(options)
	if err != nil {
		t.Fatalf("resolve Python runtime trace %s feature set: %v", state, err)
	}
	return resolved
}

func mustResolvePythonRuntimeCaptureWithTraceDisabled(t *testing.T) featureflags.Set {
	t.Helper()
	resolved, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{pythonRuntimeCaptureFeature},
		Disable: []string{pythonRuntimeTraceFeature},
	})
	if err != nil {
		t.Fatalf("resolve Python runtime capture with trace disabled: %v", err)
	}
	return resolved
}

func mustResolvePythonRuntimeCaptureAndTraceDisabled(t *testing.T) featureflags.Set {
	t.Helper()
	resolved, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Disable: []string{pythonRuntimeCaptureFeature, pythonRuntimeTraceFeature},
	})
	if err != nil {
		t.Fatalf("resolve Python runtime capture and trace disabled: %v", err)
	}
	return resolved
}

func TestServiceAnalyseRuntimeCorrelationIntegration(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, packageJSONFileName), demoPackageJSONContent)
	writeFile(t, filepath.Join(repo, indexJSFileName), "import { map } from \"lodash\"\nimport { pad } from \""+leftPadDependencyID+"\"\nmap([1], (x) => x)\n")
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", packageJSONFileName), nodeMainPackageJSON)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", indexJSFileName), mapExportJSContent)
	writeFile(t, filepath.Join(repo, "node_modules", leftPadDependencyID, packageJSONFileName), nodeMainPackageJSON)
	writeFile(t, filepath.Join(repo, "node_modules", leftPadDependencyID, indexJSFileName), "export function pad() {}\n")
	tracePath := filepath.Join(repo, ".artifacts", "runtime.ndjson")
	writeFile(t, tracePath, "{\"module\":\"lodash/map\"}\n{\"module\":\"chalk/index\"}\n")

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:         repo,
		TopN:             10,
		Language:         "js-ts",
		RuntimeTracePath: tracePath,
	})
	if err != nil {
		t.Fatalf("analyse runtime correlation: %v", err)
	}

	dependencies := make(map[string]report.DependencyReport, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		dependencies[dep.Name] = dep
	}

	lodash := dependencies["lodash"]
	if lodash.RuntimeUsage == nil || lodash.RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected lodash overlap correlation, got %#v", lodash.RuntimeUsage)
	}
	leftPad := dependencies[leftPadDependencyID]
	if leftPad.RuntimeUsage == nil || leftPad.RuntimeUsage.Correlation != report.RuntimeCorrelationStaticOnly {
		t.Fatalf("expected %s static-only correlation, got %#v", leftPadDependencyID, leftPad.RuntimeUsage)
	}
	chalk := dependencies["chalk"]
	if chalk.RuntimeUsage == nil || chalk.RuntimeUsage.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected chalk runtime-only correlation, got %#v", chalk.RuntimeUsage)
	}
	if len(chalk.RuntimeUsage.TopSymbols) == 0 || chalk.RuntimeUsage.TopSymbols[0].Symbol != "index" {
		t.Fatalf("expected runtime symbols on chalk runtime-only row, got %#v", chalk.RuntimeUsage.TopSymbols)
	}
}

func TestServiceAnalysePythonRuntimeTraceIntegration(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "requirements.txt"), "requests==2.32.0\n")
	writeFile(t, filepath.Join(repo, "main.py"), "import requests\nrequests.get('https://example.test')\n")
	tracePath := filepath.Join(repo, ".artifacts", "python-runtime.ndjson")
	writeFile(t, tracePath, "{\"language\":\"python\",\"module\":\"requests.sessions\",\"parent\":\"/repo/main.py\",\"entrypoint\":\"/repo/main.py\"}\n{\"language\":\"python\",\"module\":\"httpx._client\",\"parent\":\"/repo/main.py\",\"entrypoint\":\"/repo/main.py\"}\n")

	service := NewService()
	assertPythonRuntimeFeatureDisabled(t, service, repo, tracePath)
	assertPythonRuntimeTraceDisabled(t, service, repo, tracePath)
	assertPythonRuntimeStableDefaults(t, service, repo, tracePath)
}

func analysePythonRuntimeReport(t *testing.T, service *Service, repo, tracePath string, features featureflags.Set) report.Report {
	t.Helper()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:         repo,
		TopN:             10,
		Language:         "python",
		RuntimeTracePath: tracePath,
		Features:         features,
	})
	if err != nil {
		t.Fatalf("analyse python runtime: %v", err)
	}
	return reportData
}

func assertPythonRuntimeFeatureDisabled(t *testing.T, service *Service, repo, tracePath string) {
	t.Helper()
	reportData := analysePythonRuntimeReport(t, service, repo, tracePath, mustResolvePythonRuntimeTraceFeatureSet(t, false))
	if dep := dependencyByLanguageName(t, reportData.Dependencies, "python", "requests"); dep.RuntimeUsage != nil {
		t.Fatalf("did not expect Python runtime usage with feature disabled, got %#v", dep.RuntimeUsage)
	}
}

func assertPythonRuntimeTraceDisabled(t *testing.T, service *Service, repo, tracePath string) {
	t.Helper()
	reportData := analysePythonRuntimeReport(t, service, repo, tracePath, mustResolvePythonRuntimeCaptureWithTraceDisabled(t))
	if requests := dependencyByLanguageName(t, reportData.Dependencies, "python", "requests"); requests.RuntimeUsage != nil {
		t.Fatalf("did not expect an explicit Python trace to bypass the disabled trace feature, got %#v", requests.RuntimeUsage)
	}
}

func assertPythonRuntimeStableDefaults(t *testing.T, service *Service, repo, tracePath string) {
	t.Helper()
	stableDefault := analysePythonRuntimeReport(t, service, repo, tracePath, mustResolveStableDefaultsFeatureSet(t))
	requests := dependencyByLanguageName(t, stableDefault.Dependencies, "python", "requests")
	if requests.RuntimeUsage == nil || requests.RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected Python requests overlap correlation, got %#v", requests.RuntimeUsage)
	}
	if requests.RuntimeUsage.LoadCount != 1 {
		t.Fatalf("expected one Python requests runtime load, got %#v", requests.RuntimeUsage)
	}
	if len(requests.RuntimeUsage.Modules) != 1 || requests.RuntimeUsage.Modules[0].Module != "requests.sessions" {
		t.Fatalf("expected Python runtime module detail, got %#v", requests.RuntimeUsage.Modules)
	}
	if len(requests.RuntimeUsage.ParentModules) != 1 || requests.RuntimeUsage.ParentModules[0].Module != "/repo/main.py" {
		t.Fatalf("expected Python runtime parent detail, got %#v", requests.RuntimeUsage.ParentModules)
	}
	httpx := dependencyByLanguageName(t, stableDefault.Dependencies, "python", "httpx")
	if httpx.RuntimeUsage == nil || httpx.RuntimeUsage.Correlation != report.RuntimeCorrelationRuntimeOnly || !httpx.RuntimeUsage.RuntimeOnly {
		t.Fatalf("expected Python httpx runtime-only row, got %#v", httpx.RuntimeUsage)
	}
}

func TestServiceAnalyseJSTraceIgnoresPythonLanguageEvents(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, packageJSONFileName), demoPackageJSONContent)
	writeFile(t, filepath.Join(repo, indexJSFileName), "import { map } from \"lodash\"\nimport request from \"requests\"\nmap([1], (x) => x)\nrequest('https://example.test')\n")
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", packageJSONFileName), nodeMainPackageJSON)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", indexJSFileName), mapExportJSContent)
	writeFile(t, filepath.Join(repo, "node_modules", "requests", packageJSONFileName), nodeMainPackageJSON)
	writeFile(t, filepath.Join(repo, "node_modules", "requests", indexJSFileName), "export default function request() {}\n")
	tracePath := filepath.Join(repo, ".artifacts", "runtime.ndjson")
	writeFile(t, tracePath, "{\"module\":\"lodash/map\"}\n{\"language\":\"python\",\"module\":\"requests.sessions\"}\n")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:         repo,
		TopN:             10,
		Language:         "js-ts",
		RuntimeTracePath: tracePath,
		Features:         mustResolvePythonRuntimeTraceFeatureSet(t, true),
	})
	if err != nil {
		t.Fatalf("analyse js runtime with python event: %v", err)
	}

	lodash := dependencyByLanguageName(t, reportData.Dependencies, "js-ts", "lodash")
	if lodash.RuntimeUsage == nil || lodash.RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected lodash JS overlap correlation, got %#v", lodash.RuntimeUsage)
	}
	requests := dependencyByLanguageName(t, reportData.Dependencies, "js-ts", "requests")
	if requests.RuntimeUsage == nil || requests.RuntimeUsage.Correlation != report.RuntimeCorrelationStaticOnly || requests.RuntimeUsage.LoadCount != 0 {
		t.Fatalf("expected JS requests to ignore Python runtime event, got %#v", requests.RuntimeUsage)
	}
}

func TestServiceAnalyseMissingRuntimeTraceFallsBack(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, packageJSONFileName), demoPackageJSONContent)
	writeFile(t, filepath.Join(repo, indexJSFileName), lodashMapUsageJS)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", packageJSONFileName), nodeMainPackageJSON)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", indexJSFileName), mapExportJSContent)

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:         repo,
		TopN:             10,
		Language:         "js-ts",
		RuntimeTracePath: filepath.Join(repo, ".artifacts", "missing.ndjson"),
	})
	if err != nil {
		t.Fatalf("expected runtime missing trace fallback: %v", err)
	}
	if len(reportData.Warnings) == 0 {
		t.Fatalf("expected warning for missing runtime trace")
	}
}

func TestServiceAnalyseAllowsAbsoluteCachePathOutsideRepo(t *testing.T) {
	repo := t.TempDir()
	writeJSFixture(t, repo)
	outsideCache := filepath.Join(t.TempDir(), "lopper-cache")

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "lodash",
		Language:   "js-ts",
		Cache: &CacheOptions{
			Enabled: true,
			Path:    outsideCache,
		},
	})
	if err != nil {
		t.Fatalf("analyse with absolute cache path outside repo: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %#v", reportData.Dependencies)
	}
	for _, dir := range []string{cacheKeysDirName, cacheObjectsDirName} {
		if _, err := os.Stat(filepath.Join(outsideCache, dir)); err != nil {
			t.Fatalf("expected %s dir in external cache root: %v", dir, err)
		}
	}
}

func TestServiceAnalyseRejectsAbsoluteSymlinkEscapeCachePath(t *testing.T) {
	repo := writeScopedCacheFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "lodash",
		Language:   "js-ts",
		Cache: &CacheOptions{
			Enabled: true,
			Path:    filepath.Join(repo, "tmp", "cache"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "cachePath must stay within repoPath") {
		t.Fatalf("expected absolute symlink cache rejection, got %v", err)
	}
}

func TestServiceAnalyseRejectsCanonicalSymlinkEscapeUnderRequestedRepoAlias(t *testing.T) {
	requestedRepo, canonicalCache, outsideCache := canonicalAliasCacheEscapeFixture(t)
	writeJSFixture(t, requestedRepo)

	_, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   requestedRepo,
		Dependency: "lodash",
		Language:   "js-ts",
		Cache: &CacheOptions{
			Enabled: true,
			Path:    canonicalCache,
		},
	})
	if !CachePathSymlinkEscape(err) {
		t.Fatalf("expected service symlink escape rejection, got %v", err)
	}
	if CachePathExternal(err) {
		t.Fatalf("expected service not to classify canonical in-repo form as external, got %v", err)
	}
	if _, statErr := os.Stat(outsideCache); !os.IsNotExist(statErr) {
		t.Fatalf("expected service not to create external cache directory, stat err=%v", statErr)
	}
}

func TestServiceAnalyseRejectsAlternateAbsoluteRepoAliasesThatLaterEscape(t *testing.T) {
	for _, fixture := range alternateAbsoluteRepoAliasEscapeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			writeJSFixture(t, fixture.requestedRepo)
			_, err := NewService().Analyse(context.Background(), Request{
				RepoPath:   fixture.requestedRepo,
				Dependency: "lodash",
				Language:   "js-ts",
				Cache: &CacheOptions{
					Enabled: true,
					Path:    fixture.cachePath,
				},
			})
			if !CachePathSymlinkEscape(err) || CachePathExternal(err) {
				t.Fatalf("expected service symlink escape rejection, got %v", err)
			}
			if _, statErr := os.Stat(fixture.outsideCache); !os.IsNotExist(statErr) {
				t.Fatalf("expected service not to create outside cache, stat err=%v", statErr)
			}
		})
	}
}

func TestServiceAnalyseConfiguredRepoCacheIgnoresWorkingDirectory(t *testing.T) {
	repo := t.TempDir()
	writeJSFixture(t, repo)
	configuredCache := filepath.Join(repo, ".cache", "lopper")
	workingDir := t.TempDir()

	originalWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(workingDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWorkingDir); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	reportData, err := NewService().Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "lodash",
		Language:   "js-ts",
		Cache: &CacheOptions{
			Enabled: true,
			Path:    configuredCache,
		},
	})
	if err != nil {
		t.Fatalf("analyse with configured repo cache path: %v", err)
	}
	if reportData.Cache == nil || reportData.Cache.Path != configuredCache || reportData.Cache.Misses != 1 || reportData.Cache.Writes != 1 {
		t.Fatalf("expected configured repo cache miss/write metadata, got %#v", reportData.Cache)
	}
	assertCacheDirsExist(t, configuredCache)

	entries, err := os.ReadDir(workingDir)
	if err != nil {
		t.Fatalf("read working directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no cache writes in working directory, got %#v", entries)
	}
}

func TestServiceAnalyseScopedPinnedDefaultCacheWritesStayUnderRepoRoot(t *testing.T) {
	repo := writeScopedCacheFixture(t)

	cacheOptions, err := ResolveTrustedDefaultCacheOptions(repo, false)
	if err != nil {
		t.Fatalf("resolve trusted default cache options: %v", err)
	}
	pinnedCachePath := cacheOptions.trustedPinnedPath()

	scopedRoot := captureScopedWorkspacePath(t)
	assertScopedCacheInitUsesRepoRoot(t, repo, scopedRoot)

	req := Request{
		RepoPath:        repo,
		Dependency:      "lodash",
		Language:        "js-ts",
		IncludePatterns: []string{"src/**"},
		ExcludePatterns: []string{"vendor/**"},
		Cache:           cacheOptions,
	}

	reportData, err := NewService().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse with scoped pinned default cache path: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %#v", reportData.Dependencies)
	}
	if reportData.Cache == nil || reportData.Cache.Path != pinnedCachePath || reportData.Cache.Misses != 1 || reportData.Cache.Writes != 1 {
		t.Fatalf("expected scoped cache metadata to report pinned repo-root path and first-run miss/write, got %#v", reportData.Cache)
	}
	assertCacheDirsExist(t, filepath.Join(repo, defaultAnalysisCacheDirName))

	secondReport, err := NewService().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("repeat analyse with scoped pinned default cache path: %v", err)
	}
	if secondReport.Cache == nil || secondReport.Cache.Path != pinnedCachePath || secondReport.Cache.Hits != 1 || secondReport.Cache.Misses != 0 {
		t.Fatalf("expected second scoped run to reuse pinned repo-root cache, got %#v", secondReport.Cache)
	}
}

func TestServiceAnalyseScopedExplicitRelativeCachePathUsesPinnedRepoKeyAcrossRuns(t *testing.T) {
	repo := writeScopedCacheFixture(t)

	cacheOptions, err := ResolveTrustedCacheOptions(repo, &CacheOptions{
		Enabled: true,
		Path:    filepath.Join(".cache", "lopper"),
	})
	if err != nil {
		t.Fatalf("resolve trusted explicit relative cache path: %v", err)
	}
	if !cacheOptions.hasTrustedPin() {
		t.Fatalf("expected trusted cache options to pin explicit relative cache path")
	}

	scopedRoot := captureScopedWorkspacePath(t)
	assertScopedCacheInitExcludesCacheDir(t, scopedRoot, filepath.Join(".cache", "lopper"))

	req := Request{
		RepoPath:        repo,
		Dependency:      "lodash",
		Language:        "js-ts",
		IncludePatterns: []string{"src/**"},
		ExcludePatterns: []string{"vendor/**"},
		Cache:           cacheOptions,
	}

	firstReport, err := NewService().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse with scoped explicit relative cache path: %v", err)
	}
	if firstReport.Cache == nil || firstReport.Cache.Path != filepath.Join(".cache", "lopper") || firstReport.Cache.Misses != 1 || firstReport.Cache.Writes != 1 {
		t.Fatalf("expected first scoped run to report explicit relative cache path with miss/write metadata, got %#v", firstReport.Cache)
	}
	assertCacheDirsExist(t, filepath.Join(repo, ".cache", "lopper"))

	secondReport, err := NewService().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse with scoped explicit relative cache path: %v", err)
	}
	if secondReport.Cache == nil || secondReport.Cache.Path != filepath.Join(".cache", "lopper") || secondReport.Cache.Hits != 1 || secondReport.Cache.Misses != 0 {
		t.Fatalf("expected second scoped run to hit repo-root explicit cache with truthful metadata, got %#v", secondReport.Cache)
	}
}

func TestServiceAnalyseScopedAbsoluteInRepoCachePathPinsAndHitsAcrossRuns(t *testing.T) {
	repo := writeScopedCacheFixture(t)
	absoluteCachePath := filepath.Join(repo, ".cache", "lopper-absolute")
	if err := os.MkdirAll(absoluteCachePath, 0o750); err != nil {
		t.Fatalf("create pre-seeded absolute cache root: %v", err)
	}

	scopedRoot := captureScopedWorkspacePath(t)
	assertScopedCacheInitExcludesCacheDir(t, scopedRoot, filepath.Join(".cache", "lopper-absolute"))
	req := Request{
		RepoPath:        repo,
		Dependency:      "lodash",
		Language:        "js-ts",
		IncludePatterns: []string{"**"},
		Cache: &CacheOptions{
			Enabled: true,
			Path:    absoluteCachePath,
		},
	}

	firstReport, err := NewService().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse with scoped absolute in-repo cache path: %v", err)
	}
	if firstReport.Cache == nil || firstReport.Cache.Path != absoluteCachePath || firstReport.Cache.Misses != 1 || firstReport.Cache.Writes != 1 {
		t.Fatalf("expected first scoped run to report absolute in-repo cache miss/write metadata, got %#v", firstReport.Cache)
	}
	assertCacheDirsExist(t, absoluteCachePath)

	secondReport, err := NewService().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse with scoped absolute in-repo cache path: %v", err)
	}
	if secondReport.Cache == nil || secondReport.Cache.Path != absoluteCachePath || secondReport.Cache.Hits != 1 || secondReport.Cache.Misses != 0 {
		t.Fatalf("expected second scoped run to hit absolute in-repo cache with truthful metadata, got %#v", secondReport.Cache)
	}
}

func TestServiceAnalyseConfigPathUsesStableCacheIdentityAcrossSnapshotRuns(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	configPath := filepath.Join(repo, ".lopper.yml")
	testutil.MustWriteFile(t, configPath, "thresholds:\n  low_confidence_warning_percent: 10\n")

	service, adapter := newCacheTestService(t)
	req := newCacheRequest(repo, filepath.Join(repo, cacheTestDirectoryName), false)
	req.ConfigPath = configPath

	first, err := service.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse with explicit config path: %v", err)
	}
	if first.Cache == nil || first.Cache.Misses != 1 || first.Cache.Writes != 1 {
		t.Fatalf("expected first config-path run cache miss/write, got %#v", first.Cache)
	}

	second, err := service.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse with explicit config path: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected explicit config path to preserve cache hit across snapshot runs, adapter calls=%d", adapter.calls)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second config-path run cache hit, got %#v", second.Cache)
	}
}

func TestServiceAnalyseScopedAbsoluteInRepoCachePathSkipsCacheDirAcrossRepoAliasOnSecondRun(t *testing.T) {
	repo := writeScopedCacheFixture(t)
	aliasRoot := t.TempDir()
	repoAlias := filepath.Join(aliasRoot, "repo")
	if err := os.Symlink(repo, repoAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	cachePath := filepath.Join(repoAlias, ".cache", "lopper-alias")
	scopedRoots := captureScopedWorkspacePaths(t)
	req := Request{
		RepoPath:        repoAlias,
		Dependency:      "lodash",
		Language:        "js-ts",
		IncludePatterns: []string{"**"},
		Cache: &CacheOptions{
			Enabled: true,
			Path:    cachePath,
		},
	}

	firstReport, err := NewService().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse with aliased absolute in-repo cache path: %v", err)
	}
	if firstReport.Cache == nil || firstReport.Cache.Path != cachePath || firstReport.Cache.Misses != 1 || firstReport.Cache.Writes != 1 {
		t.Fatalf("expected first aliased scoped run to miss and write, got %#v", firstReport.Cache)
	}
	assertCacheDirsExist(t, filepath.Join(repo, ".cache", "lopper-alias"))

	previous := cacheInitBeforeObjectsEnsureFn
	cacheInitBeforeObjectsEnsureFn = func() error {
		if len(*scopedRoots) < 2 {
			t.Fatalf("expected second scoped workspace before cache init, roots=%#v", *scopedRoots)
		}
		assertScopedRootExcludesCacheDir(t, (*scopedRoots)[len(*scopedRoots)-1], filepath.Join(".cache", "lopper-alias"))
		return nil
	}
	t.Cleanup(func() {
		cacheInitBeforeObjectsEnsureFn = previous
	})

	secondReport, err := NewService().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse with aliased absolute in-repo cache path: %v", err)
	}
	if secondReport.Cache == nil || secondReport.Cache.Path != cachePath || secondReport.Cache.Hits != 1 || secondReport.Cache.Misses != 0 {
		t.Fatalf("expected second aliased scoped run to hit cached entry, got %#v", secondReport.Cache)
	}
}

func TestServiceAnalyseScopedRepoRootCachePathIsRejected(t *testing.T) {
	repo := writeScopedCacheFixture(t)

	_, err := NewService().Analyse(context.Background(), Request{
		RepoPath:        repo,
		Dependency:      "lodash",
		Language:        "js-ts",
		IncludePatterns: []string{"src/**"},
		Cache: &CacheOptions{
			Enabled: true,
			Path:    repo,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "scoped analysis does not allow cachePath at the repository root") {
		t.Fatalf("expected scoped repo-root cache rejection, got %v", err)
	}
}

func TestServiceAnalyseScopedExternalCacheAliasToRepoRootIsRejectedBeforeCacheInit(t *testing.T) {
	repo := writeScopedCacheFixture(t)
	cacheAlias := filepath.Join(t.TempDir(), "external-cache-alias")
	if err := os.Symlink(repo, cacheAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := NewService().Analyse(context.Background(), Request{
		RepoPath:        repo,
		Dependency:      "lodash",
		Language:        "js-ts",
		IncludePatterns: []string{"src/**"},
		Cache: &CacheOptions{
			Enabled: true,
			Path:    cacheAlias,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "scoped analysis does not allow cachePath at the repository root") {
		t.Fatalf("expected external alias to receive in-repo root rejection, got %v", err)
	}
	assertCacheLayoutAbsent(t, repo)
}

func TestServicePipelineScopedExternalCacheAliasToRepoSubdirUsesInRepoExclusionWithoutMutation(t *testing.T) {
	repo := writeScopedCacheFixture(t)
	cacheRoot := filepath.Join(repo, ".cache", "external-alias")
	if err := os.MkdirAll(cacheRoot, 0o750); err != nil {
		t.Fatalf("mkdir cache root: %v", err)
	}
	cacheAlias := filepath.Join(t.TempDir(), "external-cache-alias")
	if err := os.Symlink(cacheRoot, cacheAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	req := Request{
		RepoPath:        repo,
		Dependency:      "lodash",
		Language:        "js-ts",
		IncludePatterns: []string{"**"},
		Cache: &CacheOptions{
			Enabled: true,
			Path:    cacheAlias,
		},
	}
	cacheOptions, err := normalizePipelineCacheOptions(repo, req)
	if err != nil {
		t.Fatalf("normalize external alias to repo subdir: %v", err)
	}
	if !InRepoCacheOptions(cacheOptions) {
		t.Fatalf("expected external alias to mint an in-repo pin, got %#v", cacheOptions)
	}
	if relativeRoot := pinnedScopedCacheRoot(cacheOptions); relativeRoot != filepath.Join(".cache", "external-alias") {
		t.Fatalf("expected scoped copy exclusion for aliased cache subdir, got %q", relativeRoot)
	}
	req.Cache = cacheOptions
	req.Language = language.Auto
	scopedRoots := captureScopedWorkspacePaths(t)
	_, err = (&Service{Registry: language.NewRegistry()}).newAnalysisPipeline(context.Background(), req)
	if !errors.Is(err, language.ErrNoMatch) {
		t.Fatalf("expected empty registry to stop before cache initialization, got %v", err)
	}
	if len(*scopedRoots) != 1 {
		t.Fatalf("expected one scoped workspace, got %#v", *scopedRoots)
	}
	if _, statErr := os.Stat((*scopedRoots)[0]); !os.IsNotExist(statErr) {
		t.Fatalf("expected failed pipeline setup to clean scoped workspace, stat err=%v", statErr)
	}
	assertCacheLayoutAbsent(t, cacheRoot)
	assertCacheLayoutAbsent(t, repo)
}

func TestServiceAnalyseScopedAndUnscopedPinnedCacheEntriesDoNotCollide(t *testing.T) {
	repo := writeScopedCacheFixture(t)
	cacheOptions, err := ResolveTrustedDefaultCacheOptions(repo, false)
	if err != nil {
		t.Fatalf("resolve trusted default cache options: %v", err)
	}

	baseReq := Request{
		RepoPath:   repo,
		Dependency: "lodash",
		Language:   "js-ts",
		Cache:      cacheOptions,
	}
	scopedReq := baseReq
	scopedReq.IncludePatterns = []string{"src/**"}
	assertScopedCacheRun(t, baseReq, 1, 1, 1, 0)
	assertScopedCacheRun(t, scopedReq, 1, 1, 1, 0)
	assertScopedCacheRun(t, baseReq, 0, 0, 0, 1)
	assertScopedCacheRun(t, scopedReq, 0, 0, 0, 1)
}

func assertScopedCacheRun(t *testing.T, req Request, wantMisses, wantWrites, wantCallsDelta, wantHits int) {
	t.Helper()
	result, err := NewService().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if result.Cache == nil || result.Cache.Misses != wantMisses || result.Cache.Writes != wantWrites || result.Cache.Hits != wantHits {
		t.Fatalf("unexpected scoped cache metadata: %#v", result.Cache)
	}
}

func writeScopedCacheFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, packageJSONFileName), demoPackageJSONContent)
	writeFile(t, filepath.Join(repo, "src", indexJSFileName), lodashMapUsageJS)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", packageJSONFileName), nodeMainPackageJSON)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", indexJSFileName), mapExportJSContent)
	return repo
}

func captureScopedWorkspacePath(t *testing.T) *string {
	t.Helper()
	scopedRoot := new(string)
	previous := scopeWorkspaceCreatedFn
	scopeWorkspaceCreatedFn = func(path string) {
		*scopedRoot = path
	}
	t.Cleanup(func() {
		scopeWorkspaceCreatedFn = previous
	})
	return scopedRoot
}

func captureScopedWorkspacePaths(t *testing.T) *[]string {
	t.Helper()
	scopedRoots := &[]string{}
	previous := scopeWorkspaceCreatedFn
	scopeWorkspaceCreatedFn = func(path string) {
		*scopedRoots = append(*scopedRoots, path)
	}
	t.Cleanup(func() {
		scopeWorkspaceCreatedFn = previous
	})
	return scopedRoots
}

func assertScopedCacheInitUsesRepoRoot(t *testing.T, repo string, scopedRoot *string) {
	t.Helper()
	previous := cacheInitBeforeObjectsEnsureFn
	cacheInitBeforeObjectsEnsureFn = func() error {
		assertScopedRootExcludesCacheDir(t, *scopedRoot, defaultAnalysisCacheDirName)
		if _, err := os.Stat(filepath.Join(repo, defaultAnalysisCacheDirName, cacheKeysDirName)); err != nil {
			t.Fatalf("expected repo-root cache keys dir during cache init: %v", err)
		}
		return nil
	}
	t.Cleanup(func() {
		cacheInitBeforeObjectsEnsureFn = previous
	})
}

func assertScopedCacheInitExcludesCacheDir(t *testing.T, scopedRoot *string, relativeCachePath string) {
	t.Helper()
	previous := cacheInitBeforeObjectsEnsureFn
	cacheInitBeforeObjectsEnsureFn = func() error {
		assertScopedRootExcludesCacheDir(t, *scopedRoot, relativeCachePath)
		return nil
	}
	t.Cleanup(func() {
		cacheInitBeforeObjectsEnsureFn = previous
	})
}

func assertCacheDirsExist(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{cacheKeysDirName, cacheObjectsDirName} {
		if _, err := os.Stat(filepath.Join(root, dir)); err != nil {
			t.Fatalf("expected %s dir in %s: %v", dir, root, err)
		}
	}
}

func assertScopedRootExcludesCacheDir(t *testing.T, scopedRoot string, relativeCachePath string) {
	t.Helper()
	if scopedRoot == "" {
		t.Fatalf("expected scoped workspace to be created")
	}
	info, err := os.Stat(scopedRoot)
	if err != nil {
		t.Fatalf("expected scoped workspace %q to exist before cache initialization: %v", scopedRoot, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected scoped workspace %q to be a directory", scopedRoot)
	}
	if _, err := os.Stat(filepath.Join(scopedRoot, relativeCachePath)); !os.IsNotExist(err) {
		t.Fatalf("expected scoped workspace cache root %q to remain absent, stat err=%v", relativeCachePath, err)
	}
}

func dependencyByLanguageName(t *testing.T, dependencies []report.DependencyReport, languageID, name string) report.DependencyReport {
	t.Helper()
	for _, dependency := range dependencies {
		if dependency.Language == languageID && dependency.Name == name {
			return dependency
		}
	}
	t.Fatalf("dependency %s/%s not found in %#v", languageID, name, dependencies)
	return report.DependencyReport{}
}

func TestMergeRecommendationsPriorityOrder(t *testing.T) {
	left := []report.Recommendation{
		{Code: "consider-replacement", Priority: "low"},
	}
	right := []report.Recommendation{
		{Code: "prefer-subpath-imports", Priority: "medium"},
		{Code: "remove-unused-dependency", Priority: "high"},
	}

	merged := mergeRecommendations(left, right)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged recommendations, got %d", len(merged))
	}
	got := []string{
		merged[0].Code,
		merged[1].Code,
		merged[2].Code,
	}
	want := []string{
		"remove-unused-dependency",
		"prefer-subpath-imports",
		"consider-replacement",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected recommendation order: got %#v want %#v", got, want)
	}
}

func TestMergeCodemodReport(t *testing.T) {
	left := &report.CodemodReport{
		Mode: "suggest-only",
		Suggestions: []report.CodemodSuggestion{
			{File: "a.js", Line: 3, ImportName: "map", ToModule: "lodash/map"},
		},
		Skips: []report.CodemodSkip{
			{File: "b.js", Line: 2, ImportName: "*", ReasonCode: "namespace-import"},
		},
	}
	right := &report.CodemodReport{
		Suggestions: []report.CodemodSuggestion{
			{File: "a.js", Line: 3, ImportName: "map", ToModule: "lodash/map"},
			{File: "c.js", Line: 8, ImportName: "filter", ToModule: "lodash/filter"},
		},
		Skips: []report.CodemodSkip{
			{File: "d.js", Line: 5, ImportName: "map", ReasonCode: "alias-conflict"},
		},
	}

	merged := mergeCodemodReport(left, right)
	if merged == nil {
		t.Fatalf("expected merged codemod report")
		return
	}
	if merged.Mode != "suggest-only" {
		t.Fatalf("expected mode suggest-only, got %q", merged.Mode)
	}
	if len(merged.Suggestions) != 2 {
		t.Fatalf("expected deduped suggestions, got %#v", merged.Suggestions)
	}
	if len(merged.Skips) != 2 {
		t.Fatalf("expected merged skips, got %#v", merged.Skips)
	}
}

func TestScopedCandidateRootsModes(t *testing.T) {
	repo := t.TempDir()
	rootA := filepath.Join(repo, "packages", "a")
	rootB := filepath.Join(repo, "packages", "b")
	writeFile(t, filepath.Join(rootA, "a.txt"), "a1\n")
	writeFile(t, filepath.Join(rootB, "b.txt"), "b1\n")

	repoRoots, warnings := scopedCandidateRoots(ScopeModeRepo, []string{rootA, rootB}, repo)
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings in repo mode, got %#v", warnings)
	}
	if len(repoRoots) != 1 || repoRoots[0] != repo {
		t.Fatalf("expected repo root only in repo mode, got %#v", repoRoots)
	}
}

func TestScopedCandidateRootsChangedPackagesFallbackWarning(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "missing-repo")
	roots := []string{filepath.Join(repo, "packages", "a")}
	gotRoots, warnings := scopedCandidateRoots(ScopeModeChangedPackages, roots, repo)
	if len(gotRoots) != 1 || gotRoots[0] != roots[0] {
		t.Fatalf("expected fallback roots on changed-packages failure, got %#v", gotRoots)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to resolve changed packages") {
		t.Fatalf("expected fallback warning, got %#v", warnings)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLowConfidenceWarningThreshold(t *testing.T) {
	candidate := language.Candidate{
		Adapter:   nil,
		Detection: language.Detection{Confidence: 30},
	}
	candidate.Adapter = &stubAdapter{id: "js-ts"}

	warnings := lowConfidenceWarning("all", candidate, 40)
	if len(warnings) == 0 {
		t.Fatalf("expected warning for confidence below threshold")
	}

	warnings = lowConfidenceWarning("all", candidate, 20)
	if len(warnings) != 0 {
		t.Fatalf("expected no warning when threshold is lower than confidence")
	}
}

func TestServiceAnalyseCSharpAlias(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Api.csproj"), `
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Newtonsoft.Json" Version="13.0.3" />
  </ItemGroup>
</Project>`)
	writeFile(t, filepath.Join(repo, programFileName), `
using JsonConvert = Newtonsoft.Json.JsonConvert;

public class Program {
  public static void Main() {
    _ = JsonConvert.SerializeObject(new { Name = "demo" });
  }
}
`)

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: newtonsoftDependencyID,
		Language:   "csharp",
	})
	if err != nil {
		t.Fatalf("analyse csharp alias: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Language != "dotnet" {
		t.Fatalf("expected language dotnet, got %q", dep.Language)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used exports > 0")
	}
}

func TestServiceForwardsMinUsageThresholdToDotNet(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "App.csproj"), `
<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <PackageReference Include="Newtonsoft.Json" Version="13.0.3" />
  </ItemGroup>
</Project>`)
	writeFile(t, filepath.Join(repo, programFileName), `
using J = Newtonsoft.Json;
public class Program { public static void Main() {} }
`)

	service := NewService()
	withDefault, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: newtonsoftDependencyID,
		Language:   "dotnet",
	})
	if err != nil {
		t.Fatalf("analyse with default threshold: %v", err)
	}
	if len(withDefault.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(withDefault.Dependencies))
	}
	if !hasRecommendationCode(withDefault.Dependencies[0], "reduce-low-usage-package-surface") {
		t.Fatalf("expected low-usage recommendation with default threshold")
	}

	zero := 0
	withZero, err := service.Analyse(context.Background(), Request{
		RepoPath:                          repo,
		Dependency:                        newtonsoftDependencyID,
		Language:                          "dotnet",
		MinUsagePercentForRecommendations: &zero,
	})
	if err != nil {
		t.Fatalf("analyse with zero threshold: %v", err)
	}
	if len(withZero.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(withZero.Dependencies))
	}
	if hasRecommendationCode(withZero.Dependencies[0], "reduce-low-usage-package-surface") {
		t.Fatalf("did not expect low-usage recommendation when threshold is 0")
	}
}

func TestServiceAppliesDeterministicFindingConfidenceFiltering(t *testing.T) {
	registry := language.NewRegistry()
	if err := registry.Register(&findingsAdapter{id: "findings"}); err != nil {
		t.Fatalf("register findings adapter: %v", err)
	}

	service := &Service{Registry: registry}
	threshold := 90
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:                    t.TempDir(),
		Language:                    "findings",
		LowConfidenceWarningPercent: &threshold,
	})
	if err != nil {
		t.Fatalf("analyse with confidence threshold: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(reportData.Dependencies))
	}

	dep := reportData.Dependencies[0]
	if len(dep.UnusedExports) != 0 {
		t.Fatalf("expected unused exports to be filtered, got %#v", dep.UnusedExports)
	}
	if len(dep.UnusedImports) != 0 {
		t.Fatalf("expected unused imports to be filtered, got %#v", dep.UnusedImports)
	}
	if len(dep.Recommendations) != 0 {
		t.Fatalf("expected recommendations to be filtered, got %#v", dep.Recommendations)
	}
	if len(dep.RiskCues) != 0 {
		t.Fatalf("expected risk cues to be filtered, got %#v", dep.RiskCues)
	}
}

func TestServiceAnalyseCPPAlias(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "src", "main.cpp"), "#include <openssl/ssl.h>\nint main() { return 0; }\n")

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Dependency: "openssl",
		Language:   "c++",
	})
	if err != nil {
		t.Fatalf("analyse c++ alias: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Language != "cpp" {
		t.Fatalf("expected language cpp, got %q", dep.Language)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected include usage to be counted")
	}
}

func TestServiceAppliesScopeBeforeResolve(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "marker.txt"), "present\n")

	registry := language.NewRegistry()
	adapter := &markerDetectAdapter{id: "marker", markerPath: "marker.txt"}
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register marker adapter: %v", err)
	}
	service := &Service{Registry: registry}

	withoutScope, err := service.Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: "auto",
		TopN:     1,
	})
	if err != nil {
		t.Fatalf("analyse without scope: %v", err)
	}
	if len(withoutScope.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyText, len(withoutScope.Dependencies))
	}

	_, err = service.Analyse(context.Background(), Request{
		RepoPath:         repo,
		Language:         "auto",
		TopN:             1,
		IncludePatterns:  []string{"src/**/*.js"},
		ExcludePatterns:  nil,
		RuntimeTracePath: "",
	})
	if err == nil || !strings.Contains(err.Error(), "no language adapter matched") {
		t.Fatalf("expected no-language-match error when scope excludes adapter marker, got %v", err)
	}
}

func TestServiceAnalyseRepositoryViewPreservesUnsafeJVMSymlinkWarningsWithoutOutsideReads(t *testing.T) {
	repo := t.TempDir()
	outsideDir := t.TempDir()
	writeFile(t, filepath.Join(repo, buildGradleFileName), `
dependencies {
  implementation("org.junit.jupiter:junit-jupiter-api:5.10.0")
  implementation("com.outside:secret:1.0.0")
}
`)
	writeFile(t, filepath.Join(repo, "src", "main", "java", "App.java"), `
import org.junit.jupiter.api.Assertions;
class App { void run() { Assertions.assertTrue(true); } }
`)
	writeFile(t, filepath.Join(outsideDir, "Outside.java"), `
import com.outside.SecretApi;
class Outside { void use() { SecretApi.call(); } }
`)
	if err := os.Symlink(filepath.Join("..", "..", "..", "..", filepath.Base(outsideDir), "Outside.java"), filepath.Join(repo, "src", "main", "java", "Outside.java")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	service := NewService()
	direct, err := service.Analyse(context.Background(), Request{
		RepoPath:   repo,
		Language:   "jvm",
		Dependency: "secret",
		TopN:       10,
	})
	if err != nil {
		t.Fatalf("analyse direct repo: %v", err)
	}

	repository, err := ResolveTrustedRepository(repo)
	if err != nil {
		t.Fatalf("resolve trusted repository: %v", err)
	}
	view, err := OpenTrustedRepository(context.Background(), repository, repo, nil)
	if err != nil {
		t.Fatalf("open trusted repository view: %v", err)
	}
	defer func() {
		if closeErr := view.Close(); closeErr != nil {
			t.Errorf("close repository view: %v", closeErr)
		}
	}()

	withView, err := service.Analyse(context.Background(), Request{
		RepoPath:       repo,
		Repository:     repository,
		RepositoryView: view,
		Language:       "jvm",
		Dependency:     "secret",
		TopN:           10,
	})
	if err != nil {
		t.Fatalf("analyse through repository view: %v", err)
	}

	const sourceWarning = "skipped JVM source symlink src/main/java/Outside.java: target escapes repo root"
	const summaryWarning = "skipped 1 unreadable or untrusted JVM source symlink(s)"
	assertContainsWarning(t, direct.Warnings, sourceWarning)
	assertContainsWarning(t, direct.Warnings, summaryWarning)
	assertContainsWarning(t, withView.Warnings, sourceWarning)
	assertContainsWarning(t, withView.Warnings, summaryWarning)
	if countWarningsMatching(direct.Warnings, sourceWarning) != 1 {
		t.Fatalf("expected one direct symlink warning, got %#v", direct.Warnings)
	}
	if countWarningsMatching(withView.Warnings, sourceWarning) != 1 {
		t.Fatalf("expected one repository-view symlink warning, got %#v", withView.Warnings)
	}
	if hasDependencyByLanguageAndName(direct, "jvm", "secret") {
		t.Fatalf("expected direct analysis to ignore outside source content, got %#v", direct.Dependencies)
	}
	if hasDependencyByLanguageAndName(withView, "jvm", "secret") {
		t.Fatalf("expected repository-view analysis to ignore outside source content, got %#v", withView.Dependencies)
	}
	if findDependencyByLanguageAndName(t, direct, "jvm", "junit-jupiter-api").UsedExportsCount == 0 {
		t.Fatalf("expected direct in-repo dependency usage, got %#v", direct.Dependencies)
	}
	if findDependencyByLanguageAndName(t, withView, "jvm", "junit-jupiter-api").UsedExportsCount == 0 {
		t.Fatalf("expected repository-view in-repo dependency usage, got %#v", withView.Dependencies)
	}
}

func hasRecommendationCode(dep report.DependencyReport, code string) bool {
	for _, rec := range dep.Recommendations {
		if rec.Code == code {
			return true
		}
	}
	return false
}

type stubAdapter struct {
	id string
}

func (s *stubAdapter) ID() string { return s.id }

func (s *stubAdapter) Aliases() []string { return nil }

func (s *stubAdapter) Detect(context.Context, string) (bool, error) { return true, nil }

func (s *stubAdapter) Analyse(context.Context, language.Request) (report.Report, error) {
	return report.Report{}, nil
}

type markerDetectAdapter struct {
	id         string
	markerPath string
}

func (m *markerDetectAdapter) ID() string { return m.id }

func (m *markerDetectAdapter) Aliases() []string { return nil }

func (m *markerDetectAdapter) Detect(_ context.Context, repoPath string) (bool, error) {
	_, err := os.Stat(filepath.Join(repoPath, m.markerPath))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (m *markerDetectAdapter) Analyse(_ context.Context, req language.Request) (report.Report, error) {
	return report.Report{
		RepoPath: req.RepoPath,
		Dependencies: []report.DependencyReport{
			{
				Name:              "dep",
				Language:          m.id,
				UsedExportsCount:  1,
				TotalExportsCount: 1,
				UsedPercent:       100,
			},
		},
	}, nil
}

type findingsAdapter struct {
	id string
}

func (f *findingsAdapter) ID() string { return f.id }

func (f *findingsAdapter) Aliases() []string { return nil }

func (f *findingsAdapter) Detect(context.Context, string) (bool, error) { return true, nil }

func (f *findingsAdapter) Analyse(context.Context, language.Request) (report.Report, error) {
	return report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name:                 "dep",
				UsedExportsCount:     1,
				TotalExportsCount:    0,
				UnusedExports:        []report.SymbolRef{{Name: "x", Module: "dep"}},
				UnusedImports:        []report.ImportUse{{Name: "x", Module: "dep"}},
				RiskCues:             []report.RiskCue{{Code: "dynamic", Severity: "high", Message: "dynamic lookup"}},
				Recommendations:      []report.Recommendation{{Code: "remove", Priority: "high", Message: "remove dep"}},
				EstimatedUnusedBytes: 1,
			},
		},
	}, nil
}

func assertContainsWarning(t *testing.T, warnings []string, want string) {
	t.Helper()
	for _, warning := range warnings {
		if warning == want {
			return
		}
	}
	t.Fatalf("expected warning %q in %#v", want, warnings)
}

func countWarningsMatching(warnings []string, want string) int {
	count := 0
	for _, warning := range warnings {
		if warning == want {
			count++
		}
	}
	return count
}

func findDependencyByLanguageAndName(t *testing.T, reportData report.Report, languageID, name string) report.DependencyReport {
	t.Helper()
	for _, dependency := range reportData.Dependencies {
		if dependency.Language == languageID && dependency.Name == name {
			return dependency
		}
	}
	t.Fatalf("expected dependency %s/%s in %#v", languageID, name, reportData.Dependencies)
	return report.DependencyReport{}
}

func hasDependencyByLanguageAndName(reportData report.Report, languageID, name string) bool {
	for _, dependency := range reportData.Dependencies {
		if dependency.Language == languageID && dependency.Name == name {
			return true
		}
	}
	return false
}
