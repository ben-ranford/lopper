package js

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestJSMalformedSyntaxCoverageBranches(t *testing.T) {
	parser := newSourceParser()
	src := []byte("export; export {,a}; import from; const { :x } = require(\"m\");")
	tree, err := parser.Parse(context.Background(), "index.js", src)
	if err != nil {
		t.Fatalf("parse malformed source: %v", err)
	}

	var exportStmt, importStmt, requireCall *sitter.Node
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		switch node.Type() {
		case "export_statement":
			if exportStmt == nil {
				exportStmt = node
			}
		case "import_statement":
			if importStmt == nil {
				importStmt = node
			}
		case "call_expression":
			if requireCall == nil {
				requireCall = node
			}
		}
	})

	if exportStmt == nil {
		t.Fatalf("expected export statement in malformed source")
	}
	_ = parseExportStatement(exportStmt, src)

	if importStmt != nil {
		_ = parseImportStatement(importStmt, src, "index.js")
	}
	if requireCall != nil {
		_ = parseRequireCall(requireCall, src, "index.js")
	}

	// Explicitly exercise re-export collection guard for imports without local binding names.
	imports := []ImportBinding{{Module: "pkg", ExportName: "*", LocalName: "", Kind: ImportNamespace}}
	_ = collectReExportBindings(tree, src, "index.js", imports)
}

func TestJSScanDirectBranchCoverage(t *testing.T) {
	parser := newSourceParser()

	repo := t.TempDir()
	path := filepath.Join(repo, "bad.js")
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

	var importStmt, requireCall, propIdent, patternIdent *sitter.Node
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		switch node.Type() {
		case "import_statement":
			if importStmt == nil {
				importStmt = node
			}
		case "call_expression":
			if requireCall == nil {
				requireCall = node
			}
		case "property_identifier":
			if propIdent == nil {
				propIdent = node
			}
		case "identifier":
			if patternIdent == nil && node.Parent() != nil && node.Parent().Type() == "object_pattern" {
				patternIdent = node
			}
		}
	})

	if importStmt != nil {
		_ = parseImportStatement(importStmt, content, "bad.js")
	}
	if requireCall != nil {
		_ = parseRequireCall(requireCall, content, "bad.js")
		_ = parseRequireBinding(requireCall, content, "mod", "bad.js")
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
