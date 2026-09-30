package js

import (
	"context"
	"path/filepath"
	"testing"
)

func TestScanRepoFixtures(t *testing.T) {
	cases := []struct {
		name     string
		repoPath string
		module   string
	}{
		{"esm", filepath.Join("..", "..", "..", "testdata", "js", "esm"), "lodash"},
		{"cjs", filepath.Join("..", "..", "..", "testdata", "js", "cjs"), "lodash"},
		{"ts-alias", filepath.Join("..", "..", "..", "testdata", "js", "ts-alias"), "@/utils"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ScanRepo(context.Background(), tc.repoPath)
			if err != nil {
				t.Fatalf("scan repo: %v", err)
			}

			found := containsModuleImport(result, tc.module)
			if !found {
				t.Fatalf("expected to find module %q", tc.module)
			}
			if tc.module == "lodash" {
				usage := collectDependencyImportUsage(result, tc.module)
				if len(usage.UsedExports) != 1 || usage.Counts["debounce"] != 1 {
					t.Fatalf("expected one used debounce export: %+v", usage)
				}
				if len(usage.UsedImports) != 1 || len(usage.UnusedImports) != 0 {
					t.Fatalf("expected one used lodash import: %+v", usage)
				}
			}
		})
	}
}

func containsModuleImport(result ScanResult, module string) bool {
	for _, file := range result.Files {
		for _, imp := range file.Imports {
			if imp.Module == module {
				return true
			}
		}
	}
	return false
}
