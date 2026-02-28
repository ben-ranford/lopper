package js

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestScanImportAndRequireBranches(t *testing.T) {
	repo := t.TempDir()
	source := `
import React, { useState as use } from "react";
import * as util from "lodash";
const { map: m, filter } = require("lodash");
const ns = require("axios");
const ignored = require(dynamicVar);
function demo(arg) { const local = 1; return util.map([arg], m); }
`
	if err := os.WriteFile(filepath.Join(repo, "index.js"), []byte(source), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	file := assertSingleScannedFile(t, result)
	assertImportBindingCoverage(t, file.Imports)
	if len(file.UncertainImports) != 1 {
		t.Fatalf("expected one unresolved dynamic import/require, got %#v", file.UncertainImports)
	}
	if got := file.UncertainImports[0].Module; got != "<dynamic>" {
		t.Fatalf("expected dynamic placeholder module, got %q", got)
	}
	assertIdentifierUsageCoverage(t, file.IdentifierUsage)
}

func TestScanTreatsTemplateSubstitutionsAsUncertain(t *testing.T) {
	repo := t.TempDir()
	source := `
const name = "feature";
const dynamicReq = require(` + "`./${name}.js`" + `);
const staticReq = require(` + "`lodash`" + `);
const dynamicImport = import(` + "`./${name}.mjs`" + `);
`
	if err := os.WriteFile(filepath.Join(repo, "index.js"), []byte(source), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	file := assertSingleScannedFile(t, result)

	if len(file.UncertainImports) != 2 {
		t.Fatalf("expected two unresolved dynamic import/require entries, got %#v", file.UncertainImports)
	}
	hasStaticLodash := false
	hasDynamicTemplateLiteral := false
	for _, imp := range file.Imports {
		if imp.Module == "lodash" {
			hasStaticLodash = true
		}
		if imp.Module == "./${name}.js" {
			hasDynamicTemplateLiteral = true
		}
	}
	if !hasStaticLodash {
		t.Fatalf("expected static template literal require to be treated as a resolved import")
	}
	if hasDynamicTemplateLiteral {
		t.Fatalf("expected template substitution require to remain uncertain")
	}
}

func assertSingleScannedFile(t *testing.T, result ScanResult) FileScan {
	t.Helper()
	if len(result.Files) != 1 {
		t.Fatalf("expected one scanned file, got %d", len(result.Files))
	}
	return result.Files[0]
}

func assertImportBindingCoverage(t *testing.T, imports []ImportBinding) {
	t.Helper()
	if len(imports) < 5 {
		t.Fatalf("expected multiple parsed imports, got %#v", imports)
	}
	found := map[string]bool{
		"map-alias": false,
		"filter":    false,
		"namespace": false,
	}
	for _, imp := range imports {
		switch {
		case imp.Module == "lodash" && imp.ExportName == "map" && imp.LocalName == "m":
			found["map-alias"] = true
		case imp.Module == "lodash" && imp.ExportName == "filter":
			found["filter"] = true
		case imp.Module == "axios" && imp.Kind == ImportNamespace:
			found["namespace"] = true
		}
	}
	for key, ok := range found {
		if !ok {
			t.Fatalf("missing expected parsed binding %q in %#v", key, imports)
		}
	}
}

func assertIdentifierUsageCoverage(t *testing.T, usage map[string]int) {
	t.Helper()
	if usage["demo"] != 0 {
		t.Fatalf("expected function declaration identifier not counted as usage")
	}
	if usage["arg"] == 0 {
		t.Fatalf("expected parameter usage to be counted in function body")
	}
}

func TestExtractStringLiteralBranches(t *testing.T) {
	parser := newSourceParser()
	source := []byte("const a = 'x'; const b = `y`; const c = z;")
	tree, err := parser.Parse(context.Background(), "index.js", source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	root := tree.RootNode()
	var nodes []*sitter.Node
	walkNode(root, func(node *sitter.Node) {
		if node.Type() == "string" || node.Type() == "template_string" || node.Type() == "identifier" {
			nodes = append(nodes, node)
		}
	})
	if len(nodes) == 0 {
		t.Fatalf("expected literal nodes in parsed source")
	}

	values := make([]string, 0)
	for _, node := range nodes {
		if value, ok := extractStringLiteral(node, source); ok {
			values = append(values, value)
		}
	}
	if !slices.Contains(values, "x") {
		t.Fatalf("expected single-quoted literal extraction in %#v", values)
	}
}
