package dart

import (
	"context"
	"path/filepath"
	"testing"
)

func TestScanManifestRootBranches(t *testing.T) {
	repo := t.TempDir()
	rootSet := map[string]struct{}{repo: {}}
	fileCount := new(int)

	stop, err := scanManifestRoot(context.Background(), repo, packageManifest{Root: repo}, rootSet, map[string]struct{}{}, fileCount, &scanResult{SkippedFilesByBound: true})
	if err != nil {
		t.Fatalf("scanManifestRoot skipped result: %v", err)
	}
	if !stop {
		t.Fatalf("expected skipped result to stop scanning")
	}

	stop, err = scanManifestRoot(context.Background(), repo, packageManifest{Root: filepath.Join(repo, "missing")}, map[string]struct{}{}, map[string]struct{}{}, new(int), &scanResult{})
	if err == nil {
		t.Fatalf("expected scanManifestRoot to return an error for a missing root")
	}
	if stop {
		t.Fatalf("expected missing-root scanManifestRoot call to report stop=false")
	}
}

func TestScanRepoReturnsManifestRootErrors(t *testing.T) {
	repo := t.TempDir()

	_, err := scanRepo(context.Background(), repo, []packageManifest{{
		Root:         filepath.Join(repo, "missing"),
		Dependencies: map[string]dependencyInfo{},
	}})
	if err == nil {
		t.Fatalf("expected scanRepo to return the manifest root error")
	}
}

func TestParseImportDirectiveRejectsUnsupportedDirective(t *testing.T) {
	if kind, module, clause, ok := parseImportDirective(`library 'foo';`); ok || kind != "" || module != "" || clause != "" {
		t.Fatalf("expected unsupported directive to be rejected, got kind=%q module=%q clause=%q ok=%v", kind, module, clause, ok)
	}
}
