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

	if err := writeAtomicReplacementWithPinnedTarget(root, writeTestFileName, []byte("after"), 0o600, targetFile, true); err != nil {
		t.Fatalf("writeAtomicReplacementWithPinnedTarget returned error: %v", err)
	}
	if removeCalls != 1 {
		t.Fatalf("expected one temp cleanup remove, got %d", removeCalls)
	}
	if string(*targetData) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(*targetData))
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
		err:       windowsReplaceExistingError(quarantineRel, writeTestFileName),
	}

	err := fallbackAtomicReplacement(&fakeRoot{}, ".safeio-atomic-temp", writeTestFileName, targetFile, []byte("after"), renameErr)
	if err != nil {
		t.Fatalf("fallbackAtomicReplacement returned error: %v", err)
	}
	if string(*targetData) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(*targetData))
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

	err := fallbackAtomicReplacement(&fakeRoot{}, tempRel, writeTestFileName, targetFile, []byte("after"), renameErr)
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

			err := fallbackAtomicReplacement(root, ".safeio-atomic-temp", writeTestFileName, nil, []byte("after"), renameErr)
			if !errors.Is(err, renameErr) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected joined rename and %q rejection, got %v", tt.want, err)
			}
			if targetOpened {
				t.Fatal("unsafe target was opened for fallback overwrite")
			}
		})
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

type windowsFallbackTarget struct {
	data          []byte
	closeErr      error
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
			write: func(p []byte) (int, error) {
				s.writeCalls++
				s.data = append(s.data, p...)
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

	err := fallbackAtomicReplacement(root, ".safeio-atomic-temp", writeTestFileName, nil, []byte("after"), renameErr)
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
