package safeio

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeReadDirFile struct {
	*fakeFile
	readDir func(count int) ([]fs.DirEntry, error)
}

func (f *fakeReadDirFile) ReadDir(count int) ([]fs.DirEntry, error) {
	if f.readDir != nil {
		return f.readDir(count)
	}
	return nil, nil
}

func TestOpenPinnedFileReadsNestedFileAndClosesAncestors(t *testing.T) {
	repo := t.TempDir()
	targetPath := filepath.Join(repo, "nested", "leaf", "child.txt")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("mkdir nested file parent: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write nested file: %v", err)
	}

	root := openTestRoot(t, repo)
	file, err := OpenPinnedFile(root, filepath.Join("nested", "leaf", "child.txt"))
	if err != nil {
		t.Fatalf("open pinned file: %v", err)
	}

	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read/close pinned file: %v", errors.Join(readErr, closeErr))
	}
	if string(data) != "content" {
		t.Fatalf("unexpected pinned file content: %q", string(data))
	}
}

func TestPinnedFileCloseJoinsFileAndRootCloseErrors(t *testing.T) {
	fileCloseErr := errors.New("close file")
	rootCloseErr := errors.New("close root")
	err := (&pinnedFile{
		File:  &fakeFile{close: func() error { return fileCloseErr }},
		roots: []Root{&fakeRoot{close: func() error { return rootCloseErr }}},
	}).Close()
	if !errors.Is(err, fileCloseErr) || !errors.Is(err, rootCloseErr) {
		t.Fatalf("expected pinned file close to join file and root close errors, got %v", err)
	}
}

func TestOpenPinnedDirectoryReadsNestedDirectoryAndClosesAncestors(t *testing.T) {
	repo := t.TempDir()
	leafDir := filepath.Join(repo, "nested", "leaf")
	if err := os.MkdirAll(leafDir, 0o755); err != nil {
		t.Fatalf("mkdir leaf dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(leafDir, "child.txt"), []byte("content"), 0o600); err != nil {
		t.Fatalf("write child file: %v", err)
	}

	root := openTestRoot(t, repo)
	dir, err := OpenPinnedDirectory(root, filepath.Join("nested", "leaf"))
	if err != nil {
		t.Fatalf("open pinned directory: %v", err)
	}

	entries, err := dir.ReadDir(-1)
	if err != nil {
		t.Fatalf("read pinned directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "child.txt" {
		t.Fatalf("unexpected pinned directory entries: %#v", entries)
	}
	if err := dir.Close(); err != nil {
		t.Fatalf("close pinned directory: %v", err)
	}
}

func TestOpenPinnedDirectoryRejectsNonReadDirFileAndJoinsCloseErrors(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	fileCloseErr := errors.New("close opened file")
	rootCloseErr := errors.New("close pinned ancestor")
	childRoot := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "." || name == "leaf" {
				return dirInfo, nil
			}
			if name != "leaf" {
				t.Fatalf("unexpected child lstat %q", name)
			}
			return nil, fs.ErrNotExist
		},
		open: func(name string) (File, error) {
			if name != "leaf" {
				t.Fatalf("unexpected child open %q", name)
			}
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return dirInfo, nil },
				close: func() error { return fileCloseErr },
			}, nil
		},
		close: func() error { return rootCloseErr },
	}
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "nested" {
				t.Fatalf("unexpected root lstat %q", name)
			}
			return dirInfo, nil
		},
		openRoot: func(name string) (Root, error) {
			if name != "nested" {
				t.Fatalf("unexpected root openRoot %q", name)
			}
			return childRoot, nil
		},
	}

	dir, err := OpenPinnedDirectory(root, filepath.Join("nested", "leaf"))
	if dir != nil {
		t.Fatal("expected invalid pinned directory wrapper to return nil")
	}
	if !errors.Is(err, fs.ErrInvalid) || !errors.Is(err, fileCloseErr) || !errors.Is(err, rootCloseErr) {
		t.Fatalf("expected invalid directory joined with file and root close errors, got %v", err)
	}
}

func TestOpenPinnedDirectoryRejectsRootDirectoryHandleWithoutReadDir(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	closeErr := errors.New("close root directory file")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "leaf" {
				t.Fatalf("unexpected root lstat %q", name)
			}
			return dirInfo, nil
		},
		open: func(name string) (File, error) {
			if name != "leaf" {
				t.Fatalf("unexpected root open %q", name)
			}
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return dirInfo, nil },
				close: func() error { return closeErr },
			}, nil
		},
	}

	dir, err := OpenPinnedDirectory(root, "leaf")
	if dir != nil {
		t.Fatal("expected invalid root directory wrapper to return nil")
	}
	if !errors.Is(err, fs.ErrInvalid) || !errors.Is(err, closeErr) {
		t.Fatalf("expected invalid directory joined with file close error, got %v", err)
	}
}

func TestTargetPathSymlinkErrorFormatsSentinelAndPath(t *testing.T) {
	err := &targetPathSymlinkError{path: "nested/link"}
	if !errors.Is(err, ErrTargetPathSymlink) {
		t.Fatalf("expected symlink sentinel identity, got %v", err)
	}
	if got := err.Error(); !strings.Contains(got, ErrTargetPathSymlink.Error()) || !strings.Contains(got, "nested/link") {
		t.Fatalf("unexpected symlink error text: %q", got)
	}
}

func TestCloseFileWithErrorPreservesPrimaryErrorWhenCloseSucceeds(t *testing.T) {
	primary := errors.New("primary")
	err := closeFileWithError(&fakeFile{close: func() error { return nil }}, primary)
	if !errors.Is(err, primary) {
		t.Fatalf("expected primary error to survive, got %v", err)
	}
}

func TestCloseFileWithErrorJoinsCloseError(t *testing.T) {
	primary := errors.New("primary")
	closeErr := errors.New("close file")
	err := closeFileWithError(&fakeFile{close: func() error { return closeErr }}, primary)
	if !errors.Is(err, primary) || !errors.Is(err, closeErr) {
		t.Fatalf("expected closeFileWithError to join primary and close errors, got %v", err)
	}
}

func TestNormalizePathEscapesRootErrorReturnsNilForNilError(t *testing.T) {
	if err := normalizePathEscapesRootError("child.txt", nil); err != nil {
		t.Fatalf("expected nil error to stay nil, got %v", err)
	}
}

func TestNormalizePathEscapesRootErrorPreservesExistingSentinel(t *testing.T) {
	err := newPathEscapesRootError("child.txt")
	got := normalizePathEscapesRootError("other.txt", err)
	var pathErr *pathEscapesRootError
	if !errors.As(got, &pathErr) || pathErr.path != "child.txt" {
		t.Fatalf("expected existing sentinel payload to be preserved, got %#v", got)
	}
}

func TestPathEscapesRootInvariantRejectsBlankPath(t *testing.T) {
	if pathEscapesRootInvariant("   ") {
		t.Fatal("expected blank path not to be treated as an escape invariant")
	}
}

func TestIsPureSentinelErrorRecognizesDirectAndJoinedSentinels(t *testing.T) {
	if !isPureSentinelError(fs.ErrNotExist, fs.ErrNotExist) {
		t.Fatal("expected direct sentinel match to be pure")
	}
	if !isPureSentinelError(errors.Join(nil, fs.ErrNotExist), fs.ErrNotExist) {
		t.Fatal("expected joined sentinel with nil cause to be pure")
	}
}

func TestIsPureSentinelErrorRejectsNilErrorAndMissingSentinels(t *testing.T) {
	if isPureSentinelError(nil, fs.ErrNotExist) {
		t.Fatal("expected nil error to be impure")
	}
	if isPureSentinelError(fs.ErrNotExist) {
		t.Fatal("expected missing sentinel set to be impure")
	}
}

func TestArePureSentinelCausesRejectMixedCauses(t *testing.T) {
	if arePureSentinelCauses([]error{fs.ErrNotExist, errors.New("other")}, []error{fs.ErrNotExist}) {
		t.Fatal("expected mixed sentinel causes to be impure")
	}
}

func TestOpenPinnedChildAtPathNormalizesRootEscapeOpenError(t *testing.T) {
	fixturePath := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fileInfo := statTestPath(t, fixturePath)
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "child.txt" {
				t.Fatalf("unexpected lstat %q", name)
			}
			return fileInfo, nil
		},
		open: func(name string) (File, error) {
			if name != "child.txt" {
				t.Fatalf("unexpected open %q", name)
			}
			return nil, &fs.PathError{
				Op:   "openat",
				Path: filepath.Join("..", "child.txt"),
				Err:  errors.New("localized rooted-open escape"),
			}
		},
	}

	file, err := openPinnedChildAtPath(root, "child.txt", "child.txt", pinnedChildExpectFile)
	if file != nil {
		t.Fatalf("expected escaped open to return no file, got %#v", file)
	}
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected path escape sentinel, got %v", err)
	}
}

func TestOpenPinnedChildAtPathRejectsNonDirectoryWhenDirectoryExpected(t *testing.T) {
	fixturePath := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fileInfo := statTestPath(t, fixturePath)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil },
	}

	file, err := openPinnedChildAtPath(root, "child.txt", "child.txt", pinnedChildExpectDirectory)
	if file != nil {
		t.Fatalf("expected non-directory to be rejected, got %#v", file)
	}
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected invalid directory error, got %v", err)
	}
}

func TestOpenPinnedChildAtPathJoinsStatAndCloseErrors(t *testing.T) {
	fixturePath := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fileInfo := statTestPath(t, fixturePath)
	statErr := errors.New("stat opened file")
	closeErr := errors.New("close opened file")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil },
		open: func(string) (File, error) {
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return nil, statErr },
				close: func() error { return closeErr },
			}, nil
		},
	}

	file, err := openPinnedChildAtPath(root, "child.txt", "child.txt", pinnedChildExpectFile)
	if file != nil {
		t.Fatalf("expected stat failure to return no file, got %#v", file)
	}
	if !errors.Is(err, statErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected stat and close errors to be joined, got %v", err)
	}
}

func TestOpenPinnedChildAtPathRejectsDirectoryWhenFileExpected(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(filePath, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fileInfo := statTestPath(t, filePath)
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	closeErr := errors.New("close opened directory")
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil },
		open: func(string) (File, error) {
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return dirInfo, nil },
				close: func() error { return closeErr },
			}, nil
		},
	}

	file, err := openPinnedChildAtPath(root, "child.txt", "child.txt", pinnedChildExpectFile)
	if file != nil {
		t.Fatalf("expected opened directory to be rejected as file, got %#v", file)
	}
	if !errors.Is(err, fs.ErrInvalid) || !errors.Is(err, closeErr) {
		t.Fatalf("expected invalid file type joined with close error, got %v", err)
	}
}

func TestOpenRootExistingAncestorNoFollowWithReturnsMissingSuffix(t *testing.T) {
	targetPath := filepath.Join(string(os.PathSeparator), "repo", "nested", "cache")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "repo" {
				t.Fatalf("unexpected lstat %q", name)
			}
			return nil, os.ErrNotExist
		},
	}
	absFn := func(string) (string, error) { return targetPath, nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(Root, string, string) (Root, string, error) {
		t.Fatal("unexpected child open")
		return nil, "", nil
	}

	opened, ancestorPath, missingParts, err := openRootExistingAncestorNoFollowWith(targetPath, absFn, filepath.Rel, openRootFn, openChildFn)
	if err != nil {
		t.Fatalf("open existing ancestor with missing suffix: %v", err)
	}
	if opened != root {
		t.Fatalf("expected existing ancestor root, got %#v", opened)
	}
	if ancestorPath != string(os.PathSeparator) {
		t.Fatalf("expected volume root ancestor, got %q", ancestorPath)
	}
	if len(missingParts) != 3 || missingParts[0] != "repo" || missingParts[2] != "cache" {
		t.Fatalf("unexpected missing suffix: %#v", missingParts)
	}
}

func TestOpenRootExistingAncestorNoFollowWithJoinsLookupAndCloseErrors(t *testing.T) {
	targetPath := filepath.Join(string(os.PathSeparator), "repo", "nested")
	lookupErr := errors.New("lookup existing ancestor")
	closeErr := errors.New("close ancestor root")
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "repo" {
				t.Fatalf("unexpected lstat %q", name)
			}
			return nil, lookupErr
		},
		close: func() error { return closeErr },
	}
	absFn := func(string) (string, error) { return targetPath, nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(Root, string, string) (Root, string, error) {
		t.Fatal("unexpected child open")
		return nil, "", nil
	}

	opened, ancestorPath, missingParts, err := openRootExistingAncestorNoFollowWith(targetPath, absFn, filepath.Rel, openRootFn, openChildFn)
	if opened != nil || ancestorPath != "" || len(missingParts) != 0 {
		t.Fatalf("expected failed ancestor lookup to return no state, got root=%#v path=%q missing=%#v", opened, ancestorPath, missingParts)
	}
	if !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected lookup and close errors to be joined, got %v", err)
	}
}

func TestOpenRootNoFollowWithPropagatesRelativePathError(t *testing.T) {
	relErr := errors.New("compute relative path")
	absFn := func(string) (string, error) { return "/repo", nil }
	relFn := func(string, string) (string, error) { return "", relErr }
	openRootFn := func(string) (Root, error) {
		t.Fatal("unexpected root open")
		return nil, nil
	}
	openChildFn := func(Root, string, string) (Root, string, error) {
		t.Fatal("unexpected child open")
		return nil, "", nil
	}

	root, err := openRootNoFollowWith("/repo", absFn, relFn, openRootFn, openChildFn)
	if root != nil || !errors.Is(err, relErr) {
		t.Fatalf("expected relative-path error, got root=%#v err=%v", root, err)
	}
}

func TestOpenRootNoFollowWithReturnsOpenedVolumeRootForExactMatch(t *testing.T) {
	root := &fakeRoot{close: func() error { return nil }}
	absFn := func(string) (string, error) { return "/", nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(Root, string, string) (Root, string, error) {
		t.Fatal("unexpected child open")
		return nil, "", nil
	}

	opened, err := openRootNoFollowWith("/", absFn, filepath.Rel, openRootFn, openChildFn)
	if err != nil {
		t.Fatalf("open exact volume root: %v", err)
	}
	if opened != root {
		t.Fatalf("expected exact-match volume root, got %#v", opened)
	}
}

func TestOpenRootNoFollowWithSkipsDotSegments(t *testing.T) {
	root := &fakeRoot{close: func() error { return nil }}
	childInfo := statTestPath(t, t.TempDir())
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return childInfo, nil },
		close: func() error { return nil },
	}
	openCalls := 0
	absFn := func(string) (string, error) { return "/repo/child", nil }
	relFn := func(string, string) (string, error) { return filepath.Join(".", "repo", ".", "child"), nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(current Root, name, requestedPath string) (Root, string, error) {
		openCalls++
		if openCalls == 1 {
			return child, "/repo", nil
		}
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return childInfo, nil },
			close: func() error { return nil },
		}, requestedPath, nil
	}

	opened, err := openRootNoFollowWith("/repo/child", absFn, relFn, openRootFn, openChildFn)
	if err != nil {
		t.Fatalf("open rooted path with dot segments: %v", err)
	}
	if opened == nil || openCalls != 2 {
		t.Fatalf("expected two child opens after skipping dot segments, got root=%#v opens=%d", opened, openCalls)
	}
}

func TestOpenRootNoFollowWithJoinsRootAndNextCloseErrors(t *testing.T) {
	rootCloseErr := errors.New("close volume root")
	nextCloseErr := errors.New("close opened child")
	root := &fakeRoot{close: func() error { return rootCloseErr }}
	next := &fakeRoot{close: func() error { return nextCloseErr }}
	absFn := func(string) (string, error) { return "/repo", nil }
	relFn := func(string, string) (string, error) { return "repo", nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(Root, string, string) (Root, string, error) { return next, "/repo", nil }

	opened, err := openRootNoFollowWith("/repo", absFn, relFn, openRootFn, openChildFn)
	if opened != nil {
		t.Fatalf("expected close failure to return no root, got %#v", opened)
	}
	if !errors.Is(err, rootCloseErr) || !errors.Is(err, nextCloseErr) {
		t.Fatalf("expected root and next close errors to be joined, got %v", err)
	}
}

func TestOpenPinnedRootAliasWithJoinsOpenedRootLookupCloseError(t *testing.T) {
	lookupErr := errors.New("lstat opened alias")
	closeErr := errors.New("close alias root")
	targetInfo := statTestPath(t, t.TempDir())
	openRootFn := func(string) (Root, error) {
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, lookupErr },
			close: func() error { return closeErr },
		}, nil
	}
	statFn := func(string) (fs.FileInfo, error) { return targetInfo, nil }

	opened, path, err := openPinnedRootAliasWith("/private/tmp", "/tmp", openRootFn, statFn, os.SameFile)
	if opened != nil || path != "" {
		t.Fatalf("expected alias root lookup failure to return no root, got root=%#v path=%q", opened, path)
	}
	if !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected alias lookup and close errors to be joined, got %v", err)
	}
}

func TestOpenRootExistingAncestorNoFollowWithReturnsVolumeRootForExactMatch(t *testing.T) {
	root := &fakeRoot{close: func() error { return nil }}
	absFn := func(string) (string, error) { return "/", nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(Root, string, string) (Root, string, error) {
		t.Fatal("unexpected child open")
		return nil, "", nil
	}

	opened, ancestorPath, missingParts, err := openRootExistingAncestorNoFollowWith("/", absFn, filepath.Rel, openRootFn, openChildFn)
	if err != nil {
		t.Fatalf("open exact existing ancestor: %v", err)
	}
	if opened != root || ancestorPath != string(os.PathSeparator) || len(missingParts) != 0 {
		t.Fatalf("unexpected exact ancestor result: root=%#v path=%q missing=%#v", opened, ancestorPath, missingParts)
	}
}

func TestOpenRootExistingAncestorNoFollowWithSkipsDotSegments(t *testing.T) {
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return statTestPath(t, t.TempDir()), nil },
		close: func() error { return nil },
	}
	childInfo := statTestPath(t, t.TempDir())
	child := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return childInfo, nil },
		close: func() error { return nil },
	}
	openCalls := 0
	absFn := func(string) (string, error) { return "/repo/child", nil }
	relFn := func(string, string) (string, error) { return filepath.Join(".", "repo", ".", "child"), nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(current Root, name, requestedPath string) (Root, string, error) {
		openCalls++
		if openCalls == 1 {
			return child, "/repo", nil
		}
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return childInfo, nil },
			close: func() error { return nil },
		}, requestedPath, nil
	}

	opened, ancestorPath, missingParts, err := openRootExistingAncestorNoFollowWith("/repo/child", absFn, relFn, openRootFn, openChildFn)
	if err != nil {
		t.Fatalf("open existing ancestor with dot segments: %v", err)
	}
	if opened == nil || ancestorPath != "/repo/child" || len(missingParts) != 0 || openCalls != 2 {
		t.Fatalf("unexpected ancestor traversal result: root=%#v path=%q missing=%#v opens=%d", opened, ancestorPath, missingParts, openCalls)
	}
}

func TestOpenRootExistingAncestorNoFollowWithJoinsCurrentAndNextCloseErrors(t *testing.T) {
	currentCloseErr := errors.New("close current ancestor")
	nextCloseErr := errors.New("close next ancestor")
	rootInfo := statTestPath(t, t.TempDir())
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return rootInfo, nil },
		close: func() error { return currentCloseErr },
	}
	next := &fakeRoot{close: func() error { return nextCloseErr }}
	absFn := func(string) (string, error) { return "/repo", nil }
	relFn := func(string, string) (string, error) { return "repo", nil }
	openRootFn := func(string) (Root, error) { return root, nil }
	openChildFn := func(Root, string, string) (Root, string, error) { return next, "/repo", nil }

	opened, ancestorPath, missingParts, err := openRootExistingAncestorNoFollowWith("/repo", absFn, relFn, openRootFn, openChildFn)
	if opened != nil || ancestorPath != "" || len(missingParts) != 0 {
		t.Fatalf("expected failed ancestor close to return no state, got root=%#v path=%q missing=%#v", opened, ancestorPath, missingParts)
	}
	if !errors.Is(err, currentCloseErr) || !errors.Is(err, nextCloseErr) {
		t.Fatalf("expected current and next close errors to be joined, got %v", err)
	}
}

func TestOpenPinnedDirectoryReturnsDirectReadDirHandleForRootPath(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	dirFile := &fakeReadDirFile{
		fakeFile: &fakeFile{stat: func() (fs.FileInfo, error) { return dirInfo, nil }},
	}
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		open:  func(string) (File, error) { return dirFile, nil },
	}

	opened, err := OpenPinnedDirectory(root, "leaf")
	if err != nil {
		t.Fatalf("open direct read-dir handle: %v", err)
	}
	if opened != dirFile {
		t.Fatalf("expected direct read-dir handle, got %#v", opened)
	}
}

func TestOpenPinnedDirectoryRejectsRootDirectoryHandleWithoutReadDirWhenCloseSucceeds(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		open: func(string) (File, error) {
			return &fakeFile{
				stat:  func() (fs.FileInfo, error) { return dirInfo, nil },
				close: func() error { return nil },
			}, nil
		},
	}

	dir, err := OpenPinnedDirectory(root, "leaf")
	if dir != nil {
		t.Fatal("expected invalid root directory wrapper to return nil")
	}
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected invalid directory error, got %v", err)
	}
}

func TestSplitPinnedPathSkipsEmptyAndDotSegments(t *testing.T) {
	cleanName, parts := splitPinnedPath(filepath.Join(".", "nested", ".", "leaf"))
	if cleanName != filepath.Join("nested", "leaf") {
		t.Fatalf("unexpected clean name: %q", cleanName)
	}
	if len(parts) != 2 || parts[0] != "nested" || parts[1] != "leaf" {
		t.Fatalf("unexpected split parts: %#v", parts)
	}
}

func TestTrustedRootAliasTargetRejectsUntrustedPath(t *testing.T) {
	if target, ok := trustedRootAliasTarget(filepath.Join(string(os.PathSeparator), "Users", "example")); ok || target != "" {
		t.Fatalf("expected non-alias path to be rejected, got target=%q ok=%v", target, ok)
	}
}

func TestOSRootLinkCreatesHardLinkAndCloses(t *testing.T) {
	repo := t.TempDir()
	source := filepath.Join(repo, "source.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	root, err := (&osFileSystem{}).OpenRoot(repo)
	if err != nil {
		t.Fatalf("open os root: %v", err)
	}
	if err := root.Link("source.txt", "linked.txt"); err != nil {
		t.Fatalf("create hard link: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close os root: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(repo, "linked.txt"))
	if err != nil {
		t.Fatalf("read hard link: %v", err)
	}
	if string(data) != "content" {
		t.Fatalf("unexpected hard link content: %q", string(data))
	}
}

func TestOpenRootExistingAncestorNoFollowWithPropagatesSetupErrors(t *testing.T) {
	absErr := errors.New("resolve absolute ancestor path")
	relErr := errors.New("resolve relative ancestor path")
	openErr := errors.New("open ancestor volume root")

	tests := []struct {
		name       string
		absFn      func(string) (string, error)
		relFn      func(string, string) (string, error)
		openRootFn func(string) (Root, error)
		wantErr    error
	}{
		{
			name: "absolute path",
			absFn: func(string) (string, error) {
				return "", absErr
			},
			relFn: func(string, string) (string, error) {
				t.Fatal("relative path resolution must not run after absolute path failure")
				return "", nil
			},
			openRootFn: func(string) (Root, error) {
				t.Fatal("volume root open must not run after absolute path failure")
				return nil, nil
			},
			wantErr: absErr,
		},
		{
			name: "relative path",
			absFn: func(string) (string, error) {
				return filepath.Join(string(os.PathSeparator), "repo"), nil
			},
			relFn: func(string, string) (string, error) {
				return "", relErr
			},
			openRootFn: func(string) (Root, error) {
				t.Fatal("volume root open must not run after relative path failure")
				return nil, nil
			},
			wantErr: relErr,
		},
		{
			name: "volume root open",
			absFn: func(string) (string, error) {
				return filepath.Join(string(os.PathSeparator), "repo"), nil
			},
			relFn: func(string, string) (string, error) {
				return "repo", nil
			},
			openRootFn: func(string) (Root, error) {
				return nil, openErr
			},
			wantErr: openErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			openChildFn := func(Root, string, string) (Root, string, error) {
				t.Fatal("child open must not run after ancestor setup failure")
				return nil, "", nil
			}
			opened, ancestorPath, missingParts, err := openRootExistingAncestorNoFollowWith("repo", tc.absFn, tc.relFn, tc.openRootFn, openChildFn)
			if opened != nil || ancestorPath != "" || len(missingParts) != 0 {
				t.Fatalf("expected setup failure to return no ancestor state, got root=%#v path=%q missing=%#v", opened, ancestorPath, missingParts)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected setup error %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestOpenPinnedDirectoryRejectsPostOpenFileTypeChange(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	filePath := filepath.Join(t.TempDir(), "replacement.txt")
	if err := os.WriteFile(filePath, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement file: %v", err)
	}
	fileInfo := statTestPath(t, filePath)
	closeCalls := 0
	root := &fakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "child" {
				t.Fatalf("unexpected directory lookup %q", name)
			}
			return dirInfo, nil
		},
		open: func(name string) (File, error) {
			if name != "child" {
				t.Fatalf("unexpected directory open %q", name)
			}
			return &fakeFile{
				stat: func() (fs.FileInfo, error) {
					return fileInfo, nil
				},
				close: func() error {
					closeCalls++
					return nil
				},
			}, nil
		},
	}

	opened, err := OpenPinnedDirectory(root, "child")
	if opened != nil {
		t.Fatalf("expected replacement file to be rejected, got %#v", opened)
	}
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected post-open file type change to fail with fs.ErrInvalid, got %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("expected rejected replacement file to close once, got %d", closeCalls)
	}
}
