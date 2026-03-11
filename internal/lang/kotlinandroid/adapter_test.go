package kotlinandroid

import (
	"context"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAdapterMetadataAndDetect(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "kotlin-android" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	aliases := adapter.Aliases()
	for _, want := range []string{"android-kotlin", "gradle-android", "android"} {
		if !slices.Contains(aliases, want) {
			t.Fatalf("expected alias %q in %#v", want, aliases)
		}
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "app", "build.gradle"), "dependencies { implementation 'androidx.core:core-ktx:1.13.1' }\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app", "src", "main", "AndroidManifest.xml"), "<manifest package=\"com.example\"/>\n")

	ok, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true")
	}

	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected matched detection")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected confidence > 0, got %d", detection.Confidence)
	}
	if !slices.Contains(detection.Roots, filepath.Join(repo, "app")) {
		t.Fatalf("expected app module root in detection roots, got %#v", detection.Roots)
	}
}

func TestDetectWithConfidencePrunesRepoRootForSettingsOnlyWorkspace(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, settingsGradleName), "rootProject.name='demo'\ninclude ':app'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app", buildGradleName), "dependencies { implementation 'androidx.core:core-ktx:1.13.1' }\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app", "src", "main", "kotlin", "Main.kt"), "package com.example\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !slices.Contains(detection.Roots, filepath.Join(repo, "app")) {
		t.Fatalf("expected app module root in detection roots, got %#v", detection.Roots)
	}
	if slices.Contains(detection.Roots, repo) {
		t.Fatalf("did not expect repo root when only settings.gradle exists at root, got %#v", detection.Roots)
	}
}

func TestDetectWithConfidencePrunesRepoRootForAggregatorBuildGradle(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, settingsGradleName), "rootProject.name='demo'\ninclude ':app'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), `
plugins {
  id 'com.android.application' version '8.5.0' apply false
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "app", buildGradleName), "dependencies { implementation 'androidx.core:core-ktx:1.13.1' }\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app", "src", "main", "kotlin", "Main.kt"), "package com.example\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !slices.Contains(detection.Roots, filepath.Join(repo, "app")) {
		t.Fatalf("expected app module root in detection roots, got %#v", detection.Roots)
	}
	if slices.Contains(detection.Roots, repo) {
		t.Fatalf("did not expect repo root for aggregator root build.gradle, got %#v", detection.Roots)
	}
}

func TestDetectWithConfidenceRetainsRepoRootWhenRootBuildGradleExists(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, settingsGradleName), "rootProject.name='demo'\ninclude ':app'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "dependencies { implementation 'androidx.core:core-ktx:1.13.1' }\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app", buildGradleName), "dependencies { implementation 'com.squareup.okhttp3:okhttp:4.12.0' }\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app", "src", "main", "kotlin", "Main.kt"), "package com.example\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !slices.Contains(detection.Roots, repo) {
		t.Fatalf("expected repo root when root build.gradle exists, got %#v", detection.Roots)
	}
	if !slices.Contains(detection.Roots, filepath.Join(repo, "app")) {
		t.Fatalf("expected app root in detection roots, got %#v", detection.Roots)
	}
}

func TestAdapterAnalyseDependencyWithKotlinAndJavaImports(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "app", "build.gradle.kts"), `
plugins {
  id("com.android.application")
}

dependencies {
  implementation("androidx.core:core-ktx:1.13.1")
  implementation(group = "com.squareup.okhttp3", name = "okhttp", version = "4.12.0")
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "app", "src", "main", "AndroidManifest.xml"), "<manifest package=\"com.example\"/>\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app", "src", "main", "kotlin", "Main.kt"), `
package com.example

import androidx.core.content.ContextCompat
import okhttp3.OkHttpClient

fun run() {
  OkHttpClient()
  ContextCompat.checkSelfPermission(todo(), "x")
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "app", "src", "main", "java", "Main.java"), `
package com.example;

import okhttp3.OkHttpClient;

class Main {
  void run() {
    new OkHttpClient();
  }
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "okhttp",
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Language != "kotlin-android" {
		t.Fatalf("expected language kotlin-android, got %q", dep.Language)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used exports > 0")
	}
	if strings.Contains(strings.Join(reportData.Warnings, "\n"), "gradle.lockfile not found") == false {
		t.Fatalf("expected lockfile warning when lockfile is absent, got %#v", reportData.Warnings)
	}
}

func TestAdapterAnalyseTopNIncludesDeclaredDependencies(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), `
dependencies {
  implementation "androidx.core:core-ktx:1.13.1"
  implementation "com.squareup.okhttp3:okhttp:4.12.0"
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, gradleLockfileName), `
androidx.core:core-ktx:1.13.1=compileClasspath,runtimeClasspath
com.squareup.okhttp3:okhttp:4.12.0=compileClasspath,runtimeClasspath
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", "Main.kt"), `
import okhttp3.OkHttpClient

fun run() { OkHttpClient() }
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}
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
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), `
dependencies {
  implementation "com.squareup.okhttp3:okhttp:4.12.0"
  implementation "androidx.core:core-ktx:1.13.1"
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", "Main.kt"), `
import androidx.core.content.ContextCompat
import okhttp3.OkHttpClient as Client

fun run() {
  Client()
  ContextCompat.checkSelfPermission(todo(), "x")
}
`)

	adapter := NewAdapter()
	first, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	if err != nil {
		t.Fatalf("analyse first pass: %v", err)
	}
	second, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	if err != nil {
		t.Fatalf("analyse second pass: %v", err)
	}
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
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), manifest)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", "Main.kt"), `
import alpha.client.Widget
import foo.bar.Baz

fun run() {
  Widget()
  Baz()
}
`)
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

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	warnings := strings.Join(reportData.Warnings, "\n")
	if !strings.Contains(warnings, "conservatively attributed") {
		t.Fatalf("expected conservative fallback warning, got %#v", reportData.Warnings)
	}
	if !strings.Contains(warnings, "undeclared dependencies") {
		t.Fatalf("expected undeclared dependency warning, got %#v", reportData.Warnings)
	}
	if !strings.Contains(warnings, "matched multiple Gradle dependencies") {
		t.Fatalf("expected ambiguous mapping warning, got %#v", reportData.Warnings)
	}
}
