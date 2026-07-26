package jvm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestJVMDetectWithConfidencePropagatesHookError(t *testing.T) {
	repo := canonicalRepoPath(t)
	writeJVMPomFile(t, repo, "<project></project>\n")
	hookErr := errors.New("detect hook failed")
	withJVMDetectRootSignalsHook(t, func(string) error { return hookErr })

	_, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if !errors.Is(err, hookErr) {
		t.Fatalf("expected detect hook error, got %v", err)
	}
}

func TestJVMDetectWithConfidencePropagatesTraversalLimitCloseError(t *testing.T) {
	repo := canonicalRepoPath(t)
	testutil.MustWriteFile(t, filepath.Join(repo, "ignored.txt"), "ignored\n")
	entry := testutil.MustFirstFileEntry(t, repo)
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	closeErr := errors.New("close overflowing detection directory")
	directory := &jvmDetectionTestDirectory{
		fillEntry:    entry,
		extraEntries: 1,
		closeErr:     closeErr,
	}
	root := &jvmDetectionLimitTestRoot{info: info, directory: directory}
	withOpenJVMDetectionRootHook(t, func(string) (jvmDetectionRoot, error) {
		return root, nil
	})

	_, err = NewAdapter().DetectWithConfidence(context.Background(), repo)
	if !errors.Is(err, errJVMDetectionTraversalLimit) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined detection traversal-limit and close error, got %v", err)
	}
	if directory.closeCalls != 1 || root.closeCalls != 1 {
		t.Fatalf("expected directory and root to close once, got directory=%d root=%d", directory.closeCalls, root.closeCalls)
	}
}

func TestJVMDetectWithConfidenceIgnoresConfinedCandidateLimitStop(t *testing.T) {
	repo := canonicalRepoPath(t)
	for index := 0; index < defaultJVMMaxConfinedCandidates+1; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", "pkg", "Main"+strconv.Itoa(index)+".java"), "class Main {}\n")
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched || detection.Confidence == 0 {
		t.Fatalf("expected confined candidate limit stop to preserve detection, got %#v", detection)
	}
}

func TestJVMDetectWithConfidenceIgnoresUnrelatedFileFloodBeforeValidSignal(t *testing.T) {
	repo := canonicalRepoPath(t)
	for index := 0; index < defaultJVMMaxConfinedCandidates+64; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "docs", "note"+strconv.Itoa(index)+".txt"), "ignored\n")
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "z-module", "src", "main", "java", "pkg", "Main.java"), "class Main {}\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence after unrelated file flood: %v", err)
	}
	if !detection.Matched || detection.Confidence == 0 {
		t.Fatalf("expected later valid JVM signal to survive unrelated file flood, got %#v", detection)
	}
}

func TestOSJVMDetectionRootOpenRejectsNonDirectoryAndCloseErrors(t *testing.T) {
	rootInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	childPath := filepath.Join(t.TempDir(), "child.txt")
	if err := os.WriteFile(childPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	fileInfo, err := os.Stat(childPath)
	if err != nil {
		t.Fatalf("stat temp file: %v", err)
	}
	closeErr := errors.New("close non-directory")
	root := &osJVMDetectionRoot{root: &jvmRootedTestRoot{
		info: rootInfo,
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "child" {
				return fileInfo, nil
			}
			return rootInfo, nil
		},
		open: func(string) (safeio.File, error) {
			return &jvmRootedTestFile{
				close: func() error { return closeErr },
				stat:  func() (fs.FileInfo, error) { return fileInfo, nil },
			}, nil
		},
	}}

	if _, err := root.Open("child"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected invalid-directory error from non-directory open, got %v", err)
	}

	root = &osJVMDetectionRoot{root: &jvmRootedTestRoot{
		info: rootInfo,
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "child" {
				return fileInfo, nil
			}
			return rootInfo, nil
		},
		open: func(string) (safeio.File, error) {
			return &jvmRootedTestFile{
				stat: func() (fs.FileInfo, error) { return fileInfo, nil },
			}, nil
		},
	}}
	_, err = root.Open("child")
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected non-directory error, got %v", err)
	}
}

func TestOSJVMDetectionRootOpenReturnsDirectory(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	directory := &jvmRootedTestDirectory{
		jvmRootedTestFile: jvmRootedTestFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
		},
	}
	root := &osJVMDetectionRoot{root: &jvmRootedTestRoot{
		info:  info,
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
		open: func(string) (safeio.File, error) {
			return directory, nil
		},
	}}

	opened, err := root.Open("child")
	if err != nil {
		t.Fatalf("expected directory open to succeed, got %v", err)
	}
	if opened != directory {
		t.Fatalf("expected opened directory handle to be returned unchanged, got %#v", opened)
	}
}

func TestOSJVMDetectionRootOpenPropagatesOpenError(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	openErr := errors.New("open rooted directory")
	root := &osJVMDetectionRoot{root: &jvmRootedTestRoot{
		info:  info,
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
		open:  func(string) (safeio.File, error) { return nil, openErr },
	}}

	opened, err := root.Open("child")
	if opened != nil {
		t.Fatal("expected open failure to return no directory handle")
	}
	if !errors.Is(err, openErr) {
		t.Fatalf("expected open error, got %v", err)
	}
}

func TestOSJVMDetectionRootOpenRootPropagatesOpenError(t *testing.T) {
	openErr := errors.New("open pinned child root")
	root := &osJVMDetectionRoot{root: &jvmRootedTestRoot{
		openRoot: func(string) (safeio.Root, error) { return nil, openErr },
	}}

	opened, err := root.OpenRoot("child")
	if opened != nil || !errors.Is(err, openErr) {
		t.Fatalf("expected child-root open error, got root=%#v err=%v", opened, err)
	}
}

func TestJVMDetectionWalkerRejectsDirectoryReplacementBetweenEnumerationAndOpen(t *testing.T) {
	repo := canonicalRepoPath(t)
	originalDir := filepath.Join(repo, "src")
	testutil.MustWriteFile(t, filepath.Join(originalDir, "main", "java", "pkg", "Main.java"), "class Main {}\n")
	root := newReplacingJVMDetectionRoot(t, repo, originalDir)

	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	err := walker.walkPinned(root)
	if err == nil || !strings.Contains(err.Error(), "path changed while opening: src") {
		t.Fatalf("expected pinned directory replacement error, got %v", err)
	}
}

func TestOpenJVMDetectionChildRootRejectsUnsafeAndFailedChildren(t *testing.T) {
	fixture := newJVMDetectionChildRootFixture(t)
	for _, tc := range fixture.cases() {
		t.Run(tc.name, func(t *testing.T) {
			assertOpenJVMDetectionChildRootFailure(t, tc)
		})
	}
}

type jvmDetectionDirectoryReplacement struct {
	t           *testing.T
	repo        string
	originalDir string
	realRoot    safeio.Root
	swapped     bool
}

func newReplacingJVMDetectionRoot(t *testing.T, repo, originalDir string) jvmDetectionRoot {
	t.Helper()
	realRoot := openJVMTestRoot(t, repo)
	repoInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	replacement := &jvmDetectionDirectoryReplacement{
		t:           t,
		repo:        repo,
		originalDir: originalDir,
		realRoot:    realRoot,
	}
	return &osJVMDetectionRoot{root: &jvmRootedTestRoot{
		Root:     realRoot,
		info:     repoInfo,
		open:     realRoot.Open,
		openRoot: replacement.openRoot,
		lstat:    realRoot.Lstat,
	}}
}

func (r *jvmDetectionDirectoryReplacement) openRoot(name string) (safeio.Root, error) {
	if name == "src" && !r.swapped {
		r.swapped = true
		r.replace()
	}
	return r.realRoot.OpenRoot(name)
}

func (r *jvmDetectionDirectoryReplacement) replace() {
	r.t.Helper()
	if err := os.Rename(r.originalDir, filepath.Join(r.repo, "src-original")); err != nil {
		r.t.Fatalf("rename original dir: %v", err)
	}
	replacementDir := filepath.Join(r.repo, "src")
	if err := os.MkdirAll(replacementDir, 0o755); err != nil {
		r.t.Fatalf("mkdir replacement dir: %v", err)
	}
	testutil.MustWriteFile(r.t, filepath.Join(replacementDir, "main", "java", "pkg", "Replacement.java"), "class Replacement {}\n")
}

type jvmDetectionChildRootCase struct {
	name       string
	root       jvmDetectionRoot
	wantErr    error
	wantText   string
	wantJoined error
}

type jvmDetectionChildRootFixture struct {
	dirInfo         fs.FileInfo
	fileInfo        fs.FileInfo
	linkInfo        fs.FileInfo
	lookupErr       error
	openErr         error
	openedLookupErr error
	closeErr        error
}

func newJVMDetectionChildRootFixture(t *testing.T) *jvmDetectionChildRootFixture {
	t.Helper()
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat directory fixture: %v", err)
	}
	filePath := filepath.Join(t.TempDir(), "child.txt")
	testutil.MustWriteFile(t, filePath, "child\n")
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat file fixture: %v", err)
	}
	linkPath := filepath.Join(t.TempDir(), "child-link")
	if err := os.Symlink(filePath, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat symlink fixture: %v", err)
	}
	return &jvmDetectionChildRootFixture{
		dirInfo:         dirInfo,
		fileInfo:        fileInfo,
		linkInfo:        linkInfo,
		lookupErr:       errors.New("lookup child root"),
		openErr:         errors.New("open child root"),
		openedLookupErr: errors.New("lookup opened child root"),
		closeErr:        errors.New("close failed child root"),
	}
}

func (f *jvmDetectionChildRootFixture) cases() []jvmDetectionChildRootCase {
	return []jvmDetectionChildRootCase{
		{
			name: "lookup error",
			root: &jvmDetectionChildTestRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, f.lookupErr },
			},
			wantErr: f.lookupErr,
		},
		{
			name: "symlink",
			root: &jvmDetectionChildTestRoot{
				lstat: func(string) (fs.FileInfo, error) { return f.linkInfo, nil },
			},
			wantText: "root contains symlink",
		},
		{
			name: "non-directory",
			root: &jvmDetectionChildTestRoot{
				lstat: func(string) (fs.FileInfo, error) { return f.fileInfo, nil },
			},
			wantText: "root is not a directory",
		},
		{
			name: "open error",
			root: &jvmDetectionChildTestRoot{
				lstat:    func(string) (fs.FileInfo, error) { return f.dirInfo, nil },
				openRoot: func(string) (jvmDetectionRoot, error) { return nil, f.openErr },
			},
			wantErr: f.openErr,
		},
		{
			name: "opened lookup and close errors",
			root: &jvmDetectionChildTestRoot{
				lstat: func(string) (fs.FileInfo, error) { return f.dirInfo, nil },
				openRoot: func(string) (jvmDetectionRoot, error) {
					return &jvmDetectionChildTestRoot{
						lstat: func(string) (fs.FileInfo, error) { return nil, f.openedLookupErr },
						close: func() error { return f.closeErr },
					}, nil
				},
			},
			wantErr:    f.openedLookupErr,
			wantJoined: f.closeErr,
		},
	}
}

func assertOpenJVMDetectionChildRootFailure(t *testing.T, test jvmDetectionChildRootCase) {
	t.Helper()
	child, err := openJVMDetectionChildRoot(test.root, "child", filepath.Join("nested", "child"))
	if child != nil {
		t.Fatalf("expected failed child-root acquisition, got %#v", child)
	}
	if test.wantErr != nil && !errors.Is(err, test.wantErr) {
		t.Fatalf("expected error identity %v, got %v", test.wantErr, err)
	}
	if test.wantJoined != nil && !errors.Is(err, test.wantJoined) {
		t.Fatalf("expected joined error identity %v, got %v", test.wantJoined, err)
	}
	if test.wantText != "" && !strings.Contains(err.Error(), test.wantText) {
		t.Fatalf("expected error containing %q, got %v", test.wantText, err)
	}
}

func TestJVMDetectionWalkerWalkEntryPreservesSkipErrorAndFileBehavior(t *testing.T) {
	repo := canonicalRepoPath(t)
	dirInfo, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo fixture: %v", err)
	}
	filePath := filepath.Join(repo, "note.txt")
	testutil.MustWriteFile(t, filePath, "not JVM\n")
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat file fixture: %v", err)
	}

	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	skippedEntry := fs.FileInfoToDirEntry(&jvmDetectionNamedFileInfo{FileInfo: dirInfo, name: "target"})
	if err := walker.walkEntry(&jvmDetectionChildTestRoot{}, filepath.Join(repo, "target"), skippedEntry); err != nil {
		t.Fatalf("expected skipped directory to avoid opening, got %v", err)
	}

	fileEntry := fs.FileInfoToDirEntry(fileInfo)
	if err := walker.walkEntry(&jvmDetectionChildTestRoot{}, filePath, fileEntry); err != nil {
		t.Fatalf("expected ordinary file entry to complete without directory open, got %v", err)
	}

	exhaustedBudget := &jvmDetectionBudget{maxTraversalEntries: 1, traversalEntriesSeen: 1}
	exhaustedWalker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, exhaustedBudget)
	if err := exhaustedWalker.walkEntry(&jvmDetectionChildTestRoot{}, filePath, fileEntry); !errors.Is(err, errJVMDetectionTraversalLimit) {
		t.Fatalf("expected traversal-limit error to propagate, got %v", err)
	}
}

func TestJVMDetectionWalkerUsesLinearPinnedRootOperationsForDeepWideTree(t *testing.T) {
	const (
		depth = 20
		width = 7
	)

	repo := canonicalRepoPath(t)
	directoryCount, fileCount := createJVMDetectionDeepWideTree(t, repo, depth, width)
	root, err := safeio.OpenRoot(repo)
	if err != nil {
		t.Fatalf("open detection operation-count root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf("close detection operation-count root: %v", closeErr)
		}
	})

	counts := &jvmDetectionOperationCounts{}
	countingRoot := &countingJVMDetectionRoot{Root: root, counts: counts}
	budget := defaultJVMDetectionBudget()
	detection := &language.Detection{}
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, detection, budget)
	if err := walker.walkPinned(&osJVMDetectionRoot{root: countingRoot}); err != nil {
		t.Fatalf("walk deep and wide detection tree: %v", err)
	}

	if detection.Matched {
		t.Fatalf("expected non-JVM fixture to remain unmatched, got %#v", detection)
	}
	if budget.traversalEntriesSeen != directoryCount+fileCount {
		t.Fatalf("expected every fixture entry to be visited once, got %d want %d", budget.traversalEntriesSeen, directoryCount+fileCount)
	}
	if counts.openRoot != directoryCount-1 {
		t.Fatalf("expected one pinned child-root open per non-root directory, got %d want %d", counts.openRoot, directoryCount-1)
	}
	if counts.open != directoryCount {
		t.Fatalf("expected one directory read open per directory, got %d want %d", counts.open, directoryCount)
	}
	if counts.lstat != 3*directoryCount-1 {
		t.Fatalf("expected linear identity checks, got %d want %d", counts.lstat, 3*directoryCount-1)
	}
	if counts.close != directoryCount-1 {
		t.Fatalf("expected each pinned child root to close once, got %d want %d", counts.close, directoryCount-1)
	}
}

func TestJVMDetectionHelpersPropagatePinnedErrors(t *testing.T) {
	lstatErr := errors.New("root lstat failed")
	walker := newJVMDetectionWalker(t.TempDir(), map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	if err := walker.walkPinned(&jvmDetectionTestRootWithError{err: lstatErr}); !errors.Is(err, lstatErr) {
		t.Fatalf("expected pinned lstat error, got %v", err)
	}

	statErr := errors.New("root signal stat failed")
	if err := applyJVMRootSignalsWithinRoot(t.TempDir(), &jvmDetectionStatRootWithError{err: statErr}, &language.Detection{}, map[string]struct{}{}); !errors.Is(err, statErr) {
		t.Fatalf("expected root signal stat error, got %v", err)
	}
}

func TestJVMDetectionWalkerReadDirectoryJoinsCloseError(t *testing.T) {
	repo := t.TempDir()
	closeErr := errors.New("close rooted directory")
	walker := newJVMDetectionWalker(repo, map[string]struct{}{}, &language.Detection{}, defaultJVMDetectionBudget())
	walker.openDirectory = func(jvmDetectionRoot, string) (jvmDetectionDirectory, error) {
		return &jvmRootedTestDirectory{
			jvmRootedTestFile: jvmRootedTestFile{
				close: func() error { return closeErr },
			},
		}, nil
	}

	_, err := walker.readDirectory(&jvmDetectionTestRootWithError{}, repo)
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error from rooted directory read, got %v", err)
	}
}

func TestJVMDetectWithConfidencePreservesRootSignalWhenTraversalBudgetStopsWalk(t *testing.T) {
	repo := canonicalRepoPath(t)
	writeJVMPomFile(t, repo, "<project></project>\n")
	for index := 0; index < defaultJVMMaxTraversalEntries+1; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "flat", "note"+strconv.Itoa(index)+".txt"), "ignored\n")
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence after traversal stop: %v", err)
	}
	if !detection.Matched || detection.Confidence != 55 {
		t.Fatalf("expected root pom.xml signal to survive traversal stop, got %#v", detection)
	}
}

func TestJVMDetectWithConfidenceReturnsEmptyDetectionWhenTraversalBudgetStopsWithoutSignal(t *testing.T) {
	repo := canonicalRepoPath(t)
	for index := 0; index < defaultJVMMaxTraversalEntries+1; index++ {
		testutil.MustWriteFile(t, filepath.Join(repo, "flat", "note"+strconv.Itoa(index)+".txt"), "ignored\n")
	}

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence without root signal: %v", err)
	}
	if detection.Matched || detection.Confidence != 0 {
		t.Fatalf("expected bounded traversal stop to remain non-matching without a root signal, got %#v", detection)
	}
}

func TestScanRepoWithSourceReaderHonorsCanceledContext(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "class Main {}\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := scanRepoWithSourceReader(ctx, repo, map[string]string{}, map[string]string{}, safeio.ReadFileUnderLimit)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled source scan, got %v", err)
	}
}

type jvmDetectionTestRootWithError struct {
	err error
}

func (*jvmDetectionTestRootWithError) Open(string) (jvmDetectionDirectory, error) {
	return nil, errors.New("unexpected open")
}

func (*jvmDetectionTestRootWithError) OpenRoot(string) (jvmDetectionRoot, error) {
	return nil, errors.New("unexpected child root open")
}

func (r *jvmDetectionTestRootWithError) Lstat(string) (fs.FileInfo, error) {
	return nil, r.err
}

func (*jvmDetectionTestRootWithError) Close() error { return nil }

type jvmDetectionStatRootWithError struct {
	err error
}

func (*jvmDetectionStatRootWithError) Open(string) (jvmDetectionDirectory, error) {
	return nil, errors.New("unexpected open")
}

func (*jvmDetectionStatRootWithError) OpenRoot(string) (jvmDetectionRoot, error) {
	return nil, errors.New("unexpected child root open")
}

func (r *jvmDetectionStatRootWithError) Lstat(string) (fs.FileInfo, error) {
	return nil, r.err
}

func (*jvmDetectionStatRootWithError) Close() error { return nil }

type jvmDetectionLimitTestRoot struct {
	info       fs.FileInfo
	directory  jvmDetectionDirectory
	closeCalls int
}

func (r *jvmDetectionLimitTestRoot) Open(string) (jvmDetectionDirectory, error) {
	return r.directory, nil
}

func (*jvmDetectionLimitTestRoot) OpenRoot(string) (jvmDetectionRoot, error) {
	return nil, errors.New("unexpected child root open")
}

func (r *jvmDetectionLimitTestRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == "." {
		return r.info, nil
	}
	return nil, os.ErrNotExist
}

func (r *jvmDetectionLimitTestRoot) Close() error {
	r.closeCalls++
	return nil
}

func withOpenJVMDetectionRootHook(t *testing.T, hook func(string) (jvmDetectionRoot, error)) {
	t.Helper()
	original := openJVMDetectionRootHook
	openJVMDetectionRootHook = hook
	t.Cleanup(func() {
		openJVMDetectionRootHook = original
	})
}

func createJVMDetectionDeepWideTree(t *testing.T, repo string, depth, width int) (directoryCount, fileCount int) {
	t.Helper()
	directoryCount = 1
	current := repo
	for level := range depth {
		for branch := range width {
			branchDir := filepath.Join(current, fmt.Sprintf("branch-%02d-%02d", level, branch))
			if err := os.Mkdir(branchDir, 0o755); err != nil {
				t.Fatalf("mkdir wide detection branch: %v", err)
			}
			testutil.MustWriteFile(t, filepath.Join(branchDir, "note.txt"), "not JVM\n")
			directoryCount++
			fileCount++
		}
		next := filepath.Join(current, fmt.Sprintf("level-%02d", level))
		if err := os.Mkdir(next, 0o755); err != nil {
			t.Fatalf("mkdir deep detection level: %v", err)
		}
		directoryCount++
		current = next
	}
	return directoryCount, fileCount
}

type jvmDetectionOperationCounts struct {
	open     int
	openRoot int
	lstat    int
	close    int
}

type countingJVMDetectionRoot struct {
	safeio.Root
	counts *jvmDetectionOperationCounts
}

func (r *countingJVMDetectionRoot) Open(name string) (safeio.File, error) {
	r.counts.open++
	return r.Root.Open(name)
}

func (r *countingJVMDetectionRoot) OpenRoot(name string) (safeio.Root, error) {
	r.counts.openRoot++
	child, err := r.Root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &countingJVMDetectionRoot{Root: child, counts: r.counts}, nil
}

func (r *countingJVMDetectionRoot) Lstat(name string) (fs.FileInfo, error) {
	r.counts.lstat++
	return r.Root.Lstat(name)
}

func (r *countingJVMDetectionRoot) Close() error {
	r.counts.close++
	return r.Root.Close()
}

type jvmDetectionChildTestRoot struct {
	lstat    func(string) (fs.FileInfo, error)
	openRoot func(string) (jvmDetectionRoot, error)
	close    func() error
}

func (*jvmDetectionChildTestRoot) Open(string) (jvmDetectionDirectory, error) {
	return nil, errors.New("unexpected directory open")
}

func (r *jvmDetectionChildTestRoot) OpenRoot(name string) (jvmDetectionRoot, error) {
	if r.openRoot != nil {
		return r.openRoot(name)
	}
	return nil, errors.New("unexpected child root open")
}

func (r *jvmDetectionChildTestRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(name)
	}
	return nil, errors.New("unexpected child root lstat")
}

func (r *jvmDetectionChildTestRoot) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

type jvmDetectionNamedFileInfo struct {
	fs.FileInfo
	name string
}

func (i *jvmDetectionNamedFileInfo) Name() string {
	return i.name
}
