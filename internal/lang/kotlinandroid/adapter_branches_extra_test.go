package kotlinandroid

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestScanRepoAndDetectErrorBranches(t *testing.T) {
	if _, err := scanRepo(context.Background(), "", dependencyLookups{}); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected fs.ErrInvalid for empty repo path, got %v", err)
	}

	adapter := NewAdapter()
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	testutil.MustWriteFile(t, repoFile, "x")
	if _, err := adapter.DetectWithConfidence(context.Background(), repoFile); err == nil {
		t.Fatalf("expected detect-with-confidence error for non-directory repo path")
	}
}

func TestImportHelpersAndRiskRecommendations(t *testing.T) {
	if !shouldIgnoreImport("android.os.Bundle", "") {
		t.Fatalf("expected android framework import to be ignored")
	}
	if shouldIgnoreImport("androidx.core.content.ContextCompat", "") {
		t.Fatalf("did not expect androidx import to be ignored")
	}
	if got := fallbackDependency("foo.bar.Baz"); got != "foo.bar" {
		t.Fatalf("unexpected fallback dependency: %q", got)
	}
	if got := lastModuleSegment("a.b.C"); got != "C" {
		t.Fatalf("unexpected last module segment: %q", got)
	}

	scan := scanResult{
		Files: []fileScan{{
			Path: "Main.kt",
			Imports: []importBinding{{
				Dependency: "dep",
				Module:     "x.dep",
				Name:       "*",
				Local:      "*",
				Wildcard:   true,
			}},
			Usage: map[string]int{"*": 1},
		}},
		AmbiguousDependencies:  map[string]struct{}{"dep": {}},
		UndeclaredDependencies: map[string]struct{}{"dep": {}},
	}
	dep, _ := buildDependencyReport("dep", scan)
	if len(dep.RiskCues) < 3 {
		t.Fatalf("expected wildcard/ambiguous/undeclared risk cues, got %#v", dep.RiskCues)
	}
	recs := buildRecommendations(dep)
	codes := make([]string, 0, len(recs))
	for _, rec := range recs {
		codes = append(codes, rec.Code)
	}
	for _, want := range []string{"avoid-wildcard-imports", "review-ambiguous-gradle-mappings", "declare-missing-gradle-dependency"} {
		if !strings.Contains(strings.Join(codes, ","), want) {
			t.Fatalf("expected recommendation %q in %#v", want, codes)
		}
	}
}

func TestLookupBuildersAndMergeDescriptors(t *testing.T) {
	manifest := []dependencyDescriptor{{Name: "okhttp", Group: "com.squareup.okhttp3", Artifact: "okhttp"}}
	lock := []dependencyDescriptor{{Name: "okhttp", Group: "com.squareup.okhttp3", Artifact: "okhttp", Version: "4.12.0"}}
	merged := mergeDescriptors(manifest, lock)
	if len(merged) != 1 {
		t.Fatalf("expected one merged descriptor, got %#v", merged)
	}
	if !merged[0].FromManifest || !merged[0].FromLockfile || merged[0].Version != "4.12.0" {
		t.Fatalf("expected merged descriptor metadata enrichment, got %#v", merged[0])
	}

	lookups := buildDescriptorLookups([]dependencyDescriptor{
		{Name: "alpha-core", Group: "com.example.alpha", Artifact: "alpha-core"},
		{Name: "alpha-runtime", Group: "org.sample.alpha", Artifact: "alpha-runtime"},
	})
	if _, ok := lookups.Ambiguous["alpha"]; !ok {
		t.Fatalf("expected ambiguous alias for core, got %#v", lookups.Ambiguous)
	}
	if _, ok := lookups.DeclaredDependencies["alpha-core"]; !ok {
		t.Fatalf("expected declared dependency for alpha-core")
	}

	dep, ambiguous := resolveDependency("alpha.client.Type", lookups)
	if dep == "" {
		t.Fatalf("expected resolved dependency from lookups")
	}
	if len(ambiguous) == 0 {
		t.Fatalf("expected ambiguity metadata for overlapping lookups")
	}
}

func TestBuildRequestedDependenciesAndWarnings(t *testing.T) {
	scan := scanResult{}
	deps, warnings := buildRequestedKotlinAndroidDependencies(language.Request{}, scan)
	if len(deps) != 0 {
		t.Fatalf("expected no dependency rows without target, got %#v", deps)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning when no dependency or topN target provided")
	}

	rec := report.DependencyReport{RiskCues: []report.RiskCue{{Code: "x"}}}
	if hasRiskCue(rec, "missing") {
		t.Fatalf("did not expect missing risk cue")
	}
}

func TestDetectAndWalkBranchGuards(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "dependencies {}\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAdapter().DetectWithConfidence(ctx, repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}

	testutil.MustWriteFile(t, filepath.Join(repo, "Main.kt"), "package demo\n")
	if err := os.Mkdir(filepath.Join(repo, ".gradle"), 0o755); err != nil {
		t.Fatalf("mkdir .gradle: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var mainEntry fs.DirEntry
	var gradleDirEntry fs.DirEntry
	for _, entry := range entries {
		if entry.Name() == "Main.kt" {
			mainEntry = entry
		}
		if entry.Name() == ".gradle" {
			gradleDirEntry = entry
		}
	}
	if mainEntry == nil || gradleDirEntry == nil {
		t.Fatalf("expected test entries, got %#v", entries)
	}

	roots := map[string]struct{}{}
	detection := language.Detection{}
	visited := 0
	if err := walkKotlinAndroidDetectionEntry(filepath.Join(repo, ".gradle"), gradleDirEntry, roots, &detection, &visited, 5); !errors.Is(err, filepath.SkipDir) {
		t.Fatalf("expected SkipDir for skipped directory, got %v", err)
	}

	visited = 5
	if err := walkKotlinAndroidDetectionEntry(filepath.Join(repo, "Main.kt"), mainEntry, roots, &detection, &visited, 1); !errors.Is(err, fs.SkipAll) {
		t.Fatalf("expected SkipAll when file cap is exceeded, got %v", err)
	}
}

func TestModuleRootAndPathHelpers(t *testing.T) {
	if got := androidManifestModuleRoot(filepath.FromSlash("src/main/AndroidManifest.xml")); got != "" {
		t.Fatalf("expected empty root for src/main manifest at repo root, got %q", got)
	}
	if got := androidManifestModuleRoot(filepath.FromSlash("app/src/main/AndroidManifest.xml")); got != filepath.FromSlash("app") {
		t.Fatalf("expected app module root, got %q", got)
	}
	if got := androidManifestModuleRoot(filepath.FromSlash("app/src/debug/AndroidManifest.xml")); got != "" {
		t.Fatalf("expected empty root for non-main manifest, got %q", got)
	}

	if got := sourceLayoutModuleRoot(filepath.FromSlash("src/main/kotlin/Main.kt")); got != "" {
		t.Fatalf("expected empty root for src/main at repo root, got %q", got)
	}
	if got := sourceLayoutModuleRoot(filepath.FromSlash("app/src/main/java/Main.java")); got != filepath.FromSlash("app") {
		t.Fatalf("expected app module root for java source, got %q", got)
	}
	if got := sourceLayoutModuleRoot(filepath.FromSlash("app/other/Main.kt")); got != "" {
		t.Fatalf("expected empty root when no src layout exists, got %q", got)
	}

	parent := filepath.Join(t.TempDir(), "repo")
	if !isSubPath(parent, filepath.Join(parent, "app")) {
		t.Fatalf("expected nested path to be a subpath")
	}
	if isSubPath(parent, parent) {
		t.Fatalf("did not expect equal paths to be treated as subpath")
	}
	if isSubPath(parent, filepath.Dir(parent)) {
		t.Fatalf("did not expect parent dir to be a subpath")
	}
}

func TestRootPruningAndSourceLayoutBranches(t *testing.T) {
	repo := t.TempDir()
	module := filepath.Join(repo, "app")
	testutil.MustWriteFile(t, filepath.Join(module, buildGradleName), "dependencies {}\n")

	rootsNoBuild := map[string]struct{}{
		repo:                       {},
		filepath.Join(repo, "tmp"): {},
	}
	pruneKotlinAndroidRoots(repo, rootsNoBuild)
	if _, ok := rootsNoBuild[repo]; !ok {
		t.Fatalf("expected repo root to remain when no nested Gradle module root is present")
	}

	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "dependencies { implementation 'a:b:1' }\n")
	rootsWithDeps := map[string]struct{}{
		repo:   {},
		module: {},
	}
	pruneKotlinAndroidRoots(repo, rootsWithDeps)
	if _, ok := rootsWithDeps[repo]; !ok {
		t.Fatalf("expected repo root to remain when root build.gradle declares dependencies")
	}

	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), "plugins { id 'com.android.application' version '8.5.0' apply false }\n")
	rootsAggregator := map[string]struct{}{
		repo:   {},
		module: {},
	}
	pruneKotlinAndroidRoots(repo, rootsAggregator)
	if _, ok := rootsAggregator[repo]; ok {
		t.Fatalf("expected aggregator-only repo root to be pruned")
	}

	sourceRepo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(sourceRepo, "src", "build", "generated", "Main.kt"), "package generated\n")
	if hasRootSourceLayout(sourceRepo) {
		t.Fatalf("did not expect source layout from generated directories")
	}
	testutil.MustWriteFile(t, filepath.Join(sourceRepo, "src", "main", "kotlin", "Main.kt"), "package app\n")
	if !hasRootSourceLayout(sourceRepo) {
		t.Fatalf("expected source layout once real source files exist")
	}
}

func TestAnalyseWarningsAndScanBranches(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "kotlin", "Main.kt"), "package demo\nimport foo.bar.Baz\n")

	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 5})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "no Kotlin/Android dependencies discovered from Gradle manifests") {
		t.Fatalf("expected no-dependency warning, got %#v", result.Warnings)
	}
	if !strings.Contains(warnings, "gradle.lockfile not found; dependency versions may be incomplete") {
		t.Fatalf("expected missing-lockfile warning, got %#v", result.Warnings)
	}

	invalidPath := string([]byte{0})
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: invalidPath, TopN: 1}); err == nil {
		t.Fatalf("expected analyse error for invalid repo path")
	}

	scanResult, err := scanRepo(context.Background(), t.TempDir(), dependencyLookups{})
	if err != nil {
		t.Fatalf("scan empty repo: %v", err)
	}
	if !strings.Contains(strings.Join(scanResult.Warnings, "\n"), "no Kotlin/Java source files found for analysis") {
		t.Fatalf("expected no-source warning, got %#v", scanResult.Warnings)
	}

	if _, err := scanRepo(context.Background(), filepath.Join(repo, "missing"), dependencyLookups{}); err == nil {
		t.Fatalf("expected scan error for missing repo path")
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanRepo(cancelCtx, repo, dependencyLookups{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context error from scanRepo, got %v", err)
	}
}

func TestLookupParserAndBuildFileEdgeBranches(t *testing.T) {
	target := map[string]string{}
	ambiguous := map[string][]string{}
	recordLookup(target, ambiguous, "", "x")
	if len(target) != 0 {
		t.Fatalf("expected empty-key lookup write to be ignored")
	}
	recordLookup(target, ambiguous, "alpha", "dep-a")
	recordLookup(target, ambiguous, "alpha", "dep-a")
	recordLookup(target, ambiguous, "alpha", "dep-b")
	if len(ambiguous["alpha"]) == 0 {
		t.Fatalf("expected ambiguity metadata after conflicting lookup values")
	}

	values := uniqueSortedStrings([]string{"", " dep-b ", "dep-a", "dep-a"})
	if strings.Join(values, ",") != "dep-a,dep-b" {
		t.Fatalf("unexpected uniqueSortedStrings output: %#v", values)
	}
	if prefixes, aliases := groupLookupStrategy("", ""); len(prefixes) != 0 || len(aliases) != 0 {
		t.Fatalf("expected empty lookups for empty group")
	}
	if prefixes, aliases := artifactLookupStrategy("com.example", ""); len(prefixes) != 0 || len(aliases) != 0 {
		t.Fatalf("expected empty lookups for empty artifact")
	}
	if got := fallbackDependency("okhttp"); got != "okhttp" {
		t.Fatalf("unexpected fallback dependency for single segment: %q", got)
	}

	dep := report.DependencyReport{UnusedImports: []report.ImportUse{{Name: "*", Module: "dep"}}}
	recommendations := buildRecommendations(dep)
	codes := make([]string, 0, len(recommendations))
	for _, recommendation := range recommendations {
		codes = append(codes, recommendation.Code)
	}
	joined := strings.Join(codes, ",")
	if !strings.Contains(joined, "remove-unused-dependency") || !strings.Contains(joined, "avoid-wildcard-imports") {
		t.Fatalf("expected unused/wildcard recommendations, got %#v", codes)
	}

	weights := report.RemovalCandidateWeights{Usage: 2, Impact: 3, Confidence: 5}
	normalized := resolveRemovalCandidateWeights(&weights)
	if normalized.Usage <= 0 || normalized.Impact <= 0 || normalized.Confidence <= 0 {
		t.Fatalf("expected normalized positive weights, got %#v", normalized)
	}
	if _, warnings := buildDependencyReport("missing", scanResult{}); len(warnings) == 0 {
		t.Fatalf("expected warning for dependency with no imports")
	}

	if descriptors := parseGradleDependencyMatches("anything", regexp.MustCompile(`anything`)); len(descriptors) != 0 {
		t.Fatalf("expected no descriptors for regex with insufficient capture groups, got %#v", descriptors)
	}
	pattern := regexp.MustCompile(`(?m)^\s*([^:]*):([^:\n]*)`)
	if descriptors := parseGradleDependencyMatches(" :artifact\n", pattern); len(descriptors) != 0 {
		t.Fatalf("expected no descriptors when group/artifact values are empty, got %#v", descriptors)
	}
	if descriptors := parseGradleLockfileContent("# comment\nbad-line\n:artifact:1.0.0\n"); len(descriptors) != 0 {
		t.Fatalf("expected malformed lockfile lines to be ignored, got %#v", descriptors)
	}
	if descriptors := dedupeDescriptors(nil); len(descriptors) != 0 {
		t.Fatalf("expected empty descriptors for empty dedupe input, got %#v", descriptors)
	}

	repo := t.TempDir()
	lockLink := filepath.Join(repo, gradleLockfileName)
	if err := os.Symlink(filepath.Join(repo, "missing.lock"), lockLink); err == nil {
		descriptors, hasLockfile, warnings := parseGradleLockfiles(repo)
		if len(descriptors) != 0 {
			t.Fatalf("expected no descriptors from unreadable lockfile symlink, got %#v", descriptors)
		}
		if !hasLockfile {
			t.Fatalf("expected hasLockfile=true when lockfile entry exists")
		}
		if len(warnings) == 0 {
			t.Fatalf("expected warning for unreadable lockfile")
		}
	} else {
		t.Skipf("symlink creation unsupported: %v", err)
	}

	if _, _, warnings := parseGradleLockfiles(filepath.Join(repo, "missing")); len(warnings) == 0 {
		t.Fatalf("expected warning when lockfile scan path is missing")
	}

	parser := func(_ string) []dependencyDescriptor {
		return []dependencyDescriptor{{Name: "okhttp", Group: "com.squareup.okhttp3", Artifact: "okhttp"}}
	}
	testutil.MustWriteFile(t, filepath.Join(repo, ".gradle", buildGradleName), "dependencies {}\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "app", buildGradleName), "dependencies {}\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "module", buildGradleName), "dependencies {}\n")
	brokenBuildLink := filepath.Join(repo, "broken", buildGradleName)
	if err := os.MkdirAll(filepath.Dir(brokenBuildLink), 0o755); err != nil {
		t.Fatalf("mkdir broken dir: %v", err)
	}
	if err := os.Remove(brokenBuildLink); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("remove existing broken build symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(repo, "missing-build.gradle"), brokenBuildLink); err != nil {
		t.Fatalf("create broken build symlink: %v", err)
	}

	descriptors := parseBuildFiles(repo, parser, buildGradleName)
	if len(descriptors) != 1 {
		t.Fatalf("expected deduped descriptor set, got %#v", descriptors)
	}
	if descriptors[0].Name != "okhttp" || !descriptors[0].FromManifest {
		t.Fatalf("unexpected parsed descriptor metadata: %#v", descriptors[0])
	}
	if descriptors := parseBuildFiles(filepath.Join(repo, "missing"), parser, buildGradleName); len(descriptors) != 0 {
		t.Fatalf("expected empty descriptors when build-file walk path is missing, got %#v", descriptors)
	}
}

func TestAdditionalHelperBranchCoverage(t *testing.T) {
	repo := t.TempDir()

	testutil.MustWriteFile(t, filepath.Join(repo, gradleLockfileName), "x:y:1.0.0=\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "manifest-fallback", "AndroidManifest.xml"), "<manifest/>\n")
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	roots := map[string]struct{}{}
	detection := &language.Detection{}
	for _, entry := range entries {
		if entry.Name() == gradleLockfileName {
			updateKotlinAndroidDetection(filepath.Join(repo, gradleLockfileName), entry, roots, detection)
		}
	}
	manifestEntries, err := os.ReadDir(filepath.Join(repo, "manifest-fallback"))
	if err != nil {
		t.Fatalf("readdir manifest-fallback: %v", err)
	}
	updateKotlinAndroidDetection(filepath.Join(repo, "manifest-fallback", "AndroidManifest.xml"), manifestEntries[0], roots, detection)
	if _, ok := roots[filepath.Join(repo, "manifest-fallback")]; !ok {
		t.Fatalf("expected AndroidManifest fallback root to be captured")
	}

	if got := androidManifestModuleRoot(filepath.FromSlash("/src/main/AndroidManifest.xml")); got != "" {
		t.Fatalf("expected empty root for absolute repo-level manifest, got %q", got)
	}
	if got := androidManifestModuleRoot(filepath.FromSlash("app/src/main/Manifest.xml")); got != "" {
		t.Fatalf("expected empty root for non-android-manifest path, got %q", got)
	}
	if got := sourceLayoutModuleRoot(filepath.FromSlash("/src/main/kotlin/Main.kt")); got != "" {
		t.Fatalf("expected empty root for absolute repo-level source layout, got %q", got)
	}

	rootsWithoutRepo := map[string]struct{}{filepath.Join(repo, "app"): {}}
	pruneKotlinAndroidRoots(repo, rootsWithoutRepo)
	if len(rootsWithoutRepo) != 1 {
		t.Fatalf("expected roots to remain unchanged when repo root is absent")
	}
	rootsWithOutsidePath := map[string]struct{}{
		repo:               {},
		filepath.Dir(repo): {},
	}
	pruneKotlinAndroidRoots(repo, rootsWithOutsidePath)
	if _, ok := rootsWithOutsidePath[repo]; !ok {
		t.Fatalf("expected repo root to remain when compared root is outside repo")
	}

	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "resources", "README.txt"), "x\n")
	if hasRootSourceLayout(repo) {
		t.Fatalf("did not expect source layout from non-source extensions")
	}

	scanState := newScanResult()
	scanState.addFallbackModule("   ", "dep", false)
	scanState.addAmbiguousModule("   ", []string{"a", "b"}, "a")
	scanState.fallbackModules = map[string]string{
		"a.mod": "a",
		"b.mod": "b",
		"c.mod": "c",
		"d.mod": "d",
	}
	scanState.ambiguousModules = map[string][]string{
		"a.mod": {"a", "b"},
		"b.mod": {"b", "c"},
		"c.mod": {"c", "d"},
		"d.mod": {"d", "e"},
	}
	scanState.appendInferenceWarnings()
	warnings := strings.Join(scanState.Warnings, "\n")
	if !strings.Contains(warnings, "examples:") {
		t.Fatalf("expected formatted inference warning examples, got %#v", scanState.Warnings)
	}

	testutil.MustWriteFile(t, filepath.Join(repo, ".git", "src", "Ignored.kt"), "package ignored\n")
	scanResult, err := scanRepo(context.Background(), repo, dependencyLookups{})
	if err != nil {
		t.Fatalf("scan repo with skipped dirs: %v", err)
	}
	if !strings.Contains(strings.Join(scanResult.Warnings, "\n"), "no Kotlin/Java source files found for analysis") {
		t.Fatalf("expected no-source warning when only skipped directories contain sources, got %#v", scanResult.Warnings)
	}

	if err := scanKotlinAndroidSourceFile("", filepath.Join(repo, "missing.kt"), dependencyLookups{}, &scanState); err == nil {
		t.Fatalf("expected read error for missing file")
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "Loose.kt"), "package demo\nimport java.util.List\n")
	if err := scanKotlinAndroidSourceFile("", filepath.Join(repo, "Loose.kt"), dependencyLookups{}, &scanState); err != nil {
		t.Fatalf("scan loose source file: %v", err)
	}

	if imports := parseImports([]byte("import java.util.List\n"), "Main.kt", "pkg.demo", dependencyLookups{}, &scanState); len(imports) != 0 {
		t.Fatalf("expected ignored framework imports to be dropped, got %#v", imports)
	}
	if _, ok := buildImportRecord([]string{"import", "a.", "", ""}, "a.", "dep"); ok {
		t.Fatalf("expected import record build to fail for empty symbol")
	}
	record, ok := buildImportRecord([]string{"import", "a.b", ".*", "Alias"}, "a.b", "dep")
	if !ok || !record.Wildcard || record.Name != "*" {
		t.Fatalf("expected wildcard import record, got %#v ok=%v", record, ok)
	}
	if !shouldIgnoreImport("", "") {
		t.Fatalf("expected empty module to be ignored")
	}
	if !shouldIgnoreImport("pkg.demo.service", "pkg.demo") {
		t.Fatalf("expected package-local import to be ignored")
	}

	lookups := dependencyLookups{
		Prefixes: map[string]string{"alpha": "dep-alpha", "alpha.beta": "dep-beta"},
		Aliases:  map[string]string{},
		Ambiguous: map[string][]string{
			"alpha.beta": {"dep-beta", "dep-beta-alt"},
		},
	}
	resolved, ambiguousCandidates := resolveDependency("alpha.beta.Client", lookups)
	if resolved != "dep-beta" || len(ambiguousCandidates) != 2 {
		t.Fatalf("expected longest-prefix dependency resolution with ambiguity metadata, got %q %#v", resolved, ambiguousCandidates)
	}

	manifestDescriptors := []dependencyDescriptor{{Name: "same", Group: "g1", Artifact: "a1"}, {Name: "same", Group: "g2", Artifact: "a2"}}
	lockDescriptors := []dependencyDescriptor{{Name: "lock-only", Group: "g3", Artifact: "a3", Version: "1.0.0"}}
	merged := mergeDescriptors(manifestDescriptors, lockDescriptors)
	if len(merged) != 3 {
		t.Fatalf("expected merged manifest+lock descriptors, got %#v", merged)
	}
	if key := descriptorKey(dependencyDescriptor{Name: "nolookup"}); key != "nolookup" {
		t.Fatalf("expected descriptor key fallback to name, got %q", key)
	}

	lockRepo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(lockRepo, ".gradle", gradleLockfileName), "ignored:ignored:1.0.0=\n")
	lockDescriptors, hasLockfile, lockWarnings := parseGradleLockfiles(lockRepo)
	if hasLockfile {
		t.Fatalf("did not expect hasLockfile when lockfile exists only under skipped directories")
	}
	if len(lockWarnings) != 0 || len(lockDescriptors) != 0 {
		t.Fatalf("expected no lock descriptors or warnings from skipped directories, got %#v %#v", lockDescriptors, lockWarnings)
	}

	deduped := dedupeDescriptors([]dependencyDescriptor{
		{Name: "okhttp", Group: "", Artifact: "okhttp"},
		{Name: "okhttp", Group: "com.squareup.okhttp3", Artifact: "okhttp"},
		{Name: "okhttp", Group: "com.squareup.okhttp3", Artifact: "okhttp", Version: "1.0.0"},
		{Name: "other", Group: "com.example", Artifact: "other"},
	})
	if len(deduped) != 2 {
		t.Fatalf("expected dedupe to drop invalid/duplicate descriptors, got %#v", deduped)
	}
	for _, descriptor := range deduped {
		if descriptor.Name == "okhttp" && descriptor.Version != "1.0.0" {
			t.Fatalf("expected dedupe to retain populated version, got %#v", descriptor)
		}
	}
}
