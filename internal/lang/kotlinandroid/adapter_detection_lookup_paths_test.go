package kotlinandroid

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestKotlinAndroidDetectionAndHelperMoreBranches(t *testing.T) {
	repo := t.TempDir()
	writeRepoFiles(t, repo, map[string]string{
		buildGradleName: testEmptyDependencies,
		filepath.Join("src", "main", "kotlin", "Main.kt"): testAppSource,
	})

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected android-specific gate to clear non-android root-only detection, got %#v", detection)
	}
	if len(detection.Roots) != 0 {
		t.Fatalf("expected cleared roots without android-specific signal, got %#v", detection.Roots)
	}

	if buildFileSignalsAndroidPlugin(repo, repo) {
		t.Fatalf("expected directory read to fail closed for android plugin detection")
	}
	if got := androidManifestModuleRoot(filepath.FromSlash("src/main/androidmanifest.xml")); got != "" {
		t.Fatalf("expected repo-level manifest path to resolve to empty root, got %q", got)
	}
	if got := sourceLayoutModuleRoot(filepath.FromSlash("src/main/resources/Main.kt")); got != "" {
		t.Fatalf("expected unsupported source layout to return empty root, got %q", got)
	}
	if hasRootSourceLayout(t.TempDir()) {
		t.Fatalf("did not expect source layout detection without a src tree")
	}

	roots := map[string]struct{}{filepath.Join(repo, "app"): {}}
	pruneKotlinAndroidRoots(repo, roots)
	if len(roots) != 1 {
		t.Fatalf("expected prune to return early when repo root is absent, got %#v", roots)
	}

	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
		t.Fatalf("expected analyse to fail on invalid repo path")
	}
	if _, _, err := readKotlinAndroidSource(repo, filepath.Join(repo, "missing.kt")); err == nil {
		t.Fatalf("expected readKotlinAndroidSource to return missing-file error")
	}
}

func TestKotlinAndroidLookupAndGradleMoreBranches(t *testing.T) {
	lookups := dependencyLookups{
		Prefixes: map[string]string{
			"com.example":      "short",
			"com.example.deep": "long",
		},
		Aliases: map[string]string{
			"com": "root",
		},
		DeclaredDependencies: map[string]struct{}{},
	}

	dependency, ambiguous := resolveDependency("com.example.deep.Type", lookups)
	if dependency != "long" || len(ambiguous) != 0 {
		t.Fatalf("expected longest prefix match without ambiguity, got dependency=%q ambiguous=%#v", dependency, ambiguous)
	}
	dependency, ambiguous = resolveDependency("com.other.Type", lookups)
	if dependency != "root" || len(ambiguous) != 0 {
		t.Fatalf("expected alias fallback, got dependency=%q ambiguous=%#v", dependency, ambiguous)
	}

	if _, ok := buildImportRecord([]string{"", "", "", ""}, "", "dep"); ok {
		t.Fatalf("expected empty-module import record construction to fail")
	}

	prefixes, aliases := artifactLookupStrategy("", "alpha-runtime")
	if len(prefixes) != 0 || len(aliases) != 1 || aliases[0] != "alpha.runtime" {
		t.Fatalf("unexpected artifact lookup strategy result: prefixes=%#v aliases=%#v", prefixes, aliases)
	}

	if descriptors := parseGradleMapDependencies(`implementation(group: "com.example")`); len(descriptors) != 0 {
		t.Fatalf("expected incomplete map-style gradle dependency to be ignored, got %#v", descriptors)
	}

	descriptors := dedupeDescriptors([]dependencyDescriptor{
		{Name: "alpha", Group: "z.group", Artifact: "alpha"},
		{Name: "alpha", Group: "a.group", Artifact: "alpha"},
	})
	if len(descriptors) != 2 || descriptors[0].Group != "a.group" {
		t.Fatalf("expected dedupeDescriptors to tie-break on group, got %#v", descriptors)
	}
}

func TestKotlinAndroidAdditionalReachableBranches(t *testing.T) {
	t.Run("detection walk and manifest helpers", testKotlinAndroidDetectionWalkAndManifestHelpers)
	t.Run("analyse normalize error when cwd is gone", testKotlinAndroidAnalyseNormalizeErrorWhenCWDIsGone)
}

func testKotlinAndroidDetectionWalkAndManifestHelpers(t *testing.T) {
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected missing repo to fail detection walk")
	}
	if got := androidManifestModuleRoot(filepath.FromSlash("src/main/AndroidManifest.xml/ignored")); got != "" {
		t.Fatalf("expected rootless manifest path to resolve empty, got %q", got)
	}

	repo := t.TempDir()
	roots := map[string]struct{}{
		filepath.Join(repo, "module"): {},
		filepath.Join(repo, "other"):  {},
	}
	pruneKotlinAndroidRoots(repo, roots)
	if len(roots) != 2 {
		t.Fatalf("expected repo-absent roots to remain untouched, got %#v", roots)
	}
}

func testKotlinAndroidAnalyseNormalizeErrorWhenCWDIsGone(t *testing.T) {
	testutil.ChdirRemovedDir(t)

	if _, err := NewAdapter().Analyse(context.Background(), language.Request{}); err == nil {
		t.Fatalf("expected analyse to fail when cwd cannot be resolved")
	}
}
