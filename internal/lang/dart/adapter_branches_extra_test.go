package dart

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

const (
	fooPackageModule  = "package:foo/foo.dart"
	mainDartFileName  = "main.dart"
	emptyMainDartBody = "void main() {}\n"
)

func TestParseImportDirectiveAndShowSymbols(t *testing.T) {
	kind, module, clause, ok := parseImportDirective(`import 'package:foo/foo.dart' show Foo, Bar hide Baz;`)
	if !ok {
		t.Fatalf("expected import directive to parse")
	}
	if kind != "import" || module != fooPackageModule {
		t.Fatalf("unexpected directive parse: kind=%q module=%q", kind, module)
	}
	if got := parseShowSymbols(clause); !slices.Equal(got, []string{"Foo", "Bar"}) {
		t.Fatalf("unexpected show symbols: %#v", got)
	}

	if _, _, _, ok := parseImportDirective(`part 'x.dart';`); ok {
		t.Fatalf("did not expect part directive to parse")
	}
	if alias := extractAlias("deferred as foo show Bar"); alias != "foo" {
		t.Fatalf("unexpected alias: %q", alias)
	}
}

func TestBuildDirectiveBindingsBranches(t *testing.T) {
	location := report.Location{File: "lib/main.dart", Line: 1, Column: 1}
	exportBindings := buildDirectiveBindings("export", fooPackageModule, "", "foo", location)
	if len(exportBindings) != 1 || !exportBindings[0].Wildcard {
		t.Fatalf("expected wildcard export binding, got %#v", exportBindings)
	}

	aliasBindings := buildDirectiveBindings("import", fooPackageModule, "as foo", "foo", location)
	if len(aliasBindings) != 1 || aliasBindings[0].Local != "foo" {
		t.Fatalf("expected alias binding, got %#v", aliasBindings)
	}

	showBindings := buildDirectiveBindings("import", fooPackageModule, "show Foo, Bar", "foo", location)
	if len(showBindings) != 2 {
		t.Fatalf("expected two show bindings, got %#v", showBindings)
	}

	wildcardBindings := buildDirectiveBindings("import", fooPackageModule, "", "foo", location)
	if len(wildcardBindings) != 1 || !wildcardBindings[0].Wildcard {
		t.Fatalf("expected wildcard fallback binding, got %#v", wildcardBindings)
	}
}

func TestResolveDependencyFromModuleBranches(t *testing.T) {
	unresolved := map[string]int{}
	lookup := map[string]dependencyInfo{
		"local_pkg": {LocalPath: true},
		"http":      {},
	}

	if got := resolveDependencyFromModule("dart:core", lookup, unresolved); got != "" {
		t.Fatalf("expected dart SDK import to be ignored, got %q", got)
	}
	if got := resolveDependencyFromModule("package:local_pkg/local_pkg.dart", lookup, unresolved); got != "" {
		t.Fatalf("expected local path dependency to be ignored, got %q", got)
	}
	if got := resolveDependencyFromModule("package:http/http.dart", lookup, unresolved); got != "http" {
		t.Fatalf("expected resolved http dependency, got %q", got)
	}
	if got := resolveDependencyFromModule("package:dio/dio.dart", lookup, unresolved); got != "dio" {
		t.Fatalf("expected unresolved dependency to be returned, got %q", got)
	}
	if unresolved["dio"] == 0 {
		t.Fatalf("expected unresolved counter for dio, got %#v", unresolved)
	}
}

func TestCollectManifestDataNoPubspecAndNoTarget(t *testing.T) {
	repo := t.TempDir()
	manifests, warnings, err := collectManifestData(repo)
	if err != nil {
		t.Fatalf("collect manifest data: %v", err)
	}
	if len(manifests) != 0 {
		t.Fatalf("expected no manifests, got %#v", manifests)
	}
	if !containsWarning(warnings, "no pubspec.yaml") {
		t.Fatalf("expected no-pubspec warning, got %#v", warnings)
	}

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if err != nil {
		t.Fatalf("analyse no target: %v", err)
	}
	if !containsWarning(reportData.Warnings, "no dependency data available for top-N ranking") {
		t.Fatalf("expected no dependency data warning, got %#v", reportData.Warnings)
	}
}

func TestPluginMetadataHelpersAndDiscoveryCap(t *testing.T) {
	if !isLikelyFlutterPluginPackage("foo_android") {
		t.Fatalf("expected foo_android to be plugin-like")
	}
	if isLikelyFlutterPluginPackage("http") {
		t.Fatalf("did not expect http to be plugin-like")
	}
	if !hasPluginMetadataValue(map[string]any{
		"flutter": map[string]any{
			"plugin": map[string]any{
				"platforms": map[string]any{"android": map[string]any{}},
			},
		},
	}) {
		t.Fatalf("expected nested plugin metadata to be detected")
	}
	converted, ok := toStringMap(map[any]any{"name": "foo"})
	if !ok || converted["name"] != "foo" {
		t.Fatalf("expected map[any]any conversion, got ok=%v converted=%#v", ok, converted)
	}

	repo := t.TempDir()
	for i := range maxManifestCount + 3 {
		writeFile(t, filepath.Join(repo, "pkg", "p"+itoa(i), pubspecYAMLName), "name: p\ndependencies: {}\n")
	}
	_, warnings, err := discoverPubspecPaths(repo)
	if err != nil {
		t.Fatalf("discover pubspec paths: %v", err)
	}
	if !containsWarning(warnings, "discovery capped") {
		t.Fatalf("expected discovery cap warning, got %#v", warnings)
	}
}

func setupDetectionRepo(t *testing.T) string {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYMLName), "name: app\ndependencies: {}\n")
	writeFile(t, filepath.Join(repo, pubspecLockName), "packages: {}\n")
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), emptyMainDartBody)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return repo
}

func TestDetectionRootSignals(t *testing.T) {
	repo := setupDetectionRepo(t)

	detection := language.Detection{}
	roots := map[string]struct{}{}
	if err := applyDartRootSignals(repo, &detection, roots); err != nil {
		t.Fatalf("apply root signals: %v", err)
	}
	if !detection.Matched || detection.Confidence < 80 {
		t.Fatalf("unexpected root detection result: %#v", detection)
	}
	if _, ok := roots[repo]; !ok {
		t.Fatalf("expected repo root in detection roots")
	}
}

func TestDetectionWalkEntries(t *testing.T) {
	repo := setupDetectionRepo(t)
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	visited := 0
	detection := language.Detection{}
	roots := map[string]struct{}{}
	for _, entry := range entries {
		path := filepath.Join(repo, entry.Name())
		detectErr := walkDartDetectionEntry(path, entry, roots, &detection, &visited)
		if entry.Name() == ".git" {
			if !errors.Is(detectErr, filepath.SkipDir) {
				t.Fatalf("expected SkipDir for .git, got %v", detectErr)
			}
			continue
		}
		if detectErr != nil {
			t.Fatalf("walk detection entry %s: %v", entry.Name(), detectErr)
		}
	}
	if !detection.Matched || detection.Confidence < 6 {
		t.Fatalf("expected detection to match after walk, got %#v", detection)
	}
}

func TestDetectionWalkEntryCap(t *testing.T) {
	repo := setupDetectionRepo(t)
	dartEntries, err := os.ReadDir(filepath.Join(repo, "lib"))
	if err != nil {
		t.Fatalf("read lib dir: %v", err)
	}
	if len(dartEntries) == 0 {
		t.Fatalf("expected dart entries in lib")
	}
	detection := language.Detection{}
	roots := map[string]struct{}{}
	visited := maxDetectionEntries
	capErr := walkDartDetectionEntry(filepath.Join(repo, "lib", dartEntries[0].Name()), dartEntries[0], roots, &detection, &visited)
	if !errors.Is(capErr, fs.SkipAll) {
		t.Fatalf("expected SkipAll after detection cap, got %v", capErr)
	}
}

func TestManifestAndLockReaderBranches(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, pubspecYAMLName)
	lockPath := filepath.Join(repo, pubspecLockName)

	writeFile(t, manifestPath, `name: app
dependencies:
  flutter:
    sdk: flutter
  url_launcher_android: ^1.0.0
  local_pkg:
    path: ../local_pkg
flutter:
  plugin:
    platforms:
      android:
        package: demo
`)
	writeFile(t, lockPath, `packages:
  flutter:
    dependency: "direct main"
    description: flutter
    source: sdk
    version: "0.0.0"
  url_launcher_android:
    dependency: "direct main"
    description:
      name: url_launcher_android
      pluginClass: DemoPlugin
    source: hosted
    version: "1.0.0"
  local_pkg:
    dependency: "direct main"
    description: {name: local_pkg}
    source: path
    version: "0.0.1"
`)

	manifest, warnings, err := loadPackageManifest(repo, manifestPath)
	if err != nil {
		t.Fatalf("load package manifest: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings with lock present, got %#v", warnings)
	}
	if !manifest.HasLock || !manifest.HasFlutterSection || !manifest.HasFlutterPluginMetadata {
		t.Fatalf("unexpected manifest flags: %#v", manifest)
	}
	if !manifest.Dependencies["flutter"].FlutterSDK {
		t.Fatalf("expected flutter dependency to be marked as sdk")
	}
	if !manifest.Dependencies["url_launcher_android"].PluginLike {
		t.Fatalf("expected plugin package classification from lock/manifest metadata")
	}
	if !manifest.Dependencies["local_pkg"].LocalPath {
		t.Fatalf("expected path dependency to be marked as local")
	}

	if got := lockPackageName("flutter"); got != "" {
		t.Fatalf("expected empty lock package name for string description, got %q", got)
	}
	if !lockDescriptionTargetsFlutter("flutter") {
		t.Fatalf("expected string flutter description to match")
	}
	if !lockDescriptionTargetsFlutter(map[string]any{"name": "flutter"}) {
		t.Fatalf("expected flutter name map to match")
	}
	if !lockDescriptionTargetsFlutter(map[string]any{"sdk": "flutter"}) {
		t.Fatalf("expected flutter sdk map to match")
	}
	if lockDescriptionTargetsFlutter(map[string]any{"name": "http"}) {
		t.Fatalf("did not expect non-flutter description to match")
	}

	invalidManifestPath := filepath.Join(repo, "bad."+pubspecYAMLName)
	invalidLockPath := filepath.Join(repo, "bad."+pubspecLockName)
	writeFile(t, invalidManifestPath, "dependencies: [\n")
	writeFile(t, invalidLockPath, "packages: [\n")

	if _, readErr := readPubspecManifest(repo, invalidManifestPath); readErr == nil {
		t.Fatalf("expected manifest parse error")
	}
	if _, readErr := readPubspecLock(repo, invalidLockPath); readErr == nil {
		t.Fatalf("expected lock parse error")
	}
}

func TestScanBranchesAndWarnings(t *testing.T) {
	repo := t.TempDir()
	manifestPath := filepath.Join(repo, pubspecYAMLName)
	writeFile(t, manifestPath, `name: app
dependencies:
  http: ^1.0.0
flutter:
  uses-material-design: true
`)

	manifest, _, err := loadPackageManifest(repo, manifestPath)
	if err != nil {
		t.Fatalf("load package manifest: %v", err)
	}

	largeFile := filepath.Join(repo, "lib", "huge.dart")
	writeFile(t, largeFile, strings.Repeat("a", maxScannableDartFile+1))

	scan, err := scanRepo(context.Background(), repo, []packageManifest{manifest})
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if !containsWarning(scan.Warnings, "flutter plugin metadata not found") {
		t.Fatalf("expected flutter plugin metadata warning, got %#v", scan.Warnings)
	}
	if !containsWarning(scan.Warnings, "skipped 1 Dart files larger than") {
		t.Fatalf("expected large-file warning, got %#v", scan.Warnings)
	}
	if !containsWarning(scan.Warnings, "no Dart source files found") {
		t.Fatalf("expected no-source warning, got %#v", scan.Warnings)
	}

	writeFile(t, filepath.Join(repo, "lib", "small.dart"), emptyMainDartBody)
	fileCount := maxScanFiles
	result := scanResult{UnresolvedImports: make(map[string]int)}
	scanned := map[string]struct{}{}
	entryErr := scanPackageFileEntry(repo, filepath.Join(repo, "lib", "small.dart"), map[string]dependencyInfo{}, scanned, &fileCount, &result)
	if !errors.Is(entryErr, fs.SkipAll) {
		t.Fatalf("expected SkipAll for file cap branch, got %v", entryErr)
	}
	if !result.SkippedFilesByBound {
		t.Fatalf("expected file cap flag to be set")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fileCount = 0
	canceled := scanPackageRoot(ctx, repo, manifest, collectManifestRoots([]packageManifest{manifest}), map[string]struct{}{}, &fileCount, &scanResult{UnresolvedImports: map[string]int{}})
	if !errors.Is(canceled, context.Canceled) {
		t.Fatalf("expected context canceled from scanPackageRoot, got %v", canceled)
	}
}

func TestShouldSkipDirAndPluginMetadataBranches(t *testing.T) {
	if !shouldSkipDir("android") {
		t.Fatalf("expected android to be skipped")
	}
	if shouldSkipDir("src") {
		t.Fatalf("did not expect src to be skipped")
	}

	if hasPluginMetadataValue(map[string]any{"flutter": "noop"}) {
		t.Fatalf("did not expect plugin metadata in plain flutter value")
	}
	if !hasPluginMetadataValue([]any{
		"noop",
		map[any]any{"pluginClass": "DemoPlugin"},
	}) {
		t.Fatalf("expected plugin metadata from []any + map[any]any branch")
	}
}

func TestStringAndPathHelperBranches(t *testing.T) {
	if _, ok := toStringMap("invalid"); ok {
		t.Fatalf("expected toStringMap to reject non-map input")
	}
	if got := asString(nil); got != "" {
		t.Fatalf("expected empty string for nil, got %q", got)
	}
	if got := asString(42); got != "42" {
		t.Fatalf("expected numeric string conversion, got %q", got)
	}

	if got := uniquePaths([]string{" ./a ", "./a", "", "./b"}); !slices.Equal(got, []string{".", "a", "b"}) {
		t.Fatalf("unexpected unique paths result: %#v", got)
	}
	if got := dedupeWarnings([]string{"a", " ", "a", "b"}); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("unexpected dedupe warnings result: %#v", got)
	}
	if got := dedupeStrings([]string{"foo", " foo ", "", "bar", "foo"}); !slices.Equal(got, []string{"foo", "bar"}) {
		t.Fatalf("unexpected dedupe strings result: %#v", got)
	}
}

func TestThresholdAndWarningBranches(t *testing.T) {
	if recommendationPriorityRank("high") != 0 || recommendationPriorityRank("medium") != 1 || recommendationPriorityRank("low") != 2 {
		t.Fatalf("unexpected recommendation priority ranks")
	}

	if got := resolveMinUsageRecommendationThreshold(nil); got != thresholds.Defaults().MinUsagePercentForRecommendations {
		t.Fatalf("unexpected default min usage threshold: %d", got)
	}
	custom := 77
	if got := resolveMinUsageRecommendationThreshold(&custom); got != 77 {
		t.Fatalf("unexpected custom min usage threshold: %d", got)
	}

	warnings := summarizeUnresolved(map[string]int{
		"z": 1,
		"a": 2,
		"b": 2,
		"c": 3,
		"d": 4,
		"e": 5,
		"f": 6,
	})
	if len(warnings) != maxWarningSamples {
		t.Fatalf("expected warning sample cap %d, got %d", maxWarningSamples, len(warnings))
	}
	if !containsWarning(warnings, "f") || !containsWarning(warnings, "e") {
		t.Fatalf("expected highest unresolved dependencies in warnings, got %#v", warnings)
	}
}

func TestBuildRequestedDartDependenciesEmptyRequest(t *testing.T) {
	emptyDeps, emptyWarnings := buildRequestedDartDependencies(language.Request{}, scanResult{})
	if len(emptyDeps) != 0 || !containsWarning(emptyWarnings, "no dependency or top-N target provided") {
		t.Fatalf("expected empty request warning, got deps=%#v warnings=%#v", emptyDeps, emptyWarnings)
	}
}

func TestErrorAndScanBranchCoverage(t *testing.T) {
	missingRepo := filepath.Join(t.TempDir(), "missing")
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), missingRepo); err == nil {
		t.Fatalf("expected detect error for missing repo path")
	}
	if _, _, err := collectManifestData(missingRepo); err == nil {
		t.Fatalf("expected collectManifestData error for missing repo path")
	}

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), "name: app\ndependencies:\n  http: ^1.0.0\n")
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), "import 'package:http/http.dart' as http;\nvoid main() { http.Client(); }\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAdapter().DetectWithConfidence(ctx, repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation from detect walk, got %v", err)
	}

	lockDirRepo := t.TempDir()
	lockDirManifest := filepath.Join(lockDirRepo, pubspecYAMLName)
	writeFile(t, lockDirManifest, "name: app\ndependencies:\n  http: ^1.0.0\n")
	if err := os.Mkdir(filepath.Join(lockDirRepo, pubspecLockName), 0o750); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	if _, _, err := loadPackageManifest(lockDirRepo, lockDirManifest); err == nil {
		t.Fatalf("expected loadPackageManifest error when pubspec.lock is a directory")
	}

	if scanDartSourceFile(repo, filepath.Join(repo, "lib", "missing.dart"), map[string]dependencyInfo{}, &scanResult{UnresolvedImports: map[string]int{}}) == nil {
		t.Fatalf("expected scanDartSourceFile error for missing file")
	}

	nonDartFile := filepath.Join(repo, "README.md")
	writeFile(t, nonDartFile, "# sample\n")
	fileCount := 0
	scanned := map[string]struct{}{}
	scanOut := scanResult{UnresolvedImports: map[string]int{}}
	if err := scanPackageFileEntry(repo, nonDartFile, map[string]dependencyInfo{}, scanned, &fileCount, &scanOut); err != nil {
		t.Fatalf("expected non-dart file to be ignored, got %v", err)
	}

	dupPath := filepath.Join(repo, "lib", "dup.dart")
	writeFile(t, dupPath, emptyMainDartBody)
	scanned[filepath.Clean(dupPath)] = struct{}{}
	if err := scanPackageFileEntry(repo, dupPath, map[string]dependencyInfo{}, scanned, &fileCount, &scanOut); err != nil {
		t.Fatalf("expected duplicate scanned file to be ignored, got %v", err)
	}

	rootManifestPath := filepath.Join(repo, pubspecYAMLName)
	childRoot := filepath.Join(repo, "packages", "child")
	childManifestPath := filepath.Join(childRoot, pubspecYAMLName)
	writeFile(t, childManifestPath, "name: child\ndependencies:\n  collection: ^1.0.0\n")
	writeFile(t, filepath.Join(childRoot, "lib", "child.dart"), "import 'package:collection/collection.dart' as coll;\nvoid main() { coll.ListEquality(); }\n")

	rootManifest, _, err := loadPackageManifest(repo, rootManifestPath)
	if err != nil {
		t.Fatalf("load root manifest: %v", err)
	}
	childManifest, _, err := loadPackageManifest(repo, childManifestPath)
	if err != nil {
		t.Fatalf("load child manifest: %v", err)
	}
	roots := collectManifestRoots([]packageManifest{rootManifest, childManifest})
	fileCount = 0
	scanOut = scanResult{UnresolvedImports: map[string]int{}}
	if err := scanPackageRoot(context.Background(), repo, rootManifest, roots, map[string]struct{}{}, &fileCount, &scanOut); err != nil {
		t.Fatalf("scan root package: %v", err)
	}
	for _, file := range scanOut.Files {
		if strings.Contains(file.Path, filepath.ToSlash("packages/child")) {
			t.Fatalf("expected nested child root to be skipped, got scanned file %q", file.Path)
		}
	}
}

func TestAdditionalBranchCoverage(t *testing.T) {
	if got := parseShowSymbols("as foo"); len(got) != 0 {
		t.Fatalf("expected nil symbols when no show clause exists, got %#v", got)
	}
	if got := parseShowSymbols("show Foo, Foo, 1invalid"); !slices.Equal(got, []string{"Foo"}) {
		t.Fatalf("expected deduped valid show symbols, got %#v", got)
	}

	if got := resolveDependencyFromModule("package:new_pkg/new_pkg.dart", map[string]dependencyInfo{}, nil); got != "new_pkg" {
		t.Fatalf("expected unresolved dependency name even when unresolved map is nil, got %q", got)
	}

	warnings := compileScanWarnings(scanResult{
		SkippedLargeFiles:   2,
		SkippedFilesByBound: true,
		UnresolvedImports:   map[string]int{"z": 1},
	})
	if !containsWarning(warnings, "skipped 2 Dart files larger than") {
		t.Fatalf("expected skipped-large warning, got %#v", warnings)
	}
	if !containsWarning(warnings, "Dart source scanning capped at") {
		t.Fatalf("expected file-cap warning, got %#v", warnings)
	}

	hasPluginMetadata := false
	pluginSpec := map[any]any{
		"path": "../local",
		"sdk":  "flutter",
		"plugin": map[string]any{
			"platforms": map[string]any{"android": map[string]any{}},
		},
	}
	info := dependencyInfoFromSpec("my_plugin_android", pluginSpec, &hasPluginMetadata)
	if !info.LocalPath || !info.FlutterSDK || !info.PluginLike || !hasPluginMetadata {
		t.Fatalf("unexpected dependency info from spec: %#v (hasPluginMetadata=%v)", info, hasPluginMetadata)
	}

	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: missing, TopN: 1}); err == nil {
		t.Fatalf("expected analyse to fail when repo path is missing")
	}

	tempRepo := t.TempDir()
	missingManifest := packageManifest{
		Root:         filepath.Join(tempRepo, "missing"),
		Dependencies: map[string]dependencyInfo{},
	}
	err := scanPackageRoot(context.Background(), tempRepo, missingManifest, map[string]struct{}{}, map[string]struct{}{}, new(int), &scanResult{UnresolvedImports: map[string]int{}})
	if err == nil {
		t.Fatalf("expected scanPackageRoot to fail for missing root")
	}
}

func itoa(value int) string {
	return fmt.Sprintf("%d", value)
}
