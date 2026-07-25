//go:build linux || darwin || windows

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type openNoFollowResult struct {
	file File
	err  error
}

func TestOSRootCloseWaitsForOpenNoFollowDescriptorOperation(t *testing.T) {
	rootDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootDir, "trace.ndjson"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}
	rawRoot, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	root := &osRoot{root: rawRoot}

	operationEntered := make(chan struct{})
	allowOperation := make(chan struct{})
	var hookCalls atomic.Int32
	withOpenNoFollowDescriptorOperationHook(t, func() {
		if hookCalls.Add(1) == 1 {
			close(operationEntered)
			<-allowOperation
		}
	})

	openDone := make(chan openNoFollowResult, 1)
	go func() {
		file, openErr := root.OpenNoFollow("trace.ndjson")
		openDone <- openNoFollowResult{file: file, err: openErr}
	}()
	<-operationEntered

	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- root.Close()
	}()
	<-closeStarted

	closedEarly, closeErr := closeResultBeforeDescriptorRelease(closeDone)
	close(allowOperation)
	openResult := <-openDone
	closeOpenNoFollowResult(t, openResult)
	if closedEarly {
		t.Fatalf("Close returned during raw descriptor operation: %v", closeErr)
	}
	if closeErr := <-closeDone; closeErr != nil {
		t.Fatalf("close root: %v", closeErr)
	}

	file, err := root.OpenNoFollow("trace.ndjson")
	if file != nil {
		closeOpenNoFollowResult(t, openNoFollowResult{file: file})
		t.Fatal("expected post-close OpenNoFollow to return no file")
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("expected post-close OpenNoFollow to return os.ErrClosed, got %v", err)
	}
	if got := hookCalls.Load(); got != 1 {
		t.Fatalf("expected closed root to skip raw descriptor operation, hook called %d times", got)
	}
}

func TestOSRootOpenNoFollowRejectsNonLeafNamesBeforeDispatch(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	var hookCalls atomic.Int32
	withOpenNoFollowDescriptorOperationHook(t, func() {
		hookCalls.Add(1)
	})

	for _, tc := range invalidOpenNoFollowNameCases(rootDir) {
		t.Run(tc.name, func(t *testing.T) {
			file, openErr := root.OpenNoFollow(tc.path)
			if file != nil {
				if closeErr := file.Close(); closeErr != nil {
					t.Fatalf("close unexpected file: %v", closeErr)
				}
				t.Fatal("expected invalid name to return no file")
			}
			if !errors.Is(openErr, os.ErrInvalid) {
				t.Fatalf("expected invalid name rejection for %q, got %v", tc.path, openErr)
			}
		})
	}

	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("expected invalid names to be rejected before platform dispatch, hook called %d times", got)
	}
}

type invalidOpenNoFollowNameCase struct {
	name string
	path string
}

func invalidOpenNoFollowNameCases(rootDir string) []invalidOpenNoFollowNameCase {
	separator := string(filepath.Separator)
	return []invalidOpenNoFollowNameCase{
		{name: "empty", path: ""},
		{name: "dot", path: "."},
		{name: "parent", path: ".."},
		{name: "absolute", path: filepath.Join(rootDir, "trace.ndjson")},
		{name: "root separator", path: separator},
		{name: "parent escape", path: ".." + separator + "outside.ndjson"},
		{name: "multi component", path: "child" + separator + "trace.ndjson"},
		{name: "explicit current directory", path: "." + separator + "trace.ndjson"},
		{name: "embedded traversal", path: "child" + separator + ".." + separator + "trace.ndjson"},
	}
}

func closeResultBeforeDescriptorRelease(closeDone <-chan error) (bool, error) {
	select {
	case err := <-closeDone:
		return true, err
	case <-time.After(100 * time.Millisecond):
		return false, nil
	}
}

func closeOpenNoFollowResult(t *testing.T, result openNoFollowResult) {
	t.Helper()

	if result.file != nil {
		if closeErr := result.file.Close(); closeErr != nil {
			t.Fatalf("close opened file: %v", closeErr)
		}
	}
	if result.err != nil {
		t.Fatalf("OpenNoFollow: %v", result.err)
	}
}

func withOpenNoFollowDescriptorOperationHook(t *testing.T, hook func()) {
	t.Helper()

	previousHook := openNoFollowDescriptorOperationHook
	openNoFollowDescriptorOperationHook = hook
	t.Cleanup(func() {
		openNoFollowDescriptorOperationHook = previousHook
	})
}
