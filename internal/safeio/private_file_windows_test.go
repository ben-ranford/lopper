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

func TestReadRegularFilePrivateToOwnerUnderLimitReturnsSnapshotPrivacy(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open private read root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close private read root: %v", closeErr)
		}
	})

	privateName := writePrivateWindowsTestFile(t, root, []byte("secret"))
	privateData, privateInfo, private, err := root.ReadRegularFilePrivateToOwnerUnderLimit(privateName, 32)
	if err != nil {
		t.Fatalf("read private auth key: %v", err)
	}
	if string(privateData) != "secret" {
		t.Fatalf("private auth key data = %q, want %q", privateData, "secret")
	}
	if privateInfo == nil || !privateInfo.Mode().IsRegular() {
		t.Fatalf("expected regular file info, got %#v", privateInfo)
	}
	if !private {
		t.Fatal("expected owner-only DACL to validate as private")
	}

	permissiveName := "permissive.key"
	permissivePath := filepath.Join(rootDir, permissiveName)
	if err := os.WriteFile(permissivePath, []byte("public"), 0o600); err != nil {
		t.Fatalf("seed permissive auth key: %v", err)
	}
	permissiveFile, err := os.OpenFile(permissivePath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open permissive auth key: %v", err)
	}
	if err := setWindowsFileSecurity(permissiveFile, "D:P(A;;FA;;;WD)"); err != nil {
		_ = permissiveFile.Close()
		t.Fatalf("grant Everyone access to auth key: %v", err)
	}
	if err := permissiveFile.Close(); err != nil {
		t.Fatalf("close permissive auth key: %v", err)
	}

	permissiveData, _, private, err := root.ReadRegularFilePrivateToOwnerUnderLimit(permissiveName, 32)
	if err != nil {
		t.Fatalf("read permissive auth key: %v", err)
	}
	if string(permissiveData) != "public" {
		t.Fatalf("permissive auth key data = %q, want %q", permissiveData, "public")
	}
	if private {
		t.Fatal("expected Everyone DACL to fail owner-only validation")
	}
}

func TestReadRegularFilePrivateToOwnerUnderLimitRejectsPermissiveToPrivateDACLChange(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open private read root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close private read root: %v", closeErr)
		}
	})
	keyName := writePrivateWindowsTestFile(t, root, []byte("secret"))
	keyPath := filepath.Join(rootDir, keyName)
	file, err := os.OpenFile(keyPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open private key for DACL update: %v", err)
	}
	if err := setWindowsFileSecurity(file, "D:P(A;;FA;;;WD)"); err != nil {
		_ = file.Close()
		t.Fatalf("grant Everyone access to private key: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close private key after DACL update: %v", err)
	}

	var hookedFile *readHookFile
	readRoot := &WriteRoot{
		rootAbs: rootDir,
		root: &fakeRoot{
			Root: root.root,
			open: func(name string) (File, error) {
				opened, err := root.root.Open(name)
				if err != nil {
					return nil, err
				}
				osFile, ok := opened.(*os.File)
				if !ok {
					_ = opened.Close()
					t.Fatalf("opened private key type = %T, want *os.File", opened)
				}
				hookedFile = &readHookFile{File: osFile}
				hookedFile.beforeFirstRead = func() error {
					return setWindowsFileOwnerOnlySecurity(hookedFile)
				}
				return hookedFile, nil
			},
		},
	}

	data, info, private, err := readRoot.ReadRegularFilePrivateToOwnerUnderLimit(keyName, 32)
	if !errors.Is(err, ErrFileChanged) {
		t.Fatalf("error = %v, want identity %v", err, ErrFileChanged)
	}
	if len(data) != 0 || info != nil || private {
		t.Fatalf("unexpected auth-key snapshot data=%q info=%#v private=%v", data, info, private)
	}
	if hookedFile == nil || hookedFile.beforeFirstRead != nil {
		t.Fatal("DACL-tightening read hook was not called")
	}
	if hookedFile.statCalls != 3 {
		t.Fatalf("opened descriptor stat calls = %d, want 3", hookedFile.statCalls)
	}
}

func TestReadRegularFilePrivateToOwnerUnderLimitRejectsPrivateToPermissiveDACLChange(t *testing.T) {
	rootDir := t.TempDir()
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open private read root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close private read root: %v", closeErr)
		}
	})
	keyName := writePrivateWindowsTestFile(t, root, []byte("secret"))

	var hookedFile *readHookFile
	readRoot := &WriteRoot{
		rootAbs: rootDir,
		root: &fakeRoot{
			Root: root.root,
			open: func(name string) (File, error) {
				opened, err := root.root.Open(name)
				if err != nil {
					return nil, err
				}
				osFile, ok := opened.(*os.File)
				if !ok {
					_ = opened.Close()
					t.Fatalf("opened private key type = %T, want *os.File", opened)
				}
				hookedFile = &readHookFile{File: osFile}
				hookedFile.beforeFirstRead = func() error {
					return setWindowsFileSecurity(hookedFile, "D:P(A;;FA;;;WD)")
				}
				return hookedFile, nil
			},
		},
	}

	data, info, private, err := readRoot.ReadRegularFilePrivateToOwnerUnderLimit(keyName, 32)
	if !errors.Is(err, ErrFileChanged) {
		t.Fatalf("error = %v, want identity %v", err, ErrFileChanged)
	}
	if len(data) != 0 || info != nil || private {
		t.Fatalf("unexpected auth-key snapshot data=%q info=%#v private=%v", data, info, private)
	}
	if hookedFile == nil || hookedFile.beforeFirstRead != nil {
		t.Fatal("DACL-relaxing read hook was not called")
	}
	if hookedFile.statCalls != 3 {
		t.Fatalf("opened descriptor stat calls = %d, want 3", hookedFile.statCalls)
	}
}

func writePrivateWindowsTestFile(t *testing.T, root *WriteRoot, data []byte) string {
	t.Helper()
	name, file, err := root.CreatePrivateTempFile()
	if err != nil {
		t.Fatalf("create private test file: %v", err)
	}
	n, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("write private test file: %v", errors.Join(writeErr, closeErr))
	}
	if n != len(data) {
		t.Fatalf("private test file bytes written = %d, want %d", n, len(data))
	}
	return name
}

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

func setWindowsFileOwnerOnlySecurity(file File) error {
	descriptor, err := windowsOwnerOnlySecurityDescriptor()
	if err != nil {
		return err
	}
	defer func() {
		_, _ = syscall.LocalFree(syscall.Handle(uintptr(descriptor)))
	}()
	return setWindowsFileSecurityDescriptor(file, descriptor)
}

func setWindowsFileSecurityDescriptor(file File, descriptor unsafe.Pointer) error {
	handle, err := windowsFileHandle(file)
	if err != nil {
		return err
	}
	result, _, callErr := windowsSetKernelObjectSecurityForTest.Call(
		uintptr(handle),
		windowsDACLInformation,
		uintptr(descriptor),
	)
	if result == 0 {
		return windowsLastError("set file security descriptor", callErr)
	}
	return nil
}
