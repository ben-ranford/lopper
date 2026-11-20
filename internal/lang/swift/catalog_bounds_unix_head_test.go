//go:build unix

package swift

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestSwiftCatalogAndSourceReadsRejectNonRegularFiles(t *testing.T) {
	repo := t.TempDir()
	lockPath := filepath.Join(repo, podLockName)
	if err := syscall.Mkfifo(lockPath, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	catalog := newTestSwiftCatalog()
	if _, _, err := loadPodLockData(repo, &catalog); !errors.Is(err, errInputNotRegular) || errors.Is(err, safeio.ErrFileTooLarge) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected non-regular Podfile.lock to fail before parsing, got %v", err)
	}

	sourcePath := filepath.Join(repo, "Pipe.swift")
	if err := syscall.Mkfifo(sourcePath, 0o600); err != nil {
		t.Fatalf("mkfifo source: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	var sourceEntry os.DirEntry
	for _, entry := range entries {
		if entry.Name() == "Pipe.swift" {
			sourceEntry = entry
		}
	}
	if sourceEntry == nil {
		t.Fatal("missing fifo source entry")
	}
	scanner := &repoScanner{repoPath: repo}
	if err := scanner.scanSwiftFile(sourcePath, sourceEntry); !errors.Is(err, errInputNotRegular) {
		t.Fatalf("expected non-regular Swift source to fail before reading, got %v", err)
	}
}
