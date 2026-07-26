//go:build !windows

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRegularFilePrivateToOwnerUnderLimitReturnsSnapshotPrivacy(t *testing.T) {
	rootDir := t.TempDir()
	privatePath := filepath.Join(rootDir, "private.key")
	if err := os.WriteFile(privatePath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed private auth key: %v", err)
	}
	permissivePath := filepath.Join(rootDir, "permissive.key")
	if err := os.WriteFile(permissivePath, []byte("public"), 0o644); err != nil {
		t.Fatalf("seed permissive auth key: %v", err)
	}

	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open private read root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close private read root: %v", closeErr)
		}
	})

	privateData, privateInfo, private, err := root.ReadRegularFilePrivateToOwnerUnderLimit("private.key", 32)
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
		t.Fatal("expected owner-only file to validate as private")
	}

	permissiveData, _, private, err := root.ReadRegularFilePrivateToOwnerUnderLimit("permissive.key", 32)
	if err != nil {
		t.Fatalf("read permissive auth key: %v", err)
	}
	if string(permissiveData) != "public" {
		t.Fatalf("permissive auth key data = %q, want %q", permissiveData, "public")
	}
	if private {
		t.Fatal("expected permissive file to fail owner-only validation")
	}
}

func TestReadRegularFilePrivateToOwnerUnderLimitRejectsPermissionsRelaxedDuringRead(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "auth.key")
	if err := os.WriteFile(targetPath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("seed private key: %v", err)
	}
	if err := os.Chmod(targetPath, 0o600); err != nil {
		t.Fatalf("set private key mode: %v", err)
	}
	initialInfo, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("inspect private key: %v", err)
	}
	file, err := os.Open(targetPath)
	if err != nil {
		t.Fatalf("open private key: %v", err)
	}

	permissionsRelaxed := false
	hookedFile := &readHookFile{
		File: file,
		beforeFirstRead: func() error {
			if err := file.Chmod(0o644); err != nil {
				return err
			}
			permissionsRelaxed = true
			return nil
		},
	}
	root := &WriteRoot{
		rootAbs: rootDir,
		root: &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return initialInfo, nil },
			open: func(string) (File, error) {
				return hookedFile, nil
			},
		},
	}

	data, info, private, err := root.ReadRegularFilePrivateToOwnerUnderLimit("auth.key", 0)
	if !errors.Is(err, ErrFileChanged) {
		t.Fatalf("error = %v, want identity %v", err, ErrFileChanged)
	}
	if len(data) != 0 || info != nil || private {
		t.Fatalf("unexpected read result data=%q info=%#v private=%v", data, info, private)
	}
	if !permissionsRelaxed {
		t.Fatal("permission-relaxing read hook was not called")
	}
	if hookedFile.statCalls != 2 {
		t.Fatalf("opened descriptor stat calls = %d, want 2", hookedFile.statCalls)
	}
}

func TestRegularFilePrivateToOwnerRejectsGroupReadableMode(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, "auth.key")
	if err := os.WriteFile(targetPath, []byte("key"), 0o640); err != nil {
		t.Fatalf("seed group-readable key: %v", err)
	}
	if err := os.Chmod(targetPath, 0o640); err != nil {
		t.Fatalf("set group-readable key mode: %v", err)
	}
	root, err := OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open key root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close key root: %v", closeErr)
		}
	})
	info, err := root.Lstat("auth.key")
	if err != nil {
		t.Fatalf("inspect group-readable key: %v", err)
	}

	private, err := root.RegularFilePrivateToOwner("auth.key", info)
	if err != nil {
		t.Fatalf("validate group-readable key: %v", err)
	}
	if private {
		t.Fatal("expected group-readable key to fail owner-only validation")
	}
}
