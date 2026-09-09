//go:build !regressionproof

package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/dashboard"
)

func TestDashboardRepoMaterializerRemovesDeadlineInterruptedRefresh(t *testing.T) {
	cacheRoot := t.TempDir()
	spec := mustParseDashboardRepoURL(t, testHTTPSRepoURL)
	checkoutPath := mustDashboardCheckoutPath(t, cacheRoot, spec)
	if err := os.MkdirAll(filepath.Join(checkoutPath, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir checkout git dir: %v", err)
	}

	originalTimeout := dashboardRepoMaterializeTimeout
	dashboardRepoMaterializeTimeout = 10 * time.Millisecond
	t.Cleanup(func() { dashboardRepoMaterializeTimeout = originalTimeout })
	withFakeDashboardGit(t, func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		if strings.Contains(strings.Join(args, " "), "remote get-url origin") {
			return exec.CommandContext(ctx, fixedTestBinary(t, "printf"), testHTTPSRepoURL), nil
		}
		return exec.CommandContext(ctx, fixedTestBinary(t, "sleep"), "30"), nil
	})

	materializer := &dashboardRepoMaterializer{cacheRoot: cacheRoot, gitPath: "/usr/bin/git"}
	got, err := materializer.Materialize(context.Background(), testHTTPSRepoURL, dashboard.RepoRevision{})
	if got.CheckoutPath != checkoutPath {
		t.Fatalf("expected checkout path on interrupted refresh, got %#v", got)
	}
	if err == nil {
		t.Fatal("expected deadline-interrupted refresh to fail")
	}
	if _, statErr := os.Stat(checkoutPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected deadline-interrupted checkout to be removed, stat err=%v", statErr)
	}
}
