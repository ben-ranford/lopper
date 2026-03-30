package cpp

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
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

func TestCollectVcpkgLockDependenciesTraversesArraysAndContainers(t *testing.T) {
	out := make(map[string]struct{})
	payload := []any{
		map[string]any{"name": "fmt"},
		map[string]any{
			"packages": map[string]any{
				"openssl": map[string]any{
					"dependencies": []any{
						map[string]any{"name": "zlib"},
					},
				},
				"libuv": map[string]any{},
			},
		},
	}

	collectVcpkgLockDependencies(payload, out)

	if got := sortedDependencySet(out); !slices.Equal(got, []string{"fmt", "libuv", "openssl", "zlib"}) {
		t.Fatalf("unexpected vcpkg lock dependencies: %#v", got)
	}
}

func TestCollectConanLockDependenciesTraversesArraysAndMaps(t *testing.T) {
	out := make(map[string]struct{})
	payload := []any{
		map[string]any{"ref": "fmt/10.2.1"},
		map[string]any{
			"graph_lock": map[string]any{
				"nodes": []any{
					map[string]any{"ref": "openssl/3.2.1#revision"},
					map[string]any{
						"children": map[string]any{
							"zlib": map[string]any{"ref": "zlib/1.3.1"},
						},
					},
					map[string]any{"ref": "invalid"},
				},
			},
		},
	}

	collectConanLockDependencies(payload, out)

	if got := sortedDependencySet(out); !slices.Equal(got, []string{"fmt", "openssl", "zlib"}) {
		t.Fatalf("unexpected Conan lock dependencies: %#v", got)
	}
}

func TestDependencyFromConanReferenceTrimsPrefixesAndComments(t *testing.T) {
	t.Run("prefix and comment", func(t *testing.T) {
		if got := dependencyFromConanReference("&:fmt/10.2.1 # root"); got != "fmt" {
			t.Fatalf("expected normalized dependency from prefixed ref, got %q", got)
		}
	})

	t.Run("invalid reference", func(t *testing.T) {
		if got := dependencyFromConanReference("fmt"); got != "" {
			t.Fatalf("expected invalid reference to be ignored, got %q", got)
		}
	})
}

func TestParseJSONDependencyLockHandlesEmptyAndInvalidContent(t *testing.T) {
	if dependencies, warnings := parseVcpkgLock(nil); len(dependencies) != 0 || len(warnings) != 0 {
		t.Fatalf("expected empty lock content to be ignored, got deps=%#v warnings=%#v", dependencies, warnings)
	}

	dependencies, warnings := parseConanLock([]byte("{"))
	if len(dependencies) != 0 {
		t.Fatalf("expected invalid lock content to produce no dependencies, got %#v", dependencies)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "failed to parse conan.lock") {
		t.Fatalf("expected invalid lock warning, got %#v", warnings)
	}
}

func TestDependencyCatalogAddAndSourcesHandleEdgeCases(t *testing.T) {
	catalog := newDependencyCatalog()
	catalog.add("", "vcpkg manifest")
	catalog.add("fmt", "")
	catalog.add("fmt", "vcpkg manifest")
	catalog.add("fmt", "conan.lock")

	if !catalog.contains("fmt") {
		t.Fatalf("expected fmt dependency to be recorded")
	}
	if got := catalog.sources("fmt"); !slices.Equal(got, []string{"conan.lock", "vcpkg manifest"}) {
		t.Fatalf("unexpected recorded sources: %#v", got)
	}
	if got := catalog.sources("missing"); len(got) != 0 {
		t.Fatalf("expected missing dependency to have no sources, got %#v", got)
	}
}

func TestLoadDependencyManifestMissingFileIsIgnored(t *testing.T) {
	repo := t.TempDir()
	catalog := newDependencyCatalog()

	warnings, err := loadDependencyManifest(repo, filepath.Join(repo, vcpkgLockFile), &catalog)
	if err != nil {
		t.Fatalf("loadDependencyManifest: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for missing manifest, got %#v", warnings)
	}
	if got := catalog.list(); len(got) != 0 {
		t.Fatalf("expected missing manifest to add no dependencies, got %#v", got)
	}
}

func TestLoadDependencyManifestUnknownFileIsIgnored(t *testing.T) {
	repo := t.TempDir()
	catalog := newDependencyCatalog()
	path := filepath.Join(repo, "deps.txt")
	testutil.MustWriteFile(t, path, "fmt")

	warnings, err := loadDependencyManifest(repo, path, &catalog)
	if err != nil {
		t.Fatalf("loadDependencyManifest: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for unknown manifest type, got %#v", warnings)
	}
	if got := catalog.list(); len(got) != 0 {
		t.Fatalf("expected unknown manifest to add no dependencies, got %#v", got)
	}
}

func TestDedupeCPPWarningsRemovesBlankAndDuplicates(t *testing.T) {
	got := dedupeCPPWarnings([]string{" first ", "", "first", "second", " second "})
	if !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("unexpected deduped warnings: %#v", got)
	}
}

func TestParseManifestHelpersHandleEmptyContent(t *testing.T) {
	if dependencies, warnings := parseVcpkgManifest(nil); len(dependencies) != 0 || len(warnings) != 0 {
		t.Fatalf("expected empty vcpkg manifest to be ignored, got deps=%#v warnings=%#v", dependencies, warnings)
	}
	if dependencies, warnings := parseConanfileTxt(nil); len(dependencies) != 0 || len(warnings) != 0 {
		t.Fatalf("expected empty conanfile to be ignored, got deps=%#v warnings=%#v", dependencies, warnings)
	}
}

func TestCorrelateDeclaredDependencyReturnsOriginalWhenMatchIsAmbiguous(t *testing.T) {
	catalog := newDependencyCatalog()
	catalog.add("boost-asio", "vcpkg manifest")
	catalog.add("boost-filesystem", "vcpkg manifest")

	if got := correlateDeclaredDependency("boost", catalog); got != "boost" {
		t.Fatalf("expected ambiguous prefix to keep original token, got %q", got)
	}
}

func dependencyNames(dependencies []report.DependencyReport) []string {
	names := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		names = append(names, dependency.Name)
	}
	return names
}

func sortedDependencySet(values map[string]struct{}) []string {
	items := make([]string, 0, len(values))
	for value := range values {
		items = append(items, value)
	}
	slices.Sort(items)
	return items
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
