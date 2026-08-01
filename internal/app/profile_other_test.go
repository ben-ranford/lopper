//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
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
