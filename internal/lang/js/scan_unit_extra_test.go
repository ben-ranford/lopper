package js

import (
	"context"
	"slices"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

const (
	unitIndexJS       = "index.js"
	parseSourceErrFmt = "parse source: %v"
)

func parseJSNodeByType(t *testing.T, source []byte, nodeType string) *sitter.Node {
	t.Helper()
	tree, err := newSourceParser().Parse(context.Background(), unitIndexJS, source)
	if err != nil {
		t.Fatalf(parseSourceErrFmt, err)
	}
	var found *sitter.Node
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		if found == nil && node.Type() == nodeType {
			found = node
		}
	})
	if found == nil {
		t.Fatalf("expected node type %q", nodeType)
	}
	return found
}

func TestScanImportAndRequireHelperBranches(t *testing.T) {
	source := []byte(`
import "pkg";
import { map as m } from "lodash";
const { map: mm, filter } = require("lodash");
const ns = require("axios");
require("leftpad");
foo("x");
`)
	tree, err := newSourceParser().Parse(context.Background(), unitIndexJS, source)
	if err != nil {
		t.Fatalf(parseSourceErrFmt, err)
	}

	var importStmts []*sitter.Node
	var callExprs []*sitter.Node
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		switch node.Type() {
		case "import_statement":
			importStmts = append(importStmts, node)
		case "call_expression":
			callExprs = append(callExprs, node)
		}
	})
	if len(importStmts) < 2 || len(callExprs) == 0 {
		t.Fatalf("expected import and call expressions")
	}

	firstImport := parseImportStatement(importStmts[0], source, unitIndexJS)
	if len(firstImport) != 1 || firstImport[0].Kind != ImportNamespace {
		t.Fatalf("expected namespace fallback import for bare import, got %#v", firstImport)
	}

	secondImport := parseImportStatement(importStmts[1], source, unitIndexJS)
	if len(secondImport) == 0 || secondImport[0].Kind != ImportNamed {
		t.Fatalf("expected named import parsing, got %#v", secondImport)
	}

	var sawRequire bool
	for _, call := range callExprs {
		bindings := parseRequireCall(call, source, unitIndexJS)
		if len(bindings) == 0 {
			continue
		}
		sawRequire = true
	}
	if !sawRequire {
		t.Fatalf("expected parsed require bindings")
	}
}

func TestScanLiteralAndNodeHelpers(t *testing.T) {
	source := []byte(`const x = "value"; const y = \` + "`v`" + `; const z = unknown;`)
	tree, err := newSourceParser().Parse(context.Background(), unitIndexJS, source)
	if err != nil {
		t.Fatalf(parseSourceErrFmt, err)
	}
	values := make([]string, 0)
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		if node.Type() == "string" || node.Type() == "template_string" || node.Type() == "identifier" {
			if v, ok := extractStringLiteral(node, source); ok {
				values = append(values, v)
			}
		}
	})
	if !slices.Contains(values, "value") {
		t.Fatalf("expected string literal extraction, got %#v", values)
	}

	if text := nodeText(nil, source); text != "" {
		t.Fatalf("expected empty node text for nil node, got %q", text)
	}
	if firstNamedChildOfType(tree.RootNode(), "not-a-real-type") != nil {
		t.Fatalf("expected no named child for unknown type")
	}
}

func TestParseRequireBindingNoDeclarator(t *testing.T) {
	source := []byte(`require("leftpad")`)
	call := parseJSNodeByType(t, source, "call_expression")
	bindings := parseRequireBinding(call, source, "leftpad", unitIndexJS)
	if len(bindings) != 0 {
		t.Fatalf("expected no require bindings without variable declarator, got %#v", bindings)
	}
}

func TestCollectReExportBindings(t *testing.T) {
	sourceLines := []string{`import { map as remap } from "lodash"`, `export { remap as mapAlias }`, `export { filter as keep } from "lodash"`, `export * as api from "./ns"`, `export * from "./other"`, ""}
	source := []byte(strings.Join(sourceLines, "\n"))

	tree, err := newSourceParser().Parse(context.Background(), unitIndexJS, source)
	if err != nil {
		t.Fatalf(parseSourceErrFmt, err)
	}

	imports, _ := collectImportBindings(tree, source, unitIndexJS)
	reExports := collectReExportBindings(tree, source, unitIndexJS, imports)
	if len(reExports) < 3 {
		t.Fatalf("expected re-export bindings, got %#v", reExports)
	}

	found := map[string]bool{
		"mapAlias": false,
		"keep":     false,
		"api":      false,
		"*":        false,
	}
	for _, item := range reExports {
		switch {
		case item.ExportName == "mapAlias" && item.SourceModule == "lodash" && item.SourceExportName == "map":
			found["mapAlias"] = true
		case item.ExportName == "keep" && item.SourceModule == "lodash" && item.SourceExportName == "filter":
			found["keep"] = true
		case item.ExportName == "api" && item.SourceModule == "./ns" && item.SourceExportName == "*":
			found["api"] = true
		case item.ExportName == "*" && item.SourceModule == "./other" && item.SourceExportName == "*":
			found["*"] = true
		}
	}

	for key, ok := range found {
		if !ok {
			t.Fatalf("expected re-export %q in %#v", key, reExports)
		}
	}
}
