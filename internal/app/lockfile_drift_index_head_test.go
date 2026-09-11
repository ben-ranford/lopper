//go:build !regressionproof

package app

import (
	"context"
	"errors"
	"testing"
)

func TestDotnetProjectLockfileIndexCancelsRepositoryWalk(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	index, err := newDotnetProjectLockfileIndex(ctx, t.TempDir(), []lockfileRule{
		mustLockfileRule(t, ".NET", dotnetCentralManifest),
	}, false)
	if err != nil {
		t.Fatalf("new .NET lockfile index: %v", err)
	}

	original := findDotnetProjectLockfilesFn
	started := make(chan struct{})
	findDotnetProjectLockfilesFn = func(got context.Context, _ string) ([]presentLockfile, error) {
		if got != ctx {
			t.Fatal("expected repository index to receive scan context")
		}
		close(started)
		<-got.Done()
		return nil, got.Err()
	}
	t.Cleanup(func() { findDotnetProjectLockfilesFn = original })

	go func() {
		<-started
		cancel()
	}()
	_, err = index.lockfilesUnder(".")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled repository index walk, got %v", err)
	}
}
