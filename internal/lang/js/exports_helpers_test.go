package js

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

	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
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

	if got, ok, err := resolveEntrypoint(depRoot, "index"); err != nil || !ok || filepath.Base(got) != indexJSName {
		t.Fatalf("expected index.js entrypoint resolution, got %q ok=%v err=%v", got, ok, err)
	}
	if got, ok, err := resolveEntrypoint(depRoot, "subdir"); err != nil || !ok || filepath.Base(got) != indexJSName {
		t.Fatalf("expected directory entrypoint resolution, got %q ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := resolveEntrypoint(depRoot, "missing"); err != nil || ok {
		t.Fatalf("expected missing entrypoint to fail without error, got ok=%v err=%v", ok, err)
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
	badFile := filepath.Join(repo, "index.txt")
	if err := os.WriteFile(badFile, []byte("export const nope = 1\n"), 0o600); err != nil {
		t.Fatalf("write index.txt: %v", err)
	}
	missingFile := filepath.Join(repo, "missing.js")

	surface := &ExportSurface{Names: map[string]struct{}{}}
	parseEntrypointsIntoSurface(repo, []string{jsFile, jsFile, badFile, missingFile}, surface)

	if _, ok := surface.Names["value"]; !ok {
		t.Fatalf("expected parsed export name from valid entrypoint")
	}
	wantEntrypoints := []string{jsFile, badFile, missingFile}
	if !slices.Equal(surface.EntryPoints, wantEntrypoints) {
		t.Fatalf("unexpected first-seen entrypoint order: got %#v want %#v", surface.EntryPoints, wantEntrypoints)
	}
	warnings := strings.Join(surface.Warnings, "\n")
	if !strings.Contains(warnings, "failed to parse entrypoint") || !strings.Contains(warnings, "failed to read entrypoint") {
		t.Fatalf("expected parse/read warnings, got %#v", surface.Warnings)
	}
}

func TestParseEntrypointsIntoSurfaceRejectsOutsideEntryAndInvalidRoot(t *testing.T) {
	repo := t.TempDir()
	outsideEntry := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outsideEntry, []byte("export const escaped = 1\n"), 0o600); err != nil {
		t.Fatalf("write outside entry: %v", err)
	}

	surface := &ExportSurface{Names: map[string]struct{}{}}
	parseEntrypointsIntoSurface(repo, []string{outsideEntry}, surface)
	if len(surface.Warnings) != 1 || !strings.Contains(surface.Warnings[0], "failed to read entrypoint") {
		t.Fatalf("expected outside entrypoint warning, got %#v", surface.Warnings)
	}

	invalidRoot := filepath.Join(repo, "package.json")
	if err := os.WriteFile(invalidRoot, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write invalid root file: %v", err)
	}
	surface = &ExportSurface{Names: map[string]struct{}{}}
	parseEntrypointsIntoSurface(invalidRoot, []string{filepath.Join(repo, "index.js")}, surface)
	if len(surface.Warnings) != 1 || !strings.Contains(surface.Warnings[0], "failed to read entrypoint") {
		t.Fatalf("expected invalid-root warning, got %#v", surface.Warnings)
	}
}

func TestLoadPackageJSONForSurfaceReturnsCloseFailureAfterSuccessfulRead(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{"main":"index.js"}`)
	closeErr := errors.New("close failed")

	originalOpenDependencyRootNoFollow := openDependencyRootNoFollow
	openDependencyRootNoFollow = func(path string) (safeio.Root, error) {
		baseRoot, err := originalOpenDependencyRootNoFollow(path)
		if err != nil {
			return nil, err
		}
		return &closingLicenseRoot{Root: baseRoot, closeErr: closeErr}, nil
	}
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpenDependencyRootNoFollow
	})

	pkg, warnings, err := loadPackageJSONForSurface(depRoot, depRoot)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close failure after successful read, got pkg=%#v warnings=%#v err=%v", pkg, warnings, err)
	}
	if pkg.Main != "index.js" {
		t.Fatalf("expected parsed package.json result before close failure, got %#v", pkg)
	}
	if len(warnings) != 0 {
		t.Fatalf("did not expect read/parse warning when close fails after success, got %#v", warnings)
	}
}

func TestLoadPackageJSONForSurfaceJoinsParseAndCloseErrors(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{"name":`)
	closeErr := errors.New("close failed")

	originalOpenDependencyRootNoFollow := openDependencyRootNoFollow
	openDependencyRootNoFollow = func(path string) (safeio.Root, error) {
		baseRoot, err := originalOpenDependencyRootNoFollow(path)
		if err != nil {
			return nil, err
		}
		return &closingLicenseRoot{Root: baseRoot, closeErr: closeErr}, nil
	}
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpenDependencyRootNoFollow
	})

	pkg, warnings, err := loadPackageJSONForSurface(depRoot, depRoot)
	if err == nil || !errors.Is(err, closeErr) {
		t.Fatalf("expected parse and close errors, got pkg=%#v warnings=%#v err=%v", pkg, warnings, err)
	}
	if pkg.Name != "" {
		t.Fatalf("expected malformed package to remain empty, got %#v", pkg)
	}
	if len(warnings) != 1 || warnings[0] != "failed to parse dependency package.json" {
		t.Fatalf("expected stable parse warning, got %#v", warnings)
	}
	if !strings.Contains(err.Error(), "unexpected end of JSON input") || !strings.Contains(err.Error(), closeErr.Error()) {
		t.Fatalf("expected joined parse and close errors, got %v", err)
	}
}

func TestLoadPackageJSONForSurfaceJoinsReadAndCloseErrors(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{"name":"`+strings.Repeat("x", int(jsPackageJSONReadMaxBytes))+`"}`)
	closeErr := errors.New("close failed")

	originalOpenDependencyRootNoFollow := openDependencyRootNoFollow
	openDependencyRootNoFollow = func(path string) (safeio.Root, error) {
		baseRoot, err := originalOpenDependencyRootNoFollow(path)
		if err != nil {
			return nil, err
		}
		return &closingLicenseRoot{Root: baseRoot, closeErr: closeErr}, nil
	}
	t.Cleanup(func() {
		openDependencyRootNoFollow = originalOpenDependencyRootNoFollow
	})

	pkg, warnings, err := loadPackageJSONForSurface(depRoot, depRoot)
	if err == nil || !errors.Is(err, safeio.ErrFileTooLarge) || !errors.Is(err, closeErr) {
		t.Fatalf("expected read and close errors, got pkg=%#v warnings=%#v err=%v", pkg, warnings, err)
	}
	if pkg.Name != "" {
		t.Fatalf("expected oversized package to remain empty, got %#v", pkg)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read") {
		t.Fatalf("expected stable read warning, got %#v", warnings)
	}
	if !strings.Contains(err.Error(), closeErr.Error()) {
		t.Fatalf("expected close error string to be preserved, got %v", err)
	}
}

func TestResolveDependencyExportsPreservesStableEntrypointAndDynamicSampleOrder(t *testing.T) {
	depRoot := t.TempDir()
	packageData := `{"exports":{".":"./root.js","./z":"./z.js","./a":"./a.js","./m":"./m.js"}}`
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(packageData), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	orderedNames := []string{"root.js", "a.js", "m.js", "z.js"}
	wantEntrypoints := make([]string, 0, len(orderedNames))
	for _, name := range orderedNames {
		path := filepath.Join(depRoot, name)
		if err := os.WriteFile(path, []byte("const loader = require(target)\nexport { loader }\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		wantEntrypoints = append(wantEntrypoints, path)
	}
	wantCue := "dynamic require/import usage found in 4 dependency entrypoint location(s) (root.js:1, a.js:1, m.js:1)"

	for run := 0; run < 32; run++ {
		surface, err := resolveDependencyExports(dependencyExportRequest{
			dependency:         "pkg",
			dependencyRootPath: depRoot,
			runtimeProfileName: runtimeProfileNodeImport,
		})
		if err != nil {
			t.Fatalf("run %d resolve dependency exports: %v", run, err)
		}
		if !slices.Equal(surface.EntryPoints, wantEntrypoints) {
			t.Fatalf("run %d entrypoints: got %#v want %#v", run, surface.EntryPoints, wantEntrypoints)
		}

		cue, err := buildDynamicLoaderRiskCue(depRoot, surface.EntryPoints)
		if err != nil {
			t.Fatalf("run %d build dynamic-loader cue: %v", run, err)
		}
		if cue == nil || cue.Message != wantCue {
			t.Fatalf("run %d dynamic-loader cue: got %#v want %q", run, cue, wantCue)
		}
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

func TestParseExportClauseRecoversMissingAliasName(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`export { foo as }`)
	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	clause := firstNodeByType(tree.RootNode(), "export_clause")
	if clause == nil {
		t.Fatal("expected export clause")
	}
	names := parseExportClause(clause, source)
	if len(names) != 1 || names[0] != "foo" {
		t.Fatalf("expected missing export alias to fall back to source name, got %#v", names)
	}
}

func TestCollectExportNamesSkipsMalformedEmptyBindingIdentifiers(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`export const { foo: } = obj;`)
	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	names := collectExportNames(tree, source)
	if len(names) != 0 {
		t.Fatalf("expected malformed empty binding identifier to be skipped, got %#v", names)
	}
}

func TestExtractBindingNamesRejectsRecoveredEmptyIdentifier(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`const { foo: } = require("pkg");`)
	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	emptyIdentifier := firstNode(tree.RootNode(), func(node *sitter.Node) bool {
		return node.Type() == "identifier" && nodeText(node, source) == ""
	})
	if emptyIdentifier == nil {
		t.Fatal("expected recovered empty identifier node")
	}
	if names := extractBindingNames(emptyIdentifier, source); len(names) != 0 {
		t.Fatalf("expected recovered empty identifier to produce no binding names, got %#v", names)
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
	if !slices.Contains(entrypoints.ordered, "legacy.js") {
		t.Fatalf("expected fallback main entrypoint, got %#v", entrypoints)
	}
	joined := strings.Join(surface.Warnings, "\n")
	if !strings.Contains(joined, "no exports resolved for runtime profile") {
		t.Fatalf("expected fallback warning, got %#v", surface.Warnings)
	}
}

func TestResolveDependencyExportsUsesExplicitDependencyRootPath(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte("{\n  \"main\": \"index.js\"\n}\n"), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "index.js"), []byte("export const direct = 1\n"), 0o600); err != nil {
		t.Fatalf("write index.js: %v", err)
	}

	surface, err := resolveDependencyExports(dependencyExportRequest{dependencyRootPath: depRoot})
	if err != nil {
		t.Fatalf("resolve dependency exports with explicit root: %v", err)
	}
	if _, ok := surface.Names["direct"]; !ok {
		t.Fatalf("expected export from explicit dependency root, got %#v", surface.Names)
	}
}

func TestLoadPackageJSONForSurfaceUsesDependencyRootWhenRootPathEmpty(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte("{\"name\":\"pkg\"}"), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	pkg, warnings, err := loadPackageJSONForSurface("", depRoot)
	if err != nil {
		t.Fatalf("load package.json with empty root path: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %#v", warnings)
	}
	if pkg.Name != "pkg" {
		t.Fatalf("expected package name to parse, got %#v", pkg)
	}
}

func TestLoadPackageJSONForSurfaceRejectsSymlinkedDependencyRootAndInvalidRootPath(t *testing.T) {
	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outside, "package.json"), `{"name":"outside"}`)

	depRoot := filepath.Join(t.TempDir(), "pkg")
	if err := os.Symlink(outside, depRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, warnings, err := loadPackageJSONForSurface("", depRoot)
	if err == nil {
		t.Fatal("expected symlinked dependency root to be rejected")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read") {
		t.Fatalf("expected read warning for symlinked dependency root, got %#v", warnings)
	}

	validRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(validRoot, "package.json"), `{"name":"pkg"}`)
	invalidRootPath := filepath.Join(validRoot, "package.json")
	_, warnings, err = loadPackageJSONForSurface(invalidRootPath, validRoot)
	if err == nil {
		t.Fatal("expected non-directory root path to be rejected")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read") {
		t.Fatalf("expected read warning for invalid root path, got %#v", warnings)
	}
}

func TestLoadPackageJSONForSurfaceRejectsOversizedManifest(t *testing.T) {
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{"name":"`+strings.Repeat("x", int(jsPackageJSONReadMaxBytes))+`"}`)

	_, warnings, err := loadPackageJSONForSurface("", depRoot)
	if err == nil {
		t.Fatal("expected oversized package.json to be rejected")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read") {
		t.Fatalf("expected stable oversized read warning, got %#v", warnings)
	}
}

func TestLoadPackageJSONForSurfaceRejectsDependencyOutsideRoot(t *testing.T) {
	rootPath := t.TempDir()
	depRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(depRoot, "package.json"), `{"name":"pkg"}`)

	_, warnings, err := loadPackageJSONForSurface(rootPath, depRoot)
	if err == nil {
		t.Fatal("expected dependency outside root to be rejected")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unable to read") {
		t.Fatalf("expected stable outside-root warning, got %#v", warnings)
	}
}

func TestResolveEntrypointUnderRootReturnsFalseWhenRootCannotOpen(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	if path, ok, err := resolveEntrypointUnderRoot(missingRoot, missingRoot, "index.js"); ok || path != "" || err == nil {
		t.Fatalf("expected missing root to fail with error, got path=%q ok=%v err=%v", path, ok, err)
	}
}

func TestResolveEntrypointWithinRootRejectsSymlink(t *testing.T) {
	depRoot := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outsideFile, []byte("export const escaped = 1\n"), 0o600); err != nil {
		t.Fatalf("write outside.js: %v", err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(depRoot, "linked.js")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	root, err := safeio.OpenRoot(depRoot)
	if err != nil {
		t.Fatalf("open dependency root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Fatalf("close dependency root: %v", err)
		}
	})

	if path, ok := resolveEntrypointWithinRoot(root, depRoot, depRoot, "linked.js"); ok || path != "" {
		t.Fatalf("expected symlink entrypoint to be rejected, got path=%q ok=%v", path, ok)
	}
}

func TestLstatWithinRootRejectsPathOutsideRoot(t *testing.T) {
	rootPath := t.TempDir()
	outsideFile := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outsideFile, []byte("export const escaped = 1\n"), 0o600); err != nil {
		t.Fatalf("write outside.js: %v", err)
	}

	root, err := safeio.OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Fatalf("close root: %v", err)
		}
	})

	if info, ok := lstatWithinRoot(root, rootPath, outsideFile); ok || info != nil {
		t.Fatalf("expected outside path to be rejected, got info=%v ok=%v", info, ok)
	}
	if info, ok := lstatWithinRoot(root, rootPath, filepath.Dir(rootPath)); ok || info != nil {
		t.Fatalf("expected direct parent path to be rejected, got info=%v ok=%v", info, ok)
	}
}

func TestResolveEntrypointsCapsCandidateList(t *testing.T) {
	depRoot := t.TempDir()
	entrypoints := make(map[string]struct{}, maxExportEntrypoints+1)
	for i := 0; i < maxExportEntrypoints+1; i++ {
		entrypoints[fmt.Sprintf("./missing-%03d.js", i)] = struct{}{}
	}

	surface := &ExportSurface{}
	resolved := resolveEntrypoints(depRoot, depRoot, entrypointCandidates{ordered: sortedMapKeys(entrypoints), total: len(entrypoints)}, surface)
	if len(resolved) != 0 {
		t.Fatalf("expected unresolved candidate list, got %#v", resolved)
	}
	joined := strings.Join(surface.Warnings, "\n")
	if !strings.Contains(joined, fmt.Sprintf("capped dependency entrypoint resolution at %d candidates", maxExportEntrypoints)) {
		t.Fatalf("expected cap warning, got %#v", surface.Warnings)
	}
}

func TestResolveEntrypointsReturnsNilWhenRootCannotOpen(t *testing.T) {
	missingRoot := filepath.Join(t.TempDir(), "missing")
	resolved := resolveEntrypoints(missingRoot, missingRoot, entrypointCandidates{ordered: []string{"./index.js"}, total: 1}, &ExportSurface{})
	if len(resolved) != 0 {
		t.Fatalf("expected missing root to produce no resolutions, got %#v", resolved)
	}
}

func TestResolveEntrypointsClearsResultsWhenRootCloseFails(t *testing.T) {
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(depRoot, "index.js"), []byte("export const value = 1\n"), 0o600); err != nil {
		t.Fatalf("write index.js: %v", err)
	}

	originalOpenRoot := openEntrypointRoot
	openEntrypointRoot = func(string) (safeio.Root, error) {
		return &fakeEntrypointRoot{
			depRoot:   depRoot,
			closeErr:  errors.New("close failed"),
			closeHits: new(int),
		}, nil
	}
	t.Cleanup(func() {
		openEntrypointRoot = originalOpenRoot
	})

	surface := &ExportSurface{}
	resolved := resolveEntrypoints(depRoot, depRoot, entrypointCandidates{ordered: []string{"./index.js"}, total: 1}, surface)
	if len(resolved) != 0 {
		t.Fatalf("expected close failure to discard resolved entrypoints, got %#v", resolved)
	}
	if !strings.Contains(strings.Join(surface.Warnings, "\n"), "failed to close dependency root after entrypoint resolution") || !strings.Contains(strings.Join(surface.Warnings, "\n"), "close failed") {
		t.Fatalf("expected close warning, got %#v", surface.Warnings)
	}
}

func TestPrioritizedExportEntrypointsPrefersRootThenSortedSubpaths(t *testing.T) {
	profile := runtimeProfile{name: runtimeProfileNodeImport, conditions: []string{"node", "import", "default"}}
	got := prioritizedExportEntrypoints(map[string]any{"./z": "./z.js", ".": "./root.js", "./a": "./a.js"}, profile)
	want := []string{"./root.js", "./a.js", "./z.js"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected prioritized export entrypoints: got %#v want %#v", got, want)
	}

	got = prioritizedExportEntrypoints("./plain.js", profile)
	if !slices.Equal(got, []string{"./plain.js"}) {
		t.Fatalf("expected plain export string to resolve directly, got %#v", got)
	}

	got = prioritizedExportEntrypoints(map[string]any{"import": "./import.js", "default": "./default.js"}, profile)
	if !slices.Equal(got, []string{"./import.js"}) {
		t.Fatalf("expected conditional export map without subpaths to resolve directly, got %#v", got)
	}
}

func TestPrioritizedEntrypointsPrefersLegacyFieldsBeforeSortedRemainder(t *testing.T) {
	profile := runtimeProfile{name: runtimeProfileNodeImport, conditions: []string{"node", "import", "default"}}
	entrypoints := map[string]struct{}{
		"./z-last.js":   {},
		"./m-middle.js": {},
		"legacy.js":     {},
		"types.d.ts":    {},
	}

	got := prioritizedEntrypoints(packageJSON{Main: "legacy.js", Types: "types.d.ts"}, profile, entrypoints)
	wantPrefix := []string{"legacy.js", "types.d.ts"}
	if !slices.Equal(got[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("expected legacy fields to lead prioritized entrypoints, got %#v", got)
	}
}

func TestResolveDependencyExportsPrioritizesPrimaryEntrypointBeyondNaiveCap(t *testing.T) {
	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dep root: %v", err)
	}

	exports := make([]string, 0, maxExportEntrypoints+4)
	exports = append(exports, `".": "./zzz-primary.js"`)
	for i := 0; i < maxExportEntrypoints+2; i++ {
		exports = append(exports, fmt.Sprintf(`"./subpath-%03d": "./aaa-missing-%03d.js"`, i, i))
	}

	packageJSON := fmt.Sprintf("{\n  \"exports\": {\n    %s\n  }\n}\n", strings.Join(exports, ",\n    "))
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(packageJSON), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "zzz-primary.js"), []byte("export const primary = 1\n"), 0o600); err != nil {
		t.Fatalf("write primary entrypoint: %v", err)
	}

	surface, err := resolveDependencyExports(dependencyExportRequest{
		repoPath:           repo,
		dependency:         "pkg",
		runtimeProfileName: runtimeProfileNodeImport,
	})
	if err != nil {
		t.Fatalf("resolve dependency exports: %v", err)
	}
	if _, ok := surface.Names["primary"]; !ok {
		t.Fatalf("expected primary export to survive capped resolution, got names=%#v warnings=%#v", surface.Names, surface.Warnings)
	}
	joined := strings.Join(surface.Warnings, "\n")
	if !strings.Contains(joined, fmt.Sprintf("capped dependency entrypoint resolution at %d candidates", maxExportEntrypoints)) {
		t.Fatalf("expected cap warning, got %#v", surface.Warnings)
	}
}

func TestResolveEntrypointsCapOrderIsDeterministic(t *testing.T) {
	depRoot := t.TempDir()
	entrypoints := make(map[string]struct{}, maxExportEntrypoints+4)
	ordered := make([]string, 0, maxExportEntrypoints+4)

	ordered = append(ordered, "./zzz-primary.js")
	entrypoints["./zzz-primary.js"] = struct{}{}
	if err := os.WriteFile(filepath.Join(depRoot, "zzz-primary.js"), []byte("export const primary = 1\n"), 0o600); err != nil {
		t.Fatalf("write primary entrypoint: %v", err)
	}

	for i := 0; i < maxExportEntrypoints+3; i++ {
		entry := fmt.Sprintf("./subpath-%03d.js", i)
		entrypoints[entry] = struct{}{}
		ordered = append(ordered, entry)
		if err := os.WriteFile(filepath.Join(depRoot, fmt.Sprintf("subpath-%03d.js", i)), []byte(fmt.Sprintf("export const value%03d = %d\n", i, i)), 0o600); err != nil {
			t.Fatalf("write subpath entrypoint %d: %v", i, err)
		}
	}

	surface := &ExportSurface{}
	resolved := resolveEntrypoints(depRoot, depRoot, entrypointCandidates{ordered: ordered, total: len(entrypoints)}, surface)

	if len(resolved) != maxExportEntrypoints {
		t.Fatalf("expected exactly %d resolved entrypoints, got %d", maxExportEntrypoints, len(resolved))
	}
	if filepath.Base(resolved[0]) != "zzz-primary.js" {
		t.Fatalf("expected primary entrypoint first, got %#v", resolved[:min(3, len(resolved))])
	}
	if filepath.Base(resolved[len(resolved)-1]) != fmt.Sprintf("subpath-%03d.js", maxExportEntrypoints-2) {
		t.Fatalf("expected deterministic last included entrypoint, got %q", filepath.Base(resolved[len(resolved)-1]))
	}
	for _, path := range resolved {
		if filepath.Base(path) == fmt.Sprintf("subpath-%03d.js", maxExportEntrypoints-1) {
			t.Fatalf("did not expect entrypoint beyond cap to resolve, got %#v", resolved)
		}
	}

	joined := strings.Join(surface.Warnings, "\n")
	if !strings.Contains(joined, fmt.Sprintf("capped dependency entrypoint resolution at %d candidates", maxExportEntrypoints)) {
		t.Fatalf("expected cap warning, got %#v", surface.Warnings)
	}
}

func TestParseEntrypointsIntoSurfaceReturnsWhenRootPathEmpty(t *testing.T) {
	surface := &ExportSurface{Names: map[string]struct{}{}}
	parseEntrypointsIntoSurface("", []string{"index.js"}, surface)
	if len(surface.Names) != 0 || len(surface.EntryPoints) != 0 || len(surface.Warnings) != 0 {
		t.Fatalf("expected empty-root parse to leave surface untouched, got %#v", surface)
	}
}

type fakeEntrypointRoot struct {
	depRoot   string
	closeErr  error
	closeHits *int
}

func (r *fakeEntrypointRoot) Open(string) (safeio.File, error) {
	return nil, errors.New("not implemented")
}

func (r *fakeEntrypointRoot) OpenFile(string, int, os.FileMode) (safeio.File, error) {
	return nil, errors.New("not implemented")
}

func (r *fakeEntrypointRoot) OpenRoot(string) (safeio.Root, error) {
	return nil, errors.New("not implemented")
}

func (r *fakeEntrypointRoot) Lstat(name string) (fs.FileInfo, error) {
	return os.Lstat(filepath.Join(r.depRoot, name))
}

func (r *fakeEntrypointRoot) Mkdir(string, os.FileMode) error {
	return errors.New("not implemented")
}

func (r *fakeEntrypointRoot) Chmod(string, os.FileMode) error {
	return errors.New("not implemented")
}

func (r *fakeEntrypointRoot) MkdirAll(string, os.FileMode) error {
	return errors.New("not implemented")
}

func (r *fakeEntrypointRoot) Rename(string, string) error {
	return errors.New("not implemented")
}

func (r *fakeEntrypointRoot) Remove(string) error {
	return errors.New("not implemented")
}

func (r *fakeEntrypointRoot) Close() error {
	if r.closeHits != nil {
		hits := *r.closeHits
		*r.closeHits = hits + 1
	}
	return r.closeErr
}
