//go:build unix

package app

import (
	"context"
	"os/exec"
	"testing"
	"time"
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
	if elapsed := time.Since(start); elapsed > dashboardGitCommandWaitDelay+time.Second {
		t.Fatalf("runGit returned after %s; helper pipe wait should be bounded", elapsed)
	}
}
