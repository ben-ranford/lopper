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
