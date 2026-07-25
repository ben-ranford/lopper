//go:build windows

package safeio

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestJoinWindowsNTStatusErrnoPreservesStatusAndPortableIdentity(t *testing.T) {
	tests := []struct {
		name      string
		status    windows.NTStatus
		wantErr   error
		wantErrno syscall.Errno
	}{
		{
			name:      "missing leaf",
			status:    windows.STATUS_OBJECT_NAME_NOT_FOUND,
			wantErr:   os.ErrNotExist,
			wantErrno: windows.ERROR_FILE_NOT_FOUND,
		},
		{
			name:      "missing parent",
			status:    windows.STATUS_OBJECT_PATH_NOT_FOUND,
			wantErr:   os.ErrNotExist,
			wantErrno: windows.ERROR_PATH_NOT_FOUND,
		},
		{
			name:      "access denied",
			status:    windows.STATUS_ACCESS_DENIED,
			wantErr:   os.ErrPermission,
			wantErrno: windows.ERROR_ACCESS_DENIED,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := joinWindowsNTStatusErrno(tc.status)
			if !errors.Is(err, tc.status) {
				t.Fatalf("expected raw NTSTATUS %v to remain recoverable, got %v", tc.status, err)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected mapped identity %v, got %v", tc.wantErr, err)
			}
			var errno syscall.Errno
			if !errors.As(err, &errno) || errno != tc.wantErrno {
				t.Fatalf("expected mapped errno %v, got %v", tc.wantErrno, err)
			}
		})
	}
}

func TestOpenWindowsRootFileNoFollowUsesExactCreateOptions(t *testing.T) {
	originalNtCreateFile := windowsNtCreateFile
	t.Cleanup(func() {
		windowsNtCreateFile = originalNtCreateFile
	})

	var gotOptions uint32
	windowsNtCreateFile = func(
		_ *windows.Handle,
		_ uint32,
		_ *windows.OBJECT_ATTRIBUTES,
		_ *windows.IO_STATUS_BLOCK,
		_ *int64,
		_ uint32,
		_ uint32,
		_ uint32,
		options uint32,
		_ uintptr,
		_ uint32,
	) error {
		gotOptions = options
		return windows.STATUS_OBJECT_NAME_NOT_FOUND
	}

	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	file, err := openWindowsRootFileNoFollow(root, "trace.ndjson")
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
		t.Fatal("expected stubbed NtCreateFile failure to return no file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected mapped not-exist error, got %v", err)
	}

	wantOptions := uint32(
		windows.FILE_SYNCHRONOUS_IO_NONALERT |
			windows.FILE_NON_DIRECTORY_FILE |
			windows.FILE_OPEN_REPARSE_POINT,
	)
	if gotOptions != wantOptions {
		t.Fatalf("unexpected NtCreateFile options: got %#x want %#x", gotOptions, wantOptions)
	}
}
