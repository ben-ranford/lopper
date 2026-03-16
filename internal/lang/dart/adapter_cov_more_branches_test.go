package dart

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestDartAdditionalBranchCoverage(t *testing.T) {
	t.Run("detect and manifest loader errors", testDartDetectAndManifestLoaderErrors)
	t.Run("dependency metadata helpers", testDartDependencyMetadataHelpers)
	t.Run("scan helpers and warnings", testDartScanHelpersAndWarnings)
	t.Run("import parsing and dependency selection", testDartImportParsingAndDependencySelection)
}

func testDartDetectAndManifestLoaderErrors(t *testing.T) {
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected missing repo to fail detection")
	}
	if _, err := NewAdapter().DetectWithConfidence(context.Background(), "\x00"); err == nil {
		t.Fatalf("expected invalid repo path to fail detection")
	}
	if _, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: "\x00"}); err == nil {
		t.Fatalf("expected invalid repo path to fail analysis")
	}

	repo := t.TempDir()
	manifestPath := filepath.Join(repo, pubspecYAMLName)
	if err := os.Mkdir(manifestPath, 0o755); err != nil {
		t.Fatalf("mkdir pubspec dir: %v", err)
	}
	if _, _, err := loadPackageManifest(repo, manifestPath); err == nil {
		t.Fatalf("expected directory manifest to fail load")
	}

	repo = t.TempDir()
	manifestPath = filepath.Join(repo, pubspecYAMLName)
	if err := os.WriteFile(manifestPath, []byte("name: demo\ndependencies:\n  http: ^1.0.0\n"), 0o644); err != nil {
		t.Fatalf("write pubspec manifest: %v", err)
	}
	if err := os.Mkdir(filepath.Join(repo, pubspecLockName), 0o755); err != nil {
		t.Fatalf("mkdir pubspec.lock dir: %v", err)
	}
	if _, _, err := loadPackageManifest(repo, manifestPath); err == nil {
		t.Fatalf("expected pubspec.lock stat failure to bubble")
	}

	if _, _, err := collectManifestData(repo); err == nil {
		t.Fatalf("expected manifest collection to fail when discovered manifest is unreadable")
	}
}

func testDartDependencyMetadataHelpers(t *testing.T) {
	if info := dependencyInfoFromSpec("http", "^1.0.0", nil); info.LocalPath || info.FlutterSDK {
		t.Fatalf("expected scalar dependency spec to stay default, got %#v", info)
	}
	if got := lockPackageName("flutter"); got != "" {
		t.Fatalf("expected scalar lock description to produce empty package name, got %q", got)
	}
	if lockDescriptionTargetsFlutter(map[string]any{"name": "other"}) {
		t.Fatalf("did not expect unrelated lock description to target flutter")
	}
	if got := collectManifestRoots([]packageManifest{{Root: ""}, {Root: "  "}, {Root: filepath.Join("pkg", "..", "app")}}); len(got) != 2 {
		t.Fatalf("expected normalized manifest roots, got %#v", got)
	} else if _, ok := got["."]; !ok {
		t.Fatalf("expected blank roots to normalize to '.', got %#v", got)
	} else if _, ok := got["app"]; !ok {
		t.Fatalf("expected blank roots to normalize to '.' and app root to remain, got %#v", got)
	}

	deps := map[string]dependencyInfo{}
	addDependencySection(deps, map[string]any{"   ": "^1.0.0"}, "runtime", nil)
	if len(deps) != 0 {
		t.Fatalf("expected blank dependency names to be ignored, got %#v", deps)
	}

	mergeLockDependencyData(deps, map[string]pubspecLockPackage{"   ": {Description: map[string]any{"name": "   "}}}, nil)
	if len(deps) != 0 {
		t.Fatalf("expected blank lock dependency names to be ignored, got %#v", deps)
	}

	if hasPluginMetadataAnyMap(map[any]any{"note": "value"}) {
		t.Fatalf("expected plain any-map values not to count as plugin metadata")
	}
	if hasPluginMetadataSlice([]any{"value"}) {
		t.Fatalf("expected plain slices not to count as plugin metadata")
	}
}

func testDartScanHelpersAndWarnings(t *testing.T) {
	if err := walkContextErr(nil, nil); err != nil {
		t.Fatalf("expected nil walk context error, got %v", err)
	}

	root := t.TempDir()
	nestedRoot := filepath.Join(root, "packages", "demo")
	if err := os.MkdirAll(nestedRoot, 0o755); err != nil {
		t.Fatalf("mkdir nested root: %v", err)
	}
	if err := scanPackageDir(root, root, filepath.Base(root), map[string]struct{}{filepath.Clean(nestedRoot): {}}); err != nil {
		t.Fatalf("expected root directory not to be skipped, got %v", err)
	}
	if err := scanPackageDir(root, nestedRoot, filepath.Base(nestedRoot), map[string]struct{}{filepath.Clean(nestedRoot): {}}); err != filepath.SkipDir {
		t.Fatalf("expected nested manifest root to be skipped, got %v", err)
	}
	if err := scanPackageDir(root, filepath.Join(root, "android"), "android", nil); err != filepath.SkipDir {
		t.Fatalf("expected android directory to be skipped, got %v", err)
	}

	scanned := map[string]struct{}{}
	fileCount := 0
	result := &scanResult{}
	dartPath := filepath.Join(root, "lib", "main.dart")
	if err := os.MkdirAll(filepath.Dir(dartPath), 0o755); err != nil {
		t.Fatalf("mkdir lib dir: %v", err)
	}
	if err := os.WriteFile(dartPath, []byte("void main() {}\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := scanPackageFileEntry(root, dartPath, map[string]dependencyInfo{}, scanned, &fileCount, result); err != nil {
		t.Fatalf("scan package file entry: %v", err)
	}
	before := len(result.Files)
	if err := scanPackageFileEntry(root, dartPath, map[string]dependencyInfo{}, scanned, &fileCount, result); err != nil {
		t.Fatalf("duplicate scan package file entry: %v", err)
	}
	if len(result.Files) != before {
		t.Fatalf("expected duplicate file scan to be ignored, got %#v", result.Files)
	}
	fileCount = maxScanFiles
	if err := scanPackageFileEntry(root, filepath.Join(root, "lib", "next.dart"), map[string]dependencyInfo{}, map[string]struct{}{}, &fileCount, result); err != fs.SkipAll || !result.SkippedFilesByBound {
		t.Fatalf("expected file bound skip, got err=%v result=%#v", err, result)
	}

	outsidePath := filepath.Join(t.TempDir(), "outside.dart")
	if err := os.WriteFile(outsidePath, []byte("void main() {}\n"), 0o644); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	if scanDartSourceFile(root, outsidePath, map[string]dependencyInfo{}, &scanResult{}) == nil {
		t.Fatalf("expected repo-bounded read failure for outside dart file")
	}

	manifest := packageManifest{Root: "", Dependencies: map[string]dependencyInfo{}}
	if err := scanPackageRoot(context.Background(), root, manifest, map[string]struct{}{}, map[string]struct{}{}, new(int), &scanResult{}); err != nil {
		t.Fatalf("expected blank manifest root to default to repo path, got %v", err)
	}

	if scanPackageRoot(context.Background(), root, packageManifest{Root: filepath.Join(root, "missing"), Dependencies: map[string]dependencyInfo{}}, map[string]struct{}{}, map[string]struct{}{}, new(int), &scanResult{}) == nil {
		t.Fatalf("expected missing manifest root to fail scan")
	}

	warnings := compileScanWarnings(scanResult{
		SkippedLargeFiles:   1,
		SkippedFilesByBound: true,
		UnresolvedImports:   map[string]int{"dio": 2},
	})
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "skipped 1 Dart files") || !strings.Contains(joined, "Dart source scanning capped") || !strings.Contains(joined, "dio") {
		t.Fatalf("expected composed scan warnings, got %#v", warnings)
	}
}

func testDartImportParsingAndDependencySelection(t *testing.T) {
	if kind, module, clause, ok := parseImportDirective(`show "x";`); ok || kind != "" || module != "" || clause != "" {
		t.Fatalf("expected unsupported directive parse to fail")
	}
	if kind, module, clause, ok := parseImportDirective(`part "x.dart";`); ok || kind != "" || module != "" || clause != "" {
		t.Fatalf("expected non-import/export directive parse to fail")
	}
	if got := parseShowSymbols("show Foo, 1Bar, Foo hide Baz"); len(got) != 1 || got[0] != "Foo" {
		t.Fatalf("expected invalid and duplicate symbols to be filtered, got %#v", got)
	}
	if got := extractAlias("as 123bad"); got != "" {
		t.Fatalf("expected invalid alias to be rejected, got %q", got)
	}
	if got := resolveDependencyFromModule("package:", map[string]dependencyInfo{}, nil); got != "" {
		t.Fatalf("expected empty package import to be ignored, got %q", got)
	}
	if got := resolveDependencyFromModule("package:/foo", map[string]dependencyInfo{}, nil); got != "" {
		t.Fatalf("expected blank package dependency id to be ignored, got %q", got)
	}
	if got := resolveDependencyFromModule("http://example.com", map[string]dependencyInfo{}, nil); got != "" {
		t.Fatalf("expected non-package import to be ignored, got %q", got)
	}
	if got := parseShowSymbols("show "); len(got) != 0 {
		t.Fatalf("expected empty show clause to be ignored, got %#v", got)
	}
	if got := allDependencies(scanResult{
		DeclaredDependencies: map[string]dependencyInfo{
			"local_pkg": {LocalPath: true},
			"http":      {},
		},
	}); len(got) != 1 || got[0] != "http" {
		t.Fatalf("expected local path dependencies to be excluded, got %#v", got)
	}

	reportData, warnings := buildDependencyReport("http", scanResult{DeclaredDependencies: map[string]dependencyInfo{"http": {}}, Files: []fileScan{{Imports: []importBinding{{Dependency: "http", Module: "package:http/http.dart", Name: "Client", Local: "Client"}, {Dependency: "http", Module: "package:http/http.dart", Name: "Request", Local: "Request"}}, Usage: map[string]int{"Client": 1}}}}, 90)
	if len(warnings) != 0 || len(reportData.Recommendations) == 0 {
		t.Fatalf("expected low-usage dependency recommendation, report=%#v warnings=%#v", reportData, warnings)
	}
}
