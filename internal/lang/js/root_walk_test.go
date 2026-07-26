package js

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestWalkRootNoFollowStopPropagatesAcrossRecursiveFrames(t *testing.T) {
	rootDir := createRootWalkFixture(t, []string{
		filepath.Join("a", "one", "LICENSE"),
		filepath.Join("a", "two", "COPYING"),
		filepath.Join("b", "three", "LICENSE"),
		filepath.Join("b", "four", "COPYING"),
		filepath.Join("c", "five", "LICENSE"),
		filepath.Join("z", "late", "LICENSE"),
	})
	root := openRootWalkFixture(t, rootDir)
	defer closeRootWalkFixture(t, root)

	var visited []string
	err := walkRootNoFollow(context.Background(), root, func(relPath string, info os.FileInfo) (bool, bool, error) {
		if info.IsDir() {
			return false, false, nil
		}
		if isLicenseCandidate(relPath) {
			visited = append(visited, relPath)
		}
		return false, len(visited) >= 5, nil
	})

	if err != nil {
		t.Fatalf("walk root: %v", err)
	}
	if len(visited) != 5 {
		t.Fatalf("expected stop after 5 files, got %d with %#v", len(visited), visited)
	}
	assertRootWalkOmitsLateSubtree(t, visited, filepath.Join("z", "late"))
}

func TestWalkRootNoFollowBestEffortContinuesPastUnreadableSubtree(t *testing.T) {
	root := newBlockedSubtreeRootWalkFixture(t)

	var visited []string
	err := walkRootNoFollowBestEffort(context.Background(), root, func(relPath string, info os.FileInfo) (bool, bool, error) {
		if !info.IsDir() && isLicenseCandidate(relPath) {
			visited = append(visited, relPath)
		}
		return false, false, nil
	})

	if err != nil {
		t.Fatalf("walk root best effort: %v", err)
	}
	if len(visited) != 1 || visited[0] != filepath.Join("z", "LICENSE") {
		t.Fatalf("expected later license to be preserved, got %#v", visited)
	}
}

func newBlockedSubtreeRootWalkFixture(t *testing.T) safeio.Root {
	t.Helper()

	rootDir := t.TempDir()
	blockedDir := filepath.Join(rootDir, "a", "blocked")
	if err := os.MkdirAll(blockedDir, 0o755); err != nil {
		t.Fatalf("mkdir blocked dir: %v", err)
	}
	licensePath := filepath.Join(rootDir, "z", "LICENSE")
	if err := os.MkdirAll(filepath.Dir(licensePath), 0o755); err != nil {
		t.Fatalf("mkdir license dir: %v", err)
	}
	if err := os.WriteFile(licensePath, []byte("MIT License"), 0o600); err != nil {
		t.Fatalf("write license: %v", err)
	}
	blockedInfo, err := os.Lstat(blockedDir)
	if err != nil {
		t.Fatalf("lstat blocked dir: %v", err)
	}
	licenseInfo, err := os.Lstat(licensePath)
	if err != nil {
		t.Fatalf("lstat license: %v", err)
	}
	licenseDirInfo, err := os.Lstat(filepath.Dir(licensePath))
	if err != nil {
		t.Fatalf("lstat license dir: %v", err)
	}

	return &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, errors.New("unexpected open path")
			}
			return &fakeReadDirFile{
				entries: []os.DirEntry{
					&fakeDirEntry{name: "blocked", mode: blockedInfo.Mode(), info: blockedInfo},
					&fakeDirEntry{name: "z", mode: licenseDirInfo.Mode(), info: licenseDirInfo},
				},
			}, nil
		},
		lstat: func(name string) (os.FileInfo, error) {
			switch name {
			case "blocked":
				return blockedInfo, nil
			case "z":
				return licenseDirInfo, nil
			default:
				return nil, errors.New("unexpected lstat path")
			}
		},
		openRoot: func(name string) (safeio.Root, error) {
			switch name {
			case "blocked":
				return nil, fs.ErrPermission
			case "z":
				return &fakeJSRoot{
					open: func(child string) (safeio.File, error) {
						if child != "." {
							return nil, errors.New("unexpected child open path")
						}
						return &fakeReadDirFile{
							entries: []os.DirEntry{
								&fakeDirEntry{name: "LICENSE", mode: licenseInfo.Mode(), info: licenseInfo},
							},
						}, nil
					},
					lstat: func(child string) (os.FileInfo, error) {
						switch child {
						case ".":
							return licenseDirInfo, nil
						case "LICENSE":
							return licenseInfo, nil
						default:
							return nil, errors.New("unexpected child lstat path")
						}
					},
				}, nil
			default:
				return nil, errors.New("unexpected child root path")
			}
		},
	}
}

func createRootWalkFixture(t *testing.T, relPaths []string) string {
	t.Helper()

	rootDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	for _, rel := range relPaths {
		fullPath := filepath.Join(rootDir, rel)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(fullPath, []byte("MIT License"), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return rootDir
}

func openRootWalkFixture(t *testing.T, rootDir string) safeio.Root {
	t.Helper()

	resolvedRootDir, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		t.Fatalf("resolve root dir: %v", err)
	}
	root, err := safeio.OpenRootNoFollow(resolvedRootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	return root
}

func closeRootWalkFixture(t *testing.T, root safeio.Root) {
	t.Helper()

	if err := root.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}
}

func assertRootWalkOmitsLateSubtree(t *testing.T, visited []string, relPrefix string) {
	t.Helper()

	for _, rel := range visited {
		if strings.Contains(rel, relPrefix) {
			t.Fatalf("expected stop to prevent visiting later sibling subtree, got %#v", visited)
		}
	}
}

func TestWalkRootNoFollowBestEffortContinuesPastLstatFailure(t *testing.T) {
	rootDir := t.TempDir()
	licensePath := filepath.Join(rootDir, "LICENSE")
	if err := os.WriteFile(licensePath, []byte("MIT License"), 0o600); err != nil {
		t.Fatalf("write license: %v", err)
	}
	licenseInfo, err := os.Lstat(licensePath)
	if err != nil {
		t.Fatalf("lstat license: %v", err)
	}

	root := &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, errors.New("unexpected open path")
			}
			return &fakeReadDirFile{
				entries: []os.DirEntry{
					&fakeDirEntry{name: "missing.js", mode: 0, info: licenseInfo},
					&fakeDirEntry{name: "LICENSE", mode: licenseInfo.Mode(), info: licenseInfo},
				},
			}, nil
		},
		lstat: func(name string) (os.FileInfo, error) {
			switch name {
			case "missing.js":
				return nil, errors.New("lstat failed")
			case "LICENSE":
				return licenseInfo, nil
			default:
				return nil, errors.New("unexpected lstat path")
			}
		},
	}

	var visited []string
	err = walkRootNoFollowBestEffort(context.Background(), root, func(relPath string, info os.FileInfo) (bool, bool, error) {
		if !info.IsDir() {
			visited = append(visited, relPath)
		}
		return false, false, nil
	})

	if err != nil {
		t.Fatalf("walk root best effort with lstat failure: %v", err)
	}
	if len(visited) != 1 || visited[0] != "LICENSE" {
		t.Fatalf("expected later readable entry to be visited, got %#v", visited)
	}
}

func TestWalkRootNoFollowBestEffortCallbackReportsSkippedPaths(t *testing.T) {
	root := newSkippedPathRootWalkFixture(t)

	var visited []string
	var skipped []string
	visitFn := func(relPath string, info os.FileInfo) (bool, bool, error) {
		if !info.IsDir() {
			visited = append(visited, relPath)
		}
		return false, false, nil
	}
	skipFn := func(relPath string, _ error) {
		skipped = append(skipped, relPath)
	}
	err := walkRootNoFollowBestEffortWithErrorCallback(context.Background(), root, visitFn, skipFn)
	if err != nil {
		t.Fatalf("walk root best effort with callback: %v", err)
	}
	if len(visited) != 1 || visited[0] != "LICENSE" {
		t.Fatalf("expected later readable entry to be visited, got %#v", visited)
	}
	if len(skipped) != 1 || skipped[0] != "blocked" {
		t.Fatalf("expected skipped path callback, got %#v", skipped)
	}
}

func newSkippedPathRootWalkFixture(t *testing.T) safeio.Root {
	t.Helper()

	rootDir := t.TempDir()
	blockedDir := filepath.Join(rootDir, "blocked")
	if err := os.Mkdir(blockedDir, 0o755); err != nil {
		t.Fatalf("mkdir blocked dir: %v", err)
	}
	licensePath := filepath.Join(rootDir, "LICENSE")
	if err := os.WriteFile(licensePath, []byte("MIT License"), 0o600); err != nil {
		t.Fatalf("write license: %v", err)
	}
	blockedInfo, err := os.Lstat(blockedDir)
	if err != nil {
		t.Fatalf("lstat blocked dir: %v", err)
	}
	licenseInfo, err := os.Lstat(licensePath)
	if err != nil {
		t.Fatalf("lstat license: %v", err)
	}
	infos := map[string]os.FileInfo{
		"blocked": blockedInfo,
		"LICENSE": licenseInfo,
	}

	return &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, errors.New("unexpected open path")
			}
			return &fakeReadDirFile{
				entries: []os.DirEntry{
					&fakeDirEntry{name: "blocked", mode: blockedInfo.Mode(), info: blockedInfo},
					&fakeDirEntry{name: "LICENSE", mode: licenseInfo.Mode(), info: licenseInfo},
				},
			}, nil
		},
		lstat: func(name string) (os.FileInfo, error) {
			info, ok := infos[name]
			if !ok {
				return nil, errors.New("unexpected lstat path")
			}
			return info, nil
		},
		openRoot: func(name string) (safeio.Root, error) {
			if name != "blocked" {
				return nil, errors.New("unexpected child root path")
			}
			return nil, errors.New("blocked subtree")
		},
	}
}

func TestWalkRootNoFollowBestEffortReturnsUnreadableRoot(t *testing.T) {
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return nil, errors.New("open root failed")
		},
	}

	visited := false
	err := walkRootNoFollowBestEffort(context.Background(), root, func(string, os.FileInfo) (bool, bool, error) {
		visited = true
		return false, false, nil
	})

	if err == nil || !strings.Contains(err.Error(), "open root failed") {
		t.Fatalf("expected unreadable root to return its error, got %v", err)
	}
	if visited {
		t.Fatal("expected unreadable root failure to prevent visits")
	}
}

func TestReadRootWalkEntriesSuppressesNestedReadErrorWhenCallbackContinues(t *testing.T) {
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return &fakeReadDirFile{readDirErr: errors.New("readdir failed")}, nil
		},
	}

	callbacks := 0
	visit := func(string, os.FileInfo) (bool, bool, error) {
		t.Fatal("expected read error to prevent visits")
		return false, false, nil
	}
	onError := func(relPath string, walkErr error) bool {
		callbacks++
		if relPath != filepath.Join("nested", "dir") {
			t.Fatalf("unexpected callback path %q", relPath)
		}
		if !strings.Contains(walkErr.Error(), "readdir failed") {
			t.Fatalf("unexpected callback error %v", walkErr)
		}
		return true
	}
	err := walkRootNoFollowFrom(context.Background(), root, filepath.Join("nested", "dir"), visit, &rootWalkState{}, onError)
	if err != nil {
		t.Fatalf("expected nested read error to be suppressed, got %v", err)
	}
	if callbacks != 1 {
		t.Fatalf("expected one skip callback, got %d", callbacks)
	}
}

func TestReadRootWalkEntriesReturnsTopLevelReadErrorWithoutCallback(t *testing.T) {
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return &fakeReadDirFile{readDirErr: errors.New("readdir failed")}, nil
		},
	}

	callbacks := 0
	visit := func(string, os.FileInfo) (bool, bool, error) {
		t.Fatal("expected read error to prevent visits")
		return false, false, nil
	}
	onError := func(string, error) {
		callbacks++
	}
	err := walkRootNoFollowBestEffortWithErrorCallback(context.Background(), root, visit, onError)
	if err == nil || !strings.Contains(err.Error(), "readdir failed") {
		t.Fatalf("expected top-level read error to propagate, got %v", err)
	}
	if callbacks != 0 {
		t.Fatalf("expected top-level read failure to avoid callback, got %d callbacks", callbacks)
	}
}

func TestWalkRootNoFollowPropagatesLstatErrorWithoutBestEffort(t *testing.T) {
	root := &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, errors.New("unexpected open path")
			}
			return &fakeReadDirFile{entries: []os.DirEntry{&fakeDirEntry{name: "bad.js"}}}, nil
		},
		lstat: func(string) (os.FileInfo, error) {
			return nil, errors.New("lstat failed")
		},
	}

	err := walkRootNoFollow(context.Background(), root, func(string, os.FileInfo) (bool, bool, error) {
		return false, false, nil
	})

	if err == nil || !strings.Contains(err.Error(), "lstat failed") {
		t.Fatalf("expected lstat failure to propagate without best-effort mode, got %v", err)
	}
}

func TestWalkRootNoFollowPropagatesChildOpenErrorWithoutBestEffort(t *testing.T) {
	rootDir := t.TempDir()
	childDir := filepath.Join(rootDir, "child")
	if err := os.Mkdir(childDir, 0o755); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}
	childInfo, err := os.Lstat(childDir)
	if err != nil {
		t.Fatalf("lstat child dir: %v", err)
	}

	root := &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, errors.New("unexpected open path")
			}
			return &fakeReadDirFile{entries: []os.DirEntry{&fakeDirEntry{name: "child", mode: childInfo.Mode(), info: childInfo}}}, nil
		},
		lstat: func(string) (os.FileInfo, error) {
			return childInfo, nil
		},
		openRoot: func(string) (safeio.Root, error) {
			return nil, errors.New("open child failed")
		},
	}

	err = walkRootNoFollow(context.Background(), root, func(string, os.FileInfo) (bool, bool, error) {
		return false, false, nil
	})

	if err == nil || !strings.Contains(err.Error(), "open child failed") {
		t.Fatalf("expected child-open failure to propagate without best-effort mode, got %v", err)
	}
}

func TestWalkRootNoFollowFromReturnsImmediatelyWhenStateAlreadyStopped(t *testing.T) {
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			t.Fatal("expected stopped walk to avoid opening the root")
			return nil, nil
		},
	}

	visited := false
	visit := func(string, os.FileInfo) (bool, bool, error) {
		visited = true
		return false, false, nil
	}
	state := &rootWalkState{stopped: true}
	err := walkRootNoFollowFrom(context.Background(), root, "", visit, state, nil)
	if err != nil {
		t.Fatalf("expected stopped walk to return nil, got %v", err)
	}
	if visited {
		t.Fatal("expected stopped walk to avoid visits")
	}
}

func TestWalkRootNoFollowStreamsHugeDirectoryWithinEntryBudget(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "file.js")
	if err := os.WriteFile(filePath, []byte("export {}\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fileInfo, err := os.Lstat(filePath)
	if err != nil {
		t.Fatalf("lstat fixture: %v", err)
	}

	reader := &boundedRootWalkReadDirFile{total: 1_000_000}
	var lstatNames []string
	var visited []string
	root := &fakeJSRoot{
		open: func(name string) (safeio.File, error) {
			if name != "." {
				return nil, fmt.Errorf("unexpected open path %q", name)
			}
			return reader, nil
		},
		lstat: func(name string) (os.FileInfo, error) {
			lstatNames = append(lstatNames, name)
			return fileInfo, nil
		},
	}

	visit := func(relPath string, _ os.FileInfo) (bool, bool, error) {
		visited = append(visited, relPath)
		return false, false, nil
	}
	summary, err := walkRootNoFollowContext(context.Background(), root, newJSWalkEntryBudget(3), visit, nil)
	if err != nil {
		t.Fatalf("stream bounded root: %v", err)
	}
	if !summary.truncated || summary.entriesVisited != 3 {
		t.Fatalf("unexpected walk summary: %#v", summary)
	}
	if reader.enumerated != 4 || !slices.Equal(reader.requests, []int{4}) {
		t.Fatalf("expected one budget-plus-lookahead directory read, enumerated=%d requests=%v", reader.enumerated, reader.requests)
	}
	if len(lstatNames) != 3 || len(visited) != 3 {
		t.Fatalf("expected only budgeted entries to be touched, lstat=%v visited=%v", lstatNames, visited)
	}
	if reader.totalNameCalls() >= reader.total {
		t.Fatalf("expected sorting to remain batch-bounded, got %d name calls for %d entries", reader.totalNameCalls(), reader.total)
	}
	if reader.entries[3].nameCalls != 0 {
		t.Fatalf("expected overflow sentinel never to be named, got %d calls", reader.entries[3].nameCalls)
	}
	if !slices.IsSorted(visited) {
		t.Fatalf("expected each bounded batch to be deterministic, got %v", visited)
	}
}

func TestWalkRootNoFollowCancellationBeforeBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	openCalls := 0
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			openCalls++
			return nil, errors.New("unexpected open")
		},
	}
	summary, err := walkRootNoFollowContext(ctx, root, newJSWalkEntryBudget(1000), noOpRootWalkVisit, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before directory open, got %v", err)
	}
	if openCalls != 0 || summary.entriesVisited != 0 {
		t.Fatalf("expected no work before a canceled batch, opens=%d summary=%#v", openCalls, summary)
	}
}

func TestWalkRootNoFollowCancellationBeforeEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &boundedRootWalkReadDirFile{
		total:        1_000_000,
		cancelOnRead: cancel,
	}
	lstatCalls := 0
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return reader, nil
		},
		lstat: func(string) (os.FileInfo, error) {
			lstatCalls++
			return nil, errors.New("unexpected lstat")
		},
	}

	summary, err := walkRootNoFollowContext(ctx, root, newJSWalkEntryBudget(1000), noOpRootWalkVisit, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before first entry, got %v", err)
	}
	if reader.enumerated != jsWalkReadDirBatchSize || !slices.Equal(reader.requests, []int{jsWalkReadDirBatchSize}) {
		t.Fatalf("expected cancellation latency bounded to one batch, enumerated=%d requests=%v", reader.enumerated, reader.requests)
	}
	if lstatCalls != 0 || reader.totalNameCalls() != 0 {
		t.Fatalf("expected canceled entries not to be touched, lstat=%d names=%d", lstatCalls, reader.totalNameCalls())
	}
	if summary.entriesVisited != jsWalkReadDirBatchSize {
		t.Fatalf("expected enumerated work in summary, got %#v", summary)
	}
}

func TestWalkRootNoFollowCancellationBeforeDescentStopsWithoutChildOpen(t *testing.T) {
	dirPath := filepath.Join(t.TempDir(), "child")
	if err := os.Mkdir(dirPath, 0o700); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	dirInfo, err := os.Lstat(dirPath)
	if err != nil {
		t.Fatalf("lstat child: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reader := &boundedRootWalkReadDirFile{total: 1}
	openRootCalls := 0
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return reader, nil
		},
		lstat: func(string) (os.FileInfo, error) {
			return dirInfo, nil
		},
		openRoot: func(string) (safeio.Root, error) {
			openRootCalls++
			return nil, errors.New("unexpected descent")
		},
	}
	visit := func(string, os.FileInfo) (bool, bool, error) {
		cancel()
		return false, false, nil
	}
	_, err = walkRootNoFollowContext(ctx, root, newJSWalkEntryBudget(10), visit, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before descent, got %v", err)
	}
	if openRootCalls != 0 {
		t.Fatalf("expected canceled descent not to open child root, got %d calls", openRootCalls)
	}
}

func TestWalkRootNoFollowCancellationBeforeNextBatchStopsWithoutSecondRead(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "file.js")
	if err := os.WriteFile(filePath, []byte("export {}\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fileInfo, err := os.Lstat(filePath)
	if err != nil {
		t.Fatalf("lstat fixture: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reader := &boundedRootWalkReadDirFile{total: jsWalkReadDirBatchSize + 1}
	visits := 0
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return reader, nil
		},
		lstat: func(string) (os.FileInfo, error) {
			return fileInfo, nil
		},
	}
	visit := func(string, os.FileInfo) (bool, bool, error) {
		visits++
		if visits == jsWalkReadDirBatchSize {
			cancel()
		}
		return false, false, nil
	}
	_, err = walkRootNoFollowContext(ctx, root, newJSWalkEntryBudget(1000), visit, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation before the next batch, got %v", err)
	}
	if !slices.Equal(reader.requests, []int{jsWalkReadDirBatchSize}) {
		t.Fatalf("expected no second directory read after cancellation, got requests %v", reader.requests)
	}
}

func TestWalkRootNoFollowHandlesZeroBudgetAndNoProgress(t *testing.T) {
	t.Run("zero budget", func(t *testing.T) {
		reader := &boundedRootWalkReadDirFile{total: 1}
		root := &fakeJSRoot{
			open: func(string) (safeio.File, error) {
				return reader, nil
			},
		}
		summary, err := walkRootNoFollowContext(context.Background(), root, newJSWalkEntryBudget(0), noOpRootWalkVisit, nil)
		if err != nil {
			t.Fatalf("walk zero budget: %v", err)
		}
		if !summary.truncated || summary.entriesVisited != 0 || !slices.Equal(reader.requests, []int{1}) {
			t.Fatalf("expected zero budget to use one untouched lookahead, summary=%#v requests=%v", summary, reader.requests)
		}
		if reader.enumerated != 1 || reader.totalNameCalls() != 0 {
			t.Fatalf("expected zero-budget sentinel to remain untouched, enumerated=%d names=%d", reader.enumerated, reader.totalNameCalls())
		}
		if err := rootWalkResult(summary, nil); !errors.Is(err, errJSWalkEntryBudgetExceeded) {
			t.Fatalf("expected truncated summary to produce budget error, got %v", err)
		}
	})

	t.Run("reader makes no progress", func(t *testing.T) {
		reader := &boundedRootWalkReadDirFile{total: 1, noProgress: true}
		root := &fakeJSRoot{
			open: func(string) (safeio.File, error) {
				return reader, nil
			},
		}
		_, err := walkRootNoFollowContext(context.Background(), root, newJSWalkEntryBudget(1), noOpRootWalkVisit, nil)
		if !errors.Is(err, io.ErrNoProgress) {
			t.Fatalf("expected no-progress reader error, got %v", err)
		}
	})
}

func TestWalkRootNoFollowLimitMinusOneCompletes(t *testing.T) {
	assertRootWalkBudgetCase(t, 2, false, []int{4, 2})
}

func TestWalkRootNoFollowExactLimitCompletesAfterEOFProbe(t *testing.T) {
	assertRootWalkBudgetCase(t, 3, false, []int{4, 1})
}

func TestWalkRootNoFollowLimitPlusOneTruncatesAtSentinel(t *testing.T) {
	assertRootWalkBudgetCase(t, 4, true, []int{4})
}

func assertRootWalkBudgetCase(t *testing.T, total int, wantTruncated bool, wantRequests []int) {
	t.Helper()

	fileInfo := rootWalkRegularFileInfo(t)
	reader := &boundedRootWalkReadDirFile{total: total}
	var lstatNames []string
	var visited []string
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return reader, nil
		},
		lstat: func(name string) (os.FileInfo, error) {
			lstatNames = append(lstatNames, name)
			return fileInfo, nil
		},
	}
	visit := func(relPath string, _ os.FileInfo) (bool, bool, error) {
		visited = append(visited, relPath)
		return false, false, nil
	}

	summary, err := walkRootNoFollowContext(context.Background(), root, newJSWalkEntryBudget(3), visit, nil)
	if err != nil {
		t.Fatalf("walk exact budget case: %v", err)
	}
	wantVisited := min(total, 3)
	if summary.truncated != wantTruncated || summary.entriesVisited != wantVisited {
		t.Fatalf("unexpected summary for total %d: %#v", total, summary)
	}
	if reader.enumerated != total || !slices.Equal(reader.requests, wantRequests) {
		t.Fatalf("unexpected bounded reads for total %d: enumerated=%d requests=%v", total, reader.enumerated, reader.requests)
	}
	if len(lstatNames) != wantVisited || len(visited) != wantVisited {
		t.Fatalf("unexpected entry touches for total %d: lstat=%v visited=%v", total, lstatNames, visited)
	}
	if wantTruncated && reader.entries[3].nameCalls != 0 {
		t.Fatalf("overflow sentinel was named %d times", reader.entries[3].nameCalls)
	}
}

func TestWalkRootNoFollowPartialMembershipFollowsEnumerationButOrderIsSorted(t *testing.T) {
	first := collectPartialRootWalkMembership(t, []string{"c.js", "a.js", "b.js"})
	second := collectPartialRootWalkMembership(t, []string{"b.js", "c.js", "a.js"})

	if !slices.Equal(first, []string{"a.js", "c.js"}) {
		t.Fatalf("unexpected first partial membership: %v", first)
	}
	if !slices.Equal(second, []string{"b.js", "c.js"}) {
		t.Fatalf("unexpected second partial membership: %v", second)
	}
}

func collectPartialRootWalkMembership(t *testing.T, names []string) []string {
	t.Helper()

	fileInfo := rootWalkRegularFileInfo(t)
	reader := &boundedRootWalkReadDirFile{total: len(names), names: names}
	var visited []string
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return reader, nil
		},
		lstat: func(string) (os.FileInfo, error) {
			return fileInfo, nil
		},
	}
	visit := func(relPath string, _ os.FileInfo) (bool, bool, error) {
		visited = append(visited, relPath)
		return false, false, nil
	}
	summary, err := walkRootNoFollowContext(context.Background(), root, newJSWalkEntryBudget(2), visit, nil)
	if err != nil {
		t.Fatalf("collect partial membership: %v", err)
	}
	if !summary.truncated || !slices.IsSorted(visited) {
		t.Fatalf("expected sorted partial output with truncation, summary=%#v visited=%v", summary, visited)
	}
	if reader.entries[2].nameCalls != 0 {
		t.Fatalf("expected differing-order sentinel to remain unnamed, got %d calls", reader.entries[2].nameCalls)
	}
	return visited
}

func TestWalkRootNoFollowDiscardsEntriesReturnedWithNonEOFError(t *testing.T) {
	readErr := errors.New("partial directory read failed")
	reader := &boundedRootWalkReadDirFile{total: 3, readDirErr: readErr}
	lstatCalls := 0
	root := &fakeJSRoot{
		open: func(string) (safeio.File, error) {
			return reader, nil
		},
		lstat: func(string) (os.FileInfo, error) {
			lstatCalls++
			return nil, errors.New("unexpected lstat")
		},
	}
	visits := 0
	visit := func(string, os.FileInfo) (bool, bool, error) {
		visits++
		return false, false, nil
	}

	summary, err := walkRootNoFollowContext(context.Background(), root, newJSWalkEntryBudget(2), visit, nil)
	if !errors.Is(err, readErr) {
		t.Fatalf("expected partial ReadDir error, got %v", err)
	}
	if summary.entriesVisited != 2 || reader.enumerated != 3 {
		t.Fatalf("expected returned entries to count as bounded work, summary=%#v enumerated=%d", summary, reader.enumerated)
	}
	if lstatCalls != 0 || visits != 0 || reader.totalNameCalls() != 0 {
		t.Fatalf("expected errored entries to be discarded untouched, lstat=%d visits=%d names=%d", lstatCalls, visits, reader.totalNameCalls())
	}
}

func rootWalkRegularFileInfo(t *testing.T) os.FileInfo {
	t.Helper()

	path := filepath.Join(t.TempDir(), "file.js")
	if err := os.WriteFile(path, []byte("export {}\n"), 0o600); err != nil {
		t.Fatalf("write root-walk fixture: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat root-walk fixture: %v", err)
	}
	return info
}

func noOpRootWalkVisit(string, os.FileInfo) (bool, bool, error) {
	return false, false, nil
}

type boundedRootWalkReadDirFile struct {
	total        int
	names        []string
	enumerated   int
	requests     []int
	entries      []*boundedRootWalkDirEntry
	cancelOnRead context.CancelFunc
	noProgress   bool
	readDirErr   error
}

func (*boundedRootWalkReadDirFile) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (*boundedRootWalkReadDirFile) Write([]byte) (int, error) {
	return 0, errors.New("not implemented")
}

func (*boundedRootWalkReadDirFile) Close() error {
	return nil
}

func (*boundedRootWalkReadDirFile) Stat() (fs.FileInfo, error) {
	return nil, errors.New("not implemented")
}

func (*boundedRootWalkReadDirFile) Chmod(os.FileMode) error {
	return errors.New("not implemented")
}

func (f *boundedRootWalkReadDirFile) ReadDir(n int) ([]fs.DirEntry, error) {
	f.requests = append(f.requests, n)
	if f.noProgress {
		return nil, nil
	}
	if f.cancelOnRead != nil {
		f.cancelOnRead()
		f.cancelOnRead = nil
	}
	if f.enumerated >= f.total {
		return nil, io.EOF
	}
	count := min(n, f.total-f.enumerated)
	entries := make([]fs.DirEntry, 0, count)
	for index := 0; index < count; index++ {
		entry := &boundedRootWalkDirEntry{name: f.entryName(index)}
		f.entries = append(f.entries, entry)
		entries = append(entries, entry)
	}
	f.enumerated += count
	if f.readDirErr != nil {
		return entries, f.readDirErr
	}
	return entries, nil
}

func (f *boundedRootWalkReadDirFile) entryName(index int) string {
	if len(f.names) != 0 {
		return f.names[f.enumerated+index]
	}
	entryIndex := f.total - f.enumerated - index
	return fmt.Sprintf("entry-%07d.js", entryIndex)
}

func (f *boundedRootWalkReadDirFile) totalNameCalls() int {
	total := 0
	for _, entry := range f.entries {
		total += entry.nameCalls
	}
	return total
}

type boundedRootWalkDirEntry struct {
	name      string
	nameCalls int
}

func (e *boundedRootWalkDirEntry) Name() string {
	e.nameCalls++
	return e.name
}

func (*boundedRootWalkDirEntry) IsDir() bool {
	return false
}

func (*boundedRootWalkDirEntry) Type() fs.FileMode {
	return 0
}

func (*boundedRootWalkDirEntry) Info() (fs.FileInfo, error) {
	return nil, errors.New("not implemented")
}
