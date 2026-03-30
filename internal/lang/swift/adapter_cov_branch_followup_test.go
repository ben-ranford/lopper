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
	entriesByName := mustReadDirEntriesByName(t, repo)
	if err := scanner.walk(ctx, ignoredPath, entriesByName["ignored.swift"], nil); err == nil {
		t.Fatalf("expected canceled context error from scanner walk")
	}

	sourceDir := filepath.Join(repo, "Sources")
	if err := os.Mkdir(sourceDir, 0o755); err != nil {
		t.Fatalf("mkdir Sources dir: %v", err)
	}
	entriesByName = mustReadDirEntriesByName(t, repo)
	if err := scanner.walk(context.Background(), sourceDir, entriesByName["Sources"], nil); err != nil {
		t.Fatalf("expected regular directory walk to continue, got %v", err)
	}

	readmePath := filepath.Join(repo, "README.md")
	if err := os.WriteFile(readmePath, []byte("# docs\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	entriesByName = mustReadDirEntriesByName(t, repo)
	if err := scanner.walk(context.Background(), readmePath, entriesByName["README.md"], nil); err != nil {
		t.Fatalf("expected non-swift file walk to be ignored, got %v", err)
	}

	swiftPath := filepath.Join(repo, "main.swift")
	if err := os.WriteFile(swiftPath, []byte("struct Example {}\n"), 0o644); err != nil {
		t.Fatalf("write swift file: %v", err)
	}
	entriesByName = mustReadDirEntriesByName(t, repo)
	if err := scanner.walk(context.Background(), swiftPath, entriesByName["main.swift"], nil); err != nil {
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

	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	var fileEntry os.DirEntry
	for _, entry := range entries {
		if entry.Name() == "main.swift" {
			fileEntry = entry
			break
		}
	}
	if fileEntry == nil {
		t.Fatalf("main.swift entry not found")
	}

	err = detectSwiftEntry(ctx, path, fileEntry, &detection, map[string]struct{}{}, &visited)
	if err == nil {
		t.Fatalf("expected detectSwiftEntry to return the canceled context error")
	}
}
