package js

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	sitter "github.com/smacker/go-tree-sitter"
)

const (
	testJsFile  = "test.js"
	indexJSFile = "index.js"
)

func TestNamespaceUsageComputedProperty(t *testing.T) {
	repo := t.TempDir()
	source := "import * as util from \"lodash\"\nutil['map']([1], (x) => x)\n"
	path := filepath.Join(repo, indexJSFile)
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(result.Files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(result.Files))
	}

	usage := result.Files[0].NamespaceUsage
	props, ok := usage["util"]
	if !ok {
		t.Fatalf("expected namespace usage for util")
	}
	if props["map"] == 0 {
		t.Fatalf("expected computed property map usage")
	}
}

func TestNamespaceUsageMemberExpression(t *testing.T) {
	repo := t.TempDir()
	source := "import * as util from \"lodash\"\nutil.map([1], (x) => x)\nutil['filter']([1], Boolean)\n"
	path := filepath.Join(repo, indexJSFile)
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	usage := result.Files[0].NamespaceUsage["util"]
	if usage["map"] == 0 {
		t.Fatalf("expected member expression map usage")
	}
	if usage["filter"] == 0 {
		t.Fatalf("expected subscript expression filter usage")
	}
}

func TestHasDirectIdentifierUsage(t *testing.T) {
	tests := []struct {
		name     string
		imp      ImportBinding
		file     FileScan
		expected bool
	}{
		{
			name: "namespace with only property access",
			imp: ImportBinding{
				Module:     "lodash",
				ExportName: "*",
				LocalName:  "lodash",
				Kind:       ImportNamespace,
			},
			file: FileScan{
				IdentifierUsage: map[string]int{},
				NamespaceUsage: map[string]map[string]int{
					"lodash": {"get": 2, "map": 1},
				},
			},
			expected: false, // Not ambiguous - only property access
		},
		{
			name: "namespace with direct usage",
			imp: ImportBinding{
				Module:     "lodash",
				ExportName: "*",
				LocalName:  "lodash",
				Kind:       ImportNamespace,
			},
			file: FileScan{
				IdentifierUsage: map[string]int{
					"lodash": 1, // Direct usage of namespace
				},
				NamespaceUsage: map[string]map[string]int{
					"lodash": {"get": 2},
				},
			},
			expected: true, // Ambiguous - direct identifier usage
		},
		{
			name: "namespace with only direct usage",
			imp: ImportBinding{
				Module:     "myLib",
				ExportName: "*",
				LocalName:  "lib",
				Kind:       ImportNamespace,
			},
			file: FileScan{
				IdentifierUsage: map[string]int{
					"lib": 3,
				},
				NamespaceUsage: map[string]map[string]int{},
			},
			expected: true, // Ambiguous - only direct usage, no property access
		},
		{
			name: "default import with direct usage",
			imp: ImportBinding{
				Module:     "react",
				ExportName: "default",
				LocalName:  "React",
				Kind:       ImportDefault,
			},
			file: FileScan{
				IdentifierUsage: map[string]int{
					"React": 5,
				},
				NamespaceUsage: map[string]map[string]int{},
			},
			expected: true, // Direct usage
		},
		{
			name: "default import with property access",
			imp: ImportBinding{
				Module:     "express",
				ExportName: "default",
				LocalName:  "express",
				Kind:       ImportDefault,
			},
			file: FileScan{
				IdentifierUsage: map[string]int{},
				NamespaceUsage: map[string]map[string]int{
					"express": {"Router": 1},
				},
			},
			expected: false, // Not ambiguous - only property access
		},
		{
			name: "namespace not used at all",
			imp: ImportBinding{
				Module:     "unused",
				ExportName: "*",
				LocalName:  "unused",
				Kind:       ImportNamespace,
			},
			file: FileScan{
				IdentifierUsage: map[string]int{},
				NamespaceUsage:  map[string]map[string]int{},
			},
			expected: false, // No usage at all
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := hasDirectIdentifierUsage(tt.imp, tt.file)
			if result != tt.expected {
				t.Errorf("hasDirectIdentifierUsage() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestCollectDependencyImportUsageWildcardWarning(t *testing.T) {
	tests := []struct {
		name                string
		scanResult          ScanResult
		dependency          string
		expectAmbiguousFlag bool
		description         string
	}{
		{
			name:       "namespace import with only property access - no warning",
			dependency: "lodash",
			scanResult: ScanResult{
				Files: []FileScan{
					{
						Path: testJsFile,
						Imports: []ImportBinding{
							{
								Module:     "lodash",
								ExportName: "*",
								LocalName:  "lodash",
								Kind:       ImportNamespace,
								Location:   report.Location{File: testJsFile, Line: 1},
							},
						},
						IdentifierUsage: map[string]int{},
						NamespaceUsage: map[string]map[string]int{
							"lodash": {"get": 2, "map": 1},
						},
					},
				},
			},
			expectAmbiguousFlag: false,
			description:         "namespace with only property access should not trigger warning",
		},
		{
			name:       "namespace import with direct usage - warning",
			dependency: "lodash",
			scanResult: ScanResult{
				Files: []FileScan{
					{
						Path: testJsFile,
						Imports: []ImportBinding{
							{
								Module:     "lodash",
								ExportName: "*",
								LocalName:  "lodash",
								Kind:       ImportNamespace,
								Location:   report.Location{File: testJsFile, Line: 1},
							},
						},
						IdentifierUsage: map[string]int{
							"lodash": 1, // Direct usage
						},
						NamespaceUsage: map[string]map[string]int{
							"lodash": {"get": 2},
						},
					},
				},
			},
			expectAmbiguousFlag: true,
			description:         "namespace with direct usage should trigger warning",
		},
		{
			name:       "default import with direct usage - warning",
			dependency: "react",
			scanResult: ScanResult{
				Files: []FileScan{
					{
						Path: "component.jsx",
						Imports: []ImportBinding{
							{
								Module:     "react",
								ExportName: "default",
								LocalName:  "React",
								Kind:       ImportDefault,
								Location:   report.Location{File: "component.jsx", Line: 1},
							},
						},
						IdentifierUsage: map[string]int{
							"React": 3,
						},
						NamespaceUsage: map[string]map[string]int{},
					},
				},
			},
			expectAmbiguousFlag: true,
			description:         "default import with direct usage should trigger warning",
		},
		{
			name:       "named imports only - no warning",
			dependency: "lodash",
			scanResult: ScanResult{
				Files: []FileScan{
					{
						Path: testJsFile,
						Imports: []ImportBinding{
							{
								Module:     "lodash",
								ExportName: "get",
								LocalName:  "get",
								Kind:       ImportNamed,
								Location:   report.Location{File: testJsFile, Line: 1},
							},
						},
						IdentifierUsage: map[string]int{
							"get": 2,
						},
						NamespaceUsage: map[string]map[string]int{},
					},
				},
			},
			expectAmbiguousFlag: false,
			description:         "named imports should never trigger warning",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			usage := collectDependencyImportUsage(
				tt.scanResult,
				tt.dependency,
			)

			if usage.HasAmbiguousWildcard != tt.expectAmbiguousFlag {
				t.Errorf("%s: hasAmbiguous = %v, want %v", tt.description, usage.HasAmbiguousWildcard, tt.expectAmbiguousFlag)
			}
			if len(usage.Warnings) != 0 {
				t.Errorf("expected no attribution warnings, got %#v", usage.Warnings)
			}
		})
	}
}

func TestNamespaceReferenceExtractionBranches(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`
const obj = {};
obj["named"];
obj[prop];
obj.method;
other().call;
`)
	tree, err := parser.Parse(context.Background(), indexJSFile, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	refs := collectNamespaceReferences(tree, source)
	if len(refs) == 0 {
		t.Fatalf("expected namespace references")
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Local == "obj" {
			names = append(names, ref.Property)
		}
	}
	if !slices.Contains(names, "named") || !slices.Contains(names, "prop") || !slices.Contains(names, "method") {
		t.Fatalf("expected property extraction branches, got %#v", names)
	}
}

func TestExtractPropertyStringBackticksAndRaw(t *testing.T) {
	parser := newSourceParser()
	source := []byte("const a = `value`; const b = 'quoted';")
	tree, err := parser.Parse(context.Background(), indexJSFile, source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	root := tree.RootNode()
	found := []string{}
	walkNode(root, func(node *sitter.Node) {
		if node.Type() == "template_string" || node.Type() == "string" {
			found = append(found, extractPropertyString(node, source))
		}
	})
	if !slices.Contains(found, "value") || !slices.Contains(found, "quoted") {
		t.Fatalf("expected string extraction from template and quoted strings, got %#v", found)
	}
}
