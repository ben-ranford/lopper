package js

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

const (
	malformedIndexJS = "index.js"
	malformedBadJS   = "bad.js"
	malformedModule  = "mod"
)

func TestJSMalformedSyntaxCoverageBranches(t *testing.T) {
	parser := newSourceParser()
	src := []byte("export; export {,a}; import from; const { :x } = require(\"m\");")
	tree, err := parser.Parse(context.Background(), malformedIndexJS, src)
	if err != nil {
		t.Fatalf("parse malformed source: %v", err)
	}

	exportStmt := firstNodeByType(tree.RootNode(), "export_statement")
	importStmt := firstNodeByType(tree.RootNode(), "import_statement")
	requireCall := firstNodeByType(tree.RootNode(), "call_expression")

	if exportStmt == nil {
		t.Fatalf("expected export statement in malformed source")
	}
	_ = parseExportStatement(exportStmt, src)

	if importStmt != nil {
		_ = parseImportStatement(importStmt, src, malformedIndexJS)
	}
	if requireCall != nil {
		_ = parseRequireCall(requireCall, src, malformedIndexJS)
	}

	// Explicitly exercise re-export collection guard for imports without local binding names.
	imports := []ImportBinding{{Module: "pkg", ExportName: "*", LocalName: "", Kind: ImportNamespace}}
	_ = collectReExportBindings(tree, src, malformedIndexJS, imports)
}

func TestJSScanDirectBranchCoverage(t *testing.T) {
	parser := newSourceParser()

	repo := t.TempDir()
	path := filepath.Join(repo, malformedBadJS)
	if err := os.WriteFile(path, []byte("import a from b; const [x] = require(mod); const { :y } = require(\"m\");"), 0o600); err != nil {
		t.Fatalf("write bad.js: %v", err)
	}
	tree, err := parser.Parse(context.Background(), path, mustReadFile(t, path))
	if err != nil {
		t.Fatalf("parse bad.js: %v", err)
	}

	// Force scanRepoEntry read error branch by deleting file after obtaining entry metadata.
	entry, err := os.ReadDir(repo)
	if err != nil || len(entry) == 0 {
		t.Fatalf("readdir repo: %v", err)
	}
	_ = os.Remove(path)
	state := scanRepoState{parser: parser, repoPath: repo, result: &ScanResult{}}
	_ = scanRepoEntry(context.Background(), &state, path, entry[0])

	// Recreate file for AST-level helper branch exercises.
	if err := os.WriteFile(path, []byte("import a from b; const [x] = require(mod); const { :y } = require(\"m\"); const p = obj.prop;"), 0o600); err != nil {
		t.Fatalf("rewrite bad.js: %v", err)
	}
	content := mustReadFile(t, path)
	tree, err = parser.Parse(context.Background(), path, content)
	if err != nil {
		t.Fatalf("reparse bad.js: %v", err)
	}

	importStmt := firstNodeByType(tree.RootNode(), "import_statement")
	requireCall := firstNodeByType(tree.RootNode(), "call_expression")
	propIdent := firstNodeByType(tree.RootNode(), "property_identifier")
	patternIdent := firstNode(tree.RootNode(), func(node *sitter.Node) bool {
		return node.Type() == "identifier" && node.Parent() != nil && node.Parent().Type() == "object_pattern"
	})

	if importStmt != nil {
		_ = parseImportStatement(importStmt, content, malformedBadJS)
	}
	if requireCall != nil {
		_ = parseRequireCall(requireCall, content, malformedBadJS)
		_ = parseRequireBinding(requireCall, content, malformedModule, malformedBadJS)
	}
	if propIdent != nil && !isIdentifierUsage(propIdent) {
		t.Fatalf("expected property identifier usage branch to return true")
	}
	if patternIdent != nil && isIdentifierUsage(patternIdent) {
		t.Fatalf("expected object pattern identifier usage branch to return false")
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	return content
}
