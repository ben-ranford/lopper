package js

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenWorkspaceSearchRootConfinement(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	outside := t.TempDir()
	if root, warning := openWorkspaceSearchRoot(repo, outside); root != nil || !strings.Contains(warning, "outside repository") {
		t.Fatalf("expected outside workspace root rejection, root=%v warning=%q", root, warning)
	}

	searchRoot := filepath.Join(repo, "packages")
	if err := os.MkdirAll(searchRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspace search root: %v", err)
	}
	root, warning := openWorkspaceSearchRoot(repo, searchRoot)
	if warning != "" {
		t.Fatalf("expected repository workspace root to open, got %q", warning)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close workspace search root: %v", err)
	}

	linkPath := filepath.Join(repo, "linked-packages")
	if err := os.Symlink(searchRoot, linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if root, warning := openWorkspaceSearchRoot(repo, linkPath); root != nil || !strings.Contains(warning, "unable to open workspace search root") {
		t.Fatalf("expected symlink workspace root rejection, root=%v warning=%q", root, warning)
	}
}
