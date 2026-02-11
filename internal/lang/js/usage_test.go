package js

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

const testJsFile = "test.js"

func TestNamespaceUsageComputedProperty(t *testing.T) {
	repo := t.TempDir()
	source := "import * as util from \"lodash\"\nutil['map']([1], (x) => x)\n"
	path := filepath.Join(repo, "index.js")
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
			usedExports := make(map[string]struct{})
			counts := make(map[string]int)
			usedImports := make(map[string]*report.ImportUse)
			unusedImports := make(map[string]*report.ImportUse)

			hasAmbiguous := collectDependencyImportUsage(
				tt.scanResult,
				tt.dependency,
				usedExports,
				counts,
				usedImports,
				unusedImports,
			)

			if hasAmbiguous != tt.expectAmbiguousFlag {
				t.Errorf("%s: hasAmbiguous = %v, want %v", tt.description, hasAmbiguous, tt.expectAmbiguousFlag)
			}
		})
	}
}
