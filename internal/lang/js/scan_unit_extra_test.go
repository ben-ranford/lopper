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

func collectImportAndCallNodes(tree *sitter.Tree) ([]*sitter.Node, []*sitter.Node) {
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
	return importStmts, callExprs
}

func assertBareImportSideEffect(t *testing.T, importStmts []*sitter.Node, source []byte) {
	t.Helper()
	firstImport := parseImportStatement(importStmts[0], source, unitIndexJS)
	if len(firstImport) != 1 || firstImport[0].Kind != ImportSideEffect {
		t.Fatalf("expected side-effect fallback import for bare import, got %#v", firstImport)
	}
	if firstImport[0].ExportName != sideEffectImportName || firstImport[0].LocalName != "" {
		t.Fatalf("expected side-effect import marker and empty local name, got %#v", firstImport[0])
	}
}

func assertNamedImportParsing(t *testing.T, importStmts []*sitter.Node, source []byte) {
	t.Helper()
	secondImport := parseImportStatement(importStmts[1], source, unitIndexJS)
	if len(secondImport) == 0 || secondImport[0].Kind != ImportNamed {
		t.Fatalf("expected named import parsing, got %#v", secondImport)
	}
}

func scanRequireBindings(callExprs []*sitter.Node, source []byte) (bool, bool) {
	var sawRequire bool
	var sawBareRequireSideEffect bool
	for _, call := range callExprs {
		bindings := parseRequireCall(call, source, unitIndexJS)
		if len(bindings) == 0 {
			continue
		}
		sawRequire = true
		if len(bindings) == 1 && bindings[0].Module == "leftpad" {
			sawBareRequireSideEffect = bindings[0].Kind == ImportSideEffect &&
				bindings[0].ExportName == sideEffectImportName &&
				bindings[0].LocalName == ""
		}
	}
	return sawRequire, sawBareRequireSideEffect
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

	importStmts, callExprs := collectImportAndCallNodes(tree)
	if len(importStmts) < 2 || len(callExprs) == 0 {
		t.Fatalf("expected import and call expressions")
	}

	assertBareImportSideEffect(t, importStmts, source)
	assertNamedImportParsing(t, importStmts, source)
	sawRequire, sawBareRequireSideEffect := scanRequireBindings(callExprs, source)
	if !sawRequire {
		t.Fatalf("expected parsed require bindings")
	}
	if !sawBareRequireSideEffect {
		t.Fatalf("expected bare require to be treated as a side-effect import")
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

func TestParseNamedImportsHandlesDefaultAndPlainSpecifiers(t *testing.T) {
	source := []byte(`import { default as pkgDefault, bar } from "pkg";`)
	node := parseJSNodeByType(t, source, "named_imports")
	bindings := parseNamedImports(node, source, "pkg", unitIndexJS)
	if len(bindings) != 2 {
		t.Fatalf("expected named imports to be parsed, got %#v", bindings)
	}
	if bindings[0].ExportName != "default" || bindings[0].LocalName != "pkgDefault" {
		t.Fatalf("expected default import specifier to use the property identifier, got %#v", bindings[0])
	}
	if bindings[1].ExportName != "bar" || bindings[1].LocalName != "bar" {
		t.Fatalf("expected plain named import to parse, got %#v", bindings[1])
	}

	recoverySource := []byte(`import {, baz } from "pkg";`)
	recoveryNode := parseJSNodeByType(t, recoverySource, "named_imports")
	recoveryBindings := parseNamedImports(recoveryNode, recoverySource, "pkg", unitIndexJS)
	if len(recoveryBindings) != 1 || recoveryBindings[0].ExportName != "baz" || recoveryBindings[0].LocalName != "baz" {
		t.Fatalf("expected parser recovery to keep valid trailing import specifier, got %#v", recoveryBindings)
	}
}

func TestParseNamedImportsRecoversMissingAliasName(t *testing.T) {
	source := []byte(`import { foo as } from "pkg";`)
	node := parseJSNodeByType(t, source, "named_imports")

	bindings := parseNamedImports(node, source, "pkg", unitIndexJS)
	if len(bindings) != 1 {
		t.Fatalf("expected a recovered named import binding, got %#v", bindings)
	}
	if bindings[0].ExportName != "foo" || bindings[0].LocalName != "foo" {
		t.Fatalf("expected missing alias to fall back to export name, got %#v", bindings[0])
	}
}

func TestParseRequireBindingRecoversMissingObjectPatternAlias(t *testing.T) {
	source := []byte(`const { foo: } = require("pkg");`)
	call := parseJSNodeByType(t, source, "call_expression")

	bindings := parseRequireBinding(call, source, "pkg", unitIndexJS)
	if len(bindings) != 1 {
		t.Fatalf("expected recovered require binding, got %#v", bindings)
	}
	if bindings[0].ExportName != "foo" || bindings[0].LocalName != "foo" {
		t.Fatalf("expected missing object-pattern alias to fall back to export name, got %#v", bindings[0])
	}
}

func TestCollectReExportBindings(t *testing.T) {
	sourceLines := []string{`import { map as remap } from "lodash"`, `export { remap as mapAlias }`, `export { filter as keep } from "lodash"`, `export * as api from "./ns"`, `export * from "./other"`, ""}
	source := []byte(strings.Join(sourceLines, "\n"))

	reExports := collectReExportBindingsForSource(t, source)
	if len(reExports) < 3 {
		t.Fatalf("expected re-export bindings, got %#v", reExports)
	}

	assertReExportBindingPresent(t, reExports, "mapAlias", "lodash", "map")
	assertReExportBindingPresent(t, reExports, "keep", "lodash", "filter")
	assertReExportBindingPresent(t, reExports, "api", "./ns", "*")
	assertReExportBindingPresent(t, reExports, "*", "./other", "*")
}

func TestCollectReExportBindingsRecoversMissingAliasName(t *testing.T) {
	source := []byte(`export { foo as } from "pkg";`)
	reExports := collectReExportBindingsForSource(t, source)
	if len(reExports) != 1 {
		t.Fatalf("expected recovered re-export binding, got %#v", reExports)
	}
	if reExports[0].SourceModule != "pkg" || reExports[0].SourceExportName != "foo" || reExports[0].ExportName != "foo" {
		t.Fatalf("expected missing re-export alias to fall back to source name, got %#v", reExports[0])
	}
}

func collectReExportBindingsForSource(t *testing.T, source []byte) []ReExportBinding {
	t.Helper()

	tree, err := newSourceParser().Parse(context.Background(), unitIndexJS, source)
	if err != nil {
		t.Fatalf(parseSourceErrFmt, err)
	}

	imports, _ := collectImportBindings(tree, source, unitIndexJS)
	return collectReExportBindings(tree, source, unitIndexJS, imports)
}

func assertReExportBindingPresent(t *testing.T, bindings []ReExportBinding, exportName, sourceModule, sourceExportName string) {
	t.Helper()

	for _, item := range bindings {
		if item.ExportName == exportName && item.SourceModule == sourceModule && item.SourceExportName == sourceExportName {
			return
		}
	}

	t.Fatalf("expected re-export %q from %q (%q) in %#v", exportName, sourceModule, sourceExportName, bindings)
}
