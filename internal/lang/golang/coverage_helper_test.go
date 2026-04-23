package golang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestCoverageHelperBranches(t *testing.T) {
	if looksExternalImport("") {
		t.Fatalf("expected empty import path to be non-external")
	}
	if looksExternalImport("stdlib") {
		t.Fatalf("expected stdlib import path to be non-external")
	}

	if got := inferDependency(""); got != "" {
		t.Fatalf("expected empty import to infer empty dependency, got %q", got)
	}
	if got := inferDependency("single"); got != "" {
		t.Fatalf("expected non-domain import to infer empty dependency, got %q", got)
	}

	if expr, kind := parseBuildConstraintComment("not a constraint comment"); expr != nil || kind != "" {
		t.Fatalf("expected non-constraint comment parse to return nil/empty, got expr=%v kind=%q", expr, kind)
	}
	if isSupportedGoReleaseTag("invalid") {
		t.Fatalf("expected invalid release tag to be unsupported")
	}

	if _, err := readGoWorkUseEntries("\x00"); err == nil {
		t.Fatalf("expected invalid repo path to fail go.work read")
	}

	applyVendoredMetadataDirective("## ", &vendoredDependencyMetadata{})
	appendVendoredMetadataWarnings(nil, vendoredParseState{})

	if _, err := loadGoModuleInfoWithOptions("\x00", moduleLoadOptions{}); err == nil {
		t.Fatalf("expected invalid repo path to fail module loading")
	}
}

func TestLoadGoModuleInfoWithOptionsErrorBranches(t *testing.T) {
	t.Run("workspace read failure", testWorkspaceReadFailure)
	t.Run("nested walk failure", testNestedWalkFailure)
	t.Run("vendored read failure", testVendoredReadFailure)
}

func testWorkspaceReadFailure(t *testing.T) {
	repo := t.TempDir()
	goWorkPath := filepath.Join(repo, goWorkName)
	testutil.MustWriteFile(t, goWorkPath, "use ./module\n")
	setUnreadableForTest(t, goWorkPath, 0o644, "go.work")

	if _, err := loadGoModuleInfoWithOptions(repo, moduleLoadOptions{}); err == nil {
		t.Fatalf("expected unreadable go.work to fail module loading")
	}
}

func testNestedWalkFailure(t *testing.T) {
	repo := t.TempDir()
	locked := filepath.Join(repo, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("mkdir locked: %v", err)
	}
	setUnreadableForTest(t, locked, 0o755, "locked dir")

	if _, err := loadGoModuleInfoWithOptions(repo, moduleLoadOptions{}); err == nil {
		t.Fatalf("expected unreadable nested directory to fail module loading")
	}
}

func testVendoredReadFailure(t *testing.T) {
	repo := t.TempDir()
	vendorModules := filepath.Join(repo, vendorModulesTxtName)
	testutil.MustWriteFile(t, vendorModules, "# github.com/acme/dep v1.0.0\n")
	setUnreadableForTest(t, vendorModules, 0o644, "vendor/modules.txt")

	if _, err := loadGoModuleInfoWithOptions(repo, moduleLoadOptions{EnableVendoredProvenance: true}); err == nil {
		t.Fatalf("expected unreadable vendor/modules.txt to fail module loading")
	}
}

func setUnreadableForTest(t *testing.T, path string, restoreMode os.FileMode, label string) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Skipf("chmod %s unreadable: %v", label, err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, restoreMode); err != nil {
			t.Errorf("restore %s permissions: %v", label, err)
		}
	})
}

func TestResolveRepoBoundedPathAbsoluteOutside(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()

	if resolved, ok := resolveRepoBoundedPath(repo, filepath.Join(repo, "inside")); !ok || !strings.HasPrefix(resolved, repo) {
		t.Fatalf("expected absolute in-repo path to resolve, got resolved=%q ok=%v", resolved, ok)
	}
	if _, ok := resolveRepoBoundedPath(repo, filepath.Join(outside, "x")); ok {
		t.Fatalf("expected absolute path outside repo to be rejected")
	}
}
