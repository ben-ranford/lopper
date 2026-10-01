package js

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestScanRepoSkipsDisappearingCache(t *testing.T) {
	repo := t.TempDir()
	cache := filepath.Join(repo, ".lopper-cache")
	if err := os.MkdirAll(filepath.Join(cache, "keys", ".safeio-atomic-test"), 0o750); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(repo, "index.js")
	if err := os.WriteFile(source, []byte("import { map } from 'lodash'\nmap([])\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := ScanResult{}
	state := scanRepoState{parser: newSourceParser(), repoPath: repo, result: &result}
	err := filepath.WalkDir(repo, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		err := scanRepoEntry(context.Background(), &state, path, entry)
		if path == cache {
			// Model cache cleanup after enumeration, before WalkDir reads it.
			if removeErr := os.RemoveAll(cache); removeErr != nil {
				return removeErr
			}
		}
		return err
	})
	if err != nil {
		t.Fatalf("scan entered disappearing cache: %v", err)
	}
	if len(result.Files) != 1 || !containsModuleImport(result, "lodash") {
		t.Fatalf("expected source import to remain visible: %#v", result)
	}

	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	err = scanRepoEntry(context.Background(), &state, source, fs.FileInfoToDirEntry(info))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected missing source error, got %v", err)
	}
}
