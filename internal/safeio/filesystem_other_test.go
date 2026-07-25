//go:build !windows

package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCanonicalWriteRootAcceptsUnixPathsThatLookWindowsLike(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(cwd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	})

	workDir := t.TempDir()
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir workdir: %v", err)
	}

	for _, rawPath := range []string{`C:cache`, `\cache`, `\\server`, `C:\cache.`, `C:\cache `, `\\server\share\dir `} {
		if err := os.Mkdir(rawPath, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", rawPath, err)
		}
		root, err := OpenCanonicalWriteRoot(rawPath)
		if err != nil {
			t.Fatalf("OpenCanonicalWriteRoot(%q): %v", rawPath, err)
		}
		if _, err := root.Lstat("."); err != nil {
			t.Fatalf("Lstat(%q): %v", rawPath, err)
		}
		if err := root.Close(); err != nil {
			t.Fatalf("close %q: %v", rawPath, err)
		}
		if _, err := os.Stat(filepath.Join(workDir, rawPath)); err != nil {
			t.Fatalf("stat %q: %v", rawPath, err)
		}
	}
}
