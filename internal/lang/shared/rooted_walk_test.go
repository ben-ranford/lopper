package shared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestWalkRepoFilesWithinRootHonorsCancellation(t *testing.T) {
	root := openSharedTestRoot(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	budget := RootedWalkBudget{MaxTraversalEntries: 8, MaxFiles: 8, MaxWorkItems: 8}
	err := WalkRepoFilesWithinRoot(ctx, t.TempDir(), root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context error, got %v", err)
	}
}

func TestWalkRepoFilesWithinRootRejectsTraversalBudgetOverflow(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	directory := &sharedWalkTestDirectory{
		fillEntry:     fs.FileInfoToDirEntry(info),
		overflowEntry: fs.FileInfoToDirEntry(info),
		repeatEntries: 1,
	}
	root := &sharedWalkTestRoot{
		info: info,
		open: func(string) (safeio.File, error) { return directory, nil },
	}

	budget := RootedWalkBudget{MaxTraversalEntries: 1, MaxFiles: 8, MaxWorkItems: 8}
	err = WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if !errors.Is(err, errRootedWalkTraversalLimit) {
		t.Fatalf("expected traversal-limit error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected overflowing directory to close once, got %d", directory.closeCalls)
	}
}

func TestWalkRepoFilesWithinRootJoinsOversizedBatchReadError(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	readErr := errors.New("read oversized rooted-walk batch")
	directory := &sharedWalkTestDirectory{
		sharedWalkTestFile: sharedWalkTestFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
		},
		fillEntry:          fs.FileInfoToDirEntry(info),
		extraEntries:       1,
		readErrWithEntries: errors.Join(io.EOF, readErr),
	}
	root := &sharedWalkTestRoot{
		info: info,
		open: func(string) (safeio.File, error) { return directory, nil },
	}

	budget := RootedWalkBudget{MaxTraversalEntries: 2, MaxFiles: 8, MaxWorkItems: 8}
	err = WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if !errors.Is(err, errRootedWalkTraversalLimit) || !errors.Is(err, io.EOF) || !errors.Is(err, readErr) {
		t.Fatalf("expected joined oversized-batch limit, EOF, and read error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected oversized-batch directory to close once, got %d", directory.closeCalls)
	}
}

func TestWalkRepoFilesWithinRootRejectsFileAndWorkBudgetOverflow(t *testing.T) {
	repo := t.TempDir()
	fileA := filepath.Join(repo, "a.txt")
	fileB := filepath.Join(repo, "b.txt")
	if err := os.WriteFile(fileA, []byte("a"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(fileB, []byte("b"), 0o600); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	for _, tc := range []struct {
		name   string
		budget RootedWalkBudget
		want   error
	}{
		{
			name: "file limit",
			budget: RootedWalkBudget{
				MaxTraversalEntries: 8,
				MaxFiles:            1,
				MaxWorkItems:        8,
			},
			want: errRootedWalkFileLimit,
		},
		{
			name: "work limit",
			budget: RootedWalkBudget{
				MaxTraversalEntries: 8,
				MaxFiles:            8,
				MaxWorkItems:        1,
			},
			want: errRootedWalkWorkLimit,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := openSharedTestRoot(t, repo)
			err := WalkRepoFilesWithinRoot(context.Background(), repo, root, tc.budget, nil, func(string, fs.DirEntry) error { return nil })
			if !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}
}

func TestWalkRepoFilesWithinRootCountsOnlyMatchingCandidates(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "ignore.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write ignore.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "keep.java"), []byte("class Keep {}\n"), 0o600); err != nil {
		t.Fatalf("write keep.java: %v", err)
	}

	root := openSharedTestRoot(t, repo)
	budget := RootedWalkBudget{
		MaxTraversalEntries: 8,
		MaxFiles:            1,
		MaxWorkItems:        1,
		CountCandidate: func(path string, _ fs.DirEntry) bool {
			return strings.HasSuffix(path, ".java")
		},
	}

	var visited []string
	err := WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(path string, _ fs.DirEntry) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo with candidate filter: %v", err)
	}
	if len(visited) != 2 {
		t.Fatalf("expected both files to be visited, got %#v", visited)
	}
}

func TestIsPureSentinelError(t *testing.T) {
	first := errors.New("first limit")
	second := errors.New("second limit")
	closeErr := errors.New("close directory")

	if !IsPureSentinelError(fmt.Errorf("wrapped: %w", first), first) {
		t.Fatal("expected wrapped sentinel to be pure")
	}
	if !IsPureSentinelError(errors.Join(first, fmt.Errorf("wrapped: %w", second)), first, second) {
		t.Fatal("expected joined allowed sentinels to be pure")
	}
	joined := fmt.Errorf("walk failed: %w", errors.Join(fmt.Errorf("limit: %w", first), closeErr))
	if IsPureSentinelError(joined, first) {
		t.Fatal("expected joined non-sentinel cause to make the error impure")
	}
	if !errors.Is(joined, first) || !errors.Is(joined, closeErr) {
		t.Fatalf("expected original joined causes to remain discoverable, got %v", joined)
	}
	if IsPureSentinelError(nil, first) || IsPureSentinelError(first) || IsPureSentinelError(closeErr, first) {
		t.Fatal("expected nil, missing-target, and operational errors to be impure")
	}
}

func TestRootedWalkBudgetWarning(t *testing.T) {
	budget := RootedWalkBudget{MaxTraversalEntries: 7, MaxFiles: 5, MaxWorkItems: 3}

	assertRootedWalkBudgetWarning(t, budget, newRootedWalkTraversalLimitError("/repo", budget.MaxTraversalEntries), "7 traversal entries")
	assertRootedWalkBudgetWarning(t, budget, newRootedWalkFileLimitError("/repo/Main.java", budget.MaxFiles), "5 candidate files")
	assertRootedWalkBudgetWarning(t, budget, newRootedWalkWorkLimitError("/repo/Main.java", budget.MaxWorkItems), "3 candidate work items")
	assertNoRootedWalkBudgetWarning(t, budget, io.EOF)

	closeErr := errors.New("close rooted directory")
	for _, tc := range []struct {
		name   string
		limit  error
		target error
	}{
		{name: "traversal", limit: newRootedWalkTraversalLimitError("/repo", budget.MaxTraversalEntries), target: errRootedWalkTraversalLimit},
		{name: "file", limit: newRootedWalkFileLimitError("/repo/Main.java", budget.MaxFiles), target: errRootedWalkFileLimit},
		{name: "work", limit: newRootedWalkWorkLimitError("/repo/Main.java", budget.MaxWorkItems), target: errRootedWalkWorkLimit},
	} {
		t.Run(tc.name+" limit joined with close error", func(t *testing.T) {
			assertJoinedRootedWalkLimitIsNotWarning(t, budget, tc.limit, tc.target, closeErr)
		})
	}
}

func assertRootedWalkBudgetWarning(t *testing.T, budget RootedWalkBudget, err error, want string) {
	t.Helper()
	warning, ok := RootedWalkBudgetWarning("JVM source scan", budget, err)
	if !ok || !strings.Contains(warning, want) {
		t.Fatalf("expected warning containing %q, got %q ok=%v", want, warning, ok)
	}
}

func assertNoRootedWalkBudgetWarning(t *testing.T, budget RootedWalkBudget, err error) {
	t.Helper()
	if warning, ok := RootedWalkBudgetWarning("JVM source scan", budget, err); ok || warning != "" {
		t.Fatalf("expected non-budget error to stay unclassified, got %q ok=%v", warning, ok)
	}
}

func assertJoinedRootedWalkLimitIsNotWarning(t *testing.T, budget RootedWalkBudget, limit, target, closeErr error) {
	t.Helper()
	joined := errors.Join(limit, closeErr)
	if warning, ok := RootedWalkBudgetWarning("JVM source scan", budget, joined); ok || warning != "" {
		t.Fatalf("expected joined close error to prevent limit downgrade, got %q ok=%v", warning, ok)
	}
	if !errors.Is(joined, target) || !errors.Is(joined, closeErr) {
		t.Fatalf("expected joined limit and close causes to remain discoverable, got %v", joined)
	}
}

func TestLoadGradleCatalogResolverWithinRootPropagatesTraversalLimitCloseError(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	closeErr := errors.New("close Gradle catalog directory")
	directory := &sharedWalkTestDirectory{
		sharedWalkTestFile: sharedWalkTestFile{
			stat:  func() (fs.FileInfo, error) { return info, nil },
			close: func() error { return closeErr },
		},
		fillEntry:    &sharedWalkTestDirEntry{name: "ignored.txt"},
		extraEntries: 1,
	}
	root := &sharedWalkTestRoot{
		info: info,
		open: func(string) (safeio.File, error) { return directory, nil },
	}

	_, warnings, err := LoadGradleCatalogResolverWithinRoot(context.Background(), repo, root)
	if !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "rooted walk traversal limit exceeded") {
		t.Fatalf("expected joined traversal-limit and close error, got %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected operational failure not to be downgraded to warnings, got %#v", warnings)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected overflowing directory to close once, got %d", directory.closeCalls)
	}
}

func TestWalkRepoFilesWithinRootRejectsNonDirectoryOpenAndJoinsCloseError(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	closeErr := errors.New("close directory")
	filePath := filepath.Join(repo, "child.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write child file: %v", err)
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat child file: %v", err)
	}
	root := &sharedWalkTestRoot{
		info: info,
		open: func(string) (safeio.File, error) {
			return &sharedWalkTestFile{
				stat:  func() (fs.FileInfo, error) { return fileInfo, nil },
				close: func() error { return closeErr },
			}, nil
		},
	}

	budget := RootedWalkBudget{MaxTraversalEntries: 4, MaxFiles: 4, MaxWorkItems: 4}
	err = WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if !errors.Is(err, fs.ErrInvalid) || !errors.Is(err, closeErr) {
		t.Fatalf("expected invalid directory and close error, got %v", err)
	}
}

func TestWalkRepoFilesWithinRootSortsEntriesAndSkipsDirs(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("b"), 0o600); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "skip-me"), 0o755); err != nil {
		t.Fatalf("mkdir skip-me: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "skip-me", "ignored.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write ignored.txt: %v", err)
	}
	root := openSharedTestRoot(t, repo)

	var visited []string
	budget := RootedWalkBudget{MaxTraversalEntries: 8, MaxFiles: 8, MaxWorkItems: 8}
	err := WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, func(name string) bool { return name == "skip-me" }, func(path string, entry fs.DirEntry) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk sorted repo: %v", err)
	}
	if len(visited) != 2 || visited[0] != "a.txt" || visited[1] != "b.txt" {
		t.Fatalf("expected sorted visible files only, got %#v", visited)
	}
}

func TestWalkRepoFilesWithinRootSortsExactBudgetProbeSuccessBranch(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	fileA := &sharedWalkTestDirEntry{name: "a.txt"}
	fileB := &sharedWalkTestDirEntry{name: "b.txt"}
	directory := &sharedWalkTestDirectory{
		sharedWalkTestFile: sharedWalkTestFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
		},
		entries: []fs.DirEntry{fileB, fileA},
	}
	root := &sharedWalkTestRoot{
		info: info,
		open: func(string) (safeio.File, error) { return directory, nil },
	}

	var visited []string
	budget := RootedWalkBudget{MaxTraversalEntries: 3, MaxFiles: 8, MaxWorkItems: 8}
	err = WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(path string, entry fs.DirEntry) error {
		visited = append(visited, filepath.Base(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk exact-budget probe branch: %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected exact-budget directory to close once, got %d", directory.closeCalls)
	}
	if directory.readDirCalls != 2 {
		t.Fatalf("expected exact-budget branch to probe after filling budget, got %d ReadDir calls", directory.readDirCalls)
	}
	if len(visited) != 2 || visited[0] != "a.txt" || visited[1] != "b.txt" {
		t.Fatalf("expected lexical visitation after probe-success branch, got %#v", visited)
	}
}

func TestWalkRepoFilesWithinRootPropagatesVisitAndReadErrors(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "visit.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write visit.txt: %v", err)
	}
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	visitErr := errors.New("visit failed")
	root := openSharedTestRoot(t, repo)
	budget := RootedWalkBudget{MaxTraversalEntries: 4, MaxFiles: 4, MaxWorkItems: 4}
	err = WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(string, fs.DirEntry) error { return visitErr })
	if err == nil {
		t.Fatal("expected visit error")
	}

	directory := &sharedWalkTestDirectory{
		sharedWalkTestFile: sharedWalkTestFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
		},
		readErr: errors.New("read dir failed"),
	}
	fakeRoot := &sharedWalkTestRoot{
		info: info,
		open: func(string) (safeio.File, error) { return directory, nil },
	}
	err = WalkRepoFilesWithinRoot(context.Background(), repo, fakeRoot, budget, nil, func(string, fs.DirEntry) error { return nil })
	if !errors.Is(err, directory.readErr) {
		t.Fatalf("expected readDir error, got %v", err)
	}
}

func TestWalkRepoFilesWithinRootPropagatesOpenError(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	openErr := errors.New("open rooted directory failed")
	root := &sharedWalkTestRoot{
		info: info,
		open: func(string) (safeio.File, error) { return nil, openErr },
	}

	budget := RootedWalkBudget{MaxTraversalEntries: 4, MaxFiles: 4, MaxWorkItems: 4}
	err = WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if !errors.Is(err, openErr) {
		t.Fatalf("expected open error, got %v", err)
	}
}

func TestWalkRepoFilesWithinRootTraversesNestedDirectories(t *testing.T) {
	repo := t.TempDir()
	nestedFile := filepath.Join(repo, "src", "main", "App.java")
	if err := os.MkdirAll(filepath.Dir(nestedFile), 0o755); err != nil {
		t.Fatalf("mkdir nested source dir: %v", err)
	}
	if err := os.WriteFile(nestedFile, []byte("class App {}"), 0o600); err != nil {
		t.Fatalf("write nested source file: %v", err)
	}
	root := openSharedTestRoot(t, repo)

	var visited []string
	budget := RootedWalkBudget{MaxTraversalEntries: 8, MaxFiles: 8, MaxWorkItems: 8}
	err := WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(path string, entry fs.DirEntry) error {
		visited = append(visited, filepath.ToSlash(path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk nested repo: %v", err)
	}
	if len(visited) != 1 || !strings.HasSuffix(visited[0], "src/main/App.java") {
		t.Fatalf("expected nested file visit only, got %#v", visited)
	}
}

func TestWalkRepoFilesWithinRootUsesLinearPinnedRootOperationsForDeepWideTree(t *testing.T) {
	repo := t.TempDir()
	const (
		depth = 5
		width = 3
	)
	dirCount, fileCount := createSharedWalkDeepWideTree(t, repo, depth, width)
	counts := &sharedWalkOperationCounts{}
	root := newCountingSharedWalkRoot(t, repo, counts)

	visitedFiles := 0
	budget := RootedWalkBudget{
		MaxTraversalEntries: dirCount + fileCount,
		MaxFiles:            fileCount,
		MaxWorkItems:        fileCount,
	}
	err := WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(string, fs.DirEntry) error {
		visitedFiles++
		return nil
	})
	if err != nil {
		t.Fatalf("walk deep+wide repo: %v", err)
	}
	if visitedFiles != fileCount {
		t.Fatalf("expected to visit %d files, got %d", fileCount, visitedFiles)
	}
	if counts.openRootCalls != dirCount-1 {
		t.Fatalf("expected one child root open per non-root directory (%d), got %d", dirCount-1, counts.openRootCalls)
	}
	if counts.openCalls != dirCount {
		t.Fatalf("expected one directory handle open per directory (%d), got %d", dirCount, counts.openCalls)
	}
	if counts.lstatCalls != 3*dirCount-1 {
		t.Fatalf("expected bounded rooted lstat count %d, got %d", 3*dirCount-1, counts.lstatCalls)
	}
}

func TestWalkRepoFilesWithinRootRejectsDirectoryReplacementBetweenEnumerationAndOpen(t *testing.T) {
	repo := t.TempDir()
	root := newSharedWalkDirectoryReplacementRoot(t, repo)

	budget := RootedWalkBudget{MaxTraversalEntries: 8, MaxFiles: 8, MaxWorkItems: 8}
	err := WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "root changed while opening: src") {
		t.Fatalf("expected pinned directory replacement error, got %v", err)
	}
}

type sharedWalkDirectoryReplacement struct {
	t           *testing.T
	repo        string
	originalDir string
	realRoot    safeio.Root
	swapped     bool
}

func newSharedWalkDirectoryReplacementRoot(t *testing.T, repo string) safeio.Root {
	t.Helper()
	originalDir := filepath.Join(repo, "src")
	if err := os.MkdirAll(originalDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originalDir, "Original.java"), []byte("class Original {}\n"), 0o600); err != nil {
		t.Fatalf("write original source: %v", err)
	}
	replacement := &sharedWalkDirectoryReplacement{
		t:           t,
		repo:        repo,
		originalDir: originalDir,
		realRoot:    openSharedTestRoot(t, repo),
	}
	return &sharedWalkSwapRoot{Root: replacement.realRoot, openRoot: replacement.openRoot}
}

func (r *sharedWalkDirectoryReplacement) openRoot(name string) (safeio.Root, error) {
	if name == "src" && !r.swapped {
		r.swapped = true
		r.replace()
	}
	return r.realRoot.OpenRoot(name)
}

func (r *sharedWalkDirectoryReplacement) replace() {
	r.t.Helper()
	if err := os.Rename(r.originalDir, filepath.Join(r.repo, "src-original")); err != nil {
		r.t.Fatalf("rename original dir: %v", err)
	}
	replacementDir := filepath.Join(r.repo, "src")
	if err := os.MkdirAll(replacementDir, 0o755); err != nil {
		r.t.Fatalf("mkdir replacement dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(replacementDir, "Replacement.java"), []byte("class Replacement {}\n"), 0o600); err != nil {
		r.t.Fatalf("write replacement source: %v", err)
	}
}

func TestWalkRepoFilesWithinRootRejectsAncestorReplacementBeforeNestedDirectoryOpen(t *testing.T) {
	repo := t.TempDir()
	originalDir := filepath.Join(repo, "src", "main")
	if err := os.MkdirAll(originalDir, 0o755); err != nil {
		t.Fatalf("mkdir src/main: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originalDir, "Original.java"), []byte("class Original {}\n"), 0o600); err != nil {
		t.Fatalf("write original source: %v", err)
	}

	realRoot := openSharedTestRoot(t, repo)
	root := &sharedWalkSwapRoot{
		Root: realRoot,
		openRoot: func(name string) (safeio.Root, error) {
			if name == "src" {
				originalPath := filepath.Join(repo, "src-original")
				if err := os.Rename(filepath.Join(repo, "src"), originalPath); err != nil {
					t.Fatalf("rename original src: %v", err)
				}
				replacementDir := filepath.Join(repo, "src", "main")
				if err := os.MkdirAll(replacementDir, 0o755); err != nil {
					t.Fatalf("mkdir replacement src/main: %v", err)
				}
				if err := os.WriteFile(filepath.Join(replacementDir, "Replacement.java"), []byte("class Replacement {}\n"), 0o600); err != nil {
					t.Fatalf("write replacement source: %v", err)
				}
			}
			return realRoot.OpenRoot(name)
		},
	}

	budget := RootedWalkBudget{MaxTraversalEntries: 8, MaxFiles: 8, MaxWorkItems: 8}
	err := WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "root changed while opening: src") {
		t.Fatalf("expected nested ancestor replacement error, got %v", err)
	}
}

func TestWalkRepoFilesWithinRootBudgetHelpers(t *testing.T) {
	exhausted := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 1}}
	if err := exhausted.countTraversalEntry("repo"); err != nil {
		t.Fatalf("count first traversal entry: %v", err)
	}
	if err := exhausted.countTraversalEntry("repo"); !errors.Is(err, errRootedWalkTraversalLimit) {
		t.Fatalf("expected traversal limit error, got %v", err)
	}
	if got := exhausted.traversalReadSize(); got != 0 {
		t.Fatalf("expected exhausted traversal read size 0, got %d", got)
	}
	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 1}}
	if !walker.queueTraversalEntries(1) {
		t.Fatal("expected traversal queue to accept final entry")
	}
	if walker.queueTraversalEntries(1) || walker.queueTraversalEntries(-1) {
		t.Fatal("expected traversal queue to reject invalid entries")
	}
	if !walker.dequeueTraversalEntry() || walker.dequeueTraversalEntry() {
		t.Fatal("expected traversal dequeue bookkeeping to stay balanced")
	}
	var nilCtx context.Context
	if err := walkContextErr(nilCtx); err != nil {
		t.Fatalf("expected nil context to be ignored, got %v", err)
	}

	unlimited := rootedRepoWalker{}
	if got := unlimited.traversalReadSize(); got != rootedWalkReadBatchSize {
		t.Fatalf("expected unlimited traversal read size %d, got %d", rootedWalkReadBatchSize, got)
	}
}

func openSharedTestRoot(t *testing.T, repo string) safeio.Root {
	t.Helper()
	root, err := safeio.OpenRoot(repo)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf("close root: %v", closeErr)
		}
	})
	return root
}

type sharedWalkTestRoot struct {
	info fs.FileInfo
	open func(string) (safeio.File, error)
}

type sharedWalkOperationCounts struct {
	lstatCalls    int
	openCalls     int
	openRootCalls int
}

type sharedWalkSwapRoot struct {
	safeio.Root
	open     func(string) (safeio.File, error)
	openRoot func(string) (safeio.Root, error)
	close    func() error
}

type sharedPinnedChildRoot struct {
	lstat    func(string) (fs.FileInfo, error)
	openRoot func(string) (safeio.Root, error)
	close    func() error
}

func (r *sharedWalkSwapRoot) Open(name string) (safeio.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return r.Root.Open(name)
}

func (r *sharedWalkSwapRoot) OpenRoot(name string) (safeio.Root, error) {
	if r.openRoot != nil {
		return r.openRoot(name)
	}
	return r.Root.OpenRoot(name)
}

func (r *sharedWalkSwapRoot) Close() error {
	if r.close != nil {
		return r.close()
	}
	return r.Root.Close()
}

func (*sharedPinnedChildRoot) Open(string) (safeio.File, error) {
	return nil, errors.New("unexpected open")
}

func (*sharedPinnedChildRoot) OpenFile(string, int, os.FileMode) (safeio.File, error) {
	return nil, errors.New("unexpected open file")
}

func (r *sharedPinnedChildRoot) OpenRoot(name string) (safeio.Root, error) {
	if r.openRoot != nil {
		return r.openRoot(name)
	}
	return nil, errors.New("unexpected open root: " + name)
}

func (r *sharedPinnedChildRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(name)
	}
	return nil, errors.New("unexpected lstat: " + name)
}

func (*sharedPinnedChildRoot) Mkdir(string, os.FileMode) error { return errors.New("unexpected mkdir") }
func (*sharedPinnedChildRoot) Chmod(string, os.FileMode) error { return errors.New("unexpected chmod") }
func (*sharedPinnedChildRoot) MkdirAll(string, os.FileMode) error {
	return errors.New("unexpected mkdir all")
}
func (*sharedPinnedChildRoot) Link(string, string) error   { return errors.New("unexpected link") }
func (*sharedPinnedChildRoot) Rename(string, string) error { return errors.New("unexpected rename") }
func (*sharedPinnedChildRoot) Remove(string) error         { return errors.New("unexpected remove") }
func (r *sharedPinnedChildRoot) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

func (r *sharedWalkTestRoot) Open(name string) (safeio.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return nil, errors.New("unexpected open: " + name)
}

func (*sharedWalkTestRoot) OpenFile(string, int, os.FileMode) (safeio.File, error) {
	return nil, errors.New("unexpected open file")
}

func (*sharedWalkTestRoot) OpenRoot(string) (safeio.Root, error) {
	return nil, errors.New("unexpected open root")
}

func (r *sharedWalkTestRoot) Lstat(name string) (fs.FileInfo, error) {
	if name == "." && r.info != nil {
		return r.info, nil
	}
	return nil, errors.New("unexpected lstat: " + name)
}

func (*sharedWalkTestRoot) Mkdir(string, os.FileMode) error { return errors.New("unexpected mkdir") }
func (*sharedWalkTestRoot) Chmod(string, os.FileMode) error { return errors.New("unexpected chmod") }
func (*sharedWalkTestRoot) MkdirAll(string, os.FileMode) error {
	return errors.New("unexpected mkdir all")
}
func (*sharedWalkTestRoot) Link(string, string) error   { return errors.New("unexpected link") }
func (*sharedWalkTestRoot) Rename(string, string) error { return errors.New("unexpected rename") }
func (*sharedWalkTestRoot) Remove(string) error         { return errors.New("unexpected remove") }
func (*sharedWalkTestRoot) Close() error                { return nil }

type countingSharedWalkRoot struct {
	underlying safeio.Root
	counts     *sharedWalkOperationCounts
}

func newCountingSharedWalkRoot(t *testing.T, repo string, counts *sharedWalkOperationCounts) safeio.Root {
	t.Helper()
	root, err := safeio.OpenRoot(repo)
	if err != nil {
		t.Fatalf("open counting root: %v", err)
	}
	wrapped := &countingSharedWalkRoot{underlying: root, counts: counts}
	t.Cleanup(func() {
		if closeErr := wrapped.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf("close counting root: %v", closeErr)
		}
	})
	return wrapped
}

func (r *countingSharedWalkRoot) Open(name string) (safeio.File, error) {
	r.counts.openCalls++
	return r.underlying.Open(name)
}

func (r *countingSharedWalkRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	return r.underlying.OpenFile(name, flag, perm)
}

func (r *countingSharedWalkRoot) OpenRoot(name string) (safeio.Root, error) {
	r.counts.openRootCalls++
	child, err := r.underlying.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &countingSharedWalkRoot{underlying: child, counts: r.counts}, nil
}

func (r *countingSharedWalkRoot) Lstat(name string) (fs.FileInfo, error) {
	r.counts.lstatCalls++
	return r.underlying.Lstat(name)
}

func (r *countingSharedWalkRoot) Mkdir(name string, perm os.FileMode) error {
	return r.underlying.Mkdir(name, perm)
}

func (r *countingSharedWalkRoot) Chmod(name string, perm os.FileMode) error {
	return r.underlying.Chmod(name, perm)
}

func (r *countingSharedWalkRoot) MkdirAll(name string, perm os.FileMode) error {
	return r.underlying.MkdirAll(name, perm)
}

func (r *countingSharedWalkRoot) Link(oldName, newName string) error {
	return r.underlying.Link(oldName, newName)
}

func (r *countingSharedWalkRoot) Rename(oldName, newName string) error {
	return r.underlying.Rename(oldName, newName)
}

func (r *countingSharedWalkRoot) Remove(name string) error {
	return r.underlying.Remove(name)
}

func (r *countingSharedWalkRoot) Close() error {
	return r.underlying.Close()
}

type sharedWalkTestFile struct {
	read  func([]byte) (int, error)
	write func([]byte) (int, error)
	close func() error
	stat  func() (fs.FileInfo, error)
	chmod func(os.FileMode) error
}

func (f *sharedWalkTestFile) Read(p []byte) (int, error) {
	if f.read != nil {
		return f.read(p)
	}
	return 0, io.EOF
}

func (f *sharedWalkTestFile) Write(p []byte) (int, error) {
	if f.write != nil {
		return f.write(p)
	}
	return len(p), nil
}

func (f *sharedWalkTestFile) Close() error {
	if f.close != nil {
		return f.close()
	}
	return nil
}

func (f *sharedWalkTestFile) Stat() (fs.FileInfo, error) {
	if f.stat != nil {
		return f.stat()
	}
	return nil, errors.New("unexpected stat")
}

func (f *sharedWalkTestFile) Chmod(perm os.FileMode) error {
	if f.chmod != nil {
		return f.chmod(perm)
	}
	return nil
}

type sharedWalkTestDirectory struct {
	sharedWalkTestFile
	fillEntry          fs.DirEntry
	overflowEntry      fs.DirEntry
	entries            []fs.DirEntry
	entryIndex         int
	repeatEntries      int
	extraEntries       int
	noProgress         bool
	readErr            error
	readErrWithEntries error
	closeCalls         int
	readDirCalls       int
	overflowServed     bool
}

func (d *sharedWalkTestDirectory) Stat() (fs.FileInfo, error) {
	if d.stat != nil {
		return d.stat()
	}
	if d.fillEntry != nil {
		return d.fillEntry.Info()
	}
	if d.overflowEntry != nil {
		return d.overflowEntry.Info()
	}
	return nil, errors.New("unexpected stat")
}

func (d *sharedWalkTestDirectory) ReadDir(count int) ([]fs.DirEntry, error) {
	d.readDirCalls++
	if d.readErr != nil {
		return nil, d.readErr
	}
	if d.noProgress {
		return nil, nil
	}
	if d.fillEntry != nil && d.extraEntries > 0 {
		entries := make([]fs.DirEntry, count+d.extraEntries)
		for index := range entries {
			entries[index] = d.fillEntry
		}
		d.extraEntries = 0
		return entries, d.readErrWithEntries
	}
	if d.entryIndex < len(d.entries) {
		remaining := len(d.entries) - d.entryIndex
		entryCount := min(count, remaining)
		entries := make([]fs.DirEntry, entryCount)
		copy(entries, d.entries[d.entryIndex:d.entryIndex+entryCount])
		d.entryIndex += entryCount
		return entries, d.readErrWithEntries
	}
	if d.repeatEntries > 0 {
		entryCount := min(count, d.repeatEntries)
		entries := make([]fs.DirEntry, entryCount)
		for index := range entries {
			entries[index] = d.fillEntry
		}
		d.repeatEntries -= entryCount
		return entries, d.readErrWithEntries
	}
	if d.overflowEntry != nil && !d.overflowServed {
		d.overflowServed = true
		return []fs.DirEntry{d.overflowEntry}, d.readErrWithEntries
	}
	return nil, io.EOF
}

func (d *sharedWalkTestDirectory) Close() error {
	d.closeCalls++
	if d.close != nil {
		return d.close()
	}
	return nil
}

type sharedWalkTestDirEntry struct {
	name string
}

func (e *sharedWalkTestDirEntry) Name() string    { return e.name }
func (*sharedWalkTestDirEntry) IsDir() bool       { return false }
func (*sharedWalkTestDirEntry) Type() fs.FileMode { return 0 }
func (e *sharedWalkTestDirEntry) Info() (fs.FileInfo, error) {
	return &sharedWalkTestFileInfo{name: e.name}, nil
}

type sharedWalkTestFileInfo struct {
	name string
}

func (i *sharedWalkTestFileInfo) Name() string     { return i.name }
func (*sharedWalkTestFileInfo) Size() int64        { return 0 }
func (*sharedWalkTestFileInfo) Mode() fs.FileMode  { return 0 }
func (*sharedWalkTestFileInfo) ModTime() time.Time { return time.Time{} }
func (*sharedWalkTestFileInfo) IsDir() bool        { return false }
func (*sharedWalkTestFileInfo) Sys() any           { return nil }

func createSharedWalkDeepWideTree(t *testing.T, repo string, depth, width int) (dirCount int, fileCount int) {
	t.Helper()
	var createLevel func(path string, level int)
	createLevel = func(path string, level int) {
		dirCount++
		fileName := filepath.Join(path, "level-file.txt")
		if err := os.WriteFile(fileName, []byte("x"), 0o600); err != nil {
			t.Fatalf("write tree file %s: %v", fileName, err)
		}
		fileCount++
		if level == depth {
			return
		}
		for idx := 0; idx < width; idx++ {
			child := filepath.Join(path, "level-"+strconv.Itoa(level)+"-child-"+strconv.Itoa(idx))
			if err := os.MkdirAll(child, 0o755); err != nil {
				t.Fatalf("mkdir tree dir %s: %v", child, err)
			}
			createLevel(child, level+1)
		}
	}
	createLevel(repo, 0)
	return dirCount, fileCount
}

func TestWalkRepoFilesWithinRootProbeNoProgress(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	directory := &sharedWalkTestDirectory{
		sharedWalkTestFile: sharedWalkTestFile{
			stat: func() (fs.FileInfo, error) { return info, nil },
		},
		noProgress: true,
	}
	root := &sharedWalkTestRoot{
		info: info,
		open: func(string) (safeio.File, error) { return directory, nil },
	}

	budget := RootedWalkBudget{MaxTraversalEntries: 1, MaxFiles: 1, MaxWorkItems: 1}
	err = WalkRepoFilesWithinRoot(context.Background(), repo, root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if !errors.Is(err, io.ErrNoProgress) {
		t.Fatalf("expected no-progress error, got %v", err)
	}
	if directory.closeCalls != 1 {
		t.Fatalf("expected no-progress directory to close once, got %d", directory.closeCalls)
	}
}

func TestWalkRepoFilesWithinRootProbeLimitBranches(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 1}}
	entryDirectory := &sharedWalkTestDirectory{
		overflowEntry: fs.FileInfoToDirEntry(info),
	}
	if err := walker.probeDirectoryLimit(context.Background(), repo, entryDirectory); !errors.Is(err, errRootedWalkTraversalLimit) {
		t.Fatalf("expected traversal limit from probe, got %v", err)
	}

	readErr := errors.New("read overflowing probe")
	entryDirectory = &sharedWalkTestDirectory{
		overflowEntry:      fs.FileInfoToDirEntry(info),
		readErrWithEntries: readErr,
	}
	if err := walker.probeDirectoryLimit(context.Background(), repo, entryDirectory); !errors.Is(err, errRootedWalkTraversalLimit) || !errors.Is(err, readErr) {
		t.Fatalf("expected joined probe traversal-limit and read error, got %v", err)
	}

	eofDirectory := &sharedWalkTestDirectory{}
	if err := walker.probeDirectoryLimit(context.Background(), repo, eofDirectory); err != nil {
		t.Fatalf("expected eof probe to succeed, got %v", err)
	}
}

func TestWalkRepoFilesWithinRootProbeHonorsCancellationAndReadErrors(t *testing.T) {
	repo := t.TempDir()
	if _, err := os.Stat(repo); err != nil {
		t.Fatalf("stat repo: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 1}}
	directory := &sharedWalkTestDirectory{}
	if err := walker.probeDirectoryLimit(ctx, repo, directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled probe, got %v", err)
	}

	readErr := errors.New("probe read failure")
	directory = &sharedWalkTestDirectory{readErr: readErr}
	if err := walker.probeDirectoryLimit(context.Background(), repo, directory); !errors.Is(err, readErr) {
		t.Fatalf("expected probe read error, got %v", err)
	}
}

func TestWalkRepoFilesWithinRootPropagatesRootLstatError(t *testing.T) {
	root := &sharedWalkTestRoot{}

	budget := RootedWalkBudget{MaxTraversalEntries: 1, MaxFiles: 1, MaxWorkItems: 1}
	err := WalkRepoFilesWithinRoot(context.Background(), t.TempDir(), root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "unexpected lstat") {
		t.Fatalf("expected root lstat error, got %v", err)
	}
}

func TestWalkRepoFilesWithinRootRootMustBeDirectory(t *testing.T) {
	repo := t.TempDir()
	filePath := filepath.Join(repo, "repo.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat root file: %v", err)
	}
	root := &sharedWalkTestRoot{info: info}
	budget := RootedWalkBudget{MaxTraversalEntries: 1, MaxFiles: 1, MaxWorkItems: 1}

	err = WalkRepoFilesWithinRoot(context.Background(), filePath, root, budget, nil, func(string, fs.DirEntry) error { return nil })
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected invalid-root error, got %v", err)
	}
}

func TestOpenPinnedChildRootBranchCoverage(t *testing.T) {
	t.Run("lookup error", testOpenPinnedChildRootLookupError)
	t.Run("symlink rejection", testOpenPinnedChildRootSymlinkRejection)
	t.Run("non-directory rejection", testOpenPinnedChildRootNonDirectory)
	t.Run("open root failure", testOpenPinnedChildRootOpenFailure)
	t.Run("child lookup close join", testOpenPinnedChildRootLookupCloseJoin)
}

func testOpenPinnedChildRootLookupError(t *testing.T) {
	lookupErr := errors.New("lookup child")
	root := &sharedPinnedChildRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "child" {
				t.Fatalf("unexpected child lookup %q", name)
			}
			return nil, lookupErr
		},
	}
	child, err := openPinnedChildRoot(root, "child", "child")
	if child != nil || !errors.Is(err, lookupErr) {
		t.Fatalf("expected lookup error, got child=%v err=%v", child, err)
	}
}

func testOpenPinnedChildRootSymlinkRejection(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	linkPath := filepath.Join(parent, "child")
	if err := os.Symlink(target, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	root := &sharedPinnedChildRoot{
		lstat: func(string) (fs.FileInfo, error) { return linkInfo, nil },
	}
	child, err := openPinnedChildRoot(root, "child", "child")
	if child != nil || err == nil || !strings.Contains(err.Error(), "root contains symlink: child") {
		t.Fatalf("expected symlink rejection, got child=%v err=%v", child, err)
	}
}

func testOpenPinnedChildRootNonDirectory(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "child.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	root := &sharedPinnedChildRoot{
		lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil },
	}
	child, err := openPinnedChildRoot(root, "child", "child")
	if child != nil || err == nil || !strings.Contains(err.Error(), "root is not a directory: child") {
		t.Fatalf("expected non-directory rejection, got child=%v err=%v", child, err)
	}
}

func testOpenPinnedChildRootOpenFailure(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	openErr := errors.New("open child root")
	root := &sharedPinnedChildRoot{
		lstat:    func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (safeio.Root, error) { return nil, openErr },
	}
	child, err := openPinnedChildRoot(root, "child", "child")
	if child != nil || !errors.Is(err, openErr) {
		t.Fatalf("expected open root error, got child=%v err=%v", child, err)
	}
}

func testOpenPinnedChildRootLookupCloseJoin(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	lstatErr := errors.New("lstat opened child")
	closeErr := errors.New("close opened child")
	root := &sharedPinnedChildRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (safeio.Root, error) {
			return &sharedPinnedChildRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, lstatErr },
				close: func() error { return closeErr },
			}, nil
		},
	}
	child, err := openPinnedChildRoot(root, "child", "child")
	if child != nil || !errors.Is(err, lstatErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined child lookup and close errors, got child=%v err=%v", child, err)
	}
}
