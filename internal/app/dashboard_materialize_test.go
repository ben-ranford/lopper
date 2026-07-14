package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandOutputRootRelativePathUsesWorkspace(t *testing.T) {
	workspace := t.TempDir()
	canonicalWorkspace := chdirCanonicalWorkspace(t, workspace)

	root, err := commandOutputRoot("reports/output.json")
	if err != nil {
		t.Fatalf("command output root: %v", err)
	}
	if root != canonicalWorkspace {
		t.Fatalf("expected workspace root %q, got %q", canonicalWorkspace, root)
	}
}

func TestCommandOutputRootAbsolutePathUsesExistingParentOutsideWorkspace(t *testing.T) {
	outputRoot := filepath.Join(t.TempDir(), "reports")
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		t.Fatalf("mkdir output root: %v", err)
	}

	root, err := commandOutputRoot(filepath.Join(outputRoot, "output.json"))
	if err != nil {
		t.Fatalf("command output root: %v", err)
	}
	if root != outputRoot {
		t.Fatalf("expected absolute output root %q, got %q", outputRoot, root)
	}
}

func TestAbsoluteCommandOutputRootRejectsSymlinkBoundary(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "reports")); err != nil {
		t.Fatalf("create reports symlink: %v", err)
	}

	_, err := absoluteCommandOutputRoot(filepath.Join(workspace, "reports", "output.json"))
	if err == nil || !strings.Contains(err.Error(), "output root contains symlink") {
		t.Fatalf("expected symlink boundary rejection, got %v", err)
	}
}

func TestAbsoluteCommandOutputRootRejectsSymlinkBoundaryWhenTargetNestedExists(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir outside nested: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "reports")); err != nil {
		t.Fatalf("create reports symlink: %v", err)
	}
	canonicalWorkspace := chdirCanonicalWorkspace(t, workspace)

	_, err := absoluteCommandOutputRoot(filepath.Join(canonicalWorkspace, "reports", "nested", "output.json"))
	if err == nil || !strings.Contains(err.Error(), "output root contains symlink") {
		t.Fatalf("expected nested symlink boundary rejection, got %v", err)
	}
}

func TestAbsoluteCommandOutputRootUsesWorkspaceBoundary(t *testing.T) {
	workspace := t.TempDir()
	canonicalWorkspace := chdirCanonicalWorkspace(t, workspace)
	outputPath := filepath.Join(canonicalWorkspace, "reports", "existing", "output.json")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		t.Fatalf("mkdir output parent: %v", err)
	}

	root, err := absoluteCommandOutputRoot(outputPath)
	if err != nil {
		t.Fatalf("absolute command output root: %v", err)
	}
	if root != canonicalWorkspace {
		t.Fatalf("expected workspace root %q, got %q", canonicalWorkspace, root)
	}
}

func TestAbsoluteCommandOutputRootRejectsFileBoundary(t *testing.T) {
	workspace := t.TempDir()
	blocker := filepath.Join(workspace, "reports")
	writeBlockedFile(t, blocker)

	_, err := absoluteCommandOutputRoot(filepath.Join(blocker, "output.json"))
	if err == nil || !strings.Contains(err.Error(), "output root is not a directory") {
		t.Fatalf("expected file boundary rejection, got %v", err)
	}
}

func TestAbsoluteCommandOutputRootPropagatesLookupError(t *testing.T) {
	workspace := t.TempDir()
	locked := filepath.Join(workspace, "locked")
	writeBlockedFile(t, locked)

	_, err := absoluteCommandOutputRoot(filepath.Join(locked, "missing", "output.json"))
	if err == nil {
		t.Fatal("expected lookup error for inaccessible parent")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "lstat" || pathErr.Path != filepath.Join(locked, "missing") {
		t.Fatalf("expected propagated lstat path error for child under file, got %v", err)
	}
}

func TestEnsureCommandOutputParentRejectsEscapingPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	outputPath := filepath.Join(root, "..", "outside", "report.json")
	err := ensureCommandOutputParent(root, outputPath)
	if err == nil || !strings.Contains(err.Error(), "output path escapes workspace") {
		t.Fatalf("expected escaping output path rejection, got %v", err)
	}
}

func TestEnsureCommandOutputParentCreatesNestedDirectories(t *testing.T) {
	root := t.TempDir()
	outputPath := filepath.Join(root, "reports", "nested", "report.json")

	if err := ensureCommandOutputParent(root, outputPath); err != nil {
		t.Fatalf("ensure command output parent: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(outputPath)); err != nil {
		t.Fatalf("stat output parent: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("expected output parent directory, got mode %v", info.Mode())
	}
}

func TestEnsureCommandOutputParentPropagatesMkdirAllError(t *testing.T) {
	root, blocker := blockedPathFixture(t)

	err := ensureCommandOutputParent(root, filepath.Join(blocker, "report.json"))
	if err == nil {
		t.Fatal("expected mkdir failure under regular file")
	}
}

func TestRejectSymlinkedOutputParentAllowsRootParent(t *testing.T) {
	root := t.TempDir()
	if err := rejectSymlinkedOutputParent(root, root); err != nil {
		t.Fatalf("expected root parent to be allowed, got %v", err)
	}
}

func TestRejectSymlinkedOutputParentRejectsEscapingParent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	err := rejectSymlinkedOutputParent(root, filepath.Dir(root))
	if err == nil || !strings.Contains(err.Error(), "output parent escapes workspace") {
		t.Fatalf("expected escaping parent rejection, got %v", err)
	}
}

func TestRejectSymlinkedOutputParentAllowsMissingTail(t *testing.T) {
	root := t.TempDir()
	if err := rejectSymlinkedOutputParent(root, filepath.Join(root, "missing", "nested")); err != nil {
		t.Fatalf("expected missing tail to be allowed, got %v", err)
	}
}

func TestRejectSymlinkedOutputParentPropagatesLookupError(t *testing.T) {
	root, blocker := blockedPathFixture(t)

	err := rejectSymlinkedOutputParent(root, filepath.Join(blocker, "nested"))
	if err == nil {
		t.Fatal("expected lookup error under regular file")
	}
}

func TestPathWithinRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	within, err := pathWithinRoot(root, filepath.Join(root, "nested", "report.json"))
	if err != nil {
		t.Fatalf("pathWithinRoot within root: %v", err)
	}
	if !within {
		t.Fatal("expected nested path to remain within root")
	}

	within, err = pathWithinRoot(root, filepath.Join(filepath.Dir(root), "outside", "report.json"))
	if err != nil {
		t.Fatalf("pathWithinRoot outside root: %v", err)
	}
	if within {
		t.Fatal("expected sibling path to escape root")
	}
}

func blockedPathFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	blocker := filepath.Join(root, "blocked")
	writeBlockedFile(t, blocker)
	return root, blocker
}
