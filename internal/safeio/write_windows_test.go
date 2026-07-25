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
	targetSynced := 0
	targetData := []byte("before")
	targetFile := &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
			write: func(p []byte) (int, error) {
				targetData = append(targetData, p...)
				return len(p), nil
			},
			sync: func() error {
				targetSynced++
				return nil
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
	if targetSynced != 1 {
		t.Fatalf("expected pinned target to sync once, got %d syncs", targetSynced)
	}
	if string(targetData) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(targetData))
	}
}

func TestWriteFileReplacingWithExactPermissionsForcesModeDuringWindowsFallback(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, targetInfoPath)
	target := &windowsFallbackTarget{data: []byte("before")}
	targetFile := target.file(t, info)
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == writeTestFileName {
				return info, nil
			}
			return nil, os.ErrNotExist
		},
		openFile: windowsFallbackOpenFile(target, targetFile),
		rename:   windowsReplaceExistingError,
		remove:   func(string) error { return nil },
	}
	writeRoot := &WriteRoot{root: root, rootAbs: filepath.Dir(targetInfoPath)}

	if err := writeRoot.WriteFileReplacingWithExactPermissions(writeTestFileName, []byte("after"), 0o600); err != nil {
		t.Fatalf("WriteFileReplacingWithExactPermissions returned error: %v", err)
	}
	if target.chmodCalls != 1 {
		t.Fatalf("expected pinned target mode to be forced once, got %d calls", target.chmodCalls)
	}
	if target.chmodPerm != 0o600 {
		t.Fatalf("expected pinned target mode 0600, got %#o", target.chmodPerm)
	}
	if target.syncCalls != 1 {
		t.Fatalf("expected pinned target to sync after chmod, got %d calls", target.syncCalls)
	}
	if string(target.data) != "after" {
		t.Fatalf("expected fallback overwrite data, got %q", string(target.data))
	}
}

type windowsStrictAtomicFailureFixture struct {
	t                    *testing.T
	rootAbs              string
	info                 fs.FileInfo
	partialWriteErr      error
	renameErr            error
	targetData           []byte
	targetTruncateCalls  int
	targetWriteCalls     int
	candidatePath        string
	renamedCandidate     string
	candidateData        []byte
	candidateChmodCalls  int
	candidatePerm        os.FileMode
	candidateSyncCalls   int
	removeCalls          int
	removedCandidatePath string
}

func newWindowsStrictAtomicFailureFixture(t *testing.T) *windowsStrictAtomicFailureFixture {
	t.Helper()
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	const oldKey = "old-invalid-auth-key"
	if err := os.WriteFile(targetInfoPath, []byte(oldKey), 0o600); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	return &windowsStrictAtomicFailureFixture{
		t:               t,
		rootAbs:         filepath.Dir(targetInfoPath),
		info:            statTestPath(t, targetInfoPath),
		partialWriteErr: errors.New("injected partial live-key write"),
		targetData:      []byte(oldKey),
	}
}

func (f *windowsStrictAtomicFailureFixture) targetFile() File {
	return &truncatingFakeFile{
		fakeFile: &fakeFile{
			stat: func() (fs.FileInfo, error) { return f.info, nil },
			write: func(p []byte) (int, error) {
				f.targetWriteCalls++
				f.targetData = append(f.targetData, p[:2]...)
				return 2, f.partialWriteErr
			},
			chmod: chmodWithoutError,
			sync:  func() error { return nil },
			close: closeWithoutError,
		},
		truncate: func(size int64) error {
			if size != 0 {
				f.t.Fatalf("unexpected truncate size: %d", size)
			}
			f.targetTruncateCalls++
			f.targetData = f.targetData[:0]
			return nil
		},
	}
}

func (f *windowsStrictAtomicFailureFixture) candidateFile(name string) File {
	if f.candidatePath != "" {
		f.t.Fatalf("created multiple replacement candidates: %q then %q", f.candidatePath, name)
	}
	f.candidatePath = name
	return &fakeFile{
		write: func(p []byte) (int, error) {
			f.candidateData = append(f.candidateData, p...)
			return len(p), nil
		},
		chmod: func(perm os.FileMode) error {
			f.candidateChmodCalls++
			f.candidatePerm = perm
			return nil
		},
		sync: func() error {
			f.candidateSyncCalls++
			return nil
		},
		close: closeWithoutError,
	}
}

func (f *windowsStrictAtomicFailureFixture) writeRoot() *WriteRoot {
	targetFile := f.targetFile()
	return &WriteRoot{
		rootAbs: f.rootAbs,
		root: &fakeRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name == writeTestFileName {
					return f.info, nil
				}
				return nil, os.ErrNotExist
			},
			openFile: func(name string, _ int, _ os.FileMode) (File, error) {
				if name == writeTestFileName {
					return targetFile, nil
				}
				return f.candidateFile(name), nil
			},
			rename: func(oldName, newName string) error {
				f.renamedCandidate = oldName
				f.renameErr = windowsReplaceExistingError(oldName, newName)
				return f.renameErr
			},
			remove: func(name string) error {
				f.removeCalls++
				if name == writeTestFileName {
					f.t.Fatal("cleanup attempted to remove the live target")
				}
				f.removedCandidatePath = name
				return nil
			},
		},
	}
}

func (f *windowsStrictAtomicFailureFixture) assertPreserved(t *testing.T, err error) {
	t.Helper()
	const replacement = "complete-replacement-key"
	if !errors.Is(err, f.renameErr) {
		t.Fatalf("expected atomic rename error, got %v", err)
	}
	if errors.Is(err, f.partialWriteErr) {
		t.Fatalf("strict atomic replacement reached in-place fallback: %v", err)
	}
	if string(f.targetData) != "old-invalid-auth-key" {
		t.Fatalf("expected old live key to remain intact, got %q", string(f.targetData))
	}
	if f.targetTruncateCalls != 0 || f.targetWriteCalls != 0 {
		t.Fatalf("expected no live-key mutation, truncate=%d write=%d", f.targetTruncateCalls, f.targetWriteCalls)
	}
	if string(f.candidateData) != replacement || f.candidateSyncCalls != 1 {
		t.Fatalf("expected complete synced candidate before rename, data=%q syncs=%d", string(f.candidateData), f.candidateSyncCalls)
	}
	if f.candidateChmodCalls != 1 || f.candidatePerm != 0o600 {
		t.Fatalf("expected exact candidate mode 0600, chmods=%d mode=%#o", f.candidateChmodCalls, f.candidatePerm)
	}
	if f.removeCalls != 1 || f.candidatePath == "" || f.renamedCandidate != f.candidatePath || f.removedCandidatePath != f.candidatePath {
		t.Fatalf(
			"expected one candidate cleanup after rename failure, calls=%d candidate=%q renamed=%q removed=%q",
			f.removeCalls,
			f.candidatePath,
			f.renamedCandidate,
			f.removedCandidatePath,
		)
	}
}

func TestWriteFileReplacingAtomicallyWithExactPermissionsPreservesOldTargetOnWindowsRenameFailure(t *testing.T) {
	fixture := newWindowsStrictAtomicFailureFixture(t)
	const replacement = "complete-replacement-key"
	err := fixture.writeRoot().WriteFileReplacingAtomicallyWithExactPermissions(writeTestFileName, []byte(replacement), 0o600)
	fixture.assertPreserved(t, err)
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

func TestWriteFileReplacingWithinRootReturnsPinnedSyncErrorAfterWindowsFallback(t *testing.T) {
	targetInfoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(targetInfoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	syncErr := errors.New("pinned target sync failure")
	target := &windowsFallbackTarget{
		data:    []byte("before"),
		syncErr: syncErr,
	}
	root, rootState := newAppearingWindowsFallbackRoot(
		t,
		statTestPath(t, targetInfoPath),
		target,
		nil,
	)

	err := WriteFileReplacingWithinRoot(root, writeTestFileName, []byte("after"), 0o600)
	if !errors.Is(err, syncErr) {
		t.Fatalf("expected pinned target sync error, got %v", err)
	}
	if rootState.removeCalls != 1 {
		t.Fatalf("expected one temp cleanup remove, got %d", rootState.removeCalls)
	}
	if target.syncCalls != 1 {
		t.Fatalf("expected one pinned target sync, got %d", target.syncCalls)
	}
	if string(target.data) != "after" {
		t.Fatalf("expected fallback overwrite to happen before sync error, got %q", string(target.data))
	}
}

func TestFallbackAtomicReplacementRejectsUnsafeTargetThatAppearsAfterRename(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), writeTestFileName)
	if err := os.WriteFile(infoPath, []byte("before"), 0o640); err != nil {
		t.Fatalf("seed target info path: %v", err)
	}
	info := statTestPath(t, infoPath)

	for _, tt := range unsafeReplacementPathCases(info) {
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

			err := fallbackAtomicReplacement(root, ".safeio-atomic-temp", writeTestFileName, nil, []byte("after"), 0o600, false, renameErr)
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
	chmodErr      error
	syncErr       error
	openCalls     int
	closeCalls    int
	chmodCalls    int
	syncCalls     int
	truncateCalls int
	writeCalls    int
	chmodPerm     os.FileMode
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
			chmod: func(perm os.FileMode) error {
				s.chmodCalls++
				s.chmodPerm = perm
				return s.chmodErr
			},
			sync: func() error {
				s.syncCalls++
				return s.syncErr
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

func windowsFallbackOpenFile(target *windowsFallbackTarget, targetFile File) func(string, int, os.FileMode) (File, error) {
	return func(name string, _ int, _ os.FileMode) (File, error) {
		if name == writeTestFileName {
			target.openCalls++
			return targetFile, nil
		}
		return &fakeFile{
			write: func(p []byte) (int, error) { return len(p), nil },
			chmod: chmodWithoutError,
			close: closeWithoutError,
		}, nil
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
		openFile: windowsFallbackOpenFile(target, targetFile),
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

	err := fallbackAtomicReplacement(root, ".safeio-atomic-temp", writeTestFileName, nil, []byte("after"), 0o600, false, renameErr)
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

func TestSyncRootDirectoryWindowsIsNoOp(t *testing.T) {
	openCalls := 0
	err := syncRootDirectory(&fakeRoot{
		open: func(string) (File, error) {
			openCalls++
			return nil, syscall.ERROR_ACCESS_DENIED
		},
	})
	if err != nil {
		t.Fatalf("expected Windows directory sync to be a no-op, got %v", err)
	}
	if openCalls != 0 {
		t.Fatalf("expected no directory handle open for Windows sync, got %d calls", openCalls)
	}
}
