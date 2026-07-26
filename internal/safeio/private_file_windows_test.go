//go:build windows

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

var windowsSetKernelObjectSecurityForTest = windowsAdvapi32.NewProc("SetKernelObjectSecurity")

func TestRegularFilePrivateToOwnerRejectsEveryoneDACL(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "auth.key")
	if err := os.WriteFile(targetPath, []byte("key"), 0o600); err != nil {
		t.Fatalf("seed auth key: %v", err)
	}
	file, err := os.OpenFile(targetPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open auth key for DACL update: %v", err)
	}
	if err := setWindowsFileSecurity(file, "D:P(A;;FA;;;WD)"); err != nil {
		_ = file.Close()
		t.Fatalf("grant Everyone access to auth key: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close auth key after DACL update: %v", err)
	}

	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open auth-key root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close auth-key root: %v", closeErr)
		}
	})
	info, err := root.Lstat("auth.key")
	if err != nil {
		t.Fatalf("inspect auth key: %v", err)
	}
	private, err := root.RegularFilePrivateToOwner("auth.key", info)
	if err != nil {
		t.Fatalf("validate auth-key DACL: %v", err)
	}
	if private {
		t.Fatal("expected Everyone DACL to fail owner-only validation")
	}
}

func setWindowsFileSecurity(file File, sddl string) (returnErr error) {
	handle, err := windowsFileHandle(file)
	if err != nil {
		return err
	}
	descriptor, err := windowsSecurityDescriptorFromSDDL(sddl)
	if err != nil {
		return err
	}
	defer func() {
		_, freeErr := syscall.LocalFree(syscall.Handle(uintptr(descriptor)))
		returnErr = errors.Join(returnErr, freeErr)
	}()
	result, _, callErr := windowsSetKernelObjectSecurityForTest.Call(
		uintptr(handle),
		windowsDACLInformation,
		uintptr(unsafe.Pointer(descriptor)),
	)
	if result == 0 {
		return windowsLastError("set file security descriptor", callErr)
	}
	return nil
}
