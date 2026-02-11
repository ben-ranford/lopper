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
	if len(result.Files) != 1 {
		t.Fatalf("expected one scanned file, got %d", len(result.Files))
	}
	imports := result.Files[0].Imports
	if len(imports) < 5 {
		t.Fatalf("expected multiple parsed imports, got %#v", imports)
	}
	var foundMapAlias, foundFilter, foundNamespace bool
	for _, imp := range imports {
		if imp.Module == "lodash" && imp.ExportName == "map" && imp.LocalName == "m" {
			foundMapAlias = true
		}
		if imp.Module == "lodash" && imp.ExportName == "filter" {
			foundFilter = true
		}
		if imp.Module == "axios" && imp.Kind == ImportNamespace {
			foundNamespace = true
		}
	}
	if !foundMapAlias || !foundFilter || !foundNamespace {
		t.Fatalf("expected require/import branches to parse bindings, got %#v", imports)
	}

	ids := result.Files[0].IdentifierUsage
	if ids["demo"] != 0 {
		t.Fatalf("expected function declaration identifier not counted as usage")
	}
	if ids["arg"] == 0 {
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
