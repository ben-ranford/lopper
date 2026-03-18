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
	importStmts, exportStmts, callExprs := collectMalformedJSNodes(tree.RootNode())
	if len(importStmts) == 0 || len(callExprs) == 0 {
		t.Fatalf("expected malformed source to still produce import/call nodes")
	}

	assertMalformedJSStatements(source, importStmts, exportStmts, callExprs)
	assertMalformedJSGuardBranches(t, tree.RootNode(), source)
	assertMalformedJSStaticLiteralGuards(t, tree.RootNode(), source)
}

func collectMalformedJSNodes(root *sitter.Node) ([]*sitter.Node, []*sitter.Node, []*sitter.Node) {
	var importStmts []*sitter.Node
	var exportStmts []*sitter.Node
	var callExprs []*sitter.Node
	walkNode(root, func(node *sitter.Node) {
		switch node.Type() {
		case "import_statement":
			importStmts = append(importStmts, node)
		case "export_statement":
			exportStmts = append(exportStmts, node)
		case "call_expression":
			callExprs = append(callExprs, node)
		}
	})
	return importStmts, exportStmts, callExprs
}

func assertMalformedJSStatements(source []byte, importStmts, exportStmts, callExprs []*sitter.Node) {
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
}

func assertMalformedJSGuardBranches(t *testing.T, root *sitter.Node, source []byte) {
	t.Helper()

	if _, ok := parseUncertainDynamicImport(root, source, unitIndexJS); ok {
		t.Fatalf("expected non-call nodes to skip uncertain dynamic import parsing")
	}
	if bindings := parseImportStatement(root, source, unitIndexJS); len(bindings) != 0 {
		t.Fatalf("expected non-import node to produce no bindings, got %#v", bindings)
	}
	if bindings := parseRequireCall(root, source, unitIndexJS); len(bindings) != 0 {
		t.Fatalf("expected non-call node to produce no require bindings, got %#v", bindings)
	}
	if bindings := parseRequireBinding(root, source, "leftpad", unitIndexJS); len(bindings) != 0 {
		t.Fatalf("expected non-declarator ancestry to produce no bound imports, got %#v", bindings)
	}
}

func assertMalformedJSStaticLiteralGuards(t *testing.T, root *sitter.Node, source []byte) {
	t.Helper()

	if value, ok := extractStaticModuleLiteral(nil, source); ok || value != "" {
		t.Fatalf("expected nil static module literal to fail, got value=%q ok=%v", value, ok)
	}
	identifier := firstNodeByType(root, "identifier")
	if identifier == nil {
		t.Fatalf("expected identifier node in malformed source")
	}
	if value, ok := extractStaticModuleLiteral(identifier, source); ok || value != "" {
		t.Fatalf("expected bare identifier not to count as static module literal, got value=%q ok=%v", value, ok)
	}
}
