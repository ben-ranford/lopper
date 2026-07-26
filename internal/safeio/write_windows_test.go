//go:build windows

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWriteAtomicReplacementSkipsTargetPreOpenOnWindows(t *testing.T) {
	targetOpenErr := errors.New("unexpected target open")
	targetOpenCalls := 0
	root := &fakeRoot{
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				targetOpenCalls++
				return nil, targetOpenErr
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(string, string) error { return nil },
		remove: func(string) error { return nil },
	}

	err := writeAtomicReplacement(root, writeTestFileName, []byte("after"), 0o600)
	if err != nil {
		t.Fatalf("expected atomic replacement to ignore target open error, got %v", err)
	}
	if targetOpenCalls != 0 {
		t.Fatalf("expected zero target opens, got %d", targetOpenCalls)
	}
}

func TestWriteFileReplacingWithinRootReturnsRenameErrorForReplaceExistingRenameError(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	renameErr := &os.LinkError{
		Op:  "renameat",
		Old: atomicTempPrefix + "temp",
		New: writeTestFileName,
		Err: syscall.ERROR_ALREADY_EXISTS,
	}

	targetOpened := 0
	renameCalls := 0
	targetData := []byte("before")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				return nil, os.ErrNotExist
			}
			return info, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				targetOpened++
				return nil, fs.ErrPermission
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(oldName, newName string) error {
			if !strings.HasPrefix(oldName, atomicTempPrefix) || newName != writeTestFileName {
				t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			}
			renameCalls++
			return renameErr
		},
		remove: func(string) error { return nil },
	}

	err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("after"), 0o600)
	requireExactErrorIdentity(t, err, renameErr)
	if targetOpened != 0 {
		t.Fatalf("expected no existing-target pre-open, got %d opens", targetOpened)
	}
	if renameCalls != 1 {
		t.Fatalf("expected one rename attempt, got %d", renameCalls)
	}
	if string(targetData) != "before" {
		t.Fatalf("expected pinned target bytes to remain unchanged, got %q", string(targetData))
	}
}

func TestPublishFileWithinRootReturnsRenameErrorForReplaceExistingRenameError(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	renameErr := &os.LinkError{
		Op:  "renameat",
		Old: atomicTempPrefix + "temp",
		New: writeTestFileName,
		Err: syscall.ERROR_ALREADY_EXISTS,
	}

	targetOpened := 0
	renameCalls := 0
	targetData := []byte("before")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				return nil, os.ErrNotExist
			}
			return info, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				targetOpened++
				return nil, fs.ErrPermission
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(oldName, newName string) error {
			if !strings.HasPrefix(oldName, atomicTempPrefix) || newName != writeTestFileName {
				t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			}
			renameCalls++
			return renameErr
		},
		remove: func(string) error { return nil },
	}

	err := PublishFileWithinRoot(root, writeTestFileName, []byte("after"), 0o600)
	requireExactErrorIdentity(t, err, renameErr)
	if targetOpened != 0 {
		t.Fatalf("expected no existing-target pre-open, got %d opens", targetOpened)
	}
	if renameCalls != 1 {
		t.Fatalf("expected one rename attempt, got %d", renameCalls)
	}
	if string(targetData) != "before" {
		t.Fatalf("expected published target bytes to remain unchanged, got %q", string(targetData))
	}
}

func TestWriteFileReplacingWithinRootPreservesTargetWhenRenameFailsAfterLateConflict(t *testing.T) {
	root, state := newRenameConflictRoot(t)

	err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("after"), 0o600)
	if err != state.renameErr {
		t.Fatalf("expected exact late rename conflict error, got %v", err)
	}
	if state.lstatCalls != 1 {
		t.Fatalf("expected only initial target lstat, got %d", state.lstatCalls)
	}
	if state.targetOpened != 0 || state.targetClosed != 0 {
		t.Fatalf("expected no late target open/close, got opens=%d closes=%d", state.targetOpened, state.targetClosed)
	}
	if state.tempRemoved != 1 {
		t.Fatalf("expected one temp cleanup remove, got %d", state.tempRemoved)
	}
	if string(state.targetData) != "before" {
		t.Fatalf("expected live target bytes to remain unchanged, got %q", string(state.targetData))
	}
}

func TestPublishFileWithinRootPreservesTargetWhenRenameFailsAfterLateConflict(t *testing.T) {
	root, state := newRenameConflictRoot(t)

	err := PublishFileWithinRoot(root, writeTestFileName, []byte("after"), 0o600)
	if err != state.renameErr {
		t.Fatalf("expected exact late rename conflict error, got %v", err)
	}
	if state.lstatCalls != 1 {
		t.Fatalf("expected only initial target lstat, got %d", state.lstatCalls)
	}
	if state.targetOpened != 0 || state.targetClosed != 0 {
		t.Fatalf("expected no late target open/close, got opens=%d closes=%d", state.targetOpened, state.targetClosed)
	}
	if state.tempRemoved != 1 {
		t.Fatalf("expected one temp cleanup remove, got %d", state.tempRemoved)
	}
	if string(state.targetData) != "before" {
		t.Fatalf("expected published target bytes to remain unchanged, got %q", string(state.targetData))
	}
}

type renameConflictRootState struct {
	lstatCalls   int
	targetOpened int
	targetClosed int
	tempRemoved  int
	targetData   []byte
	renameErr    error
}

func newRenameConflictRoot(t *testing.T) (*fakeRoot, *renameConflictRootState) {
	t.Helper()

	state := &renameConflictRootState{
		targetData: []byte("before"),
		renameErr: &os.LinkError{
			Op:  "renameat",
			Old: atomicTempPrefix + "temp",
			New: writeTestFileName,
			Err: syscall.ERROR_FILE_EXISTS,
		},
	}
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			state.lstatCalls++
			return nil, os.ErrNotExist
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				state.targetOpened++
				return &fakeFile{
					write: func(p []byte) (int, error) {
						state.targetData = append(state.targetData, p...)
						return len(p), nil
					},
					close: func() error {
						state.targetClosed++
						return nil
					},
				}, nil
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(oldName, newName string) error {
			if !strings.HasPrefix(oldName, atomicTempPrefix) || newName != writeTestFileName {
				t.Fatalf("unexpected rename %q -> %q", oldName, newName)
			}
			return state.renameErr
		},
		remove: func(name string) error {
			if !strings.HasPrefix(name, atomicTempPrefix) {
				t.Fatalf("unexpected cleanup path %q", name)
			}
			state.tempRemoved++
			return nil
		},
	}
	return root, state
}
