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

func TestDashboardRepoMaterializerRemovesCallerCanceledRefresh(t *testing.T) {
	cacheRoot := t.TempDir()
	spec := mustParseDashboardRepoURL(t, testHTTPSRepoURL)
	checkoutPath := mustDashboardCheckoutPath(t, cacheRoot, spec)
	if err := os.MkdirAll(filepath.Join(checkoutPath, ".git"), 0o750); err != nil {
		t.Fatalf("mkdir checkout git dir: %v", err)
	}

	fetchConstructed := make(chan struct{})
	withFakeDashboardGit(t, func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		if strings.Contains(strings.Join(args, " "), "remote get-url origin") {
			return exec.CommandContext(ctx, fixedTestBinary(t, "printf"), testHTTPSRepoURL), nil
		}
		close(fetchConstructed)
		return exec.CommandContext(ctx, fixedTestBinary(t, "sleep"), "30"), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type materializeResult struct {
		materialized dashboardMaterializedRepo
		err          error
	}
	result := make(chan materializeResult, 1)
	materializer := &dashboardRepoMaterializer{cacheRoot: cacheRoot, gitPath: "/usr/bin/git"}
	go func() {
		materialized, err := materializer.Materialize(ctx, testHTTPSRepoURL, dashboard.RepoRevision{})
		result <- materializeResult{materialized: materialized, err: err}
	}()

	select {
	case <-fetchConstructed:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("wait for refresh fetch command")
	}
	got := <-result
	if got.materialized.CheckoutPath != checkoutPath {
		t.Fatalf("expected checkout path on canceled refresh, got %#v", got.materialized)
	}
	if got.err == nil {
		t.Fatal("expected caller-canceled refresh to fail")
	}
	if _, statErr := os.Stat(checkoutPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected caller-canceled checkout to be removed, stat err=%v", statErr)
	}
}
