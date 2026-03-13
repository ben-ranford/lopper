package kotlinandroid

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestAdapterMetadataAndDetect(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != testKotlinAndroidLanguage {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	aliases := adapter.Aliases()
	for _, want := range []string{"android-kotlin", "gradle-android", "android"} {
		if !slices.Contains(aliases, want) {
			t.Fatalf("expected alias %q in %#v", want, aliases)
		}
	}

	repo := t.TempDir()
	appRoot := writeAppBuildAndManifest(t, repo, testCoreKtxBuild)
	detection := requirePositiveDetection(t, adapter, repo)
	requireRootsContain(t, detection.Roots, appRoot)
}

func TestDetectWithConfidenceRootSelection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		rootBuild    string
		wantRepoRoot bool
	}{
		{name: "settings-only-workspace"},
		{name: "aggregator-root-build", rootBuild: testAggregatorRoot},
		{name: "dependency-declaring-root-build", rootBuild: testCoreKtxBuild, wantRepoRoot: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			appRoot := writeWorkspaceApp(t, repo, tc.rootBuild, testCoreKtxBuild)

			detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
			if err != nil {
				t.Fatalf("detect with confidence: %v", err)
			}
			requireRootsContain(t, detection.Roots, appRoot)
			if tc.wantRepoRoot {
				requireRootsContain(t, detection.Roots, repo)
				return
			}
			requireRootExcluded(t, detection.Roots, repo)
		})
	}
}

func TestDetectWithConfidenceIgnoresPlainGradleRepos(t *testing.T) {
	repo := t.TempDir()
	writeRepoFiles(t, repo, map[string]string{
		buildGradleName: testEmptyDependencies,
		filepath.Join("src", "main", "kotlin", testMainSourceFileName): "package demo\n",
	})

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected non-Android Gradle repo to remain unmatched, got %#v", detection)
	}
}

func TestAdapterAnalyseDependencyWithKotlinAndJavaImports(t *testing.T) {
	repo := t.TempDir()
	writeRepoFiles(t, repo, map[string]string{
		filepath.Join("app", "build.gradle.kts"): `
plugins {
  id("com.android.application")
}

dependencies {
  implementation("androidx.core:core-ktx:1.13.1")
  implementation(group = "com.squareup.okhttp3", name = "okhttp", version = "4.12.0")
}
`,
		filepath.Join("app", "src", "main", "AndroidManifest.xml"): testAppManifest,
		filepath.Join("app", "src", "main", "kotlin", testMainSourceFileName): `
package com.example

import androidx.core.content.ContextCompat
import okhttp3.OkHttpClient

fun run() {
  OkHttpClient()
  ContextCompat.checkSelfPermission(todo(), "x")
}
`,
		filepath.Join("app", "src", "main", "java", "Main.java"): `
package com.example;

import okhttp3.OkHttpClient;

class Main {
  void run() {
    new OkHttpClient();
  }
}
`,
	})

	reportData := mustAnalyse(t, language.Request{
		RepoPath:   repo,
		Dependency: "okhttp",
	})
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Language != testKotlinAndroidLanguage {
		t.Fatalf("expected language kotlin-android, got %q", dep.Language)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used exports > 0")
	}
	requireWarningContains(t, reportData.Warnings, "gradle.lockfile not found")
}

func TestAdapterAnalyseTopNIncludesDeclaredDependencies(t *testing.T) {
	repo := t.TempDir()
	writeRepoFiles(t, repo, map[string]string{
		buildGradleName: `
dependencies {
  implementation "androidx.core:core-ktx:1.13.1"
  implementation "com.squareup.okhttp3:okhttp:4.12.0"
}
`,
		gradleLockfileName: `
androidx.core:core-ktx:1.13.1=compileClasspath,runtimeClasspath
com.squareup.okhttp3:okhttp:4.12.0=compileClasspath,runtimeClasspath
`,
		filepath.Join("src", "main", "kotlin", testMainSourceFileName): `
import okhttp3.OkHttpClient

fun run() { OkHttpClient() }
`,
	})

	reportData := mustAnalyse(t, language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in topN report")
	}
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "okhttp") {
		t.Fatalf("expected okhttp in top dependencies, got %#v", names)
	}
	if slices.Contains(reportData.Warnings, "gradle.lockfile not found; dependency versions may be incomplete") {
		t.Fatalf("did not expect missing-lockfile warning when lockfile exists")
	}
}

func TestAdapterAnalyseStableDependencyOrdering(t *testing.T) {
	repo := t.TempDir()
	writeRepoFiles(t, repo, map[string]string{
		buildGradleName: `
dependencies {
  implementation "com.squareup.okhttp3:okhttp:4.12.0"
  implementation "androidx.core:core-ktx:1.13.1"
}
`,
		filepath.Join("src", "main", "kotlin", testMainSourceFileName): `
import androidx.core.content.ContextCompat
import okhttp3.OkHttpClient as Client

fun run() {
  Client()
  ContextCompat.checkSelfPermission(todo(), "x")
}
`,
	})

	req := language.Request{RepoPath: repo, TopN: 10}
	first := mustAnalyse(t, req)
	second := mustAnalyse(t, req)
	if !reflect.DeepEqual(first.Dependencies, second.Dependencies) {
		t.Fatalf("expected stable dependency ordering across runs")
	}
}

func TestDependencyParsingHelpers(t *testing.T) {
	content := `
dependencies {
  implementation("androidx.core:core-ktx:1.13.1")
  implementation(group = "com.squareup.okhttp3", name = "okhttp", version = "4.12.0")
  implementation name: 'core-ktx', group: 'androidx.core', version: '1.13.1'
  api group: 'com.google.guava', name: 'guava', version: '33.2.0-jre'
  api name: 'activity-ktx', group: 'androidx.activity'
}
`
	descriptors := parseGradleDependencyContent(content)
	if len(descriptors) != 4 {
		t.Fatalf("expected four parsed descriptors after dedupe, got %#v", descriptors)
	}
	seen := make(map[string]struct{})
	for _, descriptor := range descriptors {
		seen[descriptor.Group+":"+descriptor.Artifact] = struct{}{}
	}
	for _, want := range []string{
		"androidx.core:core-ktx",
		"com.squareup.okhttp3:okhttp",
		"com.google.guava:guava",
		"androidx.activity:activity-ktx",
	} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("expected descriptor %q in %#v", want, descriptors)
		}
	}

	lock := parseGradleLockfileContent(`
# This is a Gradle generated file
androidx.core:core-ktx:1.13.1=compileClasspath,runtimeClasspath
com.squareup.okhttp3:okhttp:4.12.0=compileClasspath,runtimeClasspath
`)
	if len(lock) != 2 {
		t.Fatalf("expected two lockfile descriptors, got %#v", lock)
	}
}

func TestDependencyLookupAmbiguityAndFallbackWarnings(t *testing.T) {
	repo := t.TempDir()
	manifest := `
dependencies {
  implementation("com.example.alpha:alpha-core:1.0.0")
  implementation("org.sample.alpha:alpha-runtime:1.0.0")
}
`
	writeRepoFiles(t, repo, map[string]string{
		buildGradleName: manifest,
		filepath.Join("src", "main", "kotlin", testMainSourceFileName): `
import alpha.client.Widget
import foo.bar.Baz

fun run() {
  Widget()
  Baz()
}
`,
	})
	parsedInline := parseGradleDependencyContent(manifest)
	if len(parsedInline) != 2 {
		t.Fatalf("expected two inline parsed gradle descriptors, got %#v", parsedInline)
	}
	_, lookups, _ := collectDeclaredDependencies(repo)
	parsed := parseGradleDependencies(repo)
	if len(parsed) != 2 {
		t.Fatalf("expected two parsed gradle descriptors, got %#v", parsed)
	}
	if _, ok := lookups.Ambiguous["alpha"]; !ok {
		t.Fatalf("expected alpha ambiguity in lookup map, got %#v", lookups.Ambiguous)
	}

	reportData := mustAnalyse(t, language.Request{RepoPath: repo, TopN: 10})
	for _, want := range []string{
		"conservatively attributed",
		"undeclared dependencies",
		"matched multiple Gradle dependencies",
	} {
		requireWarningContains(t, reportData.Warnings, want)
	}
}
