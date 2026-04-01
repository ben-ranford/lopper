package js

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestJSLicenseAndStringHelperAdditionalBranches(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "missing-license"), filepath.Join(root, "LICENSE")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(root, "COPYING"), "Mozilla Public License")

	license := detectLicenseFromFiles(root)
	if license == nil || license.SPDX != "MPL-2.0" || license.Source != "license-file" {
		t.Fatalf("expected license fallback to continue past unreadable candidate, got %#v", license)
	}

	if got := normalizeSPDXExpression("mit with classpath-exception-2.0 +"); got != "MIT WITH CLASSPATH-EXCEPTION-2.0 +" {
		t.Fatalf("unexpected SPDX normalization: %q", got)
	}
	if got := stringsJoin(nil, " -> "); got != "" {
		t.Fatalf("expected empty join for nil slice, got %q", got)
	}
	if got := stringsJoin([]string{"a", "b", "c"}, " -> "); got != "a -> b -> c" {
		t.Fatalf("unexpected joined path: %q", got)
	}
}

func TestJSBindingExtractionAdditionalBranches(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`
export const [...rest] = list;
export const { nested: { value: renamed }, plain } = obj;
import { foo as bar, baz } from "pkg";
`)

	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	names := collectExportNames(tree, source)
	for _, want := range []string{"renamed", "plain"} {
		if !slices.Contains(names, want) {
			t.Fatalf("expected export binding name %q in %#v", want, names)
		}
	}

	sawRestPattern := false
	walkNode(tree.RootNode(), func(node *sitter.Node) {
		if node.Type() != "rest_pattern" {
			return
		}
		sawRestPattern = true
		_ = extractBindingNames(node, source)
	})
	if !sawRestPattern {
		t.Fatalf("expected rest_pattern in parsed source")
	}

	bindings, uncertain := collectImportBindings(tree, source, indexJSName)
	if len(uncertain) != 0 {
		t.Fatalf("expected no uncertain imports, got %#v", uncertain)
	}
	if len(bindings) != 2 {
		t.Fatalf("expected two named import bindings, got %#v", bindings)
	}
	if bindings[0].ExportName != "foo" || bindings[0].LocalName != "bar" {
		t.Fatalf("unexpected aliased named import binding: %#v", bindings[0])
	}
	if bindings[1].ExportName != "baz" || bindings[1].LocalName != "baz" {
		t.Fatalf("unexpected unaliased named import binding: %#v", bindings[1])
	}
}

func TestJSReExportAndDependencySafetyBranches(t *testing.T) {
	if !isSafeDependencyName("@scope/pkg") || !isSafeDependencyName("pkg-name") {
		t.Fatalf("expected valid dependency names to pass safety checks")
	}
	for _, dependency := range []string{"", "@scope", ".", "..", "pkg/name", `pkg\\name`} {
		if isSafeDependencyName(dependency) {
			t.Fatalf("expected unsafe dependency name %q to be rejected", dependency)
		}
	}

	parser := newSourceParser()
	source := []byte(`
import { local } from "pkg";
export { local as alias };
export { named as renamed } from "other";
`)
	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	imports, _ := collectImportBindings(tree, source, indexJSName)
	bindings := collectReExportBindings(tree, source, indexJSName, imports)
	if len(bindings) != 2 {
		t.Fatalf("expected two re-export bindings, got %#v", bindings)
	}
	if bindings[0].SourceModule != "pkg" || bindings[0].SourceExportName != "local" || bindings[0].ExportName != "alias" {
		t.Fatalf("unexpected local import re-export binding: %#v", bindings[0])
	}
	if bindings[1].SourceModule != "other" || bindings[1].SourceExportName != "named" || bindings[1].ExportName != "renamed" {
		t.Fatalf("unexpected source-module re-export binding: %#v", bindings[1])
	}
}

func TestJSCollectIdentifierUsageBranches(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`
import { imported } from "pkg";
const declared = 1;
function fn(param, { nested }, ...rest) {
  const local = declared + param;
  return local + obj.member + obj[prop];
}
`)
	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	counts := collectIdentifierUsage(tree, source)
	for name, want := range map[string]int{
		"declared": 1,
		"param":    1,
		"local":    1,
		"prop":     1,
		"rest":     1,
	} {
		if counts[name] != want {
			t.Fatalf("identifier usage count for %q = %d, want %d; counts=%#v", name, counts[name], want, counts)
		}
	}
	if _, ok := counts["imported"]; ok {
		t.Fatalf("expected import-only identifier to be ignored, got %#v", counts)
	}
	if _, ok := counts["obj"]; ok {
		t.Fatalf("expected member-expression object identifier to be tracked separately, got %#v", counts)
	}
}

func TestJSRequireBindingAndDynamicImportBranches(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`
const { foo: alias, bar } = require("pkg");
const ns = require("other");
require(dynamicSource);
`)
	tree, err := parser.Parse(context.Background(), indexJSName, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	bindings, uncertain := collectImportBindings(tree, source, indexJSName)
	if len(bindings) != 3 {
		t.Fatalf("expected object-pattern and namespace require bindings, got %#v", bindings)
	}
	if bindings[0].ExportName != "foo" || bindings[0].LocalName != "alias" {
		t.Fatalf("unexpected aliased require binding: %#v", bindings[0])
	}
	if bindings[1].ExportName != "bar" || bindings[1].LocalName != "bar" {
		t.Fatalf("unexpected shorthand require binding: %#v", bindings[1])
	}
	if bindings[2].ExportName != "*" || bindings[2].LocalName != "ns" {
		t.Fatalf("unexpected namespace require binding: %#v", bindings[2])
	}
	if len(uncertain) != 1 || uncertain[0].Module != "<dynamic>" {
		t.Fatalf("expected one uncertain dynamic require, got %#v", uncertain)
	}
}

func TestJSLicenseAndProvenanceAdditionalBranches(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(root, "missing-copying"), filepath.Join(root, "COPYING")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(root, "LICENSE"), "Apache License Version 2.0")

	license := detectLicenseFromFiles(root)
	if license == nil || license.SPDX != "APACHE-2.0" {
		t.Fatalf("expected license detection to continue past unreadable candidate, got %#v", license)
	}

	if got := normalizeSPDXExpression(" \t \n "); got != "" {
		t.Fatalf("expected blank SPDX expression to normalize to empty, got %q", got)
	}

	pkg := packageJSON{
		Name:      "pkg",
		Version:   "1.0.0",
		Resolved:  "https://registry.npmjs.org/pkg/-/pkg-1.0.0.tgz",
		Integrity: "sha512-abc",
		Repository: map[string]any{
			"url": "https://example.test/repo",
		},
	}
	provenance := buildProvenance(pkg, true)
	if provenance == nil || provenance.Source != "local+registry-heuristics" || !slices.Contains(provenance.Signals, "repository") {
		t.Fatalf("expected repository-backed registry provenance, got %#v", provenance)
	}
}
