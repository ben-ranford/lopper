package cpp

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestLoadDependencyCatalogParsesManifestsAndLocks(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, vcpkgManifestFile), `{
  "dependencies": [
    "fmt",
    {"name": "openssl"},
    {"name": "boost-asio"}
  ]
}`)
	testutil.MustWriteFile(t, filepath.Join(repo, vcpkgLockFile), `{
  "dependencies": [
    {"name": "zlib"},
    {"name": "curl"}
  ]
}`)
	testutil.MustWriteFile(t, filepath.Join(repo, "native", conanManifestFile), `[requires]
protobuf/3.21.12
[tool_requires]
cmake/3.27.0
[test_requires]
gtest/1.14.0
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "native", conanLockFile), `{
  "graph_lock": {
    "nodes": {
      "0": {"ref": "fmt/10.2.1"},
      "1": {"ref": "bzip2/1.0.8#revision"}
    }
  }
}`)

	catalog, warnings, err := loadDependencyCatalog(repo)
	if err != nil {
		t.Fatalf("loadDependencyCatalog: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected manifest parsing without warnings, got %#v", warnings)
	}

	for _, dependency := range []string{"fmt", "openssl", "boost-asio", "zlib", "curl", "protobuf", "gtest", "bzip2"} {
		if !catalog.contains(dependency) {
			t.Fatalf("expected catalog to contain %q, got %#v", dependency, catalog.list())
		}
	}
	if catalog.contains("cmake") {
		t.Fatalf("did not expect tool_requires package to be treated as dependency")
	}
	if got := catalog.sources("fmt"); !slices.Equal(got, []string{"conan.lock", "vcpkg manifest"}) {
		t.Fatalf("unexpected sources for fmt: %#v", got)
	}
}

func TestLoadDependencyCatalogInvalidManifestWarning(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, vcpkgManifestFile), `{`)

	_, warnings, err := loadDependencyCatalog(repo)
	if err != nil {
		t.Fatalf("loadDependencyCatalog: %v", err)
	}
	if !hasWarning(warnings, "failed to parse vcpkg.json") {
		t.Fatalf("expected invalid-manifest warning, got %#v", warnings)
	}
}

func TestAnalyseWithVcpkgManifestIncludesDeclaredDepsAndUnresolvedWarnings(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, vcpkgManifestFile), `{
  "dependencies": ["fmt", "openssl"]
}`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main.cpp"), `#include <fmt/core.h>
#include "missing_header.hpp"
int main() { return 0; }
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if !hasWarning(reportData.Warnings, "compile_commands.json not found") {
		t.Fatalf("expected compile database warning, got %#v", reportData.Warnings)
	}
	if !hasWarning(reportData.Warnings, "include mapping unresolved") {
		t.Fatalf("expected unresolved include warning, got %#v", reportData.Warnings)
	}

	names := dependencyNames(reportData.Dependencies)
	if !slices.Contains(names, "fmt") || !slices.Contains(names, "openssl") {
		t.Fatalf("expected declared dependencies in top-N results, got %#v", names)
	}

	fmtReport := requireDependencyReport(t, reportData.Dependencies, "fmt")
	if fmtReport.UsedExportsCount == 0 {
		t.Fatalf("expected mapped include usage for fmt, got %#v", fmtReport)
	}
	opensslReport := requireDependencyReport(t, reportData.Dependencies, "openssl")
	if opensslReport.TotalExportsCount != 0 {
		t.Fatalf("expected declared-only openssl report, got %#v", opensslReport)
	}
}

func TestAnalyseWithConanfileTxtSurfacesDeclaredDependenciesWithoutSourceUsage(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, conanManifestFile), `[requires]
fmt/10.2.1
[tool_requires]
cmake/3.27.0
[test_requires]
gtest/1.14.0
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if !hasWarning(reportData.Warnings, "no C/C++ source files found") {
		t.Fatalf("expected no-source warning, got %#v", reportData.Warnings)
	}

	names := dependencyNames(reportData.Dependencies)
	if !slices.Equal(names, []string{"fmt", "gtest"}) {
		t.Fatalf("unexpected Conan dependency inventory: %#v", names)
	}
}

func TestBuildDependencyReportFlagsUndeclaredUsage(t *testing.T) {
	scan := scanResult{
		Files: []fileScan{{
			Path: "src/main.cpp",
			Includes: []includeRecord{{
				Dependency: "fmt",
				Header:     "fmt/core.h",
				Location:   report.Location{File: "src/main.cpp", Line: 1, Column: 1},
			}},
		}},
	}

	dep, warnings := buildDependencyReport("fmt", scan, true)
	if !hasWarning(warnings, "not declared in vcpkg or conan manifests") {
		t.Fatalf("expected undeclared-usage warning, got %#v", warnings)
	}
	if !hasRiskCue(dep.RiskCues, "undeclared-package-usage") {
		t.Fatalf("expected undeclared risk cue, got %#v", dep.RiskCues)
	}
	if !hasRecommendation(dep.Recommendations, "declare-dependency-explicitly") {
		t.Fatalf("expected declaration recommendation, got %#v", dep.Recommendations)
	}
}

func dependencyNames(dependencies []report.DependencyReport) []string {
	names := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		names = append(names, dependency.Name)
	}
	return names
}

func requireDependencyReport(t *testing.T, dependencies []report.DependencyReport, name string) report.DependencyReport {
	t.Helper()
	for _, dependency := range dependencies {
		if dependency.Name == name {
			return dependency
		}
	}
	t.Fatalf("missing dependency report %q in %#v", name, dependencyNames(dependencies))
	return report.DependencyReport{}
}

func hasRiskCue(cues []report.RiskCue, code string) bool {
	return slices.ContainsFunc(cues, func(cue report.RiskCue) bool {
		return cue.Code == code
	})
}

func hasRecommendation(recommendations []report.Recommendation, code string) bool {
	return slices.ContainsFunc(recommendations, func(recommendation report.Recommendation) bool {
		return recommendation.Code == code
	})
}
