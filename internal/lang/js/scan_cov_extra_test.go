package js

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestJSScanCoverageHelpers(t *testing.T) {
	parser := newSourceParser()
	if _, err := parser.languageForPath("x.tsx"); err != nil {
		t.Fatalf("expected tsx language support, got %v", err)
	}

	source := []byte("export const foo = 1; const x = obj.prop; require(); const [a] = require(\"mod\"); import {} from \"m\"; const t = ``;")
	tree, err := parser.Parse(context.Background(), "index.js", source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	var exportStmt, memberProp, requireCall, importStmt, tmpl *sitter.Node
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		switch node.Type() {
		case "export_statement":
			if exportStmt == nil {
				exportStmt = node
			}
		case "property_identifier":
			if memberProp == nil {
				memberProp = node
			}
		case "call_expression":
			if requireCall == nil {
				requireCall = node
			}
		case "import_statement":
			if importStmt == nil {
				importStmt = node
			}
		case "template_string":
			if tmpl == nil {
				tmpl = node
			}
		}
	})

	if exportStmt == nil || parseReExportStatement(exportStmt, source, "index.js", map[string][]ImportBinding{}) != nil {
		t.Fatalf("expected source-less re-export statement to return nil bindings")
	}

	if !isIdentifierUsage(memberProp) {
		t.Fatalf("expected member property identifier to count as usage")
	}
	if isIdentifierUsage(tree.RootNode()) {
		t.Fatalf("expected root node without parent not to count as usage")
	}

	if len(parseRequireCall(requireCall, source, "index.js")) != 0 {
		t.Fatalf("expected require() with no args to produce no bindings")
	}
	if len(parseImportStatement(importStmt, source, "index.js")) != 1 {
		t.Fatalf("expected empty import clause to fall back to wildcard binding")
	}
	if got, ok := extractStringLiteral(tmpl, source); ok || got != "" {
		t.Fatalf("expected empty template string to normalize to empty literal")
	}
}
