//go:build !windows

package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRegularPathRejectsUnixSocket(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "nc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove private socket fixture: %v", err)
		}
	})
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "control.sock")
	if len(socket) >= 104 {
		t.Fatalf("private socket fixture path exceeds Darwin sun_path: %d bytes", len(socket))
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil {
			t.Errorf("close Unix socket fixture: %v", err)
		}
	})
	if err := regularPath(socket, root, false); err == nil {
		t.Fatal("Unix socket accepted as a regular authenticated file")
	}
}

func TestCheckedFileRejectsUnreadableRegularFile(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "artifact")
	data := []byte("private artifact")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0600); err != nil {
			t.Errorf("restore artifact permissions: %v", err)
		}
	})
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}

	got, err := checkedFile(path, digest(data), root, int64(len(data)))
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("checkedFile error = %v, want rooted open EACCES", err)
	}
	if len(got) != 0 {
		t.Fatalf("checkedFile returned unreadable artifact data: %q", got)
	}
}
