package swift

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestSwiftScannerWalkFollowupBranches(t *testing.T) {
	repo := t.TempDir()
	scanner := repoScanner{
		repoPath: repo,
		scan: scanResult{
			ImportedDependencies: make(map[string]struct{}),
		},
		unresolvedImports: make(map[string]int),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ignoredPath := filepath.Join(repo, "ignored.swift")
	if err := os.WriteFile(ignoredPath, []byte("struct Ignored {}\n"), 0o644); err != nil {
		t.Fatalf("write ignored swift file: %v", err)
	}
	if err := scanner.walk(ctx, ignoredPath, mustFileEntryFromPath(t, ignoredPath), nil); err == nil {
		t.Fatalf("expected canceled context error from scanner walk")
	}

	sourceDir := filepath.Join(repo, "Sources")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir Sources dir: %v", err)
	}
	if err := scanner.walk(context.Background(), sourceDir, mustDirEntry(t, repo, "Sources"), nil); err != nil {
		t.Fatalf("expected regular directory walk to continue, got %v", err)
	}

	readmePath := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readmePath, []byte("# docs\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := scanner.walk(context.Background(), readmePath, mustFileEntryFromPath(t, readmePath), nil); err != nil {
		t.Fatalf("expected non-swift file walk to be ignored, got %v", err)
	}

	swiftPath := filepath.Join(repo, "main.swift")
	if err := os.WriteFile(swiftPath, []byte("struct Example {}\n"), 0o644); err != nil {
		t.Fatalf("write swift file: %v", err)
	}
	if err := scanner.walk(context.Background(), swiftPath, mustFileEntryFromPath(t, swiftPath), nil); err != nil {
		t.Fatalf("expected swift file walk to scan successfully, got %v", err)
	}
	if len(scanner.scan.Files) != 1 || scanner.scan.Files[0].Path != "main.swift" {
		t.Fatalf("expected scanner to record the swift file, got %#v", scanner.scan.Files)
	}
}

func TestDetectSwiftEntryReturnsContextError(t *testing.T) {
	repo := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	visited := 0
	detection := language.Detection{}
	path := filepath.Join(repo, "main.swift")
	if err := os.WriteFile(path, []byte("struct Example {}\n"), 0o644); err != nil {
		t.Fatalf("write swift file: %v", err)
	}

	err := detectSwiftEntry(ctx, path, mustFileEntryFromPath(t, path), &detection, map[string]struct{}{}, &visited)
	if err == nil {
		t.Fatalf("expected detectSwiftEntry to return the canceled context error")
	}
}

func mustDirEntry(t *testing.T, root, name string) os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return entry
		}
	}
	t.Fatalf("entry %q not found in %s", name, root)
	return nil
}

func mustFileEntryFromPath(t *testing.T, path string) os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Dir(path), err)
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(path) {
			return entry
		}
	}
	t.Fatalf("entry %q not found in %s", filepath.Base(path), filepath.Dir(path))
	return nil
}
