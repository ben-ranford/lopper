package kotlinandroid

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

const (
	testSettingsGradle = "rootProject.name='demo'\ninclude ':app'\n"
	testCoreKtxBuild   = "dependencies { implementation 'androidx.core:core-ktx:1.13.1' }\n"
	testAppSource      = "package com.example\n"
	testAppManifest    = "<manifest package=\"com.example\"/>\n"
	testAggregatorRoot = "plugins {\n  id 'com.android.application' version '8.5.0' apply false\n}\n"
)

func writeRepoFiles(t *testing.T, repo string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		testutil.MustWriteFile(t, filepath.Join(repo, name), content)
	}
}

func writeAppModule(t *testing.T, repo, buildContent string) string {
	t.Helper()
	writeRepoFiles(t, repo, map[string]string{
		filepath.Join("app", buildGradleName):                    buildContent,
		filepath.Join("app", "src", "main", "kotlin", "Main.kt"): testAppSource,
	})
	return filepath.Join(repo, "app")
}

func writeAppBuildAndManifest(t *testing.T, repo, buildContent string) string {
	t.Helper()
	writeRepoFiles(t, repo, map[string]string{
		filepath.Join("app", buildGradleName):                      buildContent,
		filepath.Join("app", "src", "main", "AndroidManifest.xml"): testAppManifest,
	})
	return filepath.Join(repo, "app")
}

func writeWorkspaceApp(t *testing.T, repo, rootBuild, appBuild string) string {
	t.Helper()
	files := map[string]string{
		settingsGradleName: testSettingsGradle,
	}
	if rootBuild != "" {
		files[buildGradleName] = rootBuild
	}
	writeRepoFiles(t, repo, files)
	return writeAppModule(t, repo, appBuild)
}

func requirePositiveDetection(t *testing.T, adapter *Adapter, repo string) language.Detection {
	t.Helper()
	ok, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true for %s", repo)
	}

	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched || detection.Confidence <= 0 {
		t.Fatalf("unexpected detection result: %#v", detection)
	}
	return detection
}

func mustAnalyse(t *testing.T, req language.Request) report.Report {
	t.Helper()
	reportData, err := NewAdapter().Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	return reportData
}

func requireContains(t *testing.T, got, want, label string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected %s to contain %q, got %q", label, want, got)
	}
}

func requireWarningContains(t *testing.T, warnings []string, want string) {
	t.Helper()
	requireContains(t, strings.Join(warnings, "\n"), want, "warnings")
}

func requireRootsContain(t *testing.T, roots []string, want ...string) {
	t.Helper()
	for _, root := range want {
		if !slices.Contains(roots, root) {
			t.Fatalf("expected root %q in %#v", root, roots)
		}
	}
}

func requireRootExcluded(t *testing.T, roots []string, unwanted string) {
	t.Helper()
	if slices.Contains(roots, unwanted) {
		t.Fatalf("did not expect root %q in %#v", unwanted, roots)
	}
}

func recommendationCodes(recommendations []report.Recommendation) []string {
	codes := make([]string, 0, len(recommendations))
	for _, recommendation := range recommendations {
		codes = append(codes, recommendation.Code)
	}
	return codes
}
