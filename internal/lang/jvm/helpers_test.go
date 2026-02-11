package jvm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestJVMParsingHelpers(t *testing.T) {
	content := []byte("package com.example.app;\nimport java.util.List;\nimport org.junit.jupiter.api.Test;\nimport com.acme.lib.Widget;\n")
	pkg := parsePackage(content)
	if pkg != "com.example.app" {
		t.Fatalf("unexpected parsed package: %q", pkg)
	}

	prefixes := map[string]string{"org.junit.jupiter": "junit-jupiter-api"}
	aliases := map[string]string{"com.acme": "acme-lib"}
	imports := parseImports(content, "App.java", pkg, prefixes, aliases)
	if len(imports) != 2 {
		t.Fatalf("expected two non-stdlib imports, got %#v", imports)
	}
	if imports[0].Dependency == "" || imports[1].Dependency == "" {
		t.Fatalf("expected dependencies to be resolved: %#v", imports)
	}

	if !shouldIgnoreImport("java.util.List", pkg) {
		t.Fatalf("expected java stdlib import to be ignored")
	}
	if !shouldIgnoreImport("com.example.app.internal.Type", pkg) {
		t.Fatalf("expected same-package import to be ignored")
	}
	if shouldIgnoreImport("com.other.lib.Type", pkg) {
		t.Fatalf("did not expect external import to be ignored")
	}

	if resolveDependency("org.junit.jupiter.api.Test", prefixes, aliases) != "junit-jupiter-api" {
		t.Fatalf("expected prefix resolution")
	}
	if resolveDependency("com.acme.lib.Widget", map[string]string{}, aliases) != "acme-lib" {
		t.Fatalf("expected alias resolution")
	}
	if got := fallbackDependency("single"); got != "single" {
		t.Fatalf("unexpected fallback dependency for single segment: %q", got)
	}
	if got := fallbackDependency("a.b.c"); got != "a.b" {
		t.Fatalf("unexpected fallback dependency for multi segment: %q", got)
	}
	if got := lastModuleSegment("a.b.C"); got != "C" {
		t.Fatalf("unexpected last module segment: %q", got)
	}
	if col := firstContentColumn("\t import x"); col <= 1 {
		t.Fatalf("expected firstContentColumn to detect indentation")
	}
	if got := stripLineComment("import a // trailing"); got != "import a " {
		t.Fatalf("unexpected stripLineComment result: %q", got)
	}
}

func TestJVMDescriptorAndBuildFileHelpers(t *testing.T) {
	descriptors := []dependencyDescriptor{
		{Name: "okhttp", Group: "com.squareup", Artifact: "okhttp"},
		{Name: "okhttp", Group: "com.squareup", Artifact: "okhttp"},
		{Name: "junit", Group: "org.junit", Artifact: "junit"},
	}
	deduped := dedupeAndSortDescriptors(descriptors)
	if len(deduped) != 2 {
		t.Fatalf("expected deduped descriptors, got %#v", deduped)
	}

	prefixes, aliases := buildDescriptorLookups(deduped)
	if prefixes["com.squareup.okhttp"] == "" {
		t.Fatalf("expected artifact prefix lookup")
	}
	if aliases["junit"] == "" {
		t.Fatalf("expected alias lookup for artifact")
	}

	if !matchesBuildFile("build.gradle", []string{"build.gradle"}) || matchesBuildFile("foo.txt", []string{"build.gradle"}) {
		t.Fatalf("unexpected build file matching")
	}
	if !shouldSkipDir(".gradle") || shouldSkipDir("src") {
		t.Fatalf("unexpected shouldSkipDir behavior")
	}

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "pom.xml"), `<dependency><groupId>org.junit</groupId><artifactId>junit</artifactId></dependency>`)
	writeFile(t, filepath.Join(repo, "build.gradle"), `implementation 'com.squareup.okhttp3:okhttp:4.12.0'`)
	poms := parsePomDependencies(repo)
	gradle := parseGradleDependencies(repo)
	if len(poms) == 0 || len(gradle) == 0 {
		t.Fatalf("expected pom and gradle dependencies, got pom=%#v gradle=%#v", poms, gradle)
	}
	all, _, _ := collectDeclaredDependencies(repo)
	names := make([]string, 0, len(all))
	for _, dep := range all {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "junit") || !slices.Contains(names, "okhttp") {
		t.Fatalf("expected declared dependencies from build files, got %#v", names)
	}
}

func TestJVMScanAndRequestedDependencyBranches(t *testing.T) {
	if _, err := scanRepo(context.Background(), "", nil, nil); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected fs.ErrInvalid for empty repo path, got %v", err)
	}

	repo := t.TempDir()
	result, err := scanRepo(context.Background(), repo, map[string]string{}, map[string]string{})
	if err != nil {
		t.Fatalf("scan empty repo: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("expected warning for repo without source files")
	}

	deps, warnings := buildRequestedJVMDependencies(language.Request{}, scanResult{})
	if deps != nil {
		t.Fatalf("expected nil dependency list when no target is provided")
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning for missing dependency/topN target")
	}
}

func TestJVMDetectAndWalkBranches(t *testing.T) {
	adapter := NewAdapter()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "pom.xml"), "<project/>")
	writeFile(t, filepath.Join(repo, "build.gradle"), "")
	writeFile(t, filepath.Join(repo, "build.gradle.kts"), "")
	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched || detection.Confidence != 95 {
		t.Fatalf("expected matched detection capped at 95, got %#v", detection)
	}

	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}
	var fileEntry fs.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			fileEntry = entry
			break
		}
	}
	if fileEntry == nil {
		t.Fatalf("expected file entry")
	}
	visited := 1
	roots := make(map[string]struct{})
	detect := &language.Detection{}
	err = walkJVMDetectionEntry(filepath.Join(repo, fileEntry.Name()), fileEntry, roots, detect, &visited, 1)
	if !errors.Is(err, fs.SkipAll) {
		t.Fatalf("expected fs.SkipAll when maxFiles exceeded, got %v", err)
	}
}

func TestJVMParseHelpersEdgeBranches(t *testing.T) {
	matches := [][]string{
		{"only-one"},
		{"", "", ""},
		{"full", "org.example", "lib"},
	}
	descriptors := parseDependencyDescriptorsFromMatches(matches)
	if len(descriptors) != 1 || descriptors[0].Name != "lib" {
		t.Fatalf("unexpected descriptor parse result: %#v", descriptors)
	}

	pattern := regexp.MustCompile(`does-not-match`)
	if got := parseGradleMatches("", pattern); len(got) != 0 {
		t.Fatalf("expected no gradle matches for nil pattern, got %#v", got)
	}

	if got := fallbackDependency(""); got != "" {
		t.Fatalf("expected empty fallback dependency for empty module, got %q", got)
	}
	if got := lastModuleSegment(""); got != "" {
		t.Fatalf("expected empty last module segment for empty module, got %q", got)
	}
}
