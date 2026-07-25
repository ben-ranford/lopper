//go:build windows

package safeio

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestWriteFileReplacingWithinRootFallsBackForReplaceExistingRenameError(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)

	targetOpened := 0
	targetClosed := 0
	targetData := []byte("before")
	targetFile := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) {
				targetData = append(targetData, p...)
				return len(p), nil
			},
			close: func() error {
				targetClosed++
				return nil
			},
		},
		truncate: func(size int64) error {
			if size != 0 {
				t.Fatalf("unexpected truncate size: %d", size)
			}
			targetData = targetData[:0]
			return nil
		},
	}
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
				return targetFile, nil
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(oldName, newName string) error {
			return &os.LinkError{
				Op:  "renameat",
				Old: oldName,
				New: newName,
				Err: syscall.ERROR_ALREADY_EXISTS,
			}
		},
		remove: func(string) error { return nil },
	}

	if err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileReplacingWithinRoot returned error: %v", err)
	}
	if targetOpened != 1 {
		t.Fatalf("expected one pinned target open, got %d opens", targetOpened)
	}
	if targetClosed != 1 {
		t.Fatalf("expected pinned target to close once, got %d closes", targetClosed)
	}
	if string(targetData) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(targetData))
	}
}

func TestWindowsReplaceExistingRenameFallbackMatchesOnlyExpectedShape(t *testing.T) {
	const tempName = ".safeio-atomic-temp"
	renameError := func(op, oldName, newName string, err error) error {
		return &os.LinkError{Op: op, Old: oldName, New: newName, Err: err}
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "already exists",
			err:  renameError("renameat", tempName, writeTestFileName, syscall.ERROR_ALREADY_EXISTS),
			want: true,
		},
		{
			name: "file exists",
			err:  renameError("renameat", tempName, writeTestFileName, syscall.ERROR_FILE_EXISTS),
			want: true,
		},
		{
			name: "generic permission",
			err:  renameError("renameat", tempName, writeTestFileName, os.ErrPermission),
		},
		{
			name: "access denied",
			err:  renameError("renameat", tempName, writeTestFileName, syscall.ERROR_ACCESS_DENIED),
		},
		{
			name: "sharing violation",
			err:  renameError("renameat", tempName, writeTestFileName, syscall.Errno(32)),
		},
		{
			name: "privilege not held",
			err:  renameError("renameat", tempName, writeTestFileName, syscall.ERROR_PRIVILEGE_NOT_HELD),
		},
		{
			name: "generic exists",
			err:  renameError("renameat", tempName, writeTestFileName, fs.ErrExist),
		},
		{
			name: "raw already exists errno",
			err:  syscall.ERROR_ALREADY_EXISTS,
		},
		{
			name: "wrong operation",
			err:  renameError("rename", tempName, writeTestFileName, syscall.ERROR_ALREADY_EXISTS),
		},
		{
			name: "wrong source path",
			err:  renameError("renameat", "other-temp", writeTestFileName, syscall.ERROR_ALREADY_EXISTS),
		},
		{
			name: "wrong target path",
			err:  renameError("renameat", tempName, "other-target", syscall.ERROR_ALREADY_EXISTS),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := windowsReplaceExistingRenameFallback(tt.err, tempName, writeTestFileName)
			if got != tt.want {
				t.Fatalf("unexpected fallback decision: got %t want %t", got, tt.want)
			}
		})
	}
}
