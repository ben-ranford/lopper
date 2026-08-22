//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestPersistProfileConfigForceOverwritesWritableTargetWhenParentLacksWritePermission(t *testing.T) {
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent write permission checks")
	}

	parentDir, outputPath := setupReadOnlyProfileParent(t)
	requireParentWriteDenied(t, parentDir, ".profile-write-probe")
	requireWritableTargetReopenable(t, outputPath)

	status, err := persistProfileConfig("thresholds:\n  fail_on_increase_percent: 1\n", outputPath, true)
	if err != nil {
		t.Fatalf("persist forced profile output: %v", err)
	}
	if status != "threshold profile config written to "+outputPath {
		t.Fatalf("unexpected status: %q", status)
	}
	assertProfileOutput(t, outputPath, "thresholds:\n  fail_on_increase_percent: 1\n", 0o600)
}

func TestPersistProfileConfigWritesIntoSearchOnlyParent(t *testing.T) {
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent permission checks")
	}

	parentDir := filepath.Join(t.TempDir(), "dropbox")
	if err := os.Mkdir(parentDir, 0o333); err != nil {
		t.Fatalf("mkdir dropbox: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parentDir, 0o755); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore dropbox permissions: %v", err)
		}
	})
	outputPath := filepath.Join(parentDir, "profile.yaml")
	requireParentReadDenied(t, parentDir)
	requireParentWriteAllowed(t, parentDir, ".profile-write-probe")

	status, err := persistProfileConfig("thresholds:\n  fail_on_increase_percent: 1\n", outputPath, false)
	if err != nil {
		t.Fatalf("persist profile output: %v", err)
	}
	if status != "threshold profile config written to "+outputPath {
		t.Fatalf("unexpected status: %q", status)
	}
	assertProfileOutput(t, outputPath, "thresholds:\n  fail_on_increase_percent: 1\n", 0o600)
	if err := os.Chmod(parentDir, 0o755); err != nil {
		t.Fatalf("restore dropbox permissions before listing: %v", err)
	}
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		t.Fatalf("read dropbox entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "profile.yaml" {
		t.Fatalf("expected temp file to be cleaned up beside target, got %v", entries)
	}
}

func TestPersistProfileConfigForceWritesAbsentOutputIntoSearchOnlyParent(t *testing.T) {
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent permission checks")
	}

	parentDir := filepath.Join(t.TempDir(), "dropbox")
	if err := os.Mkdir(parentDir, 0o333); err != nil {
		t.Fatalf("mkdir dropbox: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parentDir, 0o755); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore dropbox permissions: %v", err)
		}
	})
	outputPath := filepath.Join(parentDir, "profile.yaml")
	requireParentReadDenied(t, parentDir)
	requireParentWriteAllowed(t, parentDir, ".profile-write-probe")

	status, err := persistProfileConfig("thresholds:\n  fail_on_increase_percent: 1\n", outputPath, true)
	if err != nil {
		t.Fatalf("persist forced profile output: %v", err)
	}
	if status != "threshold profile config written to "+outputPath {
		t.Fatalf("unexpected status: %q", status)
	}
	assertProfileOutput(t, outputPath, "thresholds:\n  fail_on_increase_percent: 1\n", 0o600)
}

func TestPersistProfileConfigForceRejectsExistingOutputInSearchOnlyParent(t *testing.T) {
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent permission checks")
	}

	parentDir := filepath.Join(t.TempDir(), "dropbox")
	if err := os.Mkdir(parentDir, 0o733); err != nil {
		t.Fatalf("mkdir dropbox: %v", err)
	}
	outputPath := filepath.Join(parentDir, "profile.yaml")
	if err := os.WriteFile(outputPath, []byte("thresholds:\n  fail_on_increase_percent: 5\n"), 0o600); err != nil {
		t.Fatalf("seed profile output: %v", err)
	}
	if err := os.Chmod(parentDir, 0o333); err != nil {
		t.Fatalf("chmod dropbox search-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parentDir, 0o755); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore dropbox permissions: %v", err)
		}
	})
	requireParentReadDenied(t, parentDir)
	requireParentWriteAllowed(t, parentDir, ".profile-write-probe")
	requireWritableTargetReopenable(t, outputPath)

	status, err := persistProfileConfig("thresholds:\n  fail_on_increase_percent: 1\n", outputPath, true)
	if err == nil || !strings.Contains(err.Error(), "existing target cannot be safely replaced under descriptor fallback") {
		t.Fatalf("expected fail-closed forced profile output error, status=%q err=%v", status, err)
	}
	if status != "" {
		t.Fatalf("expected empty status on failed forced profile output, got %q", status)
	}
	assertProfileOutput(t, outputPath, "thresholds:\n  fail_on_increase_percent: 5\n", 0o600)
}

func TestPersistProfileConfigFallbackUsesPhysicalRelativeOutputPath(t *testing.T) {
	physicalRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(physicalRoot, aliasRoot); err != nil {
		t.Fatalf("create workspace symlink: %v", err)
	}

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
	if err := os.Chdir(aliasRoot); err != nil {
		t.Fatalf("chdir workspace symlink: %v", err)
	}
	t.Setenv("PWD", aliasRoot)

	originalOpenWriteRoot := openCommandOutputWriteRootFn
	originalCanonicalWrite := writeProfileConfigCanonicalIfAbsentFn
	originalCanonicalReplacing := writeProfileConfigCanonicalReplacingFn
	t.Cleanup(func() {
		openCommandOutputWriteRootFn = originalOpenWriteRoot
		writeProfileConfigCanonicalIfAbsentFn = originalCanonicalWrite
		writeProfileConfigCanonicalReplacingFn = originalCanonicalReplacing
	})

	openCommandOutputWriteRootFn = func(string) (*safeio.WriteRoot, error) {
		return nil, os.ErrPermission
	}
	var capturedPaths []string
	capturePath := func(targetPath string, _ []byte, _ os.FileMode) error {
		capturedPaths = append(capturedPaths, targetPath)
		return nil
	}
	writeProfileConfigCanonicalIfAbsentFn = capturePath
	writeProfileConfigCanonicalReplacingFn = capturePath

	outputPath := filepath.Join("dropbox", "profile.yaml")
	for _, force := range []bool{false, true} {
		status, err := persistProfileConfig("thresholds:\n  fail_on_increase_percent: 1\n", outputPath, force)
		if err != nil {
			t.Fatalf("persist profile output through symlinked cwd with force=%v: %v", force, err)
		}
		if status != "threshold profile config written to "+outputPath {
			t.Fatalf("unexpected status with force=%v: %q", force, status)
		}
	}

	resolvedPhysicalRoot, err := filepath.EvalSymlinks(physicalRoot)
	if err != nil {
		t.Fatalf("resolve physical root: %v", err)
	}
	wantPath := filepath.Join(resolvedPhysicalRoot, "dropbox", "profile.yaml")
	if len(capturedPaths) != 2 {
		t.Fatalf("expected normal and forced fallbacks to run, got %v", capturedPaths)
	}
	for _, capturedPath := range capturedPaths {
		if capturedPath != wantPath {
			t.Fatalf("fallback path = %q, want physical path %q", capturedPath, wantPath)
		}
	}
}

func TestPersistProfileConfigForceIsOptInWhenParentLacksWritePermission(t *testing.T) {
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent write permission checks")
	}

	parentDir, outputPath := setupReadOnlyProfileParent(t)
	requireParentWriteDenied(t, parentDir, ".profile-write-probe")
	requireWritableTargetReopenable(t, outputPath)

	root, err := safeio.OpenCanonicalWriteRoot(parentDir)
	if err != nil {
		t.Fatalf("open canonical write root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close canonical write root: %v", closeErr)
		}
	}()

	if err := root.WriteFileCreatingParents(filepath.Base(outputPath), []byte("shared"), 0o600, 0o750); !os.IsPermission(err) {
		t.Fatalf("expected shared writer permission error, got %v", err)
	}
	assertProfileOutput(t, outputPath, "thresholds:\n  fail_on_increase_percent: 5\n", 0o600)

	status, err := persistProfileConfig("thresholds:\n  fail_on_increase_percent: 1\n", outputPath, true)
	if err != nil {
		t.Fatalf("persist forced profile output: %v", err)
	}
	if status != "threshold profile config written to "+outputPath {
		t.Fatalf("unexpected status: %q", status)
	}
	assertProfileOutput(t, outputPath, "thresholds:\n  fail_on_increase_percent: 1\n", 0o600)
}

func setupReadOnlyProfileParent(t *testing.T) (string, string) {
	t.Helper()

	parentDir := filepath.Join(t.TempDir(), "reports")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	outputPath := filepath.Join(parentDir, "profile.yaml")
	if err := os.WriteFile(outputPath, []byte("thresholds:\n  fail_on_increase_percent: 5\n"), 0o600); err != nil {
		t.Fatalf("seed profile output: %v", err)
	}
	if err := os.Chmod(parentDir, 0o555); err != nil {
		t.Fatalf("chmod reports read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parentDir, 0o755); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore reports permissions: %v", err)
		}
	})

	return parentDir, outputPath
}

func requireParentWriteDenied(t *testing.T, parentDir, probeName string) {
	t.Helper()

	probePath := filepath.Join(parentDir, probeName)
	err := os.WriteFile(probePath, []byte("probe"), 0o600)
	switch {
	case err == nil:
		if removeErr := os.Remove(probePath); removeErr != nil {
			t.Fatalf("remove write probe: %v", removeErr)
		}
		t.Skip("effective privileges bypass missing parent write permission")
	case os.IsPermission(err):
		return
	default:
		t.Skipf("parent write permission semantics are not testable: %v", err)
	}
}

func requireParentWriteAllowed(t *testing.T, parentDir, probeName string) {
	t.Helper()

	probePath := filepath.Join(parentDir, probeName)
	if err := os.WriteFile(probePath, []byte("probe"), 0o600); err != nil {
		t.Skipf("parent write permission semantics are not testable: %v", err)
	}
	if err := os.Remove(probePath); err != nil {
		t.Fatalf("remove write probe: %v", err)
	}
}

func requireParentReadDenied(t *testing.T, parentDir string) {
	t.Helper()

	entries, err := os.ReadDir(parentDir)
	switch {
	case err == nil:
		t.Skipf("parent read permission is still available; entries=%d", len(entries))
	case os.IsPermission(err):
		return
	default:
		t.Skipf("parent read permission semantics are not testable: %v", err)
	}
}

func assertProfileOutput(t *testing.T, outputPath, want string, wantPerm os.FileMode) {
	t.Helper()

	if got, err := os.ReadFile(outputPath); err != nil {
		t.Fatalf("read forced profile output: %v", err)
	} else if string(got) != want {
		t.Fatalf("unexpected forced profile output: %q", string(got))
	}
	if info, err := os.Stat(outputPath); err != nil {
		t.Fatalf("stat forced profile output: %v", err)
	} else if info.Mode().Perm() != wantPerm {
		t.Fatalf("expected output mode %#o to be preserved, got %#o", wantPerm, info.Mode().Perm())
	}
}

func requireWritableTargetReopenable(t *testing.T, outputPath string) {
	t.Helper()

	probe, err := os.OpenFile(outputPath, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("existing writable target cannot be reopened without parent write permission: %v", err)
		return
	}
	closeErr := probe.Close()
	if closeErr != nil {
		t.Fatalf("close writable target probe: %v", closeErr)
	}
}
