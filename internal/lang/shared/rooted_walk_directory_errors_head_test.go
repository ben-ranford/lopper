package shared

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestRootedWalkSkipsOptedInDescendantDirectoryPermissionError(t *testing.T) {
	var callbackPath string
	var callbackErr error
	walker := rootedRepoWalker{
		budget: RootedWalkBudget{
			OnSkippedDescendantDirectoryError: func(path string, err error) {
				callbackPath = path
				callbackErr = err
			},
		},
		state: rootedWalkBudgetState{traversalEntriesQueued: 5},
	}
	if !walker.skipDescendantDirectoryError("blocked", "/repo/blocked", fs.ErrPermission) {
		t.Fatal("expected opted-in descendant permission error to be skipped")
	}
	walker.consumeQueuedTraversalEntries(2)
	if callbackPath != "/repo/blocked" || !errors.Is(callbackErr, fs.ErrPermission) {
		t.Fatalf("unexpected callback path=%q error=%v", callbackPath, callbackErr)
	}
	if walker.state.traversalEntriesSeen != 2 || walker.state.traversalEntriesQueued != 3 {
		t.Fatalf("unexpected preserved traversal accounting: %+v", walker.state)
	}
}

func TestRootedWalkReadDirSkipsOptedInDescendantPermissionError(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	walker := rootedRepoWalker{budget: RootedWalkBudget{OnSkippedDescendantDirectoryError: func(path string, err error) {
		called = path == "/repo/blocked" && errors.Is(err, fs.ErrPermission)
	}}}
	root := &sharedWalkTestRoot{info: info, open: func(string) (safeio.File, error) { return nil, fs.ErrPermission }}
	entries, err := walker.readDir(context.Background(), root, "blocked", "/repo/blocked")
	if err != nil || len(entries) != 0 || !called {
		t.Fatalf("entries=%v error=%v callback=%t", entries, err, called)
	}
}

func TestRootedWalkReadDirPropagatesCancellationDuringSkippedPermissionError(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	walker := rootedRepoWalker{budget: RootedWalkBudget{OnSkippedDescendantDirectoryError: func(string, error) {}}}
	root := &sharedWalkTestRoot{info: info, open: func(string) (safeio.File, error) {
		cancel()
		return nil, fs.ErrPermission
	}}
	_, err = walker.readDir(ctx, root, "blocked", "/repo/blocked")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled directory scan, got %v", err)
	}
}

func TestRootedWalkDoesNotSkipRootOrOrdinaryDirectoryErrors(t *testing.T) {
	walker := rootedRepoWalker{budget: RootedWalkBudget{OnSkippedDescendantDirectoryError: func(string, error) { t.Fatal("unexpected callback") }}}
	if walker.skipDescendantDirectoryError(".", "/repo", fs.ErrPermission) {
		t.Fatal("root permission error must remain fatal")
	}
	if walker.skipDescendantDirectoryError("child", "/repo/child", errors.New("read failed")) {
		t.Fatal("ordinary descendant error must remain fatal")
	}
	if walker.skipDescendantDirectoryError("child", "/repo/child", fs.ErrNotExist) {
		t.Fatal("not-found descendant error must remain fatal")
	}
	if walker.skipDescendantDirectoryError("child", "/repo/child", errors.Join(fs.ErrPermission, errors.New("close failed"))) {
		t.Fatal("joined permission error must remain fatal")
	}
}
