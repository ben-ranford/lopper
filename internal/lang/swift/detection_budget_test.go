package swift

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSwiftRootCarthageProbeHonorsCancellation(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", swiftMainFileName), "import Foundation\n")

	if _, _, err := probeSwiftSourceWithinRoot(testutil.CanceledContext(), repo, maxRootCarthageSourceTraversalEntries); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected already-canceled root probe to return context.Canceled, got %v", err)
	}

	ctx := newSwiftCancellationAfterContext(3)
	if _, _, err := probeSwiftSourceWithinRoot(ctx, repo, maxRootCarthageSourceTraversalEntries); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation during root metadata traversal, got %v", err)
	}

	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close test root: %v", err)
		}
	})
	ctx = newSwiftCancellationAfterContext(2)
	if _, _, err := findSwiftSourceWithinRootDirectory(ctx, root, "Sources", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation while reading source candidates, got %v", err)
	}
}

func TestSwiftCarthageProbeIgnoresOnlyPureSentinelErrors(t *testing.T) {
	operationalErr := errors.New("directory read failed")
	pureEOF := &swiftReadDirTestFile{readErr: io.EOF}
	if _, complete, err := readRootCarthageSourceBatch(pureEOF, 1); err != nil || !complete {
		t.Fatalf("pure EOF batch = complete=%v err=%v, want complete without error", complete, err)
	}
	repo := t.TempDir()
	swiftPath := filepath.Join(repo, swiftMainFileName)
	testutil.MustWriteFile(t, swiftPath, "import Foundation\n")
	info, err := os.Lstat(swiftPath)
	if err != nil {
		t.Fatalf("stat partial Swift entry: %v", err)
	}
	mixedEOF := &swiftReadDirTestFile{
		entries: []fs.DirEntry{fs.FileInfoToDirEntry(info)},
		readErr: errors.Join(io.EOF, operationalErr),
	}
	if entries, complete, err := readRootCarthageSourceBatch(mixedEOF, 1); complete || !errors.Is(err, operationalErr) || len(entries) != 1 {
		t.Fatalf("mixed EOF batch = entries=%v complete=%v err=%v, want operational error", entries, complete, err)
	}
	if !isIgnorableNestedCarthageProbeError(safeio.ErrTargetPathSymlink) || isIgnorableNestedCarthageProbeError(errors.Join(safeio.ErrTargetPathSymlink, operationalErr)) {
		t.Fatal("expected mixed nested probe error to remain operational")
	}
}

type swiftReadDirTestFile struct {
	entries []fs.DirEntry
	readErr error
}

type swiftInfoErrorDirEntry struct {
	name string
	err  error
	mode fs.FileMode
}

func (e *swiftInfoErrorDirEntry) Name() string               { return e.name }
func (*swiftInfoErrorDirEntry) IsDir() bool                  { return false }
func (e *swiftInfoErrorDirEntry) Type() fs.FileMode          { return e.mode }
func (e *swiftInfoErrorDirEntry) Info() (fs.FileInfo, error) { return nil, e.err }

func TestSwiftRegularEntryInfoErrorsPropagateOrIgnoreSentinels(t *testing.T) {
	for _, test := range []struct {
		name    string
		entry   fs.DirEntry
		wantErr error
	}{
		{name: "EIO", entry: &swiftInfoErrorDirEntry{name: "source.swift", err: syscall.EIO}, wantErr: syscall.EIO},
		{name: "disappeared", entry: &swiftInfoErrorDirEntry{name: "source.swift", err: fs.ErrNotExist}},
		{name: "symlink", entry: &swiftInfoErrorDirEntry{name: "source.swift", err: syscall.EIO}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := test.entry
			if test.name == "symlink" {
				entry = &swiftInfoErrorDirEntry{name: "source.swift", err: syscall.EIO, mode: fs.ModeSymlink}
			}
			regular, err := isRegularSwiftSource(entry)
			if regular || !errors.Is(err, test.wantErr) {
				t.Fatalf("regular=%v err=%v, want err=%v", regular, err, test.wantErr)
			}
		})
	}
	entry := &swiftInfoErrorDirEntry{name: "Cartfile", err: syscall.EIO}
	if _, _, err := carthageDetectionConfidence(entry); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Cartfile Info error = %v, want EIO", err)
	}
	if _, err := collectRootCarthageSourceCandidates([]fs.DirEntry{&swiftInfoErrorDirEntry{name: "source.swift", err: syscall.EIO}}, rootCarthageSourceDirectory{}, nil); !errors.Is(err, syscall.EIO) {
		t.Fatalf("cursor candidate Info error = %v, want EIO", err)
	}
}

func TestSwiftSourceCursorPropagatesInfoErrorsWithEntryCounts(t *testing.T) {
	for _, test := range []struct {
		name     string
		infoErr  error
		closeErr error
		wantErr  error
	}{
		{name: "EMFILE", infoErr: syscall.EMFILE, wantErr: syscall.EMFILE},
		{name: "joined EMFILE and close", infoErr: syscall.EMFILE, closeErr: syscall.EIO, wantErr: syscall.EIO},
		{name: "EIO", infoErr: syscall.EIO, wantErr: syscall.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			handles := &rootCarthageSourceHandleBudget{used: 1}
			cursor := &rootCarthageSourceCursor{
				candidate: rootCarthageSourceDirectory{path: "Sources", depth: 1},
				directory: &swiftCloseErrorReadDirFile{
					swiftReadDirTestFile: swiftReadDirTestFile{entries: []fs.DirEntry{&swiftInfoErrorDirEntry{name: "source.swift", err: test.infoErr}}, readErr: io.EOF},
					closeErr:             test.closeErr,
				},
				handles: handles,
				cost:    1,
			}
			_, complete, blocked, entries, err := advanceRootCarthageSourceCursor(context.Background(), nil, cursor, handles, 1)
			if !complete || blocked || entries != 1 || !errors.Is(err, test.infoErr) || (test.closeErr != nil && !errors.Is(err, test.wantErr)) {
				t.Fatalf("cursor result complete=%v blocked=%v entries=%d err=%v", complete, blocked, entries, err)
			}
			if cursor.directory != nil {
				t.Fatal("cursor directory remained open after Info error")
			}
			if handles.used != 0 {
				t.Fatalf("retained handle cost = %d, want 0 after close", handles.used)
			}
		})
	}
}

type swiftCloseTrackingReadDirFile struct {
	swiftReadDirTestFile
	closed bool
}

func (f *swiftCloseTrackingReadDirFile) Close() error {
	f.closed = true
	return nil
}

type swiftCloseErrorReadDirFile struct {
	swiftReadDirTestFile
	closeErr error
}

func (f *swiftCloseErrorReadDirFile) Close() error { return f.closeErr }

type swiftInfoReadDirFile struct {
	swiftCloseTrackingReadDirFile
	info fs.FileInfo
}

func (f *swiftInfoReadDirFile) Stat() (fs.FileInfo, error) { return f.info, nil }

func (f *swiftReadDirTestFile) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (f *swiftReadDirTestFile) Write(data []byte) (int, error) {
	return len(data), nil
}

func (f *swiftReadDirTestFile) Close() error {
	return nil
}

func (f *swiftReadDirTestFile) Stat() (fs.FileInfo, error) {
	return nil, errors.New("unexpected stat")
}

func (f *swiftReadDirTestFile) Chmod(fs.FileMode) error {
	return nil
}

func (f *swiftReadDirTestFile) ReadDir(int) ([]fs.DirEntry, error) {
	return f.entries, f.readErr
}

func TestSwiftRootCarthageSourceSubtreeSchedulingAndCleanup(t *testing.T) {
	open := &swiftCloseTrackingReadDirFile{}
	queue := appendRootCarthageSourceDirectories([]rootCarthageSourceDirectory{{path: "existing"}}, []rootCarthageSourceDirectory{{path: "z"}, {path: "a"}})
	if got := []string{queue[1].path, queue[2].path}; !slices.Equal(got, []string{"a", "z"}) {
		t.Fatalf("appended child order = %#v, want [a z]", got)
	}
	subtrees := newRootCarthageSourceSubtrees([]string{"first", "second"})
	if len(subtrees) != 2 || subtrees[0].pending[0].path != "first" {
		t.Fatalf("initial subtree state = %#v", subtrees)
	}
	subtrees[0].current = &rootCarthageSourceCursor{directory: open}
	if err := closeRootCarthageSourceSubtrees(subtrees); err != nil || !open.closed || subtrees[0].current.directory != nil {
		t.Fatalf("close suspended cursor = closed=%v err=%v, want closed without error", open.closed, err)
	}
}

func TestSwiftRootCarthageSourceHandleBudgetTracksPinnedPathCost(t *testing.T) {
	if got := rootCarthageSourcePathCost("."); got != 1 {
		t.Fatalf("root path cost = %d, want 1", got)
	}
	repo := t.TempDir()
	for _, path := range []string{"one/two", "one/two/three"} {
		if err := os.MkdirAll(filepath.Join(repo, path), 0o750); err != nil {
			t.Fatalf("make %q: %v", path, err)
		}
	}
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close test root: %v", err)
		}
	})
	handles := &rootCarthageSourceHandleBudget{}
	first := &rootCarthageSourceCursor{candidate: rootCarthageSourceDirectory{path: "one/two", depth: 2}}
	second := &rootCarthageSourceCursor{candidate: rootCarthageSourceDirectory{path: "one/two/three", depth: 3}}

	if opened, err := openRootCarthageSourceCursor(root, first, handles); err != nil || !opened || handles.used != 2 {
		t.Fatalf("open two-component cursor = opened=%v used=%d err=%v", opened, handles.used, err)
	}
	if opened, err := openRootCarthageSourceCursor(root, second, handles); err != nil || !opened || handles.used != 5 {
		t.Fatalf("open three-component cursor = opened=%v used=%d err=%v", opened, handles.used, err)
	}
	if err := closeRootCarthageSourceCursor(first, nil); err != nil || handles.used != 3 {
		t.Fatalf("close first cursor = used=%d err=%v", handles.used, err)
	}
	if err := closeRootCarthageSourceCursor(second, nil); err != nil || handles.used != 0 {
		t.Fatalf("close second cursor = used=%d err=%v", handles.used, err)
	}

	if !handles.acquire(rootCarthageSourceSharedHandleLimit+1) || handles.used != rootCarthageSourceSharedHandleLimit+1 {
		t.Fatalf("exclusive oversize lease = used=%d", handles.used)
	}
	if handles.acquire(1) {
		t.Fatal("expected oversize lease to exclude additional cursors")
	}
	handles.release(rootCarthageSourceSharedHandleLimit + 1)

	failed := &rootCarthageSourceCursor{candidate: rootCarthageSourceDirectory{path: "missing", depth: 1}}
	if opened, err := openRootCarthageSourceCursor(root, failed, handles); err == nil || opened || handles.used != 0 {
		t.Fatalf("failed open = opened=%v used=%d err=%v", opened, handles.used, err)
	}
}

func TestSwiftRootCarthageSourceSchedulerResumesBlockedSubtree(t *testing.T) {
	const crowdedDirectories = rootCarthageSourceSharedHandleLimit
	repo := t.TempDir()
	directories := make([]string, 0, crowdedDirectories+1)
	for index := 0; index < crowdedDirectories; index++ {
		path := "directory" + strconv.Itoa(index)
		directories = append(directories, path)
		writeSwiftProbeFiles(t, filepath.Join(repo, path), rootCarthageSourceReadBatchSize, false)
	}
	directories = append(directories, "late")
	testutil.MustWriteFile(t, filepath.Join(repo, "late", "late.swift"), "import Foundation\n")
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close test root: %v", err)
		}
	})

	found, entries, err := findSwiftSourceWithinResumableSubtrees(context.Background(), root, newRootCarthageSourceSubtrees(directories), maxRootCarthageSourceTraversalEntries)
	if err != nil || !found || entries > maxRootCarthageSourceTraversalEntries {
		t.Fatalf("blocked subtree scheduler = found=%v entries=%d err=%v", found, entries, err)
	}
}

func TestSwiftRootCarthageSourceSchedulerResumesDeepBlockedSubtree(t *testing.T) {
	repo := t.TempDir()
	directories := make([]string, 0, 5)
	for index := 0; index < 4; index++ {
		path := filepath.Join("directory"+strconv.Itoa(index), "one", "two", "three")
		directories = append(directories, path)
		writeSwiftProbeFiles(t, filepath.Join(repo, path), rootCarthageSourceReadBatchSize, false)
	}
	late := filepath.Join("late", "one", "two", "three")
	directories = append(directories, late)
	testutil.MustWriteFile(t, filepath.Join(repo, late, "late.swift"), "import Foundation\n")
	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close test root: %v", err)
		}
	})

	found, entries, err := findSwiftSourceWithinResumableSubtrees(context.Background(), root, newRootCarthageSourceSubtrees(directories), maxRootCarthageSourceTraversalEntries)
	if err != nil || !found || entries > maxRootCarthageSourceTraversalEntries {
		t.Fatalf("deep blocked subtree scheduler = found=%v entries=%d err=%v", found, entries, err)
	}
}

func TestSwiftRootCarthageSourceSchedulerPrioritizesLeaseWaiterAfterDrain(t *testing.T) {
	waiter := &rootCarthageSourceSubtree{}
	unopened := &rootCarthageSourceSubtree{}
	owner := &rootCarthageSourceSubtree{current: &rootCarthageSourceCursor{directory: &swiftReadDirTestFile{}}}
	later := &rootCarthageSourceSubtree{}
	state := rootCarthageSourceResumeState{
		queue:         []*rootCarthageSourceSubtree{unopened, owner, later},
		leaseWaiter:   waiter,
		drainForLease: true,
	}

	next := state.next()
	if next != owner || state.leaseWaiter != waiter || len(state.queue) != 2 || state.queue[0] != unopened {
		t.Fatalf("lease drain selection = next=%p waiter=%p queue=%#v", next, state.leaseWaiter, state.queue)
	}
	state.drainForLease = false
	next = state.next()
	if next != waiter || state.leaseWaiter != nil || len(state.queue) != 2 || state.queue[0] != unopened {
		t.Fatalf("post-drain waiter selection = next=%p waiter=%p queue=%#v", next, state.leaseWaiter, state.queue)
	}
}

func TestSwiftRootCarthageSourceResumeStateQueuesAdditionalLeaseWaiters(t *testing.T) {
	state := newRootCarthageSourceResumeState(nil, nil)
	if state.hasWork() || state.queueLength() != 0 {
		t.Fatalf("empty resume state = %#v", state)
	}
	first := &rootCarthageSourceSubtree{}
	second := &rootCarthageSourceSubtree{}
	state.waitForLease(first)
	state.waitForLease(second)
	if !state.drainForLease || state.leaseWaiter != first || len(state.queue) != 1 || state.queue[0] != second {
		t.Fatalf("lease waiters = %#v", state)
	}
}

func TestSwiftRootCarthageSourceSchedulerRejectsUndrainableLeaseWaiter(t *testing.T) {
	_, _, err := resumeRootCarthageSourceSubtrees(context.Background(), nil, nil,
		[]*rootCarthageSourceSubtree{{}}, &rootCarthageSourceHandleBudget{}, 1, 0)
	if err == nil || !strings.Contains(err.Error(), "could not drain a blocked subtree") {
		t.Fatalf("undrainable lease waiter error = %v", err)
	}
}

func TestSwiftRootCarthageSourceSchedulerReleasesLeaseOnCanceledCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	handles := &rootCarthageSourceHandleBudget{}
	cursor := &rootCarthageSourceCursor{
		candidate: rootCarthageSourceDirectory{path: "deep/path", depth: 2},
		directory: &swiftCloseErrorReadDirFile{closeErr: closeErr},
		handles:   handles,
		cost:      2,
	}
	if !handles.acquire(cursor.cost) {
		t.Fatal("acquire test lease")
	}
	subtree := &rootCarthageSourceSubtree{current: cursor}
	_, _, _, _, err := advanceRootCarthageSourceSubtree(testutil.CanceledContext(), nil, subtree, handles, 1)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, closeErr) || handles.used != 0 || cursor.directory != nil {
		t.Fatalf("canceled close = used=%d directory=%v err=%v", handles.used, cursor.directory, err)
	}
}

func TestSwiftRootCarthageProbeFindsRootSourceAndRejectsInvalidCandidate(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "App.SWIFT"), "import Foundation\n")

	found, _, err := probeSwiftSourceWithinRoot(context.Background(), repo, maxRootCarthageSourceTraversalEntries)
	if err != nil || !found {
		t.Fatalf("expected regular root Swift source to corroborate metadata, found=%v err=%v", found, err)
	}

	root, err := safeio.OpenRootNoFollow(repo)
	if err != nil {
		t.Fatalf("open test root: %v", err)
	}
	if _, _, err := findSwiftSourceWithinRootDirectory(context.Background(), root, "missing", 1); err == nil {
		t.Fatal("expected invalid source candidate directory to fail")
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close test root: %v", err)
	}
	if _, _, _, err := discoverSwiftSourceCandidatesWithinLimit(context.Background(), root, ".", maxRootCarthageSourceTraversalEntries); err == nil {
		t.Fatal("expected closed root to reject candidate discovery")
	}
}

func TestSwiftRootCarthageProbeRequiresRegularNonSymlinkSource(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "notes.txt"), "not a Swift source\n")
	if err := os.Mkdir(filepath.Join(repo, "Sources"), 0o750); err != nil {
		t.Fatalf("mkdir source directory: %v", err)
	}

	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outside, swiftMainFileName), "import Foundation\n")
	if err := os.Symlink(filepath.Join(outside, swiftMainFileName), filepath.Join(repo, "Sources", swiftMainFileName)); err != nil {
		t.Fatalf("symlink Swift source: %v", err)
	}

	found, _, err := probeSwiftSourceWithinRoot(context.Background(), repo, maxRootCarthageSourceTraversalEntries)
	if err != nil {
		t.Fatalf("probe symlinked source: %v", err)
	}
	if found {
		t.Fatal("expected symlinked Swift source to be ignored as Carthage corroboration")
	}
}

func TestSwiftRootCarthageProbeAllowsRequestedRootAliases(t *testing.T) {
	for _, test := range []struct {
		name     string
		ancestor bool
	}{
		{name: "root symlink"},
		{name: "ancestor symlink", ancestor: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, requested := swiftRequestedRootAlias(t, test.ancestor)
			assertSwiftRootCarthageAliasMetadata(t, resolved, requested)
			assertSwiftRootCarthageAliasSource(t, resolved, requested)
		})
	}
}

func TestSwiftDetectionCanonicalizesTrailingSeparatorAcrossRootSignals(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, packageManifestName), "// swift-tools-version: 6.0\n")
	testutil.MustWriteFile(t, filepath.Join(repo, podManifestName), "platform :ios, '17.0'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), "github \"owner/repo\"\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", swiftMainFileName), "import Foundation\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo+string(os.PathSeparator))
	if err != nil {
		t.Fatalf("detect trailing root separator: %v", err)
	}
	if !slices.Equal(detection.Roots, []string{repo}) {
		t.Fatalf("detection roots = %#v, want only %q", detection.Roots, repo)
	}
}

func TestSwiftNestedCarthageDetectionAllowsRequestedRootAlias(t *testing.T) {
	resolved, requested := swiftRequestedRootAlias(t, false)
	nestedRoot := filepath.Join(resolved, "apps", "ios")
	testutil.MustWriteFile(t, filepath.Join(nestedRoot, carthageManifestName), "github \"owner/repo\"\n")
	testutil.MustWriteFile(t, filepath.Join(nestedRoot, "Sources", swiftMainFileName), "import Foundation\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), requested)
	if err != nil {
		t.Fatalf("detect nested Carthage project through alias: %v", err)
	}
	wantRoot := filepath.Join(requested, "apps", "ios")
	if !detection.Matched || !slices.Contains(detection.Roots, wantRoot) {
		t.Fatalf("expected requested nested root to be retained, got %#v", detection)
	}
}

func TestSwiftCarthageProbeIgnoresOnlyPureEMFILE(t *testing.T) {
	emfile := &fs.PathError{Op: "open", Path: "nested", Err: syscall.EMFILE}
	if isIgnorableNestedCarthageProbeError(emfile) || !isIgnorableCarthageProbeResourceError(emfile) {
		t.Fatal("expected wrapped EMFILE to be ignorable for optional corroboration")
	}
	closeErr := errors.New("close failed")
	joined := errors.Join(emfile, closeErr)
	if isIgnorableNestedCarthageProbeError(joined) || isIgnorableCarthageProbeResourceError(joined) {
		t.Fatalf("joined EMFILE cleanup error must propagate: %v", joined)
	}
	if isIgnorableNestedCarthageProbeError(syscall.ENFILE) || isIgnorableCarthageProbeResourceError(syscall.ENFILE) {
		t.Fatal("ENFILE must remain operational")
	}
	if found, entries, err := probeSwiftSourceWithinTrustedRoot(context.Background(), &swiftEMFILEProbeRoot{}, "nested", 1); err != nil || found || entries != 0 {
		t.Fatalf("EMFILE discovery probe = found=%v entries=%d err=%v", found, entries, err)
	}
}

func TestSwiftOptionalProbeClassifiesDirectoryEntryInfoEMFILE(t *testing.T) {
	repo := t.TempDir()
	directoryInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat probe directory: %v", err)
	}

	for _, test := range []struct {
		name    string
		infoErr error
		wantErr error
	}{
		{name: "pure EMFILE", infoErr: syscall.EMFILE},
		{name: "joined EMFILE and EIO", infoErr: errors.Join(syscall.EMFILE, syscall.EIO), wantErr: syscall.EIO},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := &swiftInfoReadDirFile{
				swiftCloseTrackingReadDirFile: swiftCloseTrackingReadDirFile{
					swiftReadDirTestFile: swiftReadDirTestFile{
						entries: []fs.DirEntry{&swiftInfoErrorDirEntry{name: "source.swift", err: test.infoErr}},
						readErr: io.EOF,
					},
				},
				info: directoryInfo,
			}
			root := &swiftEMFILEProbeRoot{info: directoryInfo, directory: directory}
			found, entries, probeErr := probeSwiftSourceWithinTrustedRoot(context.Background(), root, ".", 1)
			if found || entries != 1 || !errors.Is(probeErr, test.wantErr) {
				t.Fatalf("probe result found=%v entries=%d err=%v", found, entries, probeErr)
			}
			if !directory.closed {
				t.Fatal("probe directory was not closed")
			}
		})
	}
}

func TestSwiftInfoErrorsReachDiscoveryAndRecording(t *testing.T) {
	root := &swiftEMFILEProbeRoot{lstatErr: &fs.PathError{Op: "lstat", Path: "Sources", Err: syscall.EIO}}
	if _, entries, _, err := discoverSwiftSourceCandidatesWithinLimit(context.Background(), root, "Sources", 1); entries != 0 || !errors.Is(err, syscall.EIO) {
		t.Fatalf("direct discovery result entries=%d err=%v, want EIO", entries, err)
	}

	detection := language.Detection{}
	visited := 0
	entry := &swiftInfoErrorDirEntry{name: "source.swift", err: syscall.EIO}
	err := detectSwiftEntry(context.Background(), "source.swift", entry, &detection, map[string]struct{}{}, &visited)
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("broad recording entry error = %v, want EIO", err)
	}
}

func TestSwiftDirectoryReadErrorsCloseAndReleaseHandles(t *testing.T) {
	repo := t.TempDir()
	directoryInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat probe directory: %v", err)
	}
	directory := &swiftInfoReadDirFile{
		swiftCloseTrackingReadDirFile: swiftCloseTrackingReadDirFile{
			swiftReadDirTestFile: swiftReadDirTestFile{readErr: syscall.EIO},
		},
		info: directoryInfo,
	}
	root := &swiftEMFILEProbeRoot{info: directoryInfo, directory: directory}
	if _, entries, _, err := discoverSwiftSourceCandidatesWithinLimit(context.Background(), root, ".", 1); entries != 0 || !errors.Is(err, syscall.EIO) {
		t.Fatalf("direct read error result entries=%d err=%v, want EIO", entries, err)
	}
	if !directory.closed {
		t.Fatal("direct discovery directory was not closed")
	}

	handles := &rootCarthageSourceHandleBudget{used: 1}
	cursor := &rootCarthageSourceCursor{
		directory: &swiftCloseErrorReadDirFile{
			swiftReadDirTestFile: swiftReadDirTestFile{readErr: syscall.EIO},
		},
		handles: handles,
		cost:    1,
	}
	_, complete, blocked, entries, err := advanceRootCarthageSourceCursor(context.Background(), nil, cursor, handles, 1)
	if !complete || blocked || entries != 0 || !errors.Is(err, syscall.EIO) {
		t.Fatalf("cursor read error result complete=%v blocked=%v entries=%d err=%v", complete, blocked, entries, err)
	}
	if cursor.directory != nil || handles.used != 0 {
		t.Fatalf("cursor cleanup directory=%v retained handles=%d", cursor.directory, handles.used)
	}
}

type swiftEMFILEProbeRoot struct {
	lstatErr  error
	info      fs.FileInfo
	directory safeio.File
}

func (r *swiftEMFILEProbeRoot) Open(string) (safeio.File, error) {
	if r.directory != nil {
		return r.directory, nil
	}
	return nil, errors.New("unexpected open")
}
func (*swiftEMFILEProbeRoot) OpenFile(string, int, os.FileMode) (safeio.File, error) {
	return nil, errors.New("unexpected open file")
}
func (*swiftEMFILEProbeRoot) OpenRoot(string) (safeio.Root, error) {
	return nil, errors.New("unexpected open root")
}
func (r *swiftEMFILEProbeRoot) Lstat(string) (fs.FileInfo, error) {
	if r.lstatErr != nil {
		return nil, r.lstatErr
	}
	if r.info != nil {
		return r.info, nil
	}
	return nil, &fs.PathError{Op: "lstat", Path: "nested", Err: syscall.EMFILE}
}
func (*swiftEMFILEProbeRoot) Mkdir(string, os.FileMode) error { return errors.New("unexpected mkdir") }
func (*swiftEMFILEProbeRoot) Chmod(string, os.FileMode) error { return errors.New("unexpected chmod") }
func (*swiftEMFILEProbeRoot) MkdirAll(string, os.FileMode) error {
	return errors.New("unexpected mkdir all")
}
func (*swiftEMFILEProbeRoot) Link(string, string) error   { return errors.New("unexpected link") }
func (*swiftEMFILEProbeRoot) Rename(string, string) error { return errors.New("unexpected rename") }
func (*swiftEMFILEProbeRoot) Remove(string) error         { return errors.New("unexpected remove") }
func (*swiftEMFILEProbeRoot) Close() error                { return nil }

func TestSwiftNestedCarthageDetectionRetainsAlreadyCorroboratedDeepRoot(t *testing.T) {
	repo := t.TempDir()
	nested := repo
	for index := 0; index < rootCarthageSourceSharedHandleLimit+1; index++ {
		nested = filepath.Join(nested, "level"+strconv.Itoa(index))
	}
	testutil.MustWriteFile(t, filepath.Join(nested, carthageManifestName), "github \"owner/repo\"\n")
	testutil.MustWriteFile(t, filepath.Join(nested, swiftMainFileName), "import Foundation\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect deep corroborated root: %v", err)
	}
	if !detection.Matched || !slices.Contains(detection.Roots, nested) {
		t.Fatalf("deep corroborated detection = %#v", detection)
	}
}

func assertSwiftRootCarthageAliasMetadata(t *testing.T, resolved, requested string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(resolved, carthageManifestName), "github \"owner/repo\"\n")
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), requested)
	if err != nil {
		t.Fatalf("detect metadata-only alias: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected metadata-only alias to remain uncorroborated, got %#v", detection)
	}
}

func assertSwiftRootCarthageAliasSource(t *testing.T, resolved, requested string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(resolved, "Sources", swiftMainFileName), "import Foundation\n")
	detection, err := NewAdapter().DetectWithConfidence(context.Background(), requested)
	if err != nil {
		t.Fatalf("detect Swift source through alias: %v", err)
	}
	if !detection.Matched || !slices.Contains(detection.Roots, requested) {
		t.Fatalf("expected requested alias root to be retained, got %#v", detection)
	}
}

func TestSwiftRootCarthageProbeRejectsSymlinkBelowRequestedRoot(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), "github \"owner/repo\"\n")
	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outside, swiftMainFileName), "import Foundation\n")
	if err := os.Symlink(outside, filepath.Join(repo, "Sources")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect symlinked child: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected symlinked child source to be ignored, got %#v", detection)
	}
}

func swiftRequestedRootAlias(t *testing.T, ancestor bool) (string, string) {
	t.Helper()
	resolved := t.TempDir()
	linkTarget := resolved
	if ancestor {
		resolved = filepath.Join(resolved, "repo")
		if err := os.Mkdir(resolved, 0o750); err != nil {
			t.Fatalf("mkdir resolved repo: %v", err)
		}
		linkTarget = filepath.Dir(resolved)
	}
	alias := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(linkTarget, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if ancestor {
		alias = filepath.Join(alias, "repo")
	}
	return resolved, alias
}

func TestSwiftDetectionRequiresRegularCarthageAndSwiftEntries(t *testing.T) {
	detectBroadSignals := func(repo string) (language.Detection, error) {
		detection := language.Detection{}
		err := walkSwiftDetection(context.Background(), repo, &detection, map[string]struct{}{}, rootCarthagePreflight{})
		return detection, err
	}
	for _, test := range []struct {
		name            string
		regularCarthage bool
		regularSwift    bool
		wantMatched     bool
		wantConfidence  int
		detect          func(string) (language.Detection, error)
	}{
		{name: "regular Cartfile with symlink Swift", regularCarthage: true, detect: func(repo string) (language.Detection, error) {
			return NewAdapter().DetectWithConfidence(context.Background(), repo)
		}},
		{name: "regular Swift with symlink Cartfile", regularSwift: true, wantMatched: true, wantConfidence: 2, detect: detectBroadSignals},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := t.TempDir()
			writeSwiftDetectionRegularityFixture(t, repo, test.regularCarthage, test.regularSwift)

			detection, err := test.detect(repo)
			if err != nil {
				t.Fatalf("detect non-regular entries: %v", err)
			}
			if detection.Matched != test.wantMatched || detection.Confidence != test.wantConfidence {
				t.Fatalf("detection = %#v, want matched=%v confidence=%d", detection, test.wantMatched, test.wantConfidence)
			}
		})
	}
}

func writeSwiftDetectionRegularityFixture(t *testing.T, repo string, regularCarthage, regularSwift bool) {
	t.Helper()

	outside := t.TempDir()
	cartfilePath := filepath.Join(repo, carthageManifestName)
	swiftPath := filepath.Join(repo, "main.swift")
	testutil.MustWriteFile(t, filepath.Join(outside, carthageManifestName), "github \"owner/repo\"\n")
	testutil.MustWriteFile(t, filepath.Join(outside, "main.swift"), "import Foundation\n")
	if regularCarthage {
		testutil.MustWriteFile(t, cartfilePath, "github \"owner/repo\"\n")
	} else if err := os.Symlink(filepath.Join(outside, carthageManifestName), cartfilePath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if regularSwift {
		testutil.MustWriteFile(t, swiftPath, "import Foundation\n")
	} else if err := os.Symlink(filepath.Join(outside, "main.swift"), swiftPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
}

func TestSwiftNestedCarthageProbeSharesBudgetFairly(t *testing.T) {
	repo := t.TempDir()
	first := filepath.Join(repo, "a-first")
	second := filepath.Join(repo, "b-second")
	writeSwiftProbeFiles(t, first, maxNestedCarthageSourceTraversalEntries, false)
	writeSwiftProbeFiles(t, second, 0, true)

	detection := language.Detection{}
	roots := map[string]struct{}{}
	err := applyCarthageDetectionRoots(context.Background(), repo, &detection, roots,
		map[string]int{first: 10, second: 10}, map[string]struct{}{})
	if err != nil {
		t.Fatalf("probe nested Carthage roots: %v", err)
	}
	if _, foundFirst := roots[first]; !detection.Matched || !rootsContain(roots, second) || foundFirst {
		t.Fatalf("expected the fairly budgeted second root to be retained, got detection=%#v roots=%#v", detection, roots)
	}
}

func TestSwiftRootCarthageProbeReusesBudgetReleasedByLaterEmptyDirectories(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), "github \"owner/repo\"\n")
	for index := 0; index < maxRootCarthageSourceTraversalEntries; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "000-assets", "file"+strconv.Itoa(index)+".txt"), "ignored\n")
	}
	for index := 0; index < 682; index++ {
		child := filepath.Join(repo, "Sources", "child"+strconv.Itoa(index))
		if err := os.MkdirAll(child, 0o750); err != nil {
			t.Fatalf("make source child %q: %v", child, err)
		}
		if index == 681 {
			testutil.MustWriteFile(t, filepath.Join(child, swiftMainFileName), "import Foundation\n")
		}
	}
	if err := os.Mkdir(filepath.Join(repo, "zz-empty"), 0o750); err != nil {
		t.Fatalf("make later empty directory: %v", err)
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect root Carthage project: %v", err)
	}
	if !detection.Matched || !slices.Contains(detection.Roots, repo) {
		t.Fatalf("expected resumable source probe to corroborate root metadata, got %#v", detection)
	}
}

func TestSwiftRootCarthageProbePreservesLateCandidateFairShareAboveCursorLimit(t *testing.T) {
	repo := t.TempDir()
	for directory := 0; directory < rootCarthageSourceSharedHandleLimit; directory++ {
		writeSwiftProbeFiles(t, filepath.Join(repo, "directory"+strconv.Itoa(directory)), rootCarthageSourceReadBatchSize, false)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "late-source", swiftMainFileName), "import Foundation\n")

	found, entries, err := probeSwiftSourceWithinRoot(context.Background(), repo, maxRootCarthageSourceTraversalEntries)
	if err != nil || !found || entries > maxRootCarthageSourceTraversalEntries {
		t.Fatalf("wide candidate probe = found=%v entries=%d err=%v", found, entries, err)
	}
}

func TestSwiftRootCarthageProbeReusesBudgetAboveCursorLimit(t *testing.T) {
	repo := t.TempDir()
	for index := 0; index < maxRootCarthageSourceTraversalEntries; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "000-assets", "file"+strconv.Itoa(index)+".txt"), "ignored\n")
	}
	for index := 0; index < 131; index++ {
		child := filepath.Join(repo, "Sources", "child"+strconv.Itoa(index))
		if err := os.MkdirAll(child, 0o750); err != nil {
			t.Fatalf("make source child %q: %v", child, err)
		}
		if index == 130 {
			testutil.MustWriteFile(t, filepath.Join(child, swiftMainFileName), "import Foundation\n")
		}
	}
	for index := 0; index < 15; index++ {
		if err := os.Mkdir(filepath.Join(repo, "zz-empty"+strconv.Itoa(index)), 0o750); err != nil {
			t.Fatalf("make later empty directory: %v", err)
		}
	}

	found, entries, err := probeSwiftSourceWithinRoot(context.Background(), repo, maxRootCarthageSourceTraversalEntries)
	if err != nil || !found || entries > maxRootCarthageSourceTraversalEntries {
		t.Fatalf("wide reuse probe = found=%v entries=%d err=%v", found, entries, err)
	}
}

func TestSwiftRootCarthageProbePreservesDeepCandidateFairShare(t *testing.T) {
	repo := t.TempDir()
	deep := filepath.Join(repo, "a-deep")
	for level := 0; level < maxRootCarthageSourceDepth-1; level++ {
		deep = filepath.Join(deep, "level"+strconv.Itoa(level))
	}
	testutil.MustWriteFile(t, filepath.Join(deep, swiftMainFileName), "import Foundation\n")
	writeSwiftProbeFiles(t, filepath.Join(repo, "b-heavy"), maxRootCarthageSourceTraversalEntries, false)

	found, entries, err := probeSwiftSourceWithinRoot(context.Background(), repo, maxRootCarthageSourceTraversalEntries)
	if err != nil || !found || entries > maxRootCarthageSourceTraversalEntries {
		t.Fatalf("deep candidate probe = found=%v entries=%d err=%v", found, entries, err)
	}
}

func TestSwiftNestedCarthageProbeRejectsReplacedCandidateSymlink(t *testing.T) {
	repo := t.TempDir()
	candidate := filepath.Join(repo, "Packages", "Library")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o750); err != nil {
		t.Fatalf("make candidate parent: %v", err)
	}
	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outside, swiftMainFileName), "import Foundation\n")
	if err := os.Symlink(outside, candidate); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	detection := language.Detection{}
	roots := map[string]struct{}{}
	err := applyCarthageDetectionRoots(context.Background(), repo, &detection, roots,
		map[string]int{candidate: 10}, map[string]struct{}{})
	if err != nil {
		t.Fatalf("probe replaced nested candidate: %v", err)
	}
	if detection.Matched || rootsContain(roots, candidate) {
		t.Fatalf("expected replaced candidate symlink to be ignored, got detection=%#v roots=%#v", detection, roots)
	}
}

func TestSwiftNestedCarthageProbeRejectsUntrustedRoots(t *testing.T) {
	repo := t.TempDir()
	for _, candidate := range []string{repo, filepath.Dir(repo)} {
		if relative, ok := nestedCarthageRootRelativePath(repo, candidate); ok {
			t.Fatalf("untrusted candidate %q was accepted as %q", candidate, relative)
		}
	}

	missingRepo := filepath.Join(repo, "missing")
	err := applyCarthageDetectionRoots(context.Background(), missingRepo, &language.Detection{}, map[string]struct{}{},
		map[string]int{filepath.Join(missingRepo, "Package"): 10}, map[string]struct{}{})
	if err == nil {
		t.Fatal("expected a missing trusted root to reject nested probing")
	}
}

func TestSwiftCarthageProbeReportsActualEntries(t *testing.T) {
	for _, test := range []struct {
		files       int
		budget      int
		wantEntries int
	}{
		{files: 4, budget: 2, wantEntries: 2},
		{files: 4, budget: maxNestedCarthageSourceTraversalEntries, wantEntries: 4},
		{files: 1025, budget: maxRootCarthageSourceTraversalEntries, wantEntries: 1025},
	} {
		t.Run(strconv.Itoa(test.files)+" files", func(t *testing.T) {
			repo := t.TempDir()
			writeSwiftProbeFiles(t, repo, test.files, false)

			found, entries, err := probeSwiftSourceWithinRoot(context.Background(), repo, test.budget)
			if err != nil || found || entries != test.wantEntries {
				t.Fatalf("probe budget %d = found=%v entries=%d err=%v, want found=false entries=%d", test.budget, found, entries, err, test.wantEntries)
			}
		})
	}
}

func writeSwiftProbeFiles(t *testing.T, root string, filesBeforeSource int, includeSource bool) {
	t.Helper()
	for index := 0; index < filesBeforeSource; index++ {
		testutil.MustWriteFile(t, filepath.Join(root, "file"+strconv.Itoa(index)+".txt"), "ignored\n")
	}
	if includeSource {
		testutil.MustWriteFile(t, filepath.Join(root, "zz-source.swift"), "import Foundation\n")
	}
}

func rootsContain(roots map[string]struct{}, root string) bool {
	_, found := roots[root]
	return found
}

func TestSwiftRootCarthageConfidenceCountsOnlyRegularMetadata(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, carthageManifestName), 0o750); err != nil {
		t.Fatalf("mkdir Cartfile: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(repo, carthageResolvedName), "github \"owner/repo\" \"1.0.0\"\n")

	confidence, err := rootCarthageDetectionConfidence(repo)
	if err != nil {
		t.Fatalf("read root Carthage metadata: %v", err)
	}
	if confidence != 25 {
		t.Fatalf("expected only regular Cartfile.resolved metadata to count, got %d", confidence)
	}

	if _, _, err := probeSwiftSourceWithinRoot(context.Background(), filepath.Join(repo, "missing"), maxRootCarthageSourceTraversalEntries); err == nil {
		t.Fatal("expected source probe to reject a missing root")
	}
	if _, err := rootCarthageDetectionConfidence("\x00"); err == nil {
		t.Fatal("expected invalid metadata root to fail")
	}
}

func TestSwiftWalkRetainsUncorroboratedRootCarthagePreflight(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Sources", swiftMainFileName), "import Foundation\n")
	detection := language.Detection{}
	roots := map[string]struct{}{}
	err := walkSwiftDetection(context.Background(), repo, &detection, roots, rootCarthagePreflight{confidence: 60})
	if err != nil {
		t.Fatalf("walk retained root preflight: %v", err)
	}
	if !detection.Matched || detection.Confidence < 60 || !rootsContain(roots, repo) {
		t.Fatalf("expected source corroboration to retain root preflight, got detection=%#v roots=%#v", detection, roots)
	}
}

func TestSwiftCaseVariantCarthageMetadataDoesNotInventRootConfidence(t *testing.T) {
	repo := t.TempDir()
	caseVariant := strings.ToLower(carthageManifestName)
	testutil.MustWriteFile(t, filepath.Join(repo, caseVariant), "github \"owner/repo\"\n")
	entries, err := os.ReadDir(repo)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read case-variant metadata: entries=%#v err=%v", entries, err)
	}
	if confidence, _, err := carthageDetectionConfidence(entries[0]); err != nil || confidence != 10 {
		t.Fatalf("case-variant metadata confidence = %d, want ordinary 10", confidence)
	}
}

func TestSwiftDetectPropagatesCanceledRootCarthageProbe(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, carthageManifestName), "github \"owner/repo\"\n")

	if _, err := NewAdapter().DetectWithConfidence(testutil.CanceledContext(), repo); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled root Carthage probe to abort detection, got %v", err)
	}
}

func TestSwiftCarthageInputGuardsRejectIncompleteMetadata(t *testing.T) {
	if _, ok := parseCarthageLine("github", false); ok {
		t.Fatal("expected incomplete Carthage declaration to be ignored")
	}
	if _, ok := parseCarthageLine(`github ""`, false); ok {
		t.Fatal("expected Carthage declaration without an identity to be ignored")
	}
	if isLikelyCarthageVersion("1.invalid") {
		t.Fatal("expected non-numeric Carthage version segment to be rejected")
	}

}

type swiftCancellationAfterContext struct {
	calls    int
	cancelAt int
	done     chan struct{}
	canceled bool
}

func newSwiftCancellationAfterContext(cancelAt int) *swiftCancellationAfterContext {
	return &swiftCancellationAfterContext{cancelAt: cancelAt, done: make(chan struct{})}
}

func (*swiftCancellationAfterContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *swiftCancellationAfterContext) Done() <-chan struct{} {
	return c.done
}

func (c *swiftCancellationAfterContext) Err() error {
	if c.canceled {
		return context.Canceled
	}
	c.calls++
	if c.calls >= c.cancelAt {
		close(c.done)
		c.canceled = true
		return context.Canceled
	}
	return nil
}

func (*swiftCancellationAfterContext) Value(any) any {
	return nil
}
