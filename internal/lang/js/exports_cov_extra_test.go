package js

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestJSExportsCoverageHelpers(t *testing.T) {
	if profile, _ := resolveRuntimeProfile(runtimeProfileBrowserImport); profile.name != runtimeProfileBrowserImport {
		t.Fatalf("expected browser-import runtime profile")
	}
	if profile, _ := resolveRuntimeProfile(runtimeProfileBrowserRequire); profile.name != runtimeProfileBrowserRequire {
		t.Fatalf("expected browser-require runtime profile")
	}
	if _, err := resolveDependencyExports(dependencyExportRequest{}); err == nil {
		t.Fatalf("expected dependency export resolution to error for empty request")
	}

	repo := t.TempDir()
	depRoot := filepath.Join(repo, "node_modules", "pkg")
	if err := os.MkdirAll(depRoot, 0o755); err != nil {
		t.Fatalf("mkdir dep root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "package.json"), []byte(`{"exports":{"default":"./missing.js"}}`), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	surface, err := resolveDependencyExports(dependencyExportRequest{
		repoPath:           repo,
		dependency:         "pkg",
		runtimeProfileName: "unknown-profile",
	})
	if err != nil {
		t.Fatalf("resolveDependencyExports unknown profile: %v", err)
	}
	if len(surface.Warnings) == 0 {
		t.Fatalf("expected warnings for unknown profile and unresolved entrypoint")
	}
	if got := resolveEntrypoints(depRoot, map[string]struct{}{"./missing.js": struct{}{}}, &surface); len(got) != 0 {
		t.Fatalf("expected unresolved entrypoint warning path")
	}

	entrypoints := collectCandidateEntrypoints(packageJSON{}, runtimeProfile{name: "x"}, &ExportSurface{})
	if _, ok := entrypoints["index.js"]; !ok {
		t.Fatalf("expected index.js fallback entrypoint")
	}

	profile := runtimeProfile{name: "node-import", conditions: []string{"node", "import", "default"}}
	paths, ok := resolveSubpathExportMap(map[string]interface{}{
		"import": "./x.js",
		".":      "./index.js",
	}, profile, "exports", &ExportSurface{})
	if !ok || len(paths) != 1 || paths[0] != "./index.js" {
		t.Fatalf("expected subpath-only resolution, got ok=%v paths=%#v", ok, paths)
	}
	if paths, ok := resolveConditionalExportMap(map[string]interface{}{"browser": "./x.js"}, profile, "exports", &ExportSurface{}); ok || len(paths) != 0 {
		t.Fatalf("expected conditional export miss for unmatched profile")
	}
	if got := sortedMapKeys(map[string]struct{}{}); len(got) != 0 {
		t.Fatalf("expected empty sorted map keys")
	}

	if paths, ok := resolveArrayExportNode([]interface{}{"./styles.css"}, profile, "exports", &ExportSurface{}); ok || len(paths) != 0 {
		t.Fatalf("expected array export with no code assets to fail")
	}
	if paths, ok := resolveObjectExportMap(map[string]interface{}{"a": "./styles.css"}, profile, "exports", &ExportSurface{}); ok || len(paths) != 0 {
		t.Fatalf("expected object export map with non-code entries to fail")
	}

	parser := newSourceParser()
	src := []byte("export default 1; export * from \"./x.js\"; export * as ns from \"./y.js\";")
	tree, err := parser.Parse(context.Background(), "index.js", src)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	var sawDefault, sawStar, sawNamespace bool
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		if node.Type() != "export_statement" {
			return
		}
		names := parseExportStatement(node, src)
		for _, name := range names {
			if name == "default" {
				sawDefault = true
			}
			if name == "*" {
				sawStar = true
			}
			if name == "ns" {
				sawNamespace = true
			}
		}
	})
	if !sawDefault || !sawStar || !sawNamespace {
		t.Fatalf("expected default/star/namespace export names to be parsed")
	}

	if decl := parseExportDeclaration(tree.RootNode(), src); decl != nil {
		t.Fatalf("expected non-declaration node to return nil export declaration parse")
	}
}
