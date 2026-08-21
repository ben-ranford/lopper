//go:build darwin || linux

package safeio

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathPublishesTarget(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "reports")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	target := filepath.Join(parent, "profile.yaml")

	if err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("thresholds: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomicallyIfAbsentUnderCanonicalPath returned error: %v", err)
	}
	assertFileContent(t, target, "thresholds: {}\n")
	if entries, err := os.ReadDir(parent); err != nil {
		t.Fatalf("read reports: %v", err)
	} else if len(entries) != 1 || entries[0].Name() != "profile.yaml" {
		t.Fatalf("expected only target to remain, got %v", entries)
	}
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathRejectsExistingTarget(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, writeTestFileName)
	if err := os.WriteFile(target, []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target error, got %v", err)
	}
	assertFileContent(t, target, "before")
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathRejectsParentSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workspace, "reports")); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}

	target := filepath.Join(workspace, "reports", writeTestFileName)
	err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "directory contains symlink") {
		t.Fatalf("expected parent symlink rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, writeTestFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected outside target to remain absent, got err=%v", statErr)
	}
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathRejectsDanglingTargetSymlink(t *testing.T) {
	parent := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(parent, writeTestFileName)
	outsideTarget := filepath.Join(outside, "missing", writeTestFileName)
	if err := os.Symlink(outsideTarget, target); err != nil {
		t.Fatalf("create dangling target symlink: %v", err)
	}

	err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected dangling symlink target to be treated as existing, got %v", err)
	}
	if _, statErr := os.Stat(outsideTarget); !os.IsNotExist(statErr) {
		t.Fatalf("expected symlink target to remain absent, got err=%v", statErr)
	}
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathRejectsInvalidTargets(t *testing.T) {
	t.Run("relative target", func(t *testing.T) {
		err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(filepath.Join("reports", writeTestFileName), []byte("after"), 0o600)
		if err == nil {
			t.Fatal("expected relative target under missing parent to fail")
		}
	})

	t.Run("missing parent", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "missing", writeTestFileName)
		err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected missing parent error, got %v", err)
		}
	})

	t.Run("parent file", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
			t.Fatalf("seed parent file: %v", err)
		}
		err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(filepath.Join(parent, writeTestFileName), []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "directory is not a directory") {
			t.Fatalf("expected parent file rejection, got %v", err)
		}
	})

	t.Run("unsearchable parent", func(t *testing.T) {
		if syscall.Geteuid() == 0 {
			t.Skip("effective privileges bypass parent permission checks")
		}
		parent := filepath.Join(t.TempDir(), "unsearchable")
		if err := os.Mkdir(parent, 0o666); err != nil {
			t.Fatalf("mkdir unsearchable parent: %v", err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(parent, 0o755); err != nil && !os.IsNotExist(err) {
				t.Errorf("restore unsearchable parent permissions: %v", err)
			}
		})
		err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(filepath.Join(parent, writeTestFileName), []byte("after"), 0o600)
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("expected unsearchable parent permission error, got %v", err)
		}
	})

	t.Run("invalid directory path", func(t *testing.T) {
		err := verifyCanonicalExistingDirectory(string([]byte{'b', 'a', 'd', 0}))
		if err == nil {
			t.Fatal("expected invalid directory path to fail")
		}
	})
}

func TestWriteFileAtomicallyIfAbsentUnderCanonicalPathWritesSearchOnlyParent(t *testing.T) {
	if syscall.Geteuid() == 0 {
		t.Skip("effective privileges bypass parent permission checks")
	}

	parent := filepath.Join(t.TempDir(), "dropbox")
	if err := os.Mkdir(parent, 0o333); err != nil {
		t.Fatalf("mkdir dropbox: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o755); err != nil && !os.IsNotExist(err) {
			t.Errorf("restore dropbox permissions: %v", err)
		}
	})
	if _, err := os.ReadDir(parent); !os.IsPermission(err) {
		t.Skipf("parent read permission semantics are not testable: %v", err)
	}
	target := filepath.Join(parent, writeTestFileName)

	if err := WriteFileAtomicallyIfAbsentUnderCanonicalPath(target, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomicallyIfAbsentUnderCanonicalPath returned error: %v", err)
	}
	assertFileContent(t, target, "after")
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("restore dropbox permissions before listing: %v", err)
	}
	if entries, err := os.ReadDir(parent); err != nil {
		t.Fatalf("read dropbox: %v", err)
	} else if len(entries) != 1 || entries[0].Name() != writeTestFileName {
		t.Fatalf("expected only target to remain, got %v", entries)
	}
}

func TestDescriptorPathHelpersRejectInvalidDescriptorAndTempNames(t *testing.T) {
	err := rejectExistingDescriptorPath(-1, writeTestFileName)
	if err == nil {
		t.Fatal("expected invalid descriptor error")
	}
	if file, fd, err := openSearchOnlyCanonicalDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected missing directory descriptor: %v", closeErr)
		}
		t.Fatalf("expected missing directory open to fail, got fd=%d", fd)
	}
	notDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("file"), 0o600); err != nil {
		t.Fatalf("seed non-directory: %v", err)
	}
	if file, fd, err := openSearchOnlyCanonicalDirectory(notDirectory); err == nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected non-directory descriptor: %v", closeErr)
		}
		t.Fatalf("expected non-directory open to fail, got fd=%d", fd)
	}

	parent := t.TempDir()
	parentFile, parentFD, err := openSearchOnlyCanonicalDirectory(parent)
	if err != nil {
		t.Fatalf("open parent descriptor: %v", err)
	}
	defer func() {
		if err := parentFile.Close(); err != nil {
			t.Errorf("close parent descriptor: %v", err)
		}
	}()

	originalRandomTempNameFn := randomTempNameFn
	t.Cleanup(func() {
		randomTempNameFn = originalRandomTempNameFn
	})
	randomTempNameFn = func() (string, error) { return "", errors.New("boom") }
	if name, file, err := createDescriptorTempFile(parentFD, 0o600); err == nil {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Fatalf("close unexpected random-name temp file: %v", closeErr)
			}
		}
		t.Fatalf("expected random name error, got name=%q", name)
	}

	collidingName := ".safeio-collision"
	if err := os.WriteFile(filepath.Join(parent, collidingName), []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed colliding temp file: %v", err)
	}
	randomTempNameFn = func() (string, error) { return collidingName, nil }
	if name, file, err := createDescriptorTempFile(parentFD, 0o600); err == nil || !strings.Contains(err.Error(), "too many collisions") {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Fatalf("close unexpected colliding temp file: %v", closeErr)
			}
		}
		t.Fatalf("expected collision exhaustion, got name=%q err=%v", name, err)
	}
	if name, file, err := createDescriptorTempFile(-1, 0o600); err == nil {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Fatalf("close unexpected invalid-descriptor temp file: %v", closeErr)
			}
		}
		t.Fatalf("expected invalid descriptor temp creation to fail, got name=%q", name)
	}
}

func TestDescriptorPathWriteRejectsExistingTargetAndCleansTemp(t *testing.T) {
	parent := t.TempDir()
	parentFile, parentFD, err := openSearchOnlyCanonicalDirectory(parent)
	if err != nil {
		t.Fatalf("open parent descriptor: %v", err)
	}
	defer func() {
		if err := parentFile.Close(); err != nil {
			t.Errorf("close parent descriptor: %v", err)
		}
	}()

	if err := os.WriteFile(filepath.Join(parent, writeTestFileName), []byte("before"), 0o600); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	err = writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, writeTestFileName, []byte("after"), 0o600)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target error, got %v", err)
	}
	assertFileContent(t, filepath.Join(parent, writeTestFileName), "before")
	if entries, readErr := os.ReadDir(parent); readErr != nil {
		t.Fatalf("read parent: %v", readErr)
	} else if len(entries) != 1 || entries[0].Name() != writeTestFileName {
		t.Fatalf("expected failed existing-target publish to clean temp file, got %v", entries)
	}
}

func TestDescriptorPathWriteRejectsInvalidDescriptorBeforeTemp(t *testing.T) {
	err := writeFileAtomicallyIfAbsentUnderDescriptorPath(-1, writeTestFileName, []byte("after"), 0o600)
	if err == nil {
		t.Fatal("expected invalid descriptor write to fail")
	}
}

func TestDescriptorPathWriteRejectsChangedTargetValidation(t *testing.T) {
	parent := t.TempDir()
	parentFile, parentFD, err := openSearchOnlyCanonicalDirectory(parent)
	if err != nil {
		t.Fatalf("open parent descriptor: %v", err)
	}
	defer func() {
		if err := parentFile.Close(); err != nil {
			t.Errorf("close parent descriptor: %v", err)
		}
	}()

	originalDescriptorLstatFn := descriptorLstatFn
	t.Cleanup(func() {
		descriptorLstatFn = originalDescriptorLstatFn
	})

	t.Run("missing after publish", func(t *testing.T) {
		descriptorLstatFn = func(int, string) (descriptorFileInfo, error) {
			return descriptorFileInfo{}, os.ErrNotExist
		}
		err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, "missing-after-publish.txt", []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "changed before validation") {
			t.Fatalf("expected changed target validation error, got %v", err)
		}
	})

	t.Run("symlink after publish", func(t *testing.T) {
		descriptorLstatFn = func(int, string) (descriptorFileInfo, error) {
			return descriptorFileInfo{mode: unix.S_IFLNK}, nil
		}
		err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, "symlink-after-publish.txt", []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "became a symlink") {
			t.Fatalf("expected symlink validation error, got %v", err)
		}
	})

	t.Run("different regular file after publish", func(t *testing.T) {
		descriptorLstatFn = func(int, string) (descriptorFileInfo, error) {
			return descriptorFileInfo{mode: unix.S_IFREG, dev: "different", ino: "different"}, nil
		}
		err := writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, "different-after-publish.txt", []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "changed before validation") {
			t.Fatalf("expected different file validation error, got %v", err)
		}
	})
}

func TestDescriptorPathWriteCleansTempOnPublishFailure(t *testing.T) {
	parent := t.TempDir()
	parentFile, parentFD, err := openSearchOnlyCanonicalDirectory(parent)
	if err != nil {
		t.Fatalf("open parent descriptor: %v", err)
	}
	defer func() {
		if err := parentFile.Close(); err != nil {
			t.Errorf("close parent descriptor: %v", err)
		}
	}()

	if err := os.Mkdir(filepath.Join(parent, "missing-dir-target"), 0o755); err != nil {
		t.Fatalf("seed directory target: %v", err)
	}
	err = writeFileAtomicallyIfAbsentUnderDescriptorPath(parentFD, "", []byte("after"), 0o600)
	if err == nil {
		t.Fatal("expected publish failure for empty descriptor-relative target")
	}
	if entries, readErr := os.ReadDir(parent); readErr != nil {
		t.Fatalf("read parent: %v", readErr)
	} else if len(entries) != 1 || entries[0].Name() != "missing-dir-target" {
		t.Fatalf("expected failed publish to clean temp file, got %v", entries)
	}
}

func TestCleanupDescriptorTempFileClosesAndRemovesTemp(t *testing.T) {
	parent := t.TempDir()
	parentFile, parentFD, err := openSearchOnlyCanonicalDirectory(parent)
	if err != nil {
		t.Fatalf("open parent descriptor: %v", err)
	}
	defer func() {
		if err := parentFile.Close(); err != nil {
			t.Errorf("close parent descriptor: %v", err)
		}
	}()

	tempName, tempFile, err := createDescriptorTempFile(parentFD, 0o600)
	if err != nil {
		t.Fatalf("create descriptor temp: %v", err)
	}
	if err := cleanupDescriptorTempFile(parentFD, tempName, tempFile); err != nil {
		t.Fatalf("cleanup descriptor temp: %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(parent, tempName)); !os.IsNotExist(statErr) {
		t.Fatalf("expected temp file removal, got %v", statErr)
	}
	if err := cleanupDescriptorTempFile(parentFD, tempName, nil); err != nil {
		t.Fatalf("cleanup missing descriptor temp should be ignored: %v", err)
	}

	closedTempName, closedTempFile, err := createDescriptorTempFile(parentFD, 0o600)
	if err != nil {
		t.Fatalf("create closed descriptor temp: %v", err)
	}
	if err := closedTempFile.Close(); err != nil {
		t.Fatalf("close descriptor temp before cleanup: %v", err)
	}
	if err := cleanupDescriptorTempFile(parentFD, closedTempName, closedTempFile); err != nil {
		t.Fatalf("cleanup already-closed descriptor temp: %v", err)
	}

	if err := cleanupDescriptorTempFile(-1, "still-present", nil); err == nil {
		t.Fatal("expected invalid descriptor cleanup error")
	}
	invalidFile := os.NewFile(^uintptr(0), "invalid-descriptor")
	if err := cleanupDescriptorTempFile(-1, "still-present", invalidFile); err == nil {
		t.Fatal("expected joined invalid close and unlink cleanup error")
	}
}

func TestAtomicIfAbsentErrorBranchesForCoverage(t *testing.T) {
	tempInfo := newPinnedTargetInfo(t, "temp")

	t.Run("temp create failure", func(t *testing.T) {
		err := writeFileAtomicallyIfAbsentAtRoot(&fakeRoot{
			openFile: func(string, int, os.FileMode) (File, error) {
				return nil, errors.New("create temp failed")
			},
		}, writeTestFileName, []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "create temp failed") {
			t.Fatalf("expected create temp failure, got %v", err)
		}
	})

	t.Run("temp write failure", func(t *testing.T) {
		err := writeFileAtomicallyIfAbsentAtRoot(&fakeRoot{
			openFile: func(string, int, os.FileMode) (File, error) {
				return &fakeFile{
					write: func([]byte) (int, error) { return 0, errors.New("write temp failed") },
					close: func() error { return nil },
				}, nil
			},
			remove: func(string) error { return nil },
		}, writeTestFileName, []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "write temp failed") {
			t.Fatalf("expected write temp failure, got %v", err)
		}
	})

	t.Run("temp stat failure", func(t *testing.T) {
		err := writeFileAtomicallyIfAbsentAtRoot(&fakeRoot{
			openFile: func(string, int, os.FileMode) (File, error) {
				return &fakeFile{
					write: func(p []byte) (int, error) { return len(p), nil },
					chmod: func(os.FileMode) error { return nil },
					stat:  func() (fs.FileInfo, error) { return nil, errors.New("stat temp failed") },
					close: func() error { return nil },
				}, nil
			},
			remove: func(string) error { return nil },
		}, writeTestFileName, []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "stat temp failed") {
			t.Fatalf("expected stat temp failure, got %v", err)
		}
	})

	t.Run("remove temp failure after link", func(t *testing.T) {
		err := writeFileAtomicallyIfAbsentAtRoot(&fakeRoot{
			openFile: func(string, int, os.FileMode) (File, error) {
				return &fakeFile{
					write: func(p []byte) (int, error) { return len(p), nil },
					chmod: func(os.FileMode) error { return nil },
					stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
					close: func() error { return nil },
				}, nil
			},
			link:   func(string, string) error { return nil },
			remove: func(string) error { return errors.New("remove temp failed") },
		}, writeTestFileName, []byte("after"), 0o600)
		if err == nil || !strings.Contains(err.Error(), "remove temp failed") {
			t.Fatalf("expected remove temp failure, got %v", err)
		}
	})

	t.Run("pinned replacement temp write failure", func(t *testing.T) {
		err := writeAtomicReplacementWithPinnedTarget(&fakeRoot{
			openFile: func(string, int, os.FileMode) (File, error) {
				return &fakeFile{
					write: func([]byte) (int, error) { return 0, errors.New("pinned write failed") },
					close: func() error { return nil },
				}, nil
			},
			remove: func(string) error { return nil },
		}, writeTestFileName, []byte("after"), 0o600, nil, false)
		if err == nil || !strings.Contains(err.Error(), "pinned write failed") {
			t.Fatalf("expected pinned write failure, got %v", err)
		}
	})

	t.Run("pinned replacement commit failure", func(t *testing.T) {
		err := writeAtomicReplacementWithPinnedTarget(&fakeRoot{
			openFile: func(string, int, os.FileMode) (File, error) {
				return &fakeFile{
					write: func(p []byte) (int, error) { return len(p), nil },
					chmod: func(os.FileMode) error { return nil },
					stat:  func() (fs.FileInfo, error) { return tempInfo, nil },
					close: func() error { return nil },
				}, nil
			},
			rename: func(string, string) error { return errors.New("pinned rename failed") },
			remove: func(string) error { return nil },
		}, writeTestFileName, []byte("after"), 0o600, nil, false)
		if err == nil || !strings.Contains(err.Error(), "pinned rename failed") {
			t.Fatalf("expected pinned rename failure, got %v", err)
		}
	})
}

func TestSafeIOHelperCoverageBranches(t *testing.T) {
	cleanName, parts := splitPinnedPath(string(os.PathSeparator) + filepath.Join("absolute", writeTestFileName))
	if cleanName == "." || len(parts) != 2 || parts[0] != "absolute" || parts[1] != writeTestFileName {
		t.Fatalf("unexpected split pinned path: clean=%q parts=%v", cleanName, parts)
	}

	if !arePureSentinelCauses([]error{nil, os.ErrNotExist}, []error{os.ErrNotExist}) {
		t.Fatal("expected nil joined cause to be ignored for pure sentinel matching")
	}
}
