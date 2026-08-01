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

	workspace := t.TempDir()
	parentDir := filepath.Join(workspace, "reports")
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

	probePath := filepath.Join(parentDir, ".profile-write-probe")
	if err := os.WriteFile(probePath, []byte("probe"), 0o600); err == nil {
		if removeErr := os.Remove(probePath); removeErr != nil {
			t.Fatalf("remove write probe: %v", removeErr)
		}
		t.Skip("effective privileges bypass missing parent write permission")
	} else if !os.IsPermission(err) {
		t.Skipf("parent write permission semantics are not testable: %v", err)
	}

	probe, err := os.OpenFile(outputPath, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("existing writable target cannot be reopened without parent write permission: %v", err)
	}
	if err := probe.Close(); err != nil {
		t.Fatalf("close writable target probe: %v", err)
	}

	status, err := persistProfileConfig("thresholds:\n  fail_on_increase_percent: 1\n", outputPath, true)
	if err != nil {
		t.Fatalf("persist forced profile output: %v", err)
	}
	if status != "threshold profile config written to "+outputPath {
		t.Fatalf("unexpected status: %q", status)
	}
	if got, err := os.ReadFile(outputPath); err != nil {
		t.Fatalf("read forced profile output: %v", err)
	} else if string(got) != "thresholds:\n  fail_on_increase_percent: 1\n" {
		t.Fatalf("unexpected forced profile output: %q", string(got))
	}
	if info, err := os.Stat(outputPath); err != nil {
		t.Fatalf("stat forced profile output: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected output mode 0600 to be preserved, got %#o", info.Mode().Perm())
	}
}
