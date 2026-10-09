package js

import (
	"context"
	"github.com/ben-ranford/lopper/internal/testutil"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanRepoExcludesIndividualMalformedFile(t *testing.T) {
	repo := t.TempDir()
	excluded := filepath.Join(repo, "broken.js")
	testutil.MustWriteFile(t, excluded, "const broken = {\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "kept.js"), "export const value = 1;\n")
	result, err := ScanRepoWithExcludedPaths(context.Background(), repo, map[string]struct{}{excluded: {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "kept.js" || result.UsageIncomplete || len(result.Warnings) != 0 {
		t.Fatalf("excluded malformed file affected neighboring scan: %#v", result)
	}
}

func TestListDependenciesPreservesWorkspaceManifestWarning(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "package.json"), "{")
	deps, roots, warnings := listDependencies(repo, ScanResult{})
	if len(deps) != 0 || len(roots) != 0 {
		t.Fatalf("malformed manifest produced dependencies: %v %v", deps, roots)
	}
	if len(warnings) != 1 || !strings.HasPrefix(warnings[0], "failed to parse workspace manifest package.json:") {
		t.Fatalf("workspace parse warning lost or duplicated: %v", warnings)
	}
}
