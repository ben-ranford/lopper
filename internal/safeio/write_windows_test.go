//go:build windows

package safeio

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestOpenPinnedReplacementTargetIfNeededOpensPinnedTargetOnWindows(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	openCalls := 0

	root := &fakeRoot{
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected pinned target path: %s", name)
			}
			if flag != os.O_WRONLY {
				t.Fatalf("unexpected pinned target flag: %d", flag)
			}
			openCalls++
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return info, nil },
				close: closeWithoutError,
			}, nil
		},
	}

	file, closeFile, err := openPinnedReplacementTargetIfNeeded(root, writeTestFileName, info)
	if err != nil {
		t.Fatalf("openPinnedReplacementTargetIfNeeded returned error: %v", err)
	}
	if file == nil {
		t.Fatal("expected pinned target file on Windows")
	}
	if openCalls != 1 {
		t.Fatalf("expected one pinned target open, got %d", openCalls)
	}
	if err := closeFile(); err != nil {
		t.Fatalf("close pinned target file: %v", err)
	}
}

func TestOpenPinnedReplacementTargetIfNeededReturnsOpenErrorOnWindows(t *testing.T) {
	expectedErr := errors.New("open pinned target failure")
	root := &fakeRoot{
		openFile: func(string, int, os.FileMode) (File, error) {
			return nil, expectedErr
		},
	}

	file, _, err := openPinnedReplacementTargetIfNeeded(root, writeTestFileName, statTestPath(t, t.TempDir()))
	if file != nil {
		t.Fatal("expected pinned target file to remain nil")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected open target error, got %v", err)
	}
}

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

func TestWriteAtomicReplacementWithPinnedTargetFallsBackForReplaceExistingRenameErrorOnWindows(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)

	removeCalls := 0
	targetFile, targetData := newPinnedFallbackTargetFile(t, info, "before")
	tempInfo := newPinnedTargetInfo(t, "temp")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				return info, nil
			}
			return tempInfo, nil
		},
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			return targetFile, nil
		}, tempInfo, nil),
		rename: func(oldName, newName string) error {
			return &os.LinkError{
				Op:  "renameat",
				Old: oldName,
				New: newName,
				Err: syscall.ERROR_ALREADY_EXISTS,
			}
		},
		remove: func(string) error {
			removeCalls++
			return nil
		},
	}

	postWriteCalls := 0
	if err := writeAtomicReplacementWithPinnedTargetAndPostWriteCheck(root, writeTestFileName, []byte("after"), 0o600, targetFile, true, func() error {
		postWriteCalls++
		return nil
	}); err != nil {
		t.Fatalf("writeAtomicReplacementWithPinnedTarget returned error: %v", err)
	}
	if removeCalls != 1 {
		t.Fatalf("expected one temp cleanup remove, got %d", removeCalls)
	}
	if string(*targetData) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(*targetData))
	}
	if postWriteCalls != 1 {
		t.Fatalf("expected post-write check after fallback, got %d calls", postWriteCalls)
	}
}

func TestWriteAtomicReplacementRunsPostWriteAfterWindowsFallback(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	targetFile, targetData := newPinnedFallbackTargetFile(t, info, "before")
	tempInfo := newPinnedTargetInfo(t, "temp")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				return info, nil
			}
			return tempInfo, nil
		},
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			return targetFile, nil
		}, tempInfo, nil),
		rename: func(oldName, newName string) error {
			return windowsReplaceExistingError(oldName, newName)
		},
		remove: func(string) error { return nil },
	}

	postWriteCalls := 0
	err := writeAtomicReplacementWithPostWriteCheck(root, writeTestFileName, []byte("after"), 0o600, nil, func() error {
		postWriteCalls++
		if string(*targetData) != "after" {
			t.Fatalf("expected fallback overwrite before post-write check, got %q", string(*targetData))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("writeAtomicReplacementWithPostWriteCheck returned error: %v", err)
	}
	if postWriteCalls != 1 {
		t.Fatalf("expected post-write check after fallback, got %d calls", postWriteCalls)
	}
}

func TestWriteAtomicReplacementWindowsFallbackPostWriteFailureHonorsRollbackSafety(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	targetFile, targetData := newPinnedFallbackTargetFile(t, info, "before")
	tempInfo := newPinnedTargetInfo(t, "temp")
	removeCalls := 0
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				return info, nil
			}
			return tempInfo, nil
		},
		open: func(name string) (File, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected rollback snapshot path: %s", name)
			}
			reader := strings.NewReader(string(*targetData))
			return &fakeFile{
				read:  reader.Read,
				stat:  func() (fs.FileInfo, error) { return info, nil },
				close: closeWithoutError,
			}, nil
		},
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			return targetFile, nil
		}, tempInfo, nil),
		rename: func(oldName, newName string) error {
			return windowsReplaceExistingError(oldName, newName)
		},
		remove: func(name string) error {
			removeCalls++
			if name == writeTestFileName {
				t.Fatal("fallback rollback must not remove the overwritten target path")
			}
			return nil
		},
	}

	postWriteCalls := 0
	postWriteErr := errors.New("post-write validation failure")
	err := writeAtomicReplacementWithPinnedTargetCallbacks(root, writeTestFileName, []byte("after"), 0o600, targetFile, true, pinnedReplacementChecks{
		postWrite: func() error {
			postWriteCalls++
			if string(*targetData) != "after" {
				t.Fatalf("expected fallback overwrite before post-write check, got %q", string(*targetData))
			}
			return postWriteErr
		},
		rollbackOnPostWriteFailure: true,
	})
	if !errors.Is(err, postWriteErr) {
		t.Fatalf("expected post-write validation error, got %v", err)
	}
	if strings.Contains(err.Error(), "fallback replacement cannot roll back post-write failure") {
		t.Fatalf("replace-existing fallback should not reject before post-write validation, got %v", err)
	}
	if postWriteCalls != 1 {
		t.Fatalf("expected post-write check after fallback overwrite, got %d calls", postWriteCalls)
	}
	if removeCalls != 1 {
		t.Fatalf("expected only temp cleanup remove, got %d removes", removeCalls)
	}
	if string(*targetData) != "before" {
		t.Fatalf("expected rollback to restore fallback target data, got %q", string(*targetData))
	}
}

func TestWriteAtomicReplacementWindowsFallbackPostWriteFailureSkipsRollbackAfterConcurrentWrite(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	target := &windowsFallbackTarget{data: []byte("before")}
	targetFile := target.file(t, info)
	tempInfo := newPinnedTargetInfo(t, "temp")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				return info, nil
			}
			return tempInfo, nil
		},
		open: func(name string) (File, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected rollback snapshot path: %s", name)
			}
			reader := strings.NewReader(string(target.data))
			return &fakeFile{
				read:  reader.Read,
				stat:  func() (fs.FileInfo, error) { return info, nil },
				close: closeWithoutError,
			}, nil
		},
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			return targetFile, nil
		}, tempInfo, nil),
		rename: func(oldName, newName string) error {
			return windowsReplaceExistingError(oldName, newName)
		},
		remove: func(string) error { return nil },
	}

	postWriteErr := errors.New("post-write validation failure")
	err := writeAtomicReplacementWithPinnedTargetCallbacks(root, writeTestFileName, []byte("after"), 0o600, targetFile, true, pinnedReplacementChecks{
		postWrite: func() error {
			if string(target.data) != "after" {
				t.Fatalf("expected fallback overwrite before post-write check, got %q", string(target.data))
			}
			target.data = []byte("concurrent")
			return postWriteErr
		},
		rollbackOnPostWriteFailure: true,
	})
	if !errors.Is(err, postWriteErr) {
		t.Fatalf("expected post-write validation error, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "target changed after fallback overwrite") {
		t.Fatalf("expected rollback ownership error, got %v", err)
	}
	if string(target.data) != "concurrent" {
		t.Fatalf("rollback must not overwrite concurrent target data, got %q", string(target.data))
	}
	if target.truncateCalls != 1 || target.writeCalls != 1 {
		t.Fatalf("expected only initial fallback overwrite: truncate=%d write=%d", target.truncateCalls, target.writeCalls)
	}
}

// TestRestoreWindowsFallbackTargetRejectsIdentityChangedBetweenOwnershipCheckAndWrite
// covers a narrower race than TestWriteAtomicReplacementWindowsFallbackPostWriteFailureSkipsRollbackAfterConcurrentWrite:
// there, the concurrent write happens well before the rollback's ownership
// check even starts (inside postWrite), which the ownership check's own
// content comparison already catches. Here, the target's identity changes
// strictly between the ownership check's read completing and the rollback
// write -- a window the ownership check alone cannot see, since it already
// finished reading by the time the replacement happens.
func TestRestoreWindowsFallbackTargetRejectsIdentityChangedBetweenOwnershipCheckAndWrite(t *testing.T) {
	info, replacedInfo := writePinnedTargetInfoPair(t)
	target := &windowsFallbackTarget{data: []byte("after")}
	targetFile := target.file(t, info)

	lstatCalls := 0
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			lstatCalls++
			if lstatCalls == 1 {
				// The ownership check's own Lstat, run first: the target
				// still resolves to our own fallback overwrite here.
				return info, nil
			}
			// A concurrent same-key writer replaced the target between the
			// ownership check's read and the write-adjacent Lstat that
			// immediately precedes the rollback write.
			return replacedInfo, nil
		},
		open: func(name string) (File, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected rollback snapshot path: %s", name)
			}
			reader := strings.NewReader(string(target.data))
			return &fakeFile{
				read:  reader.Read,
				stat:  func() (fs.FileInfo, error) { return info, nil },
				close: closeWithoutError,
			}, nil
		},
	}

	primaryErr := errors.New("post-write validation failure")
	err := restoreWindowsFallbackTarget(root, writeTestFileName, targetFile, []byte("before"), []byte("after"), primaryErr)
	if !errors.Is(err, primaryErr) {
		t.Fatalf("expected primary error to be preserved, got %v", err)
	}
	if !strings.Contains(err.Error(), "target changed before replacement") {
		t.Fatalf("expected rollback to reject the identity change caught right before the write, got %v", err)
	}
	if target.truncateCalls != 0 || target.writeCalls != 0 {
		t.Fatalf("rollback must not write once the target's identity changed: truncate=%d write=%d", target.truncateCalls, target.writeCalls)
	}
	if string(target.data) != "after" {
		t.Fatalf("expected the concurrent writer's data to remain untouched, got %q", string(target.data))
	}
}

func TestWriteAtomicReplacementRejectsRetargetedDestinationAfterWindowsFallback(t *testing.T) {
	originalInfo, changedInfo := writePinnedTargetInfoPair(t)
	targetFile, targetData := newPinnedFallbackTargetFile(t, originalInfo, "before")
	tempInfo := newPinnedTargetInfo(t, "temp")
	lstatCalls := 0
	postWriteCalls := 0
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				lstatCalls++
				if lstatCalls == 1 {
					return originalInfo, nil
				}
				return changedInfo, nil
			}
			return tempInfo, nil
		},
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			return targetFile, nil
		}, tempInfo, nil),
		rename: func(oldName, newName string) error {
			return windowsReplaceExistingError(oldName, newName)
		},
		remove: func(string) error { return nil },
	}

	err := writeAtomicReplacementWithPostWriteCheck(root, writeTestFileName, []byte("after"), 0o600, nil, func() error {
		postWriteCalls++
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "overwritten target changed before validation") {
		t.Fatalf("expected overwritten target validation error, got %v", err)
	}
	if postWriteCalls != 0 {
		t.Fatalf("expected post-write check to be skipped after validation failure, got %d calls", postWriteCalls)
	}
	if lstatCalls != 2 {
		t.Fatalf("expected pre-overwrite and post-overwrite validation lstat calls, got %d", lstatCalls)
	}
	if string(*targetData) != "after" {
		t.Fatalf("expected fallback overwrite before retarget detection, got %q", string(*targetData))
	}
}

// TestLockPinnedReplacementFileSerializesConcurrentHolders uses two real
// *os.File handles to the same on-disk file -- not the fakeRoot/fakeFile
// mocks used elsewhere in this file -- because the property under test
// (LockFileEx-based mutual exclusion) is a genuine kernel behavior a mock
// cannot simulate. Two separate handles to the same file, raced from two
// goroutines within one process, still exercise the real OS-level lock: it
// is keyed on the underlying file, not on which handle or thread holds it.
func TestLockPinnedReplacementFileSerializesConcurrentHolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(path, []byte("data"), 0o640); err != nil {
		t.Fatalf("seed lock target: %v", err)
	}

	firstFile, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open first handle: %v", err)
	}
	defer firstFile.Close()
	secondFile, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open second handle: %v", err)
	}
	defer secondFile.Close()

	unlockFirst, err := lockPinnedReplacementFile(firstFile)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}

	acquired := make(chan time.Time, 1)
	go func() {
		unlockSecond, lockErr := lockPinnedReplacementFile(secondFile)
		if lockErr != nil {
			t.Errorf("acquire second lock: %v", lockErr)
			acquired <- time.Time{}
			return
		}
		acquired <- time.Now()
		if unlockErr := unlockSecond(); unlockErr != nil {
			t.Errorf("release second lock: %v", unlockErr)
		}
	}()

	// Give the second goroutine a chance to attempt (and block on) its lock
	// before releasing the first, so a successful acquisition genuinely
	// proves it waited rather than winning a race that happened not to
	// overlap.
	time.Sleep(100 * time.Millisecond)
	releasedAt := time.Now()
	if unlockErr := unlockFirst(); unlockErr != nil {
		t.Fatalf("release first lock: %v", unlockErr)
	}

	select {
	case acquiredAt := <-acquired:
		if acquiredAt.IsZero() {
			t.Fatal("second lock acquisition failed, see prior error")
		}
		if acquiredAt.Before(releasedAt) {
			t.Fatalf("second lock acquired at %v before first released at %v", acquiredAt, releasedAt)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second lock acquisition")
	}
}

func TestLockPinnedReplacementFileSkipsNonOSFile(t *testing.T) {
	unlock, err := lockPinnedReplacementFile(&fakeFile{})
	if err != nil {
		t.Fatalf("expected a non-*os.File to skip locking without error, got %v", err)
	}
	if err := unlock(); err != nil {
		t.Fatalf("expected the no-op unlock to succeed, got %v", err)
	}
}

// TestFallbackLockFileRelStaysWithinNTFSComponentLimit guards against
// deriving the coordination lock name from the target's own basename: NTFS
// caps a path component at 255 characters, and a target basename already
// near that limit would overflow once a literal prefix was appended,
// blocking an otherwise-valid replacement of a long-named file.
func TestFallbackLockFileRelStaysWithinNTFSComponentLimit(t *testing.T) {
	longBase := strings.Repeat("a", 250) + ".json"
	targetRel := filepath.Join("keys", longBase)

	lockRel := fallbackLockFileRel(targetRel)

	lockBase := filepath.Base(lockRel)
	if len(lockBase) > 255 {
		t.Fatalf("coordination lock filename exceeds NTFS's 255-character component limit: %d chars (%q)", len(lockBase), lockBase)
	}
	if filepath.Dir(lockRel) != "keys" {
		t.Fatalf("expected the coordination lock file to live next to its target, got dir %q", filepath.Dir(lockRel))
	}
}

func TestFallbackLockFileRelIsDeterministicPerTarget(t *testing.T) {
	first := fallbackLockFileRel(filepath.Join("keys", "one.json"))
	second := fallbackLockFileRel(filepath.Join("keys", "one.json"))
	if first != second {
		t.Fatalf("expected the same target to derive the same coordination lock path, got %q and %q", first, second)
	}

	third := fallbackLockFileRel(filepath.Join("keys", "two.json"))
	if first == third {
		t.Fatalf("expected different targets to derive different coordination lock paths, both got %q", first)
	}
}

// TestFallbackAtomicReplacementRollbackReadsSnapshotThroughLockedHandle
// drives the full fallback transaction end to end against a real on-disk
// file and a real *os.File (via openPinnedReplacementTarget, the same
// production path fallbackAtomicReplacement's caller uses), rather than the
// mocks used elsewhere in this file. That matters here specifically:
// snapshotPinnedWindowsFallbackTarget reads through the already-open
// replacementFile handle rather than a second, independently-opened one, and
// only a real OS handle and a real Seek/Read round trip can tell that apart
// from a mock, which would happily serve either path identically.
func TestFallbackAtomicReplacementRollbackReadsSnapshotThroughLockedHandle(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	root := openTestRoot(t, rootDir)
	info := statTestPath(t, targetPath)

	replacementFile, err := openPinnedReplacementTarget(root, writeTestFileName, info)
	if err != nil {
		t.Fatalf("open pinned replacement target: %v", err)
	}
	defer replacementFile.Close()

	renameErr := windowsReplaceExistingError(".safeio-atomic-temp", writeTestFileName)
	postWriteErr := errors.New("post-write validation failure")

	err = fallbackAtomicReplacement(root, atomicReplacementFallback{
		oldName:                    ".safeio-atomic-temp",
		newName:                    writeTestFileName,
		replacementFile:            replacementFile,
		data:                       []byte("after"),
		renameErr:                  renameErr,
		postWrite:                  func() error { return postWriteErr },
		rollbackOnPostWriteFailure: true,
	})
	if !errors.Is(err, postWriteErr) {
		t.Fatalf("expected post-write error, got %v", err)
	}
	if strings.Contains(err.Error(), "unsupported operation") || strings.Contains(strings.ToLower(err.Error()), "lock violation") {
		t.Fatalf("rollback snapshot must read through the already-locked handle itself, not a second one: %v", err)
	}
	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("read restored target: %v", readErr)
	}
	if string(data) != "before" {
		t.Fatalf("expected rollback to restore the original data, got %q", string(data))
	}
}

// TestFallbackAtomicReplacementDoesNotBlockConcurrentReadersOfLiveTarget
// proves lockFallbackTransaction's coordination lock -- unlike the whole-file
// lock this fallback path used to take directly on the live target -- never
// touches the target itself. A concurrent reader opening its own independent
// handle to that same path (the same shape as analysisCache.lookup reading a
// cache pointer through ReadFileUnder, which does not participate in this
// fallback path's locking protocol at all) must be able to read it while a
// fallback transaction is in flight, rather than failing with a Windows lock
// violation. This only exercises the real bug against a genuine *os.File and
// a real LockFileEx call, via openPinnedReplacementTarget and openTestRoot,
// the same production paths used elsewhere in this file; a mock has no lock
// to block against and would pass regardless.
func TestFallbackAtomicReplacementDoesNotBlockConcurrentReadersOfLiveTarget(t *testing.T) {
	rootDir := t.TempDir()
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	root := openTestRoot(t, rootDir)
	info := statTestPath(t, targetPath)

	replacementFile, err := openPinnedReplacementTarget(root, writeTestFileName, info)
	if err != nil {
		t.Fatalf("open pinned replacement target: %v", err)
	}
	defer replacementFile.Close()

	renameErr := windowsReplaceExistingError(".safeio-atomic-temp", writeTestFileName)

	inPostWrite := make(chan struct{})
	releasePostWrite := make(chan struct{})
	fallbackDone := make(chan error, 1)
	go func() {
		fallbackDone <- fallbackAtomicReplacement(root, atomicReplacementFallback{
			oldName:         ".safeio-atomic-temp",
			newName:         writeTestFileName,
			replacementFile: replacementFile,
			data:            []byte("after"),
			renameErr:       renameErr,
			postWrite: func() error {
				close(inPostWrite)
				<-releasePostWrite
				return nil
			},
		})
	}()

	select {
	case <-inPostWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fallback transaction to reach its post-write hook")
	}

	// The fallback transaction is mid-flight here, blocked in postWrite while
	// still holding its coordination lock. Reading through this independent
	// handle must still succeed.
	data, readErr := os.ReadFile(targetPath)
	close(releasePostWrite)

	if err := <-fallbackDone; err != nil {
		t.Fatalf("expected fallback overwrite to succeed, got %v", err)
	}
	if readErr != nil {
		t.Fatalf("expected concurrent read of live target to succeed while fallback transaction was in flight, got %v", readErr)
	}
	if string(data) != "after" {
		t.Fatalf("expected concurrent read to observe the fallback's write, got %q", string(data))
	}
}

func TestFallbackAtomicReplacementAcceptsActualQuarantineRenameSourceOnWindows(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	targetFile, targetData := newPinnedFallbackTargetFile(t, info, "before")
	quarantineRel := filepath.Join("nested", ".safeio-atomic-quarantine", "entry")
	renameErr := &publishRenameError{
		sourceRel: quarantineRel,
		err:       errors.Join(windowsReplaceExistingError(quarantineRel, writeTestFileName), nil),
	}

	err := fallbackAtomicReplacement(&fakeRoot{}, atomicReplacementFallback{
		oldName:         ".safeio-atomic-temp",
		newName:         writeTestFileName,
		replacementFile: targetFile,
		data:            []byte("after"),
		renameErr:       renameErr,
	})
	if err != nil {
		t.Fatalf("fallbackAtomicReplacement returned error: %v", err)
	}
	if string(*targetData) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(*targetData))
	}
}

func TestFallbackAtomicReplacementRejectsRetainedQuarantineStagingOnWindows(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	targetFile, targetData := newPinnedFallbackTargetFile(t, info, "before")
	quarantineRel := filepath.Join("nested", ".safeio-atomic-quarantine", "entry")
	restoreErr := errIdentityBoundRestoreRetainedStaging
	renameErr := &publishRenameError{
		sourceRel: quarantineRel,
		err: errors.Join(
			windowsReplaceExistingError(quarantineRel, writeTestFileName),
			restoreErr,
		),
	}

	err := fallbackAtomicReplacement(&fakeRoot{}, atomicReplacementFallback{
		oldName:         ".safeio-atomic-temp",
		newName:         writeTestFileName,
		replacementFile: targetFile,
		data:            []byte("after"),
		renameErr:       renameErr,
	})
	if !errors.Is(err, renameErr) || !errors.Is(err, restoreErr) {
		t.Fatalf("expected retained staging error to be preserved, got %v", err)
	}
	if string(*targetData) != "before" {
		t.Fatalf("fallback must not overwrite after retained source recovery, got %q", string(*targetData))
	}
}

func TestFallbackAtomicReplacementRejectsWrongIdentityBoundStagedRenameSourceOnWindows(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	targetFile, targetData := newPinnedFallbackTargetFile(t, info, "before")
	const tempRel = ".safeio-atomic-temp"
	renameErr := &publishRenameError{
		sourceRel: tempRel + ".staged",
		err:       windowsReplaceExistingError("other-staged", writeTestFileName),
	}

	err := fallbackAtomicReplacement(&fakeRoot{}, atomicReplacementFallback{
		oldName:         tempRel,
		newName:         writeTestFileName,
		replacementFile: targetFile,
		data:            []byte("after"),
		renameErr:       renameErr,
	})
	if err == nil {
		t.Fatal("expected mismatched staged source to reject fallback")
	}
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected original rename error, got %v", err)
	}
	if string(*targetData) != "before" {
		t.Fatalf("wrong-source fallback mutated target data: %q", string(*targetData))
	}
}

func TestWriteFileReplacingWithinRootFallsBackWhenTargetAppearsBeforeRename(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	target := &windowsFallbackTarget{data: []byte("before")}
	root, rootState := newAppearingWindowsFallbackRoot(t, info, target, nil)

	if err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileReplacingWithinRoot returned error: %v", err)
	}
	if rootState.lstatCalls != 3 {
		t.Fatalf("expected initial, late-pin, and pre-overwrite lstat calls, got %d", rootState.lstatCalls)
	}
	if target.openCalls != 1 {
		t.Fatalf("expected one late pinned target open, got %d", target.openCalls)
	}
	if target.closeCalls != 1 {
		t.Fatalf("expected late pinned target to close once, got %d", target.closeCalls)
	}
	if string(target.data) != "after" {
		t.Fatalf("expected late fallback overwrite data, got %q", string(target.data))
	}
}

func TestWriteAtomicReplacementWithPinnedTargetReturnsCleanupAfterSuccessfulWindowsFallback(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	targetFile, targetData := newPinnedFallbackTargetFile(t, info, "before")
	tempInfo := newPinnedTargetInfo(t, "temp")
	cleanupErr := errors.New("publish cleanup failure")
	quarantineRel := filepath.Join("nested", ".safeio-atomic-quarantine", "entry")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				return info, nil
			}
			return tempInfo, nil
		},
		openFile: openTargetOrTempFile(writeTestFileName, func() (File, error) {
			return targetFile, nil
		}, tempInfo, nil),
		renameIfMatches: func(string, string, fs.FileInfo, string) error {
			return &publishRenameError{
				sourceRel:  quarantineRel,
				err:        windowsReplaceExistingError(quarantineRel, writeTestFileName),
				cleanupErr: cleanupErr,
			}
		},
		remove: func(string) error { return nil },
	}

	err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, targetFile, true)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected cleanup failure after successful fallback, got %v", err)
	}
	if string(*targetData) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(*targetData))
	}
}

func TestWriteFileReplacingWithinRootJoinsLatePinnedCloseAndCleanupErrors(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	closeErr := errors.New("late pinned target close failure")
	cleanupErr := errors.New("temp cleanup remove failure")
	target := &windowsFallbackTarget{
		data:     []byte("before"),
		closeErr: closeErr,
	}
	root, rootState := newAppearingWindowsFallbackRoot(
		t,
		statTestPath(t, targetInfoPath),
		target,
		cleanupErr,
	)

	err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("after"), 0o600)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected late pinned close error, got %v", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected temp cleanup remove error, got %v", err)
	}
	if rootState.removeCalls != 1 {
		t.Fatalf("expected one temp cleanup remove, got %d", rootState.removeCalls)
	}
	if target.closeCalls != 1 {
		t.Fatalf("expected late pinned target to close once, got %d", target.closeCalls)
	}
	if string(target.data) != "after" {
		t.Fatalf("expected fallback overwrite to succeed, got %q", string(target.data))
	}
}

func TestFallbackAtomicReplacementRejectsUnsafeTargetThatAppearsAfterRename(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)

	for _, tc := range unsafeTargetModeCases() {
		tt := struct {
			name string
			info fs.FileInfo
			want string
		}{
			name: tc.name,
			info: &modeOverrideFileInfo{FileInfo: info, mode: tc.mode},
			want: tc.want,
		}
		t.Run(tt.name, func(t *testing.T) {
			targetOpened := false
			root := &fakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return tt.info, nil },
				openFile: func(string, int, os.FileMode) (File, error) {
					targetOpened = true
					return nil, nil
				},
			}
			renameErr := windowsReplaceExistingError(".safeio-atomic-temp", writeTestFileName)

			err := fallbackAtomicReplacement(root, atomicReplacementFallback{
				oldName:   ".safeio-atomic-temp",
				newName:   writeTestFileName,
				data:      []byte("after"),
				renameErr: renameErr,
			})
			if !errors.Is(err, renameErr) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected joined rename and %q rejection, got %v", tt.want, err)
			}
			if targetOpened {
				t.Fatal("unsafe target was opened for fallback overwrite")
			}
		})
	}
}

// TestFallbackAtomicReplacementReusesWriteOnlyHandleWithoutRollback proves
// an ordinary (non-rollback-eligible) fallback never reopens the target: it
// reuses the caller-supplied handle exactly as given, deliberately without
// read support, standing in for a real write-only-permission target that
// an O_RDWR reopen would reject outright.
func TestFallbackAtomicReplacementReusesWriteOnlyHandleWithoutRollback(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	targetData := []byte("before")
	targetFile := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) {
				targetData = append(targetData[:0], p...)
				return len(p), nil
			},
			close: closeWithoutError,
		},
		truncate: func(size int64) error {
			if size != 0 {
				t.Fatalf("unexpected truncate size: %d", size)
			}
			targetData = targetData[:0]
			return nil
		},
		seek: func(offset int64, whence int) (int64, error) { return offset, nil },
	}
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			return info, nil
		},
		openFile: func(string, int, os.FileMode) (File, error) {
			t.Fatal("ordinary, non-rollback fallback must reuse the caller-supplied handle, not reopen it")
			return nil, nil
		},
	}
	renameErr := windowsReplaceExistingError(".safeio-atomic-temp", writeTestFileName)

	err := fallbackAtomicReplacement(root, atomicReplacementFallback{
		oldName:         ".safeio-atomic-temp",
		newName:         writeTestFileName,
		replacementFile: targetFile,
		data:            []byte("after"),
		renameErr:       renameErr,
	})
	if err != nil {
		t.Fatalf("expected write-only fallback overwrite to succeed, got %v", err)
	}
	if string(targetData) != "after" {
		t.Fatalf("expected target data to be overwritten, got %q", string(targetData))
	}
}

func TestFallbackAtomicReplacementAllowsRollbackRequiredReplacement(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	target := &windowsFallbackTarget{data: []byte("before")}
	targetFile := target.file(t, info)
	lstatCalls := 0
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			lstatCalls++
			return info, nil
		},
		// replacementFileForWindowsFallback always opens its own read/write
		// handle rather than trusting the caller-supplied targetFile (which
		// is pinned write-only in production, see write.go), reusing
		// targetFile's already-verified identity as the expected identity
		// instead of a fresh path lookup -- a fresh wrapper sharing the
		// same target state stands in for that fresh handle here.
		openFile: func(name string, _ int, _ os.FileMode) (File, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected pinned target open: %s", name)
			}
			return target.file(t, info), nil
		},
	}
	renameErr := windowsReplaceExistingError(".safeio-atomic-temp", writeTestFileName)

	err := fallbackAtomicReplacement(root, atomicReplacementFallback{
		oldName:                    ".safeio-atomic-temp",
		newName:                    writeTestFileName,
		replacementFile:            targetFile,
		data:                       []byte("after"),
		renameErr:                  renameErr,
		rollbackOnPostWriteFailure: true,
	})
	if err != nil {
		t.Fatalf("expected rollback-required fallback overwrite to succeed, got %v", err)
	}
	if lstatCalls != 3 {
		t.Fatalf("expected snapshot, pre-overwrite, and post-overwrite target validation, got %d lstat calls", lstatCalls)
	}
	if target.truncateCalls != 1 || target.writeCalls != 1 {
		t.Fatalf("expected one fallback overwrite: truncate=%d write=%d", target.truncateCalls, target.writeCalls)
	}
	if string(target.data) != "after" {
		t.Fatalf("expected target data to be overwritten, got %q", string(target.data))
	}
}

func TestFallbackAtomicReplacementRollbackRequiredSnapshotFailurePreventsMutation(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)
	snapshotErr := errors.New("rollback snapshot failure")
	// The rollback snapshot now reads through the already-pinned
	// replacementFile handle itself (see snapshotPinnedWindowsFallbackTarget),
	// not a second handle opened via root.Open -- fail it there instead.
	target := &windowsFallbackTarget{data: []byte("before"), readErr: snapshotErr}
	targetFile := target.file(t, info)
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected lstat path: %s", name)
			}
			return info, nil
		},
		// replacementFileForWindowsFallback always opens its own read/write
		// handle; a fresh wrapper sharing target's state (including
		// readErr) stands in for that fresh handle here.
		openFile: func(name string, _ int, _ os.FileMode) (File, error) {
			if name != writeTestFileName {
				t.Fatalf("unexpected pinned target open: %s", name)
			}
			return target.file(t, info), nil
		},
	}
	renameErr := windowsReplaceExistingError(".safeio-atomic-temp", writeTestFileName)

	err := fallbackAtomicReplacement(root, atomicReplacementFallback{
		oldName:                    ".safeio-atomic-temp",
		newName:                    writeTestFileName,
		replacementFile:            targetFile,
		data:                       []byte("after"),
		renameErr:                  renameErr,
		rollbackOnPostWriteFailure: true,
	})
	if !errors.Is(err, renameErr) || !errors.Is(err, snapshotErr) {
		t.Fatalf("expected rename and snapshot errors, got %v", err)
	}
	if target.truncateCalls != 0 || target.writeCalls != 0 {
		t.Fatalf("target mutated after snapshot failure: truncate=%d write=%d", target.truncateCalls, target.writeCalls)
	}
	if string(target.data) != "before" {
		t.Fatalf("expected target data to remain unchanged, got %q", string(target.data))
	}
}

func TestFallbackAtomicReplacementRejectsTargetChangedAfterLatePin(t *testing.T) {
	originalInfo, changedInfo := writePinnedTargetInfoPair(t)
	disappearedErr := errors.New("target disappeared")
	closeErr := errors.New("late pinned target close failure")

	tests := []struct {
		name          string
		revalidate    func() (fs.FileInfo, error)
		closeErr      error
		wantErr       error
		wantSubstring string
	}{
		{
			name:       "disappeared",
			revalidate: func() (fs.FileInfo, error) { return nil, disappearedErr },
			closeErr:   closeErr,
			wantErr:    disappearedErr,
		},
		{
			name:          "replaced",
			revalidate:    func() (fs.FileInfo, error) { return changedInfo, nil },
			wantSubstring: "target changed before replacement",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertWindowsFallbackRejectsAfterLatePin(
				t,
				originalInfo,
				tt.revalidate,
				tt.closeErr,
				tt.wantErr,
				tt.wantSubstring,
			)
		})
	}
}

func TestWriteFileAtomicallyIfAbsentFallsBackToWindowsNoReplaceRename(t *testing.T) {
	rootInfo, tempInfo := writePinnedTargetInfoPair(t)
	root := newWindowsNoReplaceIfAbsentRoot(t, rootInfo, tempInfo)

	renameCalls := 0
	restoreWindowsNoReplaceRename(t, func(gotRoot Root, gotRootInfo fs.FileInfo, tempRel, targetRel string, gotTempInfo fs.FileInfo) error {
		renameCalls++
		if gotRoot != root {
			t.Fatal("fallback received a different root")
		}
		if !os.SameFile(rootInfo, gotRootInfo) {
			t.Fatal("fallback received the wrong root identity")
		}
		if tempRel != ".safeio-atomic-temp" || targetRel != writeTestFileName {
			t.Fatalf("unexpected rename paths: %s -> %s", tempRel, targetRel)
		}
		if !os.SameFile(tempInfo, gotTempInfo) {
			t.Fatal("fallback received the wrong temp identity")
		}
		root.targetPublished = true
		return nil
	})

	err := writeFileAtomicallyIfAbsentAtRoot(root, writeTestFileName, []byte("hello"), 0o640)
	if err != nil {
		t.Fatalf("writeFileAtomicallyIfAbsentAtRoot returned error: %v", err)
	}
	if renameCalls != 1 {
		t.Fatalf("expected one no-replace rename fallback, got %d", renameCalls)
	}
	if root.removeCalls != 0 {
		t.Fatalf("expected no temp removal after no-replace rename, got %d", root.removeCalls)
	}
}

func TestPublishStagedIfAbsentNoReplaceFallbackUsesAttemptedLinkSource(t *testing.T) {
	rootInfo, tempInfo := writePinnedTargetInfoPair(t)
	state := newWindowsPublishStagedFallbackState(t, rootInfo, tempInfo)
	restoreWindowsNoReplaceRename(t, state.publish)

	if err := publishStagedIdentityBoundIfAbsent(state.root, "source", state.stagedRel, state.targetRel, tempInfo); err != nil {
		t.Fatalf("publishStagedIdentityBoundIfAbsent returned error: %v", err)
	}
	if !state.targetPublished {
		t.Fatal("expected no-replace fallback to publish the target")
	}
}

func TestPublishStagedIfAbsentNoReplaceFallbackPreservesLinkCleanupFailure(t *testing.T) {
	rootInfo, tempInfo := writePinnedTargetInfoPair(t)
	state := newWindowsPublishStagedFallbackState(t, rootInfo, tempInfo)
	cleanupErr := errors.New("quarantine cleanup failed")
	state.linkErr = withAtomicWriteCleanup(
		&os.LinkError{Op: "linkat", Old: ".safeio-atomic-target-link", New: state.targetRel, Err: errors.ErrUnsupported},
		cleanupErr,
	)
	publishCalls := 0
	original := windowsNoReplaceRenameFn
	windowsNoReplaceRenameFn = func(Root, fs.FileInfo, string, string, fs.FileInfo) error {
		publishCalls++
		return nil
	}
	t.Cleanup(func() {
		windowsNoReplaceRenameFn = original
	})

	err := publishStagedIdentityBoundIfAbsent(state.root, "source", state.stagedRel, state.targetRel, tempInfo)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("expected link cleanup failure, got %v", err)
	}
	if publishCalls != 0 || state.targetPublished {
		t.Fatalf("fallback must not publish after link cleanup failure, calls=%d published=%t", publishCalls, state.targetPublished)
	}
}

type windowsPublishStagedFallbackState struct {
	t               *testing.T
	root            *fakeRoot
	rootInfo        fs.FileInfo
	tempInfo        fs.FileInfo
	stagedRel       string
	targetRel       string
	targetPublished bool
	linkErr         error
}

func newWindowsPublishStagedFallbackState(t *testing.T, rootInfo, tempInfo fs.FileInfo) *windowsPublishStagedFallbackState {
	state := &windowsPublishStagedFallbackState{
		t:         t,
		rootInfo:  rootInfo,
		tempInfo:  tempInfo,
		stagedRel: ".safeio-atomic-temp",
		targetRel: writeTestFileName,
	}
	state.root = &fakeRoot{
		linkIfMatches:   state.linkIfMatches,
		lstat:           state.lstat,
		removeIfMatches: state.removeIfMatches,
	}
	return state
}

func (s *windowsPublishStagedFallbackState) linkIfMatches(oldName, newName string, expected fs.FileInfo, message string) error {
	requireSameFileInfo(s.t, expected, s.tempInfo, oldName)
	if oldName != s.stagedRel || newName != s.targetRel || message != temporaryFileChangedBeforeCommit {
		s.t.Fatalf("unexpected publish link %q -> %q (%s)", oldName, newName, message)
	}
	if s.linkErr != nil {
		return s.linkErr
	}
	return &os.LinkError{Op: "linkat", Old: ".safeio-atomic-target-link", New: s.targetRel, Err: errors.ErrUnsupported}
}

func (s *windowsPublishStagedFallbackState) lstat(name string) (fs.FileInfo, error) {
	if name == "." {
		return s.rootInfo, nil
	}
	if name == s.targetRel {
		if s.targetPublished {
			return s.tempInfo, nil
		}
		return nil, os.ErrNotExist
	}
	if name == s.stagedRel {
		if s.targetPublished {
			return nil, os.ErrNotExist
		}
		return s.tempInfo, nil
	}
	s.t.Fatalf("unexpected lstat path: %s", name)
	return nil, os.ErrNotExist
}

func (s *windowsPublishStagedFallbackState) removeIfMatches(name string, expected fs.FileInfo, message string) error {
	requireSameFileInfo(s.t, expected, s.tempInfo, name)
	if name != s.stagedRel || message != cleanupFileChangedBeforeRemoval {
		s.t.Fatalf("unexpected cleanup %q (%s)", name, message)
	}
	return nil
}

func (s *windowsPublishStagedFallbackState) publish(gotRoot Root, gotRootInfo fs.FileInfo, tempRel, targetRel string, gotTempInfo fs.FileInfo) error {
	if gotRoot != s.root {
		s.t.Fatal("fallback received a different root")
	}
	requireSameFileInfo(s.t, gotRootInfo, s.rootInfo, "root identity")
	if tempRel != s.stagedRel || targetRel != s.targetRel {
		s.t.Fatalf("unexpected no-replace rename %q -> %q", tempRel, targetRel)
	}
	requireSameFileInfo(s.t, gotTempInfo, s.tempInfo, "staged identity")
	s.targetPublished = true
	return nil
}

func TestWriteFileAtomicallyIfAbsentNoReplaceFallbackPreservesExistingTarget(t *testing.T) {
	rootInfo, tempInfo := writePinnedTargetInfoPair(t)
	root := newWindowsNoReplaceIfAbsentRoot(t, rootInfo, tempInfo)

	restoreWindowsNoReplaceRename(t, func(Root, fs.FileInfo, string, string, fs.FileInfo) error {
		return os.ErrExist
	})

	err := writeFileAtomicallyIfAbsentAtRoot(root, writeTestFileName, []byte("hello"), 0o640)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected existing target error, got %v", err)
	}
	if root.targetPublished {
		t.Fatal("target was marked published despite existing destination")
	}
	if root.removeCalls != 1 {
		t.Fatalf("expected temp cleanup after failed no-replace rename, got %d", root.removeCalls)
	}
}

func TestWindowsHardLinkUnsupportedFallbackMatchesOnlyExpectedShape(t *testing.T) {
	const tempName = ".safeio-atomic-temp"
	linkError := func(op, oldName, newName string, err error) error {
		return &os.LinkError{Op: op, Old: oldName, New: newName, Err: err}
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unsupported", err: linkError("linkat", tempName, writeTestFileName, errors.ErrUnsupported), want: true},
		{name: "windows unsupported", err: linkError("linkat", tempName, writeTestFileName, syscall.EWINDOWS), want: true},
		{name: "privilege not held", err: linkError("linkat", tempName, writeTestFileName, syscall.ERROR_PRIVILEGE_NOT_HELD), want: true},
		{name: "invalid function", err: linkError("linkat", tempName, writeTestFileName, syscall.Errno(1)), want: true},
		{name: "target exists", err: linkError("linkat", tempName, writeTestFileName, syscall.ERROR_ALREADY_EXISTS)},
		{name: "wrong operation", err: linkError("link", tempName, writeTestFileName, errors.ErrUnsupported)},
		{name: "private staging source", err: linkError("linkat", "other-temp", writeTestFileName, errors.ErrUnsupported), want: true},
		{name: "wrong target", err: linkError("linkat", tempName, "other-target", errors.ErrUnsupported)},
		{name: "raw unsupported", err: errors.ErrUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := windowsHardLinkUnsupported(tt.err, tempName, writeTestFileName)
			if got != tt.want {
				t.Fatalf("unexpected fallback decision: got %t want %t", got, tt.want)
			}
		})
	}
}

func TestStageIdentityBoundFileCopiesForWindowsHardLinkUnsupportedErrors(t *testing.T) {
	for _, linkErr := range []error{syscall.ERROR_PRIVILEGE_NOT_HELD, syscall.Errno(1)} {
		t.Run(linkErr.Error(), func(t *testing.T) {
			rootDir := t.TempDir()
			sourcePath := filepath.Join(rootDir, "source")
			if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
				t.Fatalf("seed source: %v", err)
			}
			root := &fakeRoot{
				Root: openTestRoot(t, rootDir),
				linkIfMatches: func(string, string, fs.FileInfo, string) error {
					return &os.LinkError{Op: "linkat", Old: "source", New: ".safeio-atomic-staging", Err: linkErr}
				},
			}

			stagedRel, stagedInfo, err := stageIdentityBoundFile(root, "source", statTestPath(t, sourcePath), sourceChangedMsg)
			if err != nil {
				t.Fatalf("unsupported Windows link did not copy fallback: %v", err)
			}
			assertFileContent(t, filepath.Join(rootDir, stagedRel), "source")
			if err := cleanupAtomicTempFileIfMatches(root, stagedRel, stagedInfo); err != nil {
				t.Fatalf("cleanup copied staging file: %v", err)
			}
			assertNoAtomicStagingEntries(t, rootDir)
		})
	}
}

func TestNewFileRenameInformationSupportsLongTargetNames(t *testing.T) {
	targetRel := strings.Repeat("a", syscall.MAX_PATH+1)
	renameInfo, err := newFileRenameInformation(syscall.Handle(42), targetRel)
	if err != nil {
		t.Fatalf("newFileRenameInformation returned error for long target: %v", err)
	}
	info := fileRenameInformationView(renameInfo)
	if info == nil {
		t.Fatal("expected rename info view")
	}
	if got, want := info.rootDirectory, syscall.Handle(42); got != want {
		t.Fatalf("unexpected root handle: got %v want %v", got, want)
	}
	if got, want := int(info.fileNameLength), len(targetRel)*2; got != want {
		t.Fatalf("unexpected target byte length: got %d want %d", got, want)
	}
	if got, want := len(renameInfo), int(unsafe.Offsetof(fileRenameInformation{}.fileName))+len(targetRel)*2; got != want {
		t.Fatalf("unexpected rename buffer length: got %d want %d", got, want)
	}
	fileName := unsafe.Slice(&info.fileName[0], len(targetRel))
	if got, want := syscall.UTF16ToString(fileName), targetRel; got != want {
		t.Fatalf("unexpected target name: got %q want %q", got, want)
	}
}

func TestNormalizeWindowsRootRelativeTargetMatchesRootBasenameSemantics(t *testing.T) {
	tests := []struct {
		name      string
		targetRel string
		want      string
	}{
		{name: "unchanged normal name", targetRel: "artifact.txt", want: "artifact.txt"},
		{name: "trailing dot", targetRel: "artifact.", want: "artifact"},
		{name: "trailing space", targetRel: "artifact ", want: "artifact"},
		{name: "trailing dot and space", targetRel: "artifact. ", want: "artifact"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeWindowsRootRelativeTarget(tt.targetRel)
			if err != nil {
				t.Fatalf("normalizeWindowsRootRelativeTarget returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalized target = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewWindowsObjectAttributesPreservesRootRelativeName(t *testing.T) {
	const targetRel = `artifact.`
	attrs, err := newWindowsObjectAttributes(syscall.Handle(42), targetRel)
	if err != nil {
		t.Fatalf("newWindowsObjectAttributes returned error: %v", err)
	}
	if got, want := attrs.rootDirectory, syscall.Handle(42); got != want {
		t.Fatalf("unexpected root handle: got %v want %v", got, want)
	}
	if got, want := int(attrs.objectName.length), len(targetRel)*2; got != want {
		t.Fatalf("unexpected object name byte length: got %d want %d", got, want)
	}
	if got := syscall.UTF16ToString(unsafe.Slice(attrs.objectName.buffer, len(targetRel))); got != targetRel {
		t.Fatalf("object name was normalized: got %q want %q", got, targetRel)
	}
}

func TestWindowsNoReplaceRenameUsesPinnedRootAfterAncestorRename(t *testing.T) {
	base := t.TempDir()
	originalParent := filepath.Join(base, "parent")
	movedParent := filepath.Join(base, "moved")
	if err := os.Mkdir(originalParent, 0o755); err != nil {
		t.Fatalf("create original parent: %v", err)
	}

	root, err := (&osFileSystem{}).OpenRoot(originalParent)
	if err != nil {
		t.Fatalf("open pinned parent: %v", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Fatalf("close pinned parent: %v", err)
		}
	}()
	rootInfo, err := root.Lstat(".")
	if err != nil {
		t.Fatalf("stat pinned parent: %v", err)
	}
	tempFile, err := root.OpenFile(".safeio-atomic-temp", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create temp via pinned parent: %v", err)
	}
	if _, err := tempFile.Write([]byte("payload")); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	tempInfo, err := tempFile.Stat()
	if err != nil {
		t.Fatalf("stat temp: %v", err)
	}
	if err := tempFile.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}

	if err := os.Rename(originalParent, movedParent); err != nil {
		t.Fatalf("rename opened parent: %v", err)
	}
	if err := os.Mkdir(originalParent, 0o755); err != nil {
		t.Fatalf("replace original parent path: %v", err)
	}

	if err := windowsNoReplaceRename(root, rootInfo, ".safeio-atomic-temp", writeTestFileName, tempInfo); err != nil {
		t.Fatalf("windowsNoReplaceRename returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(originalParent, writeTestFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target appeared under replaced parent path: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(movedParent, writeTestFileName))
	if err != nil {
		t.Fatalf("read target under pinned parent: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("unexpected target data: got %q", got)
	}
}

type windowsNoReplaceIfAbsentRoot struct {
	*fakeRoot
	rootInfo        fs.FileInfo
	tempInfo        fs.FileInfo
	targetPublished bool
	removeCalls     int
}

func newWindowsNoReplaceIfAbsentRoot(t *testing.T, rootInfo, tempInfo fs.FileInfo) *windowsNoReplaceIfAbsentRoot {
	t.Helper()
	root := &windowsNoReplaceIfAbsentRoot{
		rootInfo: rootInfo,
		tempInfo: tempInfo,
	}
	root.fakeRoot = &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			switch name {
			case ".":
				return root.rootInfo, nil
			case writeTestFileName:
				if root.targetPublished {
					return root.tempInfo, nil
				}
				return nil, os.ErrNotExist
			default:
				return nil, os.ErrNotExist
			}
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name != ".safeio-atomic-temp" {
				t.Fatalf("unexpected temp path: %s", name)
			}
			if flag != os.O_RDWR|os.O_CREATE|os.O_EXCL {
				t.Fatalf("unexpected temp flags: %#x", flag)
			}
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return root.tempInfo, nil },
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		link: func(oldName, newName string) error {
			return &os.LinkError{
				Op:  "linkat",
				Old: oldName,
				New: newName,
				Err: errors.ErrUnsupported,
			}
		},
		rename: func(string, string) error {
			t.Fatal("if-absent fallback must not use replace-capable Root.Rename")
			return nil
		},
		remove: func(name string) error {
			root.removeCalls++
			if name != ".safeio-atomic-temp" {
				t.Fatalf("unexpected cleanup path: %s", name)
			}
			return nil
		},
	}
	return root
}

func restoreWindowsNoReplaceRename(t *testing.T, fn func(Root, fs.FileInfo, string, string, fs.FileInfo) error) {
	t.Helper()
	previousRename := windowsNoReplaceRenameFn
	previousTempName := randomTempNameFn
	windowsNoReplaceRenameFn = fn
	randomTempNameFn = func() (string, error) { return ".safeio-atomic-temp", nil }
	t.Cleanup(func() {
		windowsNoReplaceRenameFn = previousRename
		randomTempNameFn = previousTempName
	})
}

type windowsFallbackTarget struct {
	data          []byte
	offset        int64
	closeErr      error
	readErr       error
	openCalls     int
	closeCalls    int
	truncateCalls int
	writeCalls    int
}

func (s *windowsFallbackTarget) file(t *testing.T, info fs.FileInfo) File {
	t.Helper()
	return &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			// The Windows fallback path reads its rollback snapshot through
			// this same handle rather than a second one (a second handle
			// would deadlock against lockPinnedReplacementFile's own lock),
			// so Read must reflect s.data/s.offset like a real file.
			read: func(p []byte) (int, error) {
				if s.readErr != nil {
					return 0, s.readErr
				}
				if s.offset < 0 || int(s.offset) >= len(s.data) {
					return 0, io.EOF
				}
				n := copy(p, s.data[int(s.offset):])
				s.offset += int64(n)
				return n, nil
			},
			write: func(p []byte) (int, error) {
				s.writeCalls++
				if s.offset < 0 {
					t.Fatalf("unexpected negative write offset: %d", s.offset)
				}
				end := int(s.offset) + len(p)
				if end > len(s.data) {
					s.data = append(s.data, make([]byte, end-len(s.data))...)
				}
				copy(s.data[int(s.offset):end], p)
				s.offset = int64(end)
				return len(p), nil
			},
			close: func() error {
				s.closeCalls++
				return s.closeErr
			},
		},
		truncate: func(size int64) error {
			if size != 0 {
				t.Fatalf("unexpected truncate size: %d", size)
			}
			s.truncateCalls++
			s.data = s.data[:0]
			return nil
		},
		seek: func(offset int64, whence int) (int64, error) {
			if whence != io.SeekStart {
				t.Fatalf("unexpected seek whence: %d", whence)
			}
			if offset != 0 {
				t.Fatalf("unexpected seek offset: %d", offset)
			}
			s.offset = offset
			return s.offset, nil
		},
	}
}

type windowsFallbackRootState struct {
	lstatCalls      int
	removeCalls     int
	renameAttempted bool
}

func newAppearingWindowsFallbackRoot(
	t *testing.T,
	info fs.FileInfo,
	target *windowsFallbackTarget,
	removeErr error,
) (*fakeRoot, *windowsFallbackRootState) {
	t.Helper()
	state := &windowsFallbackRootState{}
	targetFile := target.file(t, info)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			state.lstatCalls++
			if state.lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			if !state.renameAttempted {
				t.Fatal("target appeared before the rename attempt")
			}
			return info, nil
		},
		openFile: func(name string, flag int, perm os.FileMode) (File, error) {
			if name == writeTestFileName {
				target.openCalls++
				return targetFile, nil
			}
			return &fakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				chmod: chmodWithoutError,
				close: closeWithoutError,
			}, nil
		},
		rename: func(oldName, newName string) error {
			state.renameAttempted = true
			return &os.LinkError{
				Op:  "renameat",
				Old: oldName,
				New: newName,
				Err: syscall.ERROR_FILE_EXISTS,
			}
		},
		remove: func(string) error {
			state.removeCalls++
			return removeErr
		},
	}
	return root, state
}

func assertWindowsFallbackRejectsAfterLatePin(
	t *testing.T,
	originalInfo fs.FileInfo,
	revalidate func() (fs.FileInfo, error),
	closeErr error,
	wantErr error,
	wantSubstring string,
) {
	t.Helper()
	target := &windowsFallbackTarget{closeErr: closeErr}
	targetFile := target.file(t, originalInfo)
	lstatCalls := 0
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			lstatCalls++
			if lstatCalls == 1 {
				return originalInfo, nil
			}
			return revalidate()
		},
		openFile: func(string, int, os.FileMode) (File, error) {
			target.openCalls++
			return targetFile, nil
		},
	}
	renameErr := windowsReplaceExistingError(".safeio-atomic-temp", writeTestFileName)

	err := fallbackAtomicReplacement(root, atomicReplacementFallback{
		oldName:   ".safeio-atomic-temp",
		newName:   writeTestFileName,
		data:      []byte("after"),
		renameErr: renameErr,
	})
	if !errors.Is(err, renameErr) {
		t.Fatalf("expected original rename error, got %v", err)
	}
	if wantErr != nil && !errors.Is(err, wantErr) {
		t.Fatalf("expected target race error %v, got %v", wantErr, err)
	}
	if closeErr != nil && !errors.Is(err, closeErr) {
		t.Fatalf("expected late pinned close error %v, got %v", closeErr, err)
	}
	if wantSubstring != "" && !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected %q rejection, got %v", wantSubstring, err)
	}
	if target.truncateCalls != 0 || target.writeCalls != 0 {
		t.Fatalf("target mutated before rejection: truncate=%d write=%d", target.truncateCalls, target.writeCalls)
	}
	if target.closeCalls != 1 {
		t.Fatalf("expected late pinned target to close once, got %d", target.closeCalls)
	}
}

func windowsReplaceExistingError(oldName, newName string) error {
	return &os.LinkError{
		Op:  "renameat",
		Old: oldName,
		New: newName,
		Err: syscall.ERROR_ALREADY_EXISTS,
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
