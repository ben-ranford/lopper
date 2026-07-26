//go:build darwin

package advisory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareAdvisoryCacheRootAllowsTrustedTmpAliasCreation(t *testing.T) {
	testPrepareAdvisoryCacheRootAllowsTrustedDarwinAliasCreation(t, filepath.Join(string(os.PathSeparator), "tmp"), filepath.Join(string(os.PathSeparator), "private", "tmp"))
}

func TestPrepareAdvisoryCacheRootAllowsTrustedVarAliasCreation(t *testing.T) {
	testPrepareAdvisoryCacheRootAllowsTrustedDarwinAliasCreation(t, filepath.Join(string(os.PathSeparator), "var", "tmp"), filepath.Join(string(os.PathSeparator), "private", "var", "tmp"))
}

func testPrepareAdvisoryCacheRootAllowsTrustedDarwinAliasCreation(t *testing.T, aliasParent, resolvedParent string) {
	topDir, err := os.MkdirTemp(aliasParent, "lopper-advisory-")
	if err != nil {
		t.Fatalf("create advisory cache test directory under %q: %v", aliasParent, err)
	}
	cachePath := filepath.Join(topDir, "cache")
	t.Cleanup(func() {
		if err := os.RemoveAll(topDir); err != nil {
			t.Errorf("remove advisory cache test directory: %v", err)
		}
	})

	root, err := prepareAdvisoryCacheRoot(cachePath)
	if err != nil {
		t.Fatalf("prepareAdvisoryCacheRoot returned error: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close advisory cache root: %v", closeErr)
		}
	}()

	openedInfo, err := root.Lstat(".")
	if err != nil {
		t.Fatalf("lstat advisory cache root: %v", err)
	}

	resolvedPath, err := filepath.EvalSymlinks(cachePath)
	if err != nil {
		t.Fatalf("eval symlinks for advisory cache root: %v", err)
	}
	targetInfo, err := os.Stat(resolvedPath)
	if err != nil {
		t.Fatalf("stat resolved advisory cache root: %v", err)
	}
	if !os.SameFile(openedInfo, targetInfo) {
		t.Fatalf("expected advisory cache root to pin resolved path %q", resolvedPath)
	}
	if !strings.HasPrefix(resolvedPath, resolvedParent+string(os.PathSeparator)) {
		t.Fatalf("expected resolved advisory cache root under %q, got %q", resolvedParent, resolvedPath)
	}
}
