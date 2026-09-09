//go:build unix && !regressionproof

package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestDashboardRepoMaterializerRunGitBoundsHelperPipeWaitAfterCancellation(t *testing.T) {
	originalExec := execDashboardGitCommandFn
	execDashboardGitCommandFn = func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
		// The background child retains the parent's stderr pipe after the shell
		// is killed by CommandContext, matching a Git transport helper.
		return exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & wait"), nil
	}
	t.Cleanup(func() { execDashboardGitCommandFn = originalExec })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (&dashboardRepoMaterializer{gitPath: "/bin/sh"}).runGit(ctx, "status")
	if err == nil {
		t.Fatal("expected canceled git command to fail")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("runGit returned after %s; helper pipe wait should be bounded", elapsed)
	}
}

func TestDashboardRepoMaterializerRunGitCancelsTransportHelperProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "helper.pid")
	var command *exec.Cmd
	originalExec := execDashboardGitCommandFn
	execDashboardGitCommandFn = func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
		command = exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & helper=$!; printf '%s' \"$helper\" > \"$1\"; wait", "dashboard-helper", marker)
		return command, nil
	}
	t.Cleanup(func() {
		execDashboardGitCommandFn = originalExec
		content, err := os.ReadFile(marker)
		if err == nil {
			if pid, parseErr := strconv.Atoi(string(content)); parseErr == nil {
				if killErr := syscall.Kill(pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
					t.Errorf("clean up helper %d: %v", pid, killErr)
				}
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := (&dashboardRepoMaterializer{gitPath: "/bin/sh"}).runGit(ctx, "status"); err == nil {
		t.Fatal("expected canceled git command to fail")
	}

	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read helper pid: %v", err)
	}
	pid, err := strconv.Atoi(string(content))
	if err != nil {
		t.Fatalf("parse helper pid %q: %v", content, err)
	}
	if command == nil || command.Process == nil {
		t.Fatal("expected git command process")
	}
	terminated, err := testutil.ProcessGroupTerminated(command.Process.Pid)
	if err != nil {
		t.Fatalf("check transport helper process group %d: %v", pid, err)
	}
	if !terminated {
		t.Fatalf("expected transport helper %d to be terminated", pid)
	}
}
