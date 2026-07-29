package js

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

const indexJSName = "index.js"
const testExportPathA = "./a.js"
const testMalformedDependency = "@scope"
const testDottedDependency = "pkg.name"

func TestExportParsingHelpers(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`
export { alpha as beta, gamma };
export default function main() {}
export function helper() {}
export class Widget {}
export const { first, nested: { second }, alias: third } = value;
export const [arrOne, , arrTwo] = list;
export * from "./other.js";
`)
	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	names := collectExportNames(tree, source)
	for _, want := range []string{"beta", "gamma", "helper", "Widget", "first", "second", "third", "arrOne", "arrTwo", "*"} {
		if !slices.Contains(names, want) {
			t.Fatalf("expected export name %q in %#v", want, names)
		}
	}

	surface := &ExportSurface{Names: map[string]struct{}{}}
	addCollectedExports(surface, names)
	if !surface.IncludesWildcard {
		t.Fatalf("expected wildcard export surface flag")
	}
	if _, ok := surface.Names["beta"]; !ok {
		t.Fatalf("expected named exports in export surface")
	}
}

func TestEntrypointAndPathHelpers(t *testing.T) {
	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dep root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, indexJSName), []byte("export const x = 1"), 0o600); err != nil {
		t.Fatalf("write index.js: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(depRoot, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "subdir", indexJSName), []byte("export const y = 2"), 0o600); err != nil {
		t.Fatalf("write subdir index.js: %v", err)
	}

	if got, ok := resolveEntrypoint(depRoot, "index"); !ok || filepath.Base(got) != indexJSName {
		t.Fatalf("expected index.js entrypoint resolution, got %q ok=%v", got, ok)
	}
	if got, ok := resolveEntrypoint(depRoot, "subdir"); !ok || filepath.Base(got) != indexJSName {
		t.Fatalf("expected directory entrypoint resolution, got %q ok=%v", got, ok)
	}
	if _, ok := resolveEntrypoint(depRoot, "missing"); ok {
		t.Fatalf("expected missing entrypoint to fail")
	}

	if _, err := dependencyRoot("", "pkg"); err == nil {
		t.Fatalf("expected repo-path validation error")
	}
	if _, err := dependencyRoot(repo, ""); err == nil {
		t.Fatalf("expected dependency validation error")
	}
	if _, err := dependencyRoot(repo, testMalformedDependency); err == nil {
		t.Fatalf("expected scoped dependency validation error")
	}
	if got, err := dependencyRoot(repo, testMalformedDependency+"/pkg"); err != nil || got != filepath.Join(repo, "node_modules", testMalformedDependency, "pkg") {
		t.Fatalf("unexpected scoped root: %q err=%v", got, err)
	}
}

func TestResolveEntrypointsMarksMissingCoverageIncomplete(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, indexJSName), []byte("export const safe = 1\n"), 0o600); err != nil {
		t.Fatalf("write index.js: %v", err)
	}

	surface := &ExportSurface{Names: map[string]struct{}{}}
	entrypoints := map[string]struct{}{
		indexJSName:    {},
		"./missing.js": {},
	}
	resolved := resolveEntrypoints(depRoot, entrypoints, surface)

	if len(resolved) != 1 {
		t.Fatalf("expected valid entrypoint to remain resolved, got %#v", resolved)
	}
	if !boolFieldForTest(*surface, "CoverageIncomplete") {
		t.Fatal("expected unresolved declared entrypoint to mark export coverage incomplete")
	}
}

func TestDependencyRootRejectsMalformedNames(t *testing.T) {
	repo := t.TempDir()
	valid := map[string]string{
		"pkg":                                 filepath.Join(repo, "node_modules", "pkg"),
		"left-pad":                            filepath.Join(repo, "node_modules", "left-pad"),
		testDottedDependency:                  filepath.Join(repo, "node_modules", testDottedDependency),
		testMalformedDependency + "/pkg":      filepath.Join(repo, "node_modules", testMalformedDependency, "pkg"),
		testMalformedDependency + "/pkg.name": filepath.Join(repo, "node_modules", testMalformedDependency, testDottedDependency),
	}
	for dep, want := range valid {
		got, err := dependencyRoot(repo, dep)
		if err != nil {
			t.Fatalf("expected valid dependency %q, got err=%v", dep, err)
		}
		if got != want {
			t.Fatalf("unexpected root for %q: got %q want %q", dep, got, want)
		}
	}

	invalid := []string{
		".",
		"..",
		"../../evil",
		"pkg/subpath",
		testMalformedDependency,
		testMalformedDependency + "/pkg/subpath",
		`pkg\subpath`,
		testMalformedDependency + `\pkg`,
		`..\evil`,
	}
	for _, dep := range invalid {
		if _, err := dependencyRoot(repo, dep); err == nil {
			t.Fatalf("expected invalid dependency %q to fail", dep)
		}
	}
}

func TestCollectExportPathsConditionWarnings(t *testing.T) {
	dest := make(map[string]struct{})
	surface := &ExportSurface{}
	exports := map[string]any{
		"import": "./index.js",
		"types":  "./index.d.ts",
		"browser": map[string]any{
			"default": "./bundle.css",
		},
		"nested": []any{"./sub.js"},
	}
	collectExportPaths(exports, dest, surface)
	if len(dest) == 0 {
		t.Fatalf("expected export paths to be collected")
	}
	if _, ok := dest["./index.js"]; !ok {
		t.Fatalf("expected js asset entrypoint")
	}
	if len(surface.Warnings) == 0 {
		t.Fatalf("expected warning for non-js condition asset")
	}
	if !looksLikeConditionKey("default") || looksLikeConditionKey("custom") {
		t.Fatalf("unexpected condition key detection")
	}
	if !isLikelyCodeAsset("file.ts") || isLikelyCodeAsset("file.css") {
		t.Fatalf("unexpected code asset detection")
	}
}

func TestResolveDependencyExportsMissingAndInvalidPackageJSON(t *testing.T) {
	repo := t.TempDir()

	surface, err := resolveDependencyExports(dependencyExportRequest{
		repoPath:   repo,
		dependency: "missing",
	})
	if err != nil {
		t.Fatalf("resolve missing dependency exports: %v", err)
	}
	if len(surface.Warnings) == 0 {
		t.Fatalf("expected warning for missing dependency package.json")
	}

	badRoot := filepath.Join(repo, "node_modules", "bad")
	if err := os.MkdirAll(badRoot, 0o755); err != nil {
		t.Fatalf("mkdir bad root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badRoot, "package.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid package.json: %v", err)
	}
	surface, err = resolveDependencyExports(dependencyExportRequest{
		repoPath:   repo,
		dependency: "bad",
	})
	if err != nil {
		t.Fatalf("resolve invalid dependency exports: %v", err)
	}
	if len(surface.Warnings) == 0 {
		t.Fatalf("expected parse warning for invalid package.json")
	}
}

func TestParseEntrypointsIntoSurfaceReadAndParseWarnings(t *testing.T) {
	repo := t.TempDir()
	jsFile := filepath.Join(repo, indexJSName)
	if err := os.WriteFile(jsFile, []byte("export const value = 1\n"), 0o600); err != nil {
		t.Fatalf("write index.js: %v", err)
	}
	largeFile := filepath.Join(repo, "large.js")
	if err := os.WriteFile(largeFile, []byte(strings.Repeat("a", oversizedJSFileSize)), 0o600); err != nil {
		t.Fatalf("write large.js: %v", err)
	}
	badFile := filepath.Join(repo, "index.txt")
	if err := os.WriteFile(badFile, []byte("export const nope = 1\n"), 0o600); err != nil {
		t.Fatalf("write index.txt: %v", err)
	}
	missingFile := filepath.Join(repo, "missing.js")

	surface := &ExportSurface{Names: map[string]struct{}{}}
	parseEntrypointsIntoSurface(repo, []string{jsFile, jsFile, largeFile, badFile, missingFile}, surface)

	if _, ok := surface.Names["value"]; !ok {
		t.Fatalf("expected parsed export name from valid entrypoint")
	}
	if len(surface.EntryPoints) < 2 {
		t.Fatalf("expected deduplicated entrypoint list, got %#v", surface.EntryPoints)
	}
	warnings := strings.Join(surface.Warnings, "\n")
	if !strings.Contains(warnings, "failed to parse entrypoint") || !strings.Contains(warnings, "failed to read entrypoint") {
		t.Fatalf("expected parse/read warnings, got %#v", surface.Warnings)
	}
	if !slices.Contains(surface.Warnings, "failed to read entrypoint: "+largeFile) {
		t.Fatalf("expected oversized entrypoint warning for large.js, got %#v", surface.Warnings)
	}
	if !boolFieldForTest(*surface, "CoverageIncomplete") {
		t.Fatal("expected skipped entrypoints to mark export coverage incomplete")
	}
}

func TestParseEntrypointsIntoSurfaceMarksRecoveryTreesIncomplete(t *testing.T) {
	depRoot := t.TempDir()
	validFile := filepath.Join(depRoot, indexJSName)
	if err := os.WriteFile(validFile, []byte("export const safe = 1\n"), 0o600); err != nil {
		t.Fatalf("write valid entrypoint: %v", err)
	}
	brokenFile := filepath.Join(depRoot, "broken.js")
	if err := os.WriteFile(brokenFile, []byte("export const partial = ;\n"), 0o600); err != nil {
		t.Fatalf("write broken entrypoint: %v", err)
	}

	surface := &ExportSurface{Names: map[string]struct{}{}}
	parseEntrypointsIntoSurface(depRoot, []string{validFile, brokenFile}, surface)

	if _, ok := surface.Names["safe"]; !ok {
		t.Fatal("expected valid entrypoint export to remain available")
	}
	if !boolFieldForTest(*surface, "CoverageIncomplete") {
		t.Fatal("expected syntax-recovery tree to mark export coverage incomplete")
	}
	if !slices.Contains(surface.Warnings, "failed to parse entrypoint: "+brokenFile) {
		t.Fatalf("expected syntax-recovery warning, got %#v", surface.Warnings)
	}
}

func TestBuildDependencyReportSuppressesIncompleteExportSurface(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(`{"name":"dep","exports":{".":"./index.js","./large":"./large.js"}}`), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, indexJSName), []byte("export const safe = 1\n"), 0o600); err != nil {
		t.Fatalf("write index.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "large.js"), []byte(strings.Repeat("a", oversizedJSFileSize)), 0o600); err != nil {
		t.Fatalf("write oversized entrypoint: %v", err)
	}

	dependency, warnings := buildDependencyReport(dependencyReportOptions{
		Dependency:                        "dep",
		DependencyRootPath:                depRoot,
		ScanResult:                        ScanResult{},
		MinUsagePercentForRecommendations: 100,
		SuggestOnly:                       true,
	})

	if dependency.UsedExportsCount != 0 || dependency.TotalExportsCount != 0 || dependency.UsedPercent != 0 {
		t.Fatalf("expected partial export metrics to be suppressed, got %#v", dependency)
	}
	if len(dependency.UnusedExports) != 0 || len(dependency.Recommendations) != 0 {
		t.Fatalf("expected partial export removal advice to be suppressed, got %#v", dependency)
	}
	assertRemovalSignalsSuppressed(t, dependency, warnings)
}

func TestBuildDependencyReportSuppressesSignalsForMalformedPackageMetadata(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, indexJSName), []byte("import { broken } from \"broken\"\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	depRoot := filepath.Join(repo, "node_modules", "broken")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dependency root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write malformed package.json: %v", err)
	}

	scanResult, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	dependency, warnings := buildDependencyReport(dependencyReportOptions{
		RepoPath:                          repo,
		Dependency:                        "broken",
		DependencyRootPath:                depRoot,
		ScanResult:                        scanResult,
		MinUsagePercentForRecommendations: 1,
		SuggestOnly:                       true,
	})

	if !dependency.UsageIncomplete {
		t.Fatalf("expected malformed package metadata to mark dependency usage incomplete, got %#v", dependency)
	}
	if len(dependency.UnusedExports) != 0 {
		t.Fatalf("expected malformed package metadata to suppress unused exports, got %#v", dependency.UnusedExports)
	}
	if len(dependency.Recommendations) != 0 {
		t.Fatalf("expected malformed package metadata to suppress recommendations, got %#v", dependency.Recommendations)
	}
	assertRemovalSignalsSuppressed(t, dependency, warnings)

	joinedWarnings := strings.Join(warnings, "\n")
	if !strings.Contains(joinedWarnings, "failed to parse dependency package.json") {
		t.Fatalf("expected malformed package metadata warning to remain visible, got %#v", warnings)
	}
}

func boolFieldForTest(value any, name string) bool {
	field := reflect.ValueOf(value).FieldByName(name)
	return field.IsValid() && field.Kind() == reflect.Bool && field.Bool()
}

func TestParseEntrypointsIntoSurfaceRejectsSymlinkedEntrypoint(t *testing.T) {
	depPath := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outsidePath, []byte("export const outside = 1\n"), 0o600); err != nil {
		t.Fatalf("write outside entrypoint: %v", err)
	}
	entrypoint := filepath.Join(depPath, indexJSName)
	if err := os.Symlink(outsidePath, entrypoint); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	surface := &ExportSurface{Names: map[string]struct{}{}}
	parseEntrypointsIntoSurface(depPath, []string{entrypoint}, surface)

	if !slices.Contains(surface.Warnings, "failed to read entrypoint: "+entrypoint) {
		t.Fatalf("expected symlinked entrypoint warning, got %#v", surface.Warnings)
	}
	if len(surface.Names) != 0 {
		t.Fatalf("expected no exports from outside target, got %#v", surface.Names)
	}
}

func TestExportBindingExtractionBranches(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`
export const { base: alias = 1, ...rest } = obj;
export const [first = 1, ...tail] = arr;
function f(...args) { return args }
`)
	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	names := collectExportNames(tree, source)
	for _, want := range []string{"alias", "first"} {
		if !slices.Contains(names, want) {
			t.Fatalf("expected export binding name %q in %#v", want, names)
		}
	}

	var sawAssignmentPattern bool
	var sawRestPattern bool
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		switch node.Type() {
		case "assignment_pattern":
			sawAssignmentPattern = true
			binding := extractBindingNames(node, source)
			if len(binding) == 0 {
				t.Fatalf("expected assignment_pattern binding names")
			}
		case "rest_pattern":
			sawRestPattern = true
			_ = extractBindingNames(node, source)
		}
	})
	if !sawAssignmentPattern || !sawRestPattern {
		t.Fatalf("expected assignment and rest patterns in parsed source")
	}
}

func TestResolveRuntimeProfileUnknownAndSupportedList(t *testing.T) {
	profile, warning := resolveRuntimeProfile("custom-runtime")
	if profile.name != defaultRuntimeProfile {
		t.Fatalf("expected default runtime profile %q, got %q", defaultRuntimeProfile, profile.name)
	}
	if !strings.Contains(warning, "unknown runtime profile") {
		t.Fatalf("expected unknown-profile warning, got %q", warning)
	}
	for _, expected := range supportedRuntimeProfiles() {
		if !strings.Contains(warning, expected) {
			t.Fatalf("expected warning to include supported profile %q, got %q", expected, warning)
		}
	}
}

func TestResolveExportNodeBranches(t *testing.T) {
	profile := runtimeProfile{name: "node-import", conditions: []string{"node", "import", "default"}}
	surface := &ExportSurface{}

	if paths, ok := resolveExportNode(42, profile, "exports", surface); ok || len(paths) != 0 {
		t.Fatalf("expected unsupported export value type to fail, got ok=%v paths=%#v", ok, paths)
	}
	if paths, ok := resolveExportNode(map[string]any{}, profile, "exports", surface); ok || len(paths) != 0 {
		t.Fatalf("expected empty export map to fail, got ok=%v paths=%#v", ok, paths)
	}

	paths, ok := resolveExportNode([]any{42, testExportPathA}, profile, "exports", surface)
	if !ok || len(paths) != 1 || paths[0] != testExportPathA {
		t.Fatalf("expected array export node to resolve first valid path, got ok=%v paths=%#v", ok, paths)
	}

	paths, ok = resolveExportNode(map[string]any{"zz": "./z.js", "aa": testExportPathA}, profile, "exports", surface)
	if !ok || len(paths) != 2 || paths[0] != testExportPathA || paths[1] != "./z.js" {
		t.Fatalf("expected non-condition map traversal with sorted unique paths, got ok=%v paths=%#v", ok, paths)
	}
}

func TestCollectCandidateEntrypointsFallsBackWhenProfileResolvesNoExports(t *testing.T) {
	surface := &ExportSurface{}
	entrypoints := collectCandidateEntrypoints(packageJSON{Exports: map[string]any{".": map[string]any{"import": "./styles.css"}}, Main: "legacy.js"}, runtimeProfile{name: "node-import", conditions: []string{"node", "import", "default"}}, surface)
	if _, ok := entrypoints["legacy.js"]; !ok {
		t.Fatalf("expected fallback main entrypoint, got %#v", entrypoints)
	}
	joined := strings.Join(surface.Warnings, "\n")
	if !strings.Contains(joined, "no exports resolved for runtime profile") {
		t.Fatalf("expected fallback warning, got %#v", surface.Warnings)
	}
}
