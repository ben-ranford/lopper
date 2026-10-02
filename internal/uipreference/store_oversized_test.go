//go:build !regressionproof

package uipreference

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRejectsOversizedPreference(t *testing.T) {
	root := t.TempDir()
	store := Store{ConfigDir: func() (string, error) { return root, nil }}
	path, err := store.path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, (4<<10)+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("accepted oversized UI preference")
	}
}
