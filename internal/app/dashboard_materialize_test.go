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

func TestPersistCommandOutputRejectsTrailingSeparator(t *testing.T) {
	workspace := t.TempDir()
	chdirCanonicalWorkspace(t, workspace)

	existingFile := filepath.Join(workspace, "existing-file")
	if err := os.WriteFile(existingFile, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	for _, outputPath := range []string{"reports/", "existing-file/", "reports/.", "existing-file/."} {
		_, err := persistCommandOutput("{}", outputPath, "dashboard report")
		if err == nil || !strings.Contains(err.Error(), "output path must name a file") {
			t.Fatalf("expected directory-style output rejection for %q, got %v", outputPath, err)
		}
	}

	data, err := os.ReadFile(existingFile)
	if err != nil {
		t.Fatalf("read existing file: %v", err)
	}
	if string(data) != "keep" {
		t.Fatalf("expected existing file to remain unchanged, got %q", string(data))
	}
}

func TestRootedCommandOutputRootPropagatesOutputPathResolutionError(t *testing.T) {
	withUnreadableWorkingDirectory(t, func() {
		_, err := rootedCommandOutputRoot("report.json")
		if err == nil || !strings.Contains(err.Error(), "resolve output path") {
			t.Fatalf("expected output path resolution failure, got %v", err)
		}
	})
}

func TestRootedCommandOutputRootAllowsAbsolutePathWhenWorkspaceLookupFails(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "reports", "output.json")
	withUnreadableWorkingDirectory(t, func() {
		root, err := rootedCommandOutputRoot(outputPath)
		if err != nil {
			t.Fatalf("rooted command output root: %v", err)
		}
		wantRoot := filepath.Dir(filepath.Dir(outputPath))
		if root != wantRoot {
			t.Fatalf("expected fallback output root %q, got %q", wantRoot, root)
		}
	})
}

func TestRootedCommandOutputRootAllowsAbsolutePathWhenRelativeTrustedRootLookupFails(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "reports", "output.json")
	withRemovedWorkingDirectory(t, func() {
		root, err := rootedCommandOutputRoot(outputPath, ".")
		if err != nil {
			t.Fatalf("rooted command output root: %v", err)
		}
		wantRoot := filepath.Dir(filepath.Dir(outputPath))
		if root != wantRoot {
			t.Fatalf("expected fallback output root %q, got %q", wantRoot, root)
		}
	})
}

func TestRootedCommandOutputRootPropagatesTrustedRootLookupError(t *testing.T) {
	assertTrustedRootLookupError(t, "trusted root lookup failure", func(outputPath, trustedRoot string) error {
		_, err := rootedCommandOutputRoot(outputPath, trustedRoot)
		return err
	})
}

func TestRootedCommandOutputRootUsesTrustedRoot(t *testing.T) {
	trustedRoot := t.TempDir()

	root, err := rootedCommandOutputRoot(filepath.Join(trustedRoot, "reports", "output.json"), trustedRoot)
	if err != nil {
		t.Fatalf("rooted command output root: %v", err)
	}
	if root != trustedRoot {
		t.Fatalf("expected trusted root %q, got %q", trustedRoot, root)
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

func TestPersistCommandOutputAllowsAbsoluteExternalPathWhenWorkingDirectoryRemoved(t *testing.T) {
	assertPersistCommandOutputWritesAbsolutePath(t, withRemovedWorkingDirectory)
}

func TestPersistCommandOutputAllowsAbsoluteExternalPathWhenWorkingDirectoryUnreadable(t *testing.T) {
	assertPersistCommandOutputWritesAbsolutePath(t, withUnreadableWorkingDirectory)
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

func TestPersistCommandOutputPropagatesParentCreationError(t *testing.T) {
	workspace := t.TempDir()
	chdirCanonicalWorkspace(t, workspace)
	if err := os.WriteFile(filepath.Join(workspace, "reports"), []byte("block"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	_, err := persistCommandOutput("{}", filepath.Join("reports", "output.json"), "dashboard report")
	if err == nil {
		t.Fatal("expected parent creation error")
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

func TestPersistCommandOutputRejectsAbsolutePathWithLexicalAncestorSymlink(t *testing.T) {
	workspace := t.TempDir()
	chdirCanonicalWorkspace(t, workspace)

	base := filepath.Join(t.TempDir(), "base")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir base: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir outside nested: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "link")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}

	outputPath := filepath.Join(base, "link", "nested", "output.json")
	_, err := persistCommandOutput("{}", outputPath, "dashboard report")
	if err == nil || !strings.Contains(err.Error(), "output root contains symlink") {
		t.Fatalf("expected lexical ancestor symlink rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "nested", "output.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside output to remain absent, got err=%v", statErr)
	}
}

func TestRejectLexicalOutputRootSymlinksAllowsRegularExistingPath(t *testing.T) {
	existingRoot := filepath.Join(t.TempDir(), "base", "nested")
	if err := os.MkdirAll(existingRoot, 0o755); err != nil {
		t.Fatalf("mkdir existing root: %v", err)
	}

	if err := rejectLexicalOutputRootSymlinks(existingRoot); err != nil {
		t.Fatalf("reject lexical output root symlinks: %v", err)
	}
}

func TestRejectLexicalOutputRootSymlinksAllowsKnownDarwinAliasRoot(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("known system alias roots only apply on darwin")
	}

	if err := rejectLexicalOutputRootSymlinks("/tmp"); err != nil {
		t.Fatalf("reject lexical output root symlinks: %v", err)
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

func TestPersistCommandOutputRejectsRelativeParentSymlinkEscape(t *testing.T) {
	workspaceParent := t.TempDir()
	workspace := filepath.Join(workspaceParent, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "app"), 0o755); err != nil {
		t.Fatalf("mkdir workspace app: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir outside nested: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "reports")); err != nil {
		t.Fatalf("create reports symlink: %v", err)
	}
	chdirCanonicalWorkspace(t, filepath.Join(workspace, "app"))

	outputPath := filepath.Join("..", "reports", "nested", "output.json")
	_, err := persistCommandOutput("{}", outputPath, "dashboard report")
	if err == nil || !strings.Contains(err.Error(), "output root contains symlink") {
		t.Fatalf("expected parent-relative symlink rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "nested", "output.json")); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside output to remain absent, got err=%v", statErr)
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

func TestTrustedCommandOutputRootForRootsUsesFirstMatchingRoot(t *testing.T) {
	workspace := t.TempDir()
	outputPath := filepath.Join(workspace, "reports", "output.json")
	root, err := trustedCommandOutputRootForRoots(outputPath, "", filepath.Join(t.TempDir(), "other"), workspace)
	if err != nil {
		t.Fatalf("trusted command output root for roots: %v", err)
	}
	if root != workspace {
		t.Fatalf("expected matching trusted root %q, got %q", workspace, root)
	}
}

func TestTrustedCommandOutputRootForRootsReturnsEmptyWhenRootsDoNotMatch(t *testing.T) {
	root, err := trustedCommandOutputRootForRoots(filepath.Join(t.TempDir(), "reports", "output.json"), filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatalf("trusted command output root for roots: %v", err)
	}
	if root != "" {
		t.Fatalf("expected no trusted root, got %q", root)
	}
}

func TestTrustedCommandOutputRootForRootsSkipsInvalidNonMatchingRoot(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	writeBlockedFile(t, blocker)
	validRoot := t.TempDir()
	outputPath := filepath.Join(validRoot, "reports", "output.json")

	root, err := trustedCommandOutputRootForRoots(outputPath, filepath.Join(blocker, "repo"), validRoot)
	if err != nil {
		t.Fatalf("trusted command output root for roots: %v", err)
	}
	if root != validRoot {
		t.Fatalf("expected later valid trusted root %q, got %q", validRoot, root)
	}
}

func TestTrustedCommandOutputRootForRootsFailsClosedForInvalidMatchingRoot(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	writeBlockedFile(t, blocker)
	invalidRoot := filepath.Join(blocker, "repo")
	outputPath := filepath.Join(invalidRoot, "reports", "output.json")

	_, err := trustedCommandOutputRootForRoots(outputPath, invalidRoot, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "resolve trusted output workspace") {
		t.Fatalf("expected invalid matching trusted root to fail closed, got %v", err)
	}
}

func TestTrustedCommandOutputRootForRootIgnoresMissingRootOutsideOutputPath(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "reports", "output.json")
	root, err := trustedCommandOutputRootForRoot(outputPath, filepath.Join(t.TempDir(), "missing", "repo"))
	if err != nil {
		t.Fatalf("trusted command output root for missing root: %v", err)
	}
	if root != "" {
		t.Fatalf("expected no trusted root for missing path, got %q", root)
	}
}

func TestTrustedCommandOutputRootForRootIgnoresRelativeRootWhenLookupFailsForAbsoluteOutput(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "reports", "output.json")
	withRemovedWorkingDirectory(t, func() {
		root, err := trustedCommandOutputRootForRoot(outputPath, ".")
		if err != nil {
			t.Fatalf("trusted command output root for relative root: %v", err)
		}
		if root != "" {
			t.Fatalf("expected no trusted root when relative lookup fails, got %q", root)
		}
	})
}

func TestTrustedCommandOutputRootForRootUsesAliasPath(t *testing.T) {
	_, workspaceAlias := createWorkspaceAlias(t)

	root, err := trustedCommandOutputRootForRoot(filepath.Join(workspaceAlias, "reports", "output.json"), workspaceAlias)
	if err != nil {
		t.Fatalf("trusted command output root for alias: %v", err)
	}
	if root != workspaceAlias {
		t.Fatalf("expected alias trusted root %q, got %q", workspaceAlias, root)
	}
}

func TestTrustedCommandOutputRootForRootUsesResolvedRootForRealPath(t *testing.T) {
	workspace, workspaceAlias := createWorkspaceAlias(t)

	root, err := trustedCommandOutputRootForRoot(filepath.Join(workspace, "reports", "output.json"), workspaceAlias)
	if err != nil {
		t.Fatalf("trusted command output root for real path: %v", err)
	}
	if root != workspace {
		t.Fatalf("expected resolved trusted root %q, got %q", workspace, root)
	}
}

func TestTrustedCommandOutputRootForRootsPropagatesRootError(t *testing.T) {
	assertTrustedRootLookupError(t, "trusted root resolution error", func(outputPath, trustedRoot string) error {
		_, err := trustedCommandOutputRootForRoots(outputPath, trustedRoot)
		return err
	})
}

func TestPersistCommandOutputAllowsNewDirectoriesThroughWorkspaceAlias(t *testing.T) {
	workspace := t.TempDir()
	workspaceAlias := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(workspace, workspaceAlias); err != nil {
		t.Fatalf("create workspace alias: %v", err)
	}
	chdirCanonicalWorkspace(t, workspace)

	outputPath := filepath.Join(workspaceAlias, "new", "report.json")
	status, err := persistCommandOutput("{}", outputPath, "dashboard report")
	if err != nil {
		t.Fatalf("persist command output: %v", err)
	}
	if status != "dashboard report written to "+outputPath {
		t.Fatalf("unexpected status: %q", status)
	}

	data, err := os.ReadFile(filepath.Join(workspace, "new", "report.json"))
	if err != nil {
		t.Fatalf("read aliased output: %v", err)
	}
	if string(data) != "{}" {
		t.Fatalf("unexpected aliased output content: %q", string(data))
	}
}

func TestTrustedCommandOutputRootUsesWorkspaceSubdirectoryAlias(t *testing.T) {
	workspace := t.TempDir()
	workspaceAlias := filepath.Join(t.TempDir(), "reports-alias")
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.Symlink(filepath.Join(workspace, "reports"), workspaceAlias); err != nil {
		t.Fatalf("create reports alias: %v", err)
	}
	chdirCanonicalWorkspace(t, workspace)

	root, err := trustedCommandOutputRoot(filepath.Join(workspaceAlias, "nested", "output.json"))
	if err != nil {
		t.Fatalf("trusted command output root: %v", err)
	}
	if root != workspaceAlias {
		t.Fatalf("expected workspace subdirectory alias root %q, got %q", workspaceAlias, root)
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

func TestTrustedCommandOutputRootPropagatesWorkspaceLookupError(t *testing.T) {
	withUnreadableWorkingDirectory(t, func() {
		_, err := trustedCommandOutputRoot(filepath.Join(t.TempDir(), "reports", "output.json"))
		if err == nil || !strings.Contains(err.Error(), "resolve output workspace") {
			t.Fatalf("expected workspace lookup failure, got %v", err)
		}
	})
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

func TestAbsoluteCommandOutputRootRejectsSymlinkBoundaryViaWorkspaceSubdirectoryAlias(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(workspace, "reports"), 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir outside nested: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "reports", "link")); err != nil {
		t.Fatalf("create reports link symlink: %v", err)
	}
	reportsAlias := filepath.Join(t.TempDir(), "reports-alias")
	if err := os.Symlink(filepath.Join(workspace, "reports"), reportsAlias); err != nil {
		t.Fatalf("create reports alias: %v", err)
	}
	chdirCanonicalWorkspace(t, workspace)

	_, err := absoluteCommandOutputRoot(filepath.Join(reportsAlias, "link", "nested", "output.json"))
	if err == nil || !strings.Contains(err.Error(), "output root contains symlink") {
		t.Fatalf("expected symlink boundary rejection via subdirectory alias, got %v", err)
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

func TestInspectOutputRootPathPropagatesLookupError(t *testing.T) {
	workspace := t.TempDir()
	locked := filepath.Join(workspace, "locked")
	writeBlockedFile(t, locked)

	_, _, err := inspectOutputRootPath(filepath.Join(locked, "missing"), "output.json")
	if err == nil {
		t.Fatal("expected lookup error for child under file")
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

func TestEnsureCommandOutputParentPropagatesRootResolutionError(t *testing.T) {
	withUnreadableWorkingDirectory(t, func() {
		err := ensureCommandOutputParent("reports", "report.json")
		if err == nil || !strings.Contains(err.Error(), "resolve output root") {
			t.Fatalf("expected output root resolution failure, got %v", err)
		}
	})
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

func TestHasDirectoryStyleOutputPath(t *testing.T) {
	if !hasTrailingOutputPathSeparator("reports/") {
		t.Fatal("expected trailing slash to be treated as a path separator")
	}
	if !hasDirectoryStyleOutputPath("reports/.") {
		t.Fatal("expected trailing dot path element to be rejected")
	}
	if !hasDirectoryStyleOutputPath("reports/..") {
		t.Fatal("expected trailing dotdot path element to be rejected")
	}
	if hasDirectoryStyleOutputPath(filepath.Join("reports", "out.json")) {
		t.Fatal("expected ordinary file path to remain valid")
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

func TestResolveAliasedWorkspaceRootPropagatesLookupError(t *testing.T) {
	workspace := t.TempDir()
	locked := filepath.Join(t.TempDir(), "locked")
	writeBlockedFile(t, locked)

	_, err := resolveAliasedWorkspaceRoot(filepath.Join(locked, "reports", "output.json"), workspace)
	if err == nil {
		t.Fatal("expected lookup error for child under file")
	}
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || pathErr.Op != "lstat" || pathErr.Path != filepath.Join(locked, "reports") {
		t.Fatalf("expected propagated lstat path error for child under file, got %v", err)
	}
}

func TestResolveAliasedWorkspaceRootReturnsTopmostWorkspaceAlias(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "reports", "existing"), 0o755); err != nil {
		t.Fatalf("mkdir reports existing: %v", err)
	}
	resolvedWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("resolve workspace symlinks: %v", err)
	}
	reportsAlias := filepath.Join(t.TempDir(), "reports-alias")
	if err := os.Symlink(filepath.Join(workspace, "reports"), reportsAlias); err != nil {
		t.Fatalf("create reports alias: %v", err)
	}

	root, err := resolveAliasedWorkspaceRoot(filepath.Join(reportsAlias, "existing", "output.json"), resolvedWorkspace)
	if err != nil {
		t.Fatalf("resolve aliased workspace root: %v", err)
	}
	if root != reportsAlias {
		t.Fatalf("expected topmost workspace alias %q, got %q", reportsAlias, root)
	}
}

func blockedPathFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	blocker := filepath.Join(root, "blocked")
	writeBlockedFile(t, blocker)
	return root, blocker
}

func withUnreadableWorkingDirectory(t *testing.T, fn func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("working directory permission errors are not stable on windows")
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	workspace := t.TempDir()
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir unreadable workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
		if err := os.Chmod(workspace, 0o755); err != nil {
			t.Errorf("restore workspace permissions: %v", err)
		}
	})
	if err := os.Chmod(workspace, 0); err != nil {
		t.Fatalf("chmod unreadable workspace: %v", err)
	}
	if _, err := os.Getwd(); err == nil {
		if err := os.Chmod(workspace, 0o755); err != nil {
			t.Fatalf("restore workspace permissions for removal: %v", err)
		}
		if err := os.RemoveAll(workspace); err != nil {
			t.Fatalf("remove unreadable workspace fallback: %v", err)
		}
	}

	fn()
}

func withRemovedWorkingDirectory(t *testing.T, fn func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("removed working directory semantics are not stable on windows")
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	workspace := t.TempDir()
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir removed workspace: %v", err)
	}
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatalf("remove workspace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	})

	fn()
}

func assertPersistCommandOutputWritesAbsolutePath(t *testing.T, withWorkspaceFailure func(*testing.T, func())) {
	t.Helper()

	outputPath := filepath.Join(t.TempDir(), "reports", "output.json")
	withWorkspaceFailure(t, func() {
		status, err := persistCommandOutput("{}", outputPath, "dashboard report")
		if err != nil {
			t.Fatalf("persist command output: %v", err)
		}
		if status != "dashboard report written to "+outputPath {
			t.Fatalf("unexpected status: %q", status)
		}
	})

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "{}" {
		t.Fatalf("unexpected output content: %q", string(data))
	}
}

func assertTrustedRootLookupError(t *testing.T, label string, lookup func(outputPath string, trustedRoot string) error) {
	t.Helper()

	root := t.TempDir()
	blocker := filepath.Join(root, "blocked")
	writeBlockedFile(t, blocker)
	trustedRoot := filepath.Join(blocker, "repo")

	err := lookup(filepath.Join(trustedRoot, "reports", "output.json"), trustedRoot)
	if err == nil || !strings.Contains(err.Error(), "resolve trusted output workspace") {
		t.Fatalf("expected %s, got %v", label, err)
	}
}

func createWorkspaceAlias(t *testing.T) (string, string) {
	t.Helper()

	workspace := t.TempDir()
	workspaceAlias := filepath.Join(t.TempDir(), "repo")
	if err := os.Symlink(workspace, workspaceAlias); err != nil {
		t.Fatalf("create workspace alias: %v", err)
	}
	return workspace, workspaceAlias
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
