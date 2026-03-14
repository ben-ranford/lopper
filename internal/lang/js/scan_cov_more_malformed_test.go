package js

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestJSMalformedScannerFallbackBranches(t *testing.T) {
	source := []byte(`
import from;
import { foo as , bar, } from "pkg";
export { as alias, bar as , baz } from "pkg";
require();
const = require("leftpad");
const { :alias, qux: } = require("pkg");
`)

	tree, err := newSourceParser().Parse(context.Background(), unitIndexJS, source)
	if err != nil {
		t.Fatalf(parseSourceErrFmt, err)
	}

	var importStmts []*sitter.Node
	var exportStmts []*sitter.Node
	var callExprs []*sitter.Node
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		switch node.Type() {
		case "import_statement":
			importStmts = append(importStmts, node)
		case "export_statement":
			exportStmts = append(exportStmts, node)
		case "call_expression":
			callExprs = append(callExprs, node)
		}
	})
	if len(importStmts) == 0 || len(callExprs) == 0 {
		t.Fatalf("expected malformed source to still produce import/call nodes")
	}

	for _, stmt := range importStmts {
		_ = parseImportStatement(stmt, source, unitIndexJS)
	}
	for _, stmt := range exportStmts {
		_ = parseReExportStatement(stmt, source, unitIndexJS, map[string][]ImportBinding{})
	}
	for _, call := range callExprs {
		_ = parseRequireCall(call, source, unitIndexJS)
		_, _ = parseUncertainDynamicImport(call, source, unitIndexJS)
		_ = parseRequireBinding(call, source, "leftpad", unitIndexJS)
	}
	if _, ok := parseUncertainDynamicImport(tree.RootNode(), source, unitIndexJS); ok {
		t.Fatalf("expected non-call nodes to skip uncertain dynamic import parsing")
	}

	if bindings := parseImportStatement(tree.RootNode(), source, unitIndexJS); len(bindings) != 0 {
		t.Fatalf("expected non-import node to produce no bindings, got %#v", bindings)
	}
	if bindings := parseRequireCall(tree.RootNode(), source, unitIndexJS); len(bindings) != 0 {
		t.Fatalf("expected non-call node to produce no require bindings, got %#v", bindings)
	}
	if bindings := parseRequireBinding(tree.RootNode(), source, "leftpad", unitIndexJS); len(bindings) != 0 {
		t.Fatalf("expected non-declarator ancestry to produce no bound imports, got %#v", bindings)
	}

	if value, ok := extractStaticModuleLiteral(nil, source); ok || value != "" {
		t.Fatalf("expected nil static module literal to fail, got value=%q ok=%v", value, ok)
	}
	identifier := firstNodeByType(tree.RootNode(), "identifier")
	if identifier == nil {
		t.Fatalf("expected identifier node in malformed source")
	}
	if value, ok := extractStaticModuleLiteral(identifier, source); ok || value != "" {
		t.Fatalf("expected bare identifier not to count as static module literal, got value=%q ok=%v", value, ok)
	}
}
