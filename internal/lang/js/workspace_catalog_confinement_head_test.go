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
	if err := os.MkdirAll(filepath.Join(searchRoot, "app"), 0o755); err != nil {
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

	if root, warning := openWorkspaceSearchRoot(repo, filepath.Join(linkPath, "app")); root != nil || !strings.Contains(warning, "unable to open workspace search root") {
		t.Fatalf("expected aliased workspace root rejection, root=%v warning=%q", root, warning)
	}
}

func TestDiscoverWorkspacePackageDirsInRootReportsUnavailableRoots(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	regularFile := filepath.Join(repo, "package-list.txt")
	if err := os.WriteFile(regularFile, []byte("packages"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name        string
		searchRoot  string
		wantWarning string
	}{
		{name: "missing", searchRoot: filepath.Join(repo, "missing")},
		{name: "regular file", searchRoot: regularFile},
		{name: "invalid path", searchRoot: filepath.Join(repo, "invalid\x00root"), wantWarning: "unable to access workspace search root"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dirs, warnings := discoverWorkspacePackageDirsInRoot(repo, filepath.Join(repo, jsPackageFile), tc.searchRoot, nil)
			if len(dirs) != 0 {
				t.Fatalf("expected no package directories, got %v", dirs)
			}
			if tc.wantWarning == "" {
				if len(warnings) != 0 {
					t.Fatalf("expected no warnings, got %v", warnings)
				}
			} else if len(warnings) != 1 || !strings.Contains(warnings[0], tc.wantWarning) {
				t.Fatalf("expected one warning containing %q, got %v", tc.wantWarning, warnings)
			}
		})
	}
}
