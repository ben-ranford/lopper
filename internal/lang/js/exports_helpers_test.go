package js

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

const indexJSName = "index.js"

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
	if _, err := dependencyRoot(repo, "@scope"); err == nil {
		t.Fatalf("expected scoped dependency validation error")
	}
	if got, err := dependencyRoot(repo, "@scope/pkg"); err != nil || got != filepath.Join(repo, "node_modules", "@scope", "pkg") {
		t.Fatalf("unexpected scoped root: %q err=%v", got, err)
	}
}

func TestCollectExportPathsConditionWarnings(t *testing.T) {
	dest := make(map[string]struct{})
	surface := &ExportSurface{}
	exports := map[string]interface{}{
		"import": "./index.js",
		"types":  "./index.d.ts",
		"browser": map[string]interface{}{
			"default": "./bundle.css",
		},
		"nested": []interface{}{"./sub.js"},
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

	surface, err := resolveDependencyExports(repo, "missing", "")
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
	surface, err = resolveDependencyExports(repo, "bad", "")
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
	parseEntrypointsIntoSurface([]string{jsFile, jsFile, badFile, missingFile}, surface)

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
