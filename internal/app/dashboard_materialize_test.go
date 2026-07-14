package app

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

func TestPersistCommandOutputBypassesFileWritesForEmptyAndDash(t *testing.T) {
	for _, outputPath := range []string{"   ", " - "} {
		status, err := persistCommandOutput("formatted output", outputPath, "dashboard report")
		if err != nil {
			t.Fatalf("persist command output %q: %v", outputPath, err)
		}
		if status != "formatted output" {
			t.Fatalf("expected passthrough output for %q, got %q", outputPath, status)
		}
	}
}

func TestPersistDashboardOutputUsesDashboardLabel(t *testing.T) {
	workspace := t.TempDir()
	chdirCanonicalWorkspace(t, workspace)

	status, err := persistDashboardOutput("{}", "report.json")
	if err != nil {
		t.Fatalf("persist dashboard output: %v", err)
	}
	if status != "dashboard report written to report.json" {
		t.Fatalf("unexpected status: %q", status)
	}
}

func TestPersistCommandOutputPropagatesDirectoryTargetError(t *testing.T) {
	workspace := t.TempDir()
	chdirCanonicalWorkspace(t, workspace)
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}

	_, err := persistCommandOutput("{}", "reports", "dashboard report")
	if err == nil {
		t.Fatal("expected directory target error")
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

func TestCommandOutputRootAllowsRelativeParentOutsideWorkspace(t *testing.T) {
	workspaceParent := t.TempDir()
	workspace := filepath.Join(workspaceParent, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	chdirCanonicalWorkspace(t, workspace)

	outputPath := filepath.Join("..", "reports", "output.json")
	status, err := persistCommandOutput("{}", outputPath, "dashboard report")
	if err != nil {
		t.Fatalf("persist command output: %v", err)
	}
	if status != "dashboard report written to "+outputPath {
		t.Fatalf("unexpected status: %q", status)
	}

	outputAbs := filepath.Join(workspaceParent, "reports", "output.json")
	data, err := os.ReadFile(outputAbs)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "{}" {
		t.Fatalf("unexpected output content: %q", string(data))
	}
}

func TestCommandOutputRootAllowsKnownDarwinSystemAliasRoots(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("known system alias roots only apply on darwin")
	}

	workspace := t.TempDir()
	chdirCanonicalWorkspace(t, workspace)

	outputPath := filepath.Join("/tmp", "lopper-command-output-root", t.Name(), "report.json")
	root, err := commandOutputRoot(outputPath)
	if err != nil {
		t.Fatalf("command output root: %v", err)
	}
	if root != "/tmp" {
		t.Fatalf("expected /tmp root, got %q", root)
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

func TestTrustedCommandOutputRootUsesWorkspaceAlias(t *testing.T) {
	workspace := t.TempDir()
	workspaceAlias := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(workspace, workspaceAlias); err != nil {
		t.Fatalf("create workspace alias: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	chdirCanonicalWorkspace(t, workspace)

	root, err := trustedCommandOutputRoot(filepath.Join(workspaceAlias, "reports", "output.json"))
	if err != nil {
		t.Fatalf("trusted command output root: %v", err)
	}
	if root != workspaceAlias {
		t.Fatalf("expected workspace alias root %q, got %q", workspaceAlias, root)
	}
}

func TestTrustedCommandOutputRootUsesResolvedWorkspaceForRealPathWhenCwdIsAlias(t *testing.T) {
	workspace := t.TempDir()
	workspaceAlias := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(workspace, workspaceAlias); err != nil {
		t.Fatalf("create workspace alias: %v", err)
	}
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(workspaceAlias); err != nil {
		t.Fatalf("chdir workspace alias: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})
	t.Setenv("PWD", workspaceAlias)

	root, err := trustedCommandOutputRoot(filepath.Join(workspace, "reports", "output.json"))
	if err != nil {
		t.Fatalf("trusted command output root: %v", err)
	}
	if root != workspace {
		t.Fatalf("expected resolved workspace root %q, got %q", workspace, root)
	}
}

func TestTrustedCommandOutputRootPropagatesBrokenWorkspaceAliasError(t *testing.T) {
	workspace := t.TempDir()
	chdirCanonicalWorkspace(t, workspace)

	brokenAlias := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), brokenAlias); err != nil {
		t.Fatalf("create broken alias: %v", err)
	}

	_, err := trustedCommandOutputRoot(filepath.Join(brokenAlias, "reports", "output.json"))
	if err == nil {
		t.Fatal("expected broken workspace alias lookup to fail")
	}
}

func TestTrustedCommandOutputRootReturnsEmptyOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	chdirCanonicalWorkspace(t, workspace)

	root, err := trustedCommandOutputRoot(filepath.Join(t.TempDir(), "reports", "output.json"))
	if err != nil {
		t.Fatalf("trusted command output root: %v", err)
	}
	if root != "" {
		t.Fatalf("expected no trusted workspace root, got %q", root)
	}
}

func TestTrustedCommandOutputRootReturnsWorkspaceForWorkspaceRootPath(t *testing.T) {
	workspace := t.TempDir()
	canonicalWorkspace := chdirCanonicalWorkspace(t, workspace)

	root, err := trustedCommandOutputRoot(canonicalWorkspace)
	if err != nil {
		t.Fatalf("trusted command output root: %v", err)
	}
	if root != canonicalWorkspace {
		t.Fatalf("expected trusted workspace root %q, got %q", canonicalWorkspace, root)
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

func TestEnsureCommandOutputParentRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "reports")); err != nil {
		t.Fatalf("create reports symlink: %v", err)
	}

	err := ensureCommandOutputParent(root, filepath.Join(root, "reports", "report.json"))
	if err == nil || !strings.Contains(err.Error(), "output parent contains symlink") {
		t.Fatalf("expected symlinked parent rejection, got %v", err)
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

func TestPathWithinRootAllowsRootPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	within, err := pathWithinRoot(root, root)
	if err != nil {
		t.Fatalf("pathWithinRoot root path: %v", err)
	}
	if !within {
		t.Fatal("expected root path to remain within root")
	}
}

func TestPathWithinRootTreatsDifferentWindowsVolumesAsOutside(t *testing.T) {
	within, err := pathWithinRoot(`C:\repo`, `D:\reports\output.json`)
	if err != nil {
		t.Fatalf("pathWithinRoot different volumes: %v", err)
	}
	if within {
		t.Fatal("expected different-volume path to be treated as outside root")
	}
}

func TestPathVolumeNameFallsBackToWindowsDrivePrefix(t *testing.T) {
	if got := pathVolumeName(`D:\reports\output.json`); got != "d:" {
		t.Fatalf("expected windows drive prefix, got %q", got)
	}
	if got := pathVolumeName("/tmp/report.json"); got != "" {
		t.Fatalf("expected no volume for posix path, got %q", got)
	}
}

func TestIsKnownSystemAliasRoot(t *testing.T) {
	if runtime.GOOS == "darwin" {
		for _, path := range []string{"/tmp", "/var"} {
			if !isKnownSystemAliasRoot(path) {
				t.Fatalf("expected %q to be treated as a known system alias root", path)
			}
		}
	}
	if isKnownSystemAliasRoot("reports") {
		t.Fatal("expected relative path to be rejected as a known system alias root")
	}
}

func TestResolveAliasedWorkspaceRootPropagatesBrokenAliasError(t *testing.T) {
	workspace := t.TempDir()
	brokenAlias := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), brokenAlias); err != nil {
		t.Fatalf("create broken alias: %v", err)
	}

	_, err := resolveAliasedWorkspaceRoot(filepath.Join(brokenAlias, "reports", "output.json"), workspace)
	if err == nil {
		t.Fatal("expected broken alias resolution to fail")
	}
}

func blockedPathFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	blocker := filepath.Join(root, "blocked")
	writeBlockedFile(t, blocker)
	return root, blocker
}

func TestRejectSymlinkedOutputParentRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "reports")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("create reports symlink: %v", err)
	}

	err := rejectSymlinkedOutputParent(root, filepath.Join(link, "nested"))
	if err == nil || !strings.Contains(err.Error(), "output parent contains symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}
