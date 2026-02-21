package js

import (
	"context"
	"testing"
)

func TestJSScanCoverageHelpers(t *testing.T) {
	parser := newSourceParser()
	if _, err := parser.languageForPath("x.tsx"); err != nil {
		t.Fatalf("expected tsx language support, got %v", err)
	}

	source := []byte("export const foo = 1; const x = obj.prop; require(); const [a] = require(\"mod\"); import {} from \"m\"; const t = ``;")
	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	exportStmt := firstNodeByType(tree.RootNode(), "export_statement")
	memberProp := firstNodeByType(tree.RootNode(), "property_identifier")
	requireCall := firstNodeByType(tree.RootNode(), "call_expression")
	importStmt := firstNodeByType(tree.RootNode(), "import_statement")
	tmpl := firstNodeByType(tree.RootNode(), "template_string")

	if exportStmt == nil || parseReExportStatement(exportStmt, source, indexJSName, map[string][]ImportBinding{}) != nil {
		t.Fatalf("expected source-less re-export statement to return nil bindings")
	}

	if !isIdentifierUsage(memberProp) {
		t.Fatalf("expected member property identifier to count as usage")
	}
	if isIdentifierUsage(tree.RootNode()) {
		t.Fatalf("expected root node without parent not to count as usage")
	}

	if len(parseRequireCall(requireCall, source, indexJSName)) != 0 {
		t.Fatalf("expected require() with no args to produce no bindings")
	}
	if len(parseImportStatement(importStmt, source, indexJSName)) != 1 {
		t.Fatalf("expected empty import clause to fall back to wildcard binding")
	}
	if got, ok := extractStringLiteral(tmpl, source); ok || got != "" {
		t.Fatalf("expected empty template string to normalize to empty literal")
	}
}
