package swift

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func mustReadDirEntriesByName(t *testing.T, dir string) map[string]os.DirEntry {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}

	entriesByName := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		entriesByName[entry.Name()] = entry
	}
	return entriesByName
}

func mustReadSwiftDirEntry(t *testing.T, dir, name string) os.DirEntry {
	t.Helper()

	entriesByName := mustReadDirEntriesByName(t, dir)
	entry, ok := entriesByName[name]
	if !ok {
		t.Fatalf("expected %s dir entry", name)
	}
	return entry
}

func mustReadSwiftDetectionEntries(t *testing.T) (string, os.DirEntry, os.DirEntry) {
	t.Helper()

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, swiftBuildDirName, swiftMainFileName), "import Foundation\n")
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), "// manifest\n")

	entriesByName := mustReadDirEntriesByName(t, repo)
	buildEntry, ok := entriesByName[swiftBuildDirName]
	if !ok {
		t.Fatalf("expected build entry, got %#v", entriesByName)
	}
	manifestEntry, ok := entriesByName[packageManifestName]
	if !ok {
		t.Fatalf("expected manifest entry, got %#v", entriesByName)
	}

	return repo, buildEntry, manifestEntry
}
