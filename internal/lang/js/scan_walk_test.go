package js

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
)

func TestScanRepoStopsAtEntryBudgetWithPartialWarningAndNoReadsBeyondBound(t *testing.T) {
	repo := t.TempDir()
	for _, file := range []string{"a.js", "b.js", "c.js"} {
		source := "export const name = " + `"` + file + `"` + "\n"
		if err := os.WriteFile(filepath.Join(repo, file), []byte(source), 0o600); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}

	originalReadRepoSourceUnderLimit := readRepoSourceUnderLimit
	var reads []string
	readRepoSourceUnderLimit = func(rootPath, path string, limit int64) ([]byte, error) {
		reads = append(reads, filepath.Base(path))
		return originalReadRepoSourceUnderLimit(rootPath, path, limit)
	}
	t.Cleanup(func() {
		readRepoSourceUnderLimit = originalReadRepoSourceUnderLimit
	})

	result, err := scanRepoWithEntryLimit(context.Background(), repo, 2)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}

	if len(reads) != 2 {
		t.Fatalf("expected exactly two bounded reads before truncation, got %#v", reads)
	}
	wantPaths := slices.Clone(reads)
	sort.Strings(wantPaths)
	if got := scannedPaths(result); !slices.Equal(got, wantPaths) {
		t.Fatalf("expected partial scan output to preserve and sort read files, got %#v want %#v", got, wantPaths)
	}
	if !warningsContain(result.Warnings, jsRepoScanBudgetWarning(scanRepoWalkSummary{entriesVisited: 2, truncated: true})) {
		t.Fatalf("expected deterministic budget warning, got %#v", result.Warnings)
	}
	if warningsContain(result.Warnings, "no JS/TS files found") {
		t.Fatalf("did not expect no-files warning for partial scan with files, got %#v", result.Warnings)
	}
}

func TestScanRepoBudgetTruncationWarnsWithoutSilentlySucceeding(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "a", "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a", "nested", "ignore.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a", "nested", "z.js"), []byte("export const unseen = true\n"), 0o600); err != nil {
		t.Fatalf("write z.js: %v", err)
	}

	originalReadRepoSourceUnderLimit := readRepoSourceUnderLimit
	readCount := 0
	readRepoSourceUnderLimit = func(rootPath, path string, limit int64) ([]byte, error) {
		readCount++
		return originalReadRepoSourceUnderLimit(rootPath, path, limit)
	}
	t.Cleanup(func() {
		readRepoSourceUnderLimit = originalReadRepoSourceUnderLimit
	})

	result, err := scanRepoWithEntryLimit(context.Background(), repo, 1)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}

	if readCount != 0 {
		t.Fatalf("expected truncation before any JS reads, got %d reads", readCount)
	}
	if len(result.Files) != 0 {
		t.Fatalf("expected no scanned files after pre-read truncation, got %#v", result.Files)
	}
	if !warningsContain(result.Warnings, jsRepoScanBudgetWarning(scanRepoWalkSummary{entriesVisited: 1, truncated: true})) {
		t.Fatalf("expected deterministic budget warning, got %#v", result.Warnings)
	}
	if !warningsContain(result.Warnings, "no JS/TS files found for analysis") {
		t.Fatalf("expected no-files warning for truncated empty result, got %#v", result.Warnings)
	}
}

func TestScanRepoStreamingWalkPreservesSortedNormalOutput(t *testing.T) {
	repo := t.TempDir()
	fileCount := jsWalkReadDirBatchSize + 3
	wantPaths := make([]string, 0, fileCount)
	for index := 0; index < fileCount; index++ {
		name := fmt.Sprintf("file-%03d.js", fileCount-index)
		wantPaths = append(wantPaths, name)
		if err := os.WriteFile(filepath.Join(repo, name), []byte("export const value = true\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	sort.Strings(wantPaths)

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if got := scannedPaths(result); !slices.Equal(got, wantPaths) {
		t.Fatalf("expected complete sorted scan output, got %#v want %#v", got, wantPaths)
	}
	if warningsContain(result.Warnings, "analysis is partial") {
		t.Fatalf("did not expect a budget warning for a normal scan, got %#v", result.Warnings)
	}
}

func scannedPaths(result ScanResult) []string {
	paths := make([]string, 0, len(result.Files))
	for _, file := range result.Files {
		paths = append(paths, file.Path)
	}
	return paths
}
