package safeio

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	unexpectedContentFmt = "unexpected content: got %q"
	writeFileErrFmt      = "write file: %v"
	missingFileName      = "missing.txt"
	resolveTargetPathErr = "resolve target path"
	rootCloseErrFmt      = "expected root close error, got %v"
)

func TestReadFileUnderReadsFileInsideRoot(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, "nested", writeTestFileName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	data, err := ReadFileUnder(rootDir, targetPath)
	if err != nil {
		t.Fatalf("ReadFileUnder returned error: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf(unexpectedContentFmt, got)
	}
}

func TestReadFileUnderRejectsSymlinkedRoot(t *testing.T) {
	canonicalRoot := t.TempDir()
	targetPath := filepath.Join(canonicalRoot, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	rootLink := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(canonicalRoot, rootLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := ReadFileUnder(rootLink, filepath.Join(rootLink, writeTestFileName))
	if err == nil {
		t.Fatal("expected symlinked root to be rejected")
	}
	if !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderReadsFileInsideTrustedTempAliasRoot(t *testing.T) {
	rootDir, _, ok := tempDirAliasPair(t)
	if !ok {
		t.Skip("trusted temp alias unavailable")
	}

	targetPath := filepath.Join(rootDir, "nested", writeTestFileName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	data, err := ReadFileUnder(rootDir, targetPath)
	if err != nil {
		t.Fatalf("ReadFileUnder returned error: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf(unexpectedContentFmt, got)
	}
}

func TestRootedReadCloserReadAtReadsRandomAccessFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reader-at.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open file: %v", err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Errorf("close file: %v", err)
		}
	})

	buffer := make([]byte, 3)
	count, err := (&rootedReadCloser{file: file}).ReadAt(buffer, 1)
	if err != nil {
		t.Fatalf("ReadAt returned error: %v", err)
	}
	if count != len(buffer) || string(buffer) != "ell" {
		t.Fatalf("unexpected random-access read: count=%d data=%q", count, string(buffer))
	}
}

func TestRootedReadCloserReadAtRejectsUnsupportedFile(t *testing.T) {
	buffer := make([]byte, 1)
	count, err := (&rootedReadCloser{file: &fakeFile{}}).ReadAt(buffer, 0)
	if err == nil || !strings.Contains(err.Error(), "random access") {
		t.Fatalf("expected random-access error, got count=%d err=%v", count, err)
	}
}

func TestRootedReadCloserStatDelegatesToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stat.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	want, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}

	got, err := (&rootedReadCloser{file: &fakeFile{stat: func() (fs.FileInfo, error) {
		return want, nil
	}}}).Stat()
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if got.Name() != want.Name() || got.Size() != want.Size() {
		t.Fatalf("unexpected stat result: got=%s/%d want=%s/%d", got.Name(), got.Size(), want.Name(), want.Size())
	}
}

func TestPermittedRootAliasPartsDarwinMappings(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only alias behavior")
	}

	cases := []struct {
		name  string
		parts []string
		want  []string
	}{
		{name: "empty", parts: nil, want: nil},
		{name: "etc", parts: []string{"etc", "hosts"}, want: []string{"private", "etc"}},
		{name: "tmp", parts: []string{"tmp", "trace.ndjson"}, want: []string{"private", "tmp"}},
		{name: "var", parts: []string{"var", "log"}, want: []string{"private", "var"}},
		{name: "other", parts: []string{"Users", "ben"}, want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := permittedRootAliasParts(tc.parts)
			if strings.Join(got, "/") != strings.Join(tc.want, "/") {
				t.Fatalf("permittedRootAliasParts(%v) = %v, want %v", tc.parts, got, tc.want)
			}
		})
	}
}

func TestPermittedRootAliasPartsReturnsNilOutsideDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("non-darwin behavior")
	}

	if got := permittedRootAliasParts([]string{"tmp", "trace.ndjson"}); len(got) != 0 {
		t.Fatalf("expected nil alias parts outside darwin, got %v", got)
	}
}

func TestNonDotPathPartsFiltersEmptyAndDotSegments(t *testing.T) {
	got := nonDotPathParts("alpha//./beta/./gamma", "/")
	if strings.Join(got, "/") != "alpha/beta/gamma" {
		t.Fatalf("unexpected filtered parts: %v", got)
	}
}

func TestNormalizeOpenParentRootNoFollowError(t *testing.T) {
	parentDir := filepath.Join(string(filepath.Separator), "tmp", "trace-parent")
	closeErr := errors.New("close failed")
	got := normalizeOpenParentRootNoFollowError(errors.Join(&RootContainsSymlinkError{Path: filepath.Join(string(filepath.Separator), "tmp", "link")}, closeErr), parentDir)
	if got.Error() != "path contains symlink: "+parentDir {
		t.Fatalf("unexpected normalized error: %v", got)
	}
	if !errors.Is(got, ErrPathContainsSymlink) {
		t.Fatalf("expected normalized error to match ErrPathContainsSymlink, got %v", got)
	}
	if !errors.Is(got, closeErr) {
		t.Fatalf("expected normalized error to retain joined close error, got %v", got)
	}
	var rootErr *RootContainsSymlinkError
	if !errors.As(got, &rootErr) {
		t.Fatalf("expected normalized error to retain root symlink cause, got %v", got)
	}
	if rootErr.Path != filepath.Join(string(filepath.Separator), "tmp", "link") {
		t.Fatalf("unexpected root symlink cause path: %q", rootErr.Path)
	}

	unchanged := errors.New("permission denied")
	if got := normalizeOpenParentRootNoFollowError(unchanged, parentDir); !errors.Is(got, unchanged) {
		t.Fatalf("expected non-symlink error to remain unchanged, got %v", got)
	}
}

func TestPathContainsSymlinkErrorWithoutCauseUnwrapsSentinel(t *testing.T) {
	pathErr := &PathContainsSymlinkError{Path: filepath.Join(string(filepath.Separator), "tmp", "link")}
	if !errors.Is(pathErr, ErrPathContainsSymlink) {
		t.Fatalf("expected error to match ErrPathContainsSymlink, got %v", pathErr)
	}

	unwrapped := pathErr.Unwrap()
	if len(unwrapped) != 1 {
		t.Fatalf("unexpected unwrapped errors: %v", unwrapped)
	}
	if !errors.Is(unwrapped[0], ErrPathContainsSymlink) {
		t.Fatalf("expected unwrapped sentinel, got %v", unwrapped[0])
	}
}

func TestOpenParentRootNoFollowOpensVolumeRoot(t *testing.T) {
	rootPath := string(filepath.Separator)
	root, err := openParentRootNoFollow(rootPath)
	if err != nil {
		t.Fatalf("openParentRootNoFollow(%q): %v", rootPath, err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close root: %v", err)
	}
}

func TestReadFileUnderLimitReadsFileInsideRoot(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, "nested", writeTestFileName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("create parent dir: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	data, err := ReadFileUnderLimit(rootDir, targetPath, 5)
	if err != nil {
		t.Fatalf("ReadFileUnderLimit returned error: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf(unexpectedContentFmt, got)
	}
}

func TestReadFileUnderLimitRejectsOversizedFile(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, "large.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	_, err := ReadFileUnderLimit(rootDir, targetPath, 4)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestReadFileWithinRootReadsRelativeFile(t *testing.T) {
	rootDir := canonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(rootDir, writeTestFileName), []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	root := openTestRoot(t, rootDir)
	data, err := ReadFileWithinRoot(root, writeTestFileName)
	if err != nil {
		t.Fatalf("ReadFileWithinRoot returned error: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf(unexpectedContentFmt, got)
	}
}

func TestOpenFileWithinRootReadsRelativeFile(t *testing.T) {
	rootDir := canonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(rootDir, writeTestFileName), []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	root := openTestRoot(t, rootDir)
	file, err := OpenFileWithinRoot(root, writeTestFileName)
	if err != nil {
		t.Fatalf("OpenFileWithinRoot returned error: %v", err)
	}

	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("OpenFileWithinRoot read/close error: %v", errors.Join(readErr, closeErr))
	}
	if got := string(data); got != "hello" {
		t.Fatalf(unexpectedContentFmt, got)
	}
}

func TestReadFileWithinRootRejectsAbsolutePath(t *testing.T) {
	root := openTestRoot(t, canonicalTempDir(t))

	_, err := ReadFileWithinRoot(root, filepath.Join(canonicalTempDir(t), writeTestFileName))
	if err == nil || !strings.Contains(err.Error(), escapesRootErr) || !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected absolute path rejection, got %v", err)
	}
}

func TestReadFileWithinRootLimitReturnsNotExistForMissingFile(t *testing.T) {
	root := &fakeRoot{
		open: func(string) (File, error) {
			return nil, os.ErrNotExist
		},
	}

	_, err := ReadFileWithinRootLimit(root, writeTestFileName, 1)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing file error, got %v", err)
	}
}

func TestReadFileWithinRootCloseError(t *testing.T) {
	expectedErr := errors.New("close failure")
	path := filepath.Join(t.TempDir(), "close.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}
	info := statTestPath(t, path)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return info, nil },
		open: func(string) (File, error) {
			return &fakeFile{
				read: func(p []byte) (int, error) {
					copy(p, "hello")
					return len("hello"), io.EOF
				},
				stat:  func() (fs.FileInfo, error) { return info, nil },
				close: func() error { return expectedErr },
			}, nil
		},
	}

	_, err := ReadFileWithinRoot(root, writeTestFileName)
	if err == nil || !errors.Is(err, expectedErr) {
		t.Fatalf("expected close error, got %v", err)
	}
}

func TestReadFileWithinRootLimitTranslatesMissingFileAndJoinsReadCloseErrors(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		root := &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			open:  func(string) (File, error) { return nil, os.ErrNotExist },
		}

		_, err := ReadFileWithinRootLimit(root, writeTestFileName, 1)
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected missing file error, got %v", err)
		}
	})

	t.Run("read and close errors", func(t *testing.T) {
		readErr := errors.New("read failure")
		closeErr := errors.New("close failure")
		path := filepath.Join(t.TempDir(), "read-close.txt")
		if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
			t.Fatalf(writeFileErrFmt, err)
		}
		info := statTestPath(t, path)
		root := &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return info, nil },
			open: func(string) (File, error) {
				return &fakeFile{
					read:  func([]byte) (int, error) { return 0, readErr },
					stat:  func() (fs.FileInfo, error) { return info, nil },
					close: func() error { return closeErr },
				}, nil
			},
		}

		_, err := ReadFileWithinRootLimit(root, writeTestFileName, 0)
		if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
			t.Fatalf("expected joined read and close errors, got %v", err)
		}
	})
}

func TestReadFileWithinRootLimitMarksParentEscapeWithSentinel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}
	info := statTestPath(t, path)
	root := &fakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			return info, nil
		},
		open: func(string) (File, error) {
			return nil, &fs.PathError{
				Op:   "openat",
				Path: filepath.Join("..", "child.txt"),
				Err:  errors.New("localized rooted-open escape"),
			}
		},
	}

	_, err := ReadFileWithinRootLimit(root, "child.txt", 1)
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected path escape sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "path escapes root: child.txt") {
		t.Fatalf("expected normalized path escape wording, got %v", err)
	}
}

func TestNormalizePathEscapesRootErrorUsesPathInvariant(t *testing.T) {
	err := normalizePathEscapesRootError("child.txt", &fs.PathError{
		Op:   "openat",
		Path: filepath.Join("..", "child.txt"),
		Err:  errors.New("localized rooted-open escape"),
	})
	if !errors.Is(err, ErrPathEscapesRoot) {
		t.Fatalf("expected structural escape path to normalize, got %v", err)
	}
	if !strings.Contains(err.Error(), "path escapes root: child.txt") {
		t.Fatalf("expected normalized structural escape wording, got %v", err)
	}
}

func TestNormalizePathEscapesRootErrorPreservesNonEscapingPathErrors(t *testing.T) {
	pathErr := &fs.PathError{
		Op:   "openat",
		Path: "child.txt",
		Err:  errors.New("localized rooted-open escape"),
	}
	err := normalizePathEscapesRootError("child.txt", pathErr)
	if !errors.Is(err, pathErr) {
		t.Fatalf("expected non-escaping path error to stay unchanged, got %#v", err)
	}
}

func TestTranslateOpenNotExistDowngradesPureMissingTree(t *testing.T) {
	targetPath := filepath.Join("nested", "missing.txt")
	err := translateOpenNotExist(fmt.Errorf("wrapped missing: %w", os.ErrNotExist), targetPath)

	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("expected path error, got %#v", err)
	}
	if pathErr.Path != targetPath {
		t.Fatalf("expected translated path %q, got %q", targetPath, pathErr.Path)
	}
	if !errors.Is(pathErr.Err, os.ErrNotExist) {
		t.Fatalf("expected pure missing tree to downgrade to os.ErrNotExist, got %#v", pathErr.Err)
	}
}

func TestReadFileWithinRootLimitPreservesNestedMissingTargetAndAncestorCloseError(t *testing.T) {
	repo := canonicalTempDir(t)
	nestedDir := filepath.Join(repo, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	repoRoot := openTestRoot(t, repo)
	nestedRoot := openTestRoot(t, nestedDir)
	ancestorCloseErr := errors.New("close nested ancestor")
	root := &fakeRoot{
		Root:  repoRoot,
		lstat: repoRoot.Lstat,
		openRoot: func(name string) (Root, error) {
			if name != "nested" {
				return repoRoot.OpenRoot(name)
			}
			return &fakeRoot{
				Root:  nestedRoot,
				lstat: nestedRoot.Lstat,
				close: func() error {
					if err := nestedRoot.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
						return err
					}
					return ancestorCloseErr
				},
			}, nil
		},
	}

	targetPath := filepath.Join("nested", missingFileName)
	_, err := ReadFileWithinRootLimit(root, targetPath, 1)
	if err == nil {
		t.Fatal("expected missing nested target error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing identity, got %v", err)
	}
	if !errors.Is(err, ancestorCloseErr) {
		t.Fatalf("expected ancestor close identity, got %v", err)
	}

	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("expected path error, got %#v", err)
	}
	if pathErr.Path != targetPath {
		t.Fatalf("expected nested target path %q, got %q", targetPath, pathErr.Path)
	}
	if !errors.Is(pathErr.Err, os.ErrNotExist) {
		t.Fatalf("expected impure missing tree to preserve missing identity, got %#v", pathErr.Err)
	}
	if !errors.Is(pathErr.Err, os.ErrNotExist) || !errors.Is(pathErr.Err, ancestorCloseErr) {
		t.Fatalf("expected path error cause to preserve missing and ancestor close identities, got %v", pathErr.Err)
	}
}

func TestReadFileWithinRootLimitRejectsFileReplacementBetweenLstatAndOpen(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetName := "swap.txt"
	targetPath := filepath.Join(rootDir, targetName)
	if err := os.WriteFile(targetPath, []byte("original"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	realRoot := openTestRoot(t, rootDir)
	swapped := false
	root := &fakeRoot{
		Root: realRoot,
		open: func(name string) (File, error) {
			if !swapped {
				swapped = true
				originalPath := filepath.Join(rootDir, "swap-original.txt")
				if err := os.Rename(targetPath, originalPath); err != nil {
					t.Fatalf("rename original file: %v", err)
				}
				if err := os.WriteFile(targetPath, []byte("replacement"), 0o600); err != nil {
					t.Fatalf("write replacement file: %v", err)
				}
			}
			return realRoot.Open(name)
		},
	}

	_, err := ReadFileWithinRootLimit(root, targetName, 0)
	if err == nil || !strings.Contains(err.Error(), "path changed while opening: "+targetName) {
		t.Fatalf("expected pinned file replacement error, got %v", err)
	}
}

func TestPublicPathReadersRejectLeafSymlinks(t *testing.T) {
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	for _, reader := range pinnedPathReaders() {
		t.Run(reader.name, func(t *testing.T) {
			rootDir := canonicalTempDir(t)
			targetPath := filepath.Join(rootDir, "leaf.txt")
			if err := os.Symlink(outsidePath, targetPath); err != nil {
				t.Skipf("symlink not supported: %v", err)
			}

			_, err := reader.read(rootDir, targetPath)
			if !errors.Is(err, ErrTargetPathSymlink) {
				t.Fatalf("expected target symlink sentinel, got %v", err)
			}
		})
	}
}

func TestPublicPathReadersRejectLeafReplacementBetweenCheckAndOpen(t *testing.T) {
	for _, reader := range pinnedPathReaders() {
		t.Run(reader.name, func(t *testing.T) {
			rootDir := canonicalTempDir(t)
			targetPath := filepath.Join(rootDir, "swap.txt")
			if err := os.WriteFile(targetPath, []byte("original"), 0o600); err != nil {
				t.Fatalf(writeFileErrFmt, err)
			}

			withPinnedPublicReaderSwap(t, reader.rootOpenPath(rootDir, targetPath), targetPath)

			_, err := reader.read(rootDir, targetPath)
			if err == nil || !strings.Contains(err.Error(), "path changed while opening: "+filepath.Base(targetPath)) {
				t.Fatalf("expected pinned replacement error, got %v", err)
			}
		})
	}
}

func TestReadFileWithinRootLimitRejectsAncestorReplacementBeforeNestedOpen(t *testing.T) {
	root, targetName := newReadAncestorReplacementFixture(t)
	_, err := ReadFileWithinRootLimit(root, targetName, 0)
	if err == nil || !strings.Contains(err.Error(), "root changed while opening: src") {
		t.Fatalf("expected nested ancestor replacement error, got %v", err)
	}
}

type readAncestorReplacementFixture struct {
	t        *testing.T
	rootDir  string
	realRoot Root
	swapped  bool
}

func newReadAncestorReplacementFixture(t *testing.T) (Root, string) {
	t.Helper()
	rootDir := canonicalTempDir(t)
	targetName := filepath.Join("src", "main", "Main.java")
	targetPath := filepath.Join(rootDir, targetName)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("mkdir nested source dir: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("class Main {}\n"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	realRoot := openTestRoot(t, rootDir)
	fixture := &readAncestorReplacementFixture{
		t:        t,
		rootDir:  rootDir,
		realRoot: realRoot,
	}
	return &fakeRoot{Root: realRoot, openRoot: fixture.openRoot}, targetName
}

func (f *readAncestorReplacementFixture) openRoot(name string) (Root, error) {
	if name == "src" && !f.swapped {
		f.swapped = true
		f.replace()
	}
	return f.realRoot.OpenRoot(name)
}

func (f *readAncestorReplacementFixture) replace() {
	f.t.Helper()
	if err := os.Rename(filepath.Join(f.rootDir, "src"), filepath.Join(f.rootDir, "src-original")); err != nil {
		f.t.Fatalf("rename original src: %v", err)
	}
	replacementPath := filepath.Join(f.rootDir, "src", "main")
	if err := os.MkdirAll(replacementPath, 0o755); err != nil {
		f.t.Fatalf("mkdir replacement source dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(replacementPath, "Main.java"), []byte("class Replacement {}\n"), 0o600); err != nil {
		f.t.Fatalf("write replacement source file: %v", err)
	}
}

func TestReadFileLimitReadsFile(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	data, err := ReadFileLimit(targetPath, 5)
	if err != nil {
		t.Fatalf("ReadFileLimit returned error: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf(unexpectedContentFmt, got)
	}
}

func TestReadFileLimitRejectsOversizedFile(t *testing.T) {
	targetPath := filepath.Join(canonicalTempDir(t), "large.txt")
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	_, err := ReadFileLimit(targetPath, 4)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}

func TestReadFileLimitRejectsEmptyPath(t *testing.T) {
	if _, err := ReadFileLimit("", 1); err == nil {
		t.Fatal("expected empty path error")
	}
}

func TestReadFileLimitTargetAbsFailureViaFileSystem(t *testing.T) {
	targetPath := filepath.Join(canonicalTempDir(t), writeTestFileName)
	withFileSystem(t, &fakeFileSystem{abs: func(path string) (string, error) {
		if path == targetPath {
			return "", errors.New("target abs failure")
		}
		return (&osFileSystem{}).Abs(path)
	}})

	_, err := ReadFileLimit(targetPath, 1)
	if err == nil {
		t.Fatal("expected target path absolute resolution error")
	}
	if !strings.Contains(err.Error(), resolveTargetPathErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileLimitRejectsMissingParentDirectory(t *testing.T) {
	targetPath := filepath.Join(canonicalTempDir(t), "missing", "file.txt")

	_, err := ReadFileLimit(targetPath, 1)
	if err == nil {
		t.Fatal("expected missing parent directory error")
	}
	if !strings.Contains(err.Error(), "open parent root") {
		t.Fatalf("expected open parent root error, got %v", err)
	}
}

func TestReadFileLimitRejectsSymlinkedParentAncestor(t *testing.T) {
	canonicalParent := filepath.Join(t.TempDir(), "actual")
	if err := os.MkdirAll(canonicalParent, 0o755); err != nil {
		t.Fatalf("mkdir canonical parent: %v", err)
	}
	targetPath := filepath.Join(canonicalParent, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	ancestorLink := filepath.Join(t.TempDir(), "ancestor-link")
	if err := os.Symlink(filepath.Dir(canonicalParent), ancestorLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := ReadFileLimit(filepath.Join(ancestorLink, filepath.Base(canonicalParent), writeTestFileName), 5)
	if err == nil {
		t.Fatal("expected symlinked parent ancestor to be rejected")
	}
	if !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestOpenFileNoFollowRejectsSymlinkedParent(t *testing.T) {
	rootDir := t.TempDir()
	targetDir := filepath.Join(rootDir, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("create target dir: %v", err)
	}
	targetPath := filepath.Join(targetDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}
	linkDir := filepath.Join(rootDir, "linked")
	if err := os.Symlink(targetDir, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	file, err := OpenFileNoFollow(filepath.Join(linkDir, writeTestFileName))
	if err == nil || !strings.Contains(err.Error(), "path contains symlink") {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Fatalf("close unexpected file: %v", closeErr)
			}
		}
		t.Fatalf("expected symlinked parent rejection, got file=%v err=%v", file, err)
	}
	if !errors.Is(err, ErrPathContainsSymlink) {
		t.Fatalf("expected symlinked parent rejection to match ErrPathContainsSymlink, got %v", err)
	}
	var rootErr *RootContainsSymlinkError
	if !errors.As(err, &rootErr) {
		t.Fatalf("expected symlinked parent rejection to expose root symlink cause, got %v", err)
	}
}

func TestOpenFileNoFollowRejectsSuffixPreservingSymlinkedParent(t *testing.T) {
	rootDir := t.TempDir()
	suffixPath := strings.TrimPrefix(filepath.Clean(rootDir), string(filepath.Separator))
	targetDir := filepath.Join(rootDir, "pivot", suffixPath, "linked")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("create crafted target dir: %v", err)
	}
	targetPath := filepath.Join(targetDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hello"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}
	linkDir := filepath.Join(rootDir, "linked")
	if err := os.Symlink(targetDir, linkDir); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	file, err := OpenFileNoFollow(filepath.Join(linkDir, writeTestFileName))
	if err == nil || !strings.Contains(err.Error(), "path contains symlink") {
		if file != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Fatalf("close unexpected file: %v", closeErr)
			}
		}
		t.Fatalf("expected suffix-preserving symlinked parent rejection, got file=%v err=%v", file, err)
	}
	if !errors.Is(err, ErrPathContainsSymlink) {
		t.Fatalf("expected suffix-preserving symlink rejection to match ErrPathContainsSymlink, got %v", err)
	}
}

func assertOpenTargetAbsFailureViaFileSystem(t *testing.T, targetPath string, marker string, open func(string) (io.ReadCloser, error)) {
	t.Helper()

	withFileSystem(t, &fakeFileSystem{abs: func(path string) (string, error) {
		if path == targetPath {
			return "", errors.New(marker)
		}
		return (&osFileSystem{}).Abs(path)
	}})

	_, err := open(targetPath)
	if err == nil {
		t.Fatal("expected target path absolute resolution error")
	}
	if !strings.Contains(err.Error(), resolveTargetPathErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestOpenFileNoFollowTargetAbsFailureViaFileSystem(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), writeTestFileName)
	assertOpenTargetAbsFailureViaFileSystem(t, targetPath, "openfilenofollow target abs failure", OpenFileNoFollow)
}

func TestOpenFileNoFollowOpenErrorClosesRoot(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	openErr := errors.New("open nofollow failure")
	closeErr := errors.New("nofollow root close failure")
	childClosed := false

	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		child := &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
			openNoFollow: func(string) (File, error) {
				return nil, openErr
			},
			close: func() error {
				childClosed = true
				return closeErr
			},
		}
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
			openRoot: func(string) (Root, error) {
				return child, nil
			},
			close: func() error { return nil },
		}, nil
	}})

	_, err := OpenFileNoFollow(filepath.Join(string(filepath.Separator), "alpha", writeTestFileName))
	if err == nil {
		t.Fatal("expected OpenFileNoFollow to fail")
	}
	if !errors.Is(err, openErr) {
		t.Fatalf("expected original open error, got %v", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf(rootCloseErrFmt, err)
	}
	if !childClosed {
		t.Fatal("expected nofollow root to be closed on open failure")
	}
}
func TestReadFileLimitRejectsMissingFile(t *testing.T) {
	targetPath := filepath.Join(canonicalTempDir(t), "missing.txt")

	_, err := ReadFileLimit(targetPath, 1)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing file error, got %v", err)
	}
}

func TestOpenParentRootNoFollowReturnsRelErrorForInvalidPath(t *testing.T) {
	_, err := openParentRootNoFollow(string([]byte{'/', 'b', 'a', 'd', 0}))
	if err == nil {
		t.Fatal("expected invalid path error")
	}
}

func TestOpenParentRootNoFollowReturnsOpenRootError(t *testing.T) {
	expectedErr := errors.New("open volume root failure")
	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		return nil, expectedErr
	}})

	_, err := openParentRootNoFollow(filepath.Join(string(filepath.Separator), "alpha"))
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected open root error, got %v", err)
	}
}

func TestOpenParentRootNoFollowClosesNextRootWhenParentCloseFails(t *testing.T) {
	dirInfo := statTestPath(t, t.TempDir())
	parentCloseErr := errors.New("parent close failure")
	childClosed := false

	withFileSystem(t, &fakeFileSystem{openRoot: func(string) (Root, error) {
		child := &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
			close: func() error {
				childClosed = true
				return nil
			},
		}
		return &fakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
			openRoot: func(string) (Root, error) {
				return child, nil
			},
			close: func() error { return parentCloseErr },
		}, nil
	}})

	root, err := openParentRootNoFollow(filepath.Join(string(filepath.Separator), "alpha"))
	if root != nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close unexpected root: %v", closeErr)
		}
		t.Fatal("expected root open to fail when parent close fails")
	}
	if !errors.Is(err, parentCloseErr) {
		t.Fatalf("expected parent close error, got %v", err)
	}
	if !childClosed {
		t.Fatal("expected child root to be closed after parent close failure")
	}
}

func TestReadFileLimitCloseRootError(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	expectedErr := errors.New("read limit root close failure")
	withRootCloseError(t, expectedErr)

	_, err := ReadFileLimit(targetPath, 0)
	if err == nil {
		t.Fatal("expected root close error to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf(rootCloseErrFmt, err)
	}
}

func TestReadFileLimitCloseFileError(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	expectedErr := errors.New("read limit file close failure")
	withOpenedFileCloseError(t, expectedErr)

	_, err := ReadFileLimit(targetPath, 0)
	if err == nil {
		t.Fatal("expected file close error to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected file close error, got %v", err)
	}
}

func TestReadFileLimitRejectsSpecialFile(t *testing.T) {
	for _, path := range []string{"/dev/zero", "NUL"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().IsRegular() {
			continue
		}

		if _, err := ReadFileLimit(path, 1024); !errors.Is(err, ErrFileTooLarge) {
			t.Fatalf("expected ErrFileTooLarge for %s, got %v", path, err)
		}
		return
	}
	t.Skip("non-regular file unavailable")
}

func TestReadOpenedFileRejectsOversizedPipeContent(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatalf("close pipe reader: %v", closeErr)
		}
	})

	done := make(chan error, 1)
	go func() {
		_, writeErr := writer.Write([]byte("hello"))
		closeErr := writer.Close()
		if writeErr != nil {
			done <- writeErr
			return
		}
		done <- closeErr
	}()

	_, err = readOpenedFile(reader, 4)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge for pipe content, got %v", err)
	}
	if writeErr := <-done; writeErr != nil {
		t.Fatalf("pipe writer error: %v", writeErr)
	}
}

func TestReadOpenedFileAllowsMaxInt64Limit(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := reader.Close(); closeErr != nil {
			t.Fatalf("close pipe reader: %v", closeErr)
		}
	})

	done := make(chan error, 1)
	go func() {
		_, writeErr := writer.Write([]byte("ok"))
		closeErr := writer.Close()
		if writeErr != nil {
			done <- writeErr
			return
		}
		done <- closeErr
	}()

	data, err := readOpenedFile(reader, math.MaxInt64)
	if err != nil {
		t.Fatalf("readOpenedFile returned error: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf(unexpectedContentFmt, string(data))
	}
	if writeErr := <-done; writeErr != nil {
		t.Fatalf("pipe writer error: %v", writeErr)
	}
}

func TestReadOpenedFileDirectoryReadError(t *testing.T) {
	dirFile, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open temp dir: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := dirFile.Close(); closeErr != nil {
			t.Fatalf("close temp dir file: %v", closeErr)
		}
	})

	if _, err := readOpenedFile(dirFile, 1); err == nil {
		t.Fatalf("expected readOpenedFile to fail when reading a directory")
	}
}

func TestReadFileUnderRejectsPathTraversalOutsideRoot(t *testing.T) {
	parentDir := canonicalTempDir(t)
	rootDir := filepath.Join(parentDir, "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("create root dir: %v", err)
	}

	outsidePath := filepath.Join(parentDir, "secret.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	_, err := ReadFileUnder(rootDir, outsidePath)
	if err == nil {
		t.Fatal("expected error for outside path, got nil")
	}
	if !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderRejectsParentDirectoryTarget(t *testing.T) {
	parentDir := canonicalTempDir(t)
	rootDir := filepath.Join(parentDir, "root")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("create root dir: %v", err)
	}

	_, err := ReadFileUnder(rootDir, parentDir)
	if err == nil {
		t.Fatal("expected error for parent directory target")
	}
	if !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderReturnsErrorForMissingFile(t *testing.T) {
	rootDir := canonicalTempDir(t)
	missingPath := filepath.Join(rootDir, missingFileName)

	_, err := ReadFileUnder(rootDir, missingPath)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestReadFileUnderRejectsNonDirectoryRoot(t *testing.T) {
	rootDir := canonicalTempDir(t)
	rootFile := filepath.Join(rootDir, "root-file")
	if err := os.WriteFile(rootFile, []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("write root file: %v", err)
	}

	_, err := ReadFileUnder(rootFile, rootFile)
	if err == nil {
		t.Fatal("expected error when root is not a directory")
	}
	if !strings.Contains(err.Error(), "open root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderRootAbsFailureWhenCWDRemoved(t *testing.T) {
	withRemovedWorkingDir(t, "dead")

	_, err := ReadFileUnder(".", "x")
	if err == nil {
		t.Fatal("expected root path resolution error")
	}
	if !strings.Contains(err.Error(), "resolve root path") && !strings.Contains(err.Error(), "open root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderTargetAbsFailureWhenCWDRemoved(t *testing.T) {
	rootDir := canonicalTempDir(t)
	withRemovedWorkingDir(t, "dead-target")

	_, err := ReadFileUnder(rootDir, "relative-target.txt")
	if err == nil {
		t.Fatal("expected target path resolution error")
	}
	if !strings.Contains(err.Error(), resolveTargetPathErr) && !strings.Contains(err.Error(), escapesRootErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderDirectoryTargetReturnsReadError(t *testing.T) {
	rootDir := canonicalTempDir(t)
	dirTarget := filepath.Join(rootDir, "nested")
	if err := os.MkdirAll(dirTarget, 0o755); err != nil {
		t.Fatalf("create dir target: %v", err)
	}

	_, err := ReadFileUnder(rootDir, dirTarget)
	if err == nil {
		t.Fatal("expected error when reading a directory target")
	}
}

func TestReadFileUnderRootPathAsTargetReturnsError(t *testing.T) {
	rootDir := canonicalTempDir(t)
	_, err := ReadFileUnder(rootDir, rootDir)
	if err == nil {
		t.Fatal("expected error when target is root directory")
	}
}

func TestPathReadersReadAbsoluteAndRelativePaths(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, "target.txt")
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	for _, reader := range pathReaders() {
		t.Run(reader.name, func(t *testing.T) {
			assertReadContent(t, reader.read, targetPath, "content")
			withWorkingDir(t, rootDir)
			assertReadContent(t, reader.read, "target.txt", "content")
		})
	}
}

func TestPathReadersReturnErrorForMissingFile(t *testing.T) {
	missingPath := filepath.Join(canonicalTempDir(t), missingFileName)
	for _, reader := range pathReaders() {
		t.Run(reader.name, func(t *testing.T) {
			if _, err := reader.read(missingPath); err == nil {
				t.Fatal("expected error for missing file")
			}
		})
	}
}

func TestReadFileReturnsErrorWhenParentIsNotDirectory(t *testing.T) {
	rootDir := canonicalTempDir(t)
	parentFile := filepath.Join(rootDir, "not-a-dir")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write parent file: %v", err)
	}

	_, err := ReadFile(filepath.Join(parentFile, "child.txt"))
	if err == nil {
		t.Fatal("expected error when parent path is a file")
	}
	if !strings.Contains(err.Error(), "open parent root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileTargetAbsFailureWhenCWDRemoved(t *testing.T) {
	withRemovedWorkingDir(t, "dead-readfile")

	_, err := ReadFile("relative.txt")
	if err == nil {
		t.Fatal("expected target path resolution error")
	}
	if !strings.Contains(err.Error(), resolveTargetPathErr) && !strings.Contains(err.Error(), "open parent root") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderRootAbsFailureViaFileSystem(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)

	withFileSystem(t, &fakeFileSystem{abs: func(path string) (string, error) {
		if path == rootDir {
			return "", errors.New("root abs failure")
		}
		return (&osFileSystem{}).Abs(path)
	}})

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected root path absolute resolution error")
	}
	if !strings.Contains(err.Error(), "resolve root path") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderTargetAbsFailureViaFileSystem(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)

	withFileSystem(t, &fakeFileSystem{abs: func(path string) (string, error) {
		if path == targetPath {
			return "", errors.New("target abs failure")
		}
		return (&osFileSystem{}).Abs(path)
	}})

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected target path absolute resolution error")
	}
	if !strings.Contains(err.Error(), resolveTargetPathErr) {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderRelFailureViaFileSystem(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	withFileSystem(t, &fakeFileSystem{rel: func(_, _ string) (string, error) {
		return "", errors.New("rel failure")
	}})

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected relative path resolution error")
	}
	if !strings.Contains(err.Error(), "compute relative path") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func TestReadFileUnderPropagatesNoFollowRootOpenError(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)

	withFileSystem(t, &fakeFileSystem{
		abs: (&osFileSystem{}).Abs,
		rel: (&osFileSystem{}).Rel,
		openRootNoFollow: func(string) (Root, error) {
			return nil, errors.New("root changed while opening: " + rootDir)
		},
	})

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected root-open failure to be returned")
	}
	if !strings.Contains(err.Error(), "root changed while opening") {
		t.Fatalf(unexpectedErrFmt, err)
	}
}

func withRootCloseError(t *testing.T, expectedErr error) {
	t.Helper()
	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		root, err := (&osFileSystem{}).OpenRoot(name)
		if err != nil {
			return nil, err
		}
		return &fakeRoot{
			Root: root,
			close: func() error {
				if err := root.Close(); err != nil {
					return err
				}
				return expectedErr
			},
		}, nil
	}})
}

func withOpenedFileCloseError(t *testing.T, expectedErr error) {
	t.Helper()
	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		root, err := (&osFileSystem{}).OpenRoot(name)
		if err != nil {
			return nil, err
		}
		return &fakeRoot{
			Root: root,
			open: func(name string) (File, error) {
				file, err := root.Open(name)
				if err != nil {
					return nil, err
				}
				return &fakeFile{
					File: file,
					close: func() error {
						if err := file.Close(); err != nil {
							return err
						}
						return expectedErr
					},
				}, nil
			},
		}, nil
	}})
}

func TestReadFileUnderCloseRootError(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	expectedErr := errors.New("root close failure")
	withRootCloseError(t, expectedErr)

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected root close error to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf(rootCloseErrFmt, err)
	}
}

func TestReadFileUnderCloseFileError(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	expectedErr := errors.New("file close failure")
	withOpenedFileCloseError(t, expectedErr)

	_, err := ReadFileUnder(rootDir, targetPath)
	if err == nil {
		t.Fatal("expected file close error to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected file close error, got %v", err)
	}
}

func TestReadFileCloseError(t *testing.T) {
	rootDir := canonicalTempDir(t)
	targetPath := filepath.Join(rootDir, writeTestFileName)
	if err := os.WriteFile(targetPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}

	expectedErr := errors.New("read closer close failure")
	withOpenedFileCloseError(t, expectedErr)

	_, err := ReadFile(targetPath)
	if err == nil {
		t.Fatal("expected read closer close error to be returned")
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected close error, got %v", err)
	}
}

func TestOpenFileTargetAbsFailureViaFileSystem(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), writeTestFileName)
	assertOpenTargetAbsFailureViaFileSystem(t, targetPath, "openfile target abs failure", OpenFile)
}

func TestOpenFileMissingFileCloseRootError(t *testing.T) {
	targetPath := filepath.Join(canonicalTempDir(t), missingFileName)

	expectedErr := errors.New("open parent root close failure")
	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		root, err := (&osFileSystem{}).OpenRoot(name)
		if err != nil {
			return nil, err
		}
		return &fakeRoot{
			Root: root,
			open: func(string) (File, error) {
				return nil, os.ErrNotExist
			},
			close: func() error {
				if err := root.Close(); err != nil {
					return err
				}
				return expectedErr
			},
		}, nil
	}})

	_, err := OpenFile(targetPath)
	if err == nil {
		t.Fatal("expected fs.ErrNotExist on missing file with root close error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected wrapped ErrNotExist, got %v", err)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf(rootCloseErrFmt, err)
	}
}

func TestOpenFileOpenErrorCloseRootError(t *testing.T) {
	targetDir := canonicalTempDir(t)
	targetPath := filepath.Join(targetDir, "child.txt")
	markerPath := filepath.Join(targetDir, "marker")
	if err := os.WriteFile(markerPath, []byte("x"), 0o600); err != nil {
		t.Fatalf(writeFileErrFmt, err)
	}
	markerInfo := statTestPath(t, markerPath)

	openErr := errors.New("open child failure")
	expectedErr := errors.New("open root close failure")
	withFileSystem(t, &fakeFileSystem{openRoot: func(name string) (Root, error) {
		root, err := (&osFileSystem{}).OpenRoot(name)
		if err != nil {
			return nil, err
		}
		return &fakeRoot{
			Root: root,
			lstat: func(string) (fs.FileInfo, error) {
				return markerInfo, nil
			},
			open: func(string) (File, error) {
				return nil, openErr
			},
			close: func() error {
				if err := root.Close(); err != nil {
					return err
				}
				return expectedErr
			},
		}, nil
	}})

	_, err := OpenFile(targetPath)
	if err == nil {
		t.Fatal("expected open error joined with root close error")
	}
	if !errors.Is(err, openErr) {
		t.Fatalf("expected original open error, got %v", err)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf(rootCloseErrFmt, err)
	}
}

type pathReaderCase struct {
	name string
	read func(string) (string, error)
}

type pinnedPathReaderCase struct {
	name         string
	read         func(rootDir, path string) (string, error)
	rootOpenPath func(rootDir, path string) string
}

func pathReaders() []pathReaderCase {
	return []pathReaderCase{
		{name: "read-file", read: readFileContent},
		{name: "open-file", read: openFileContent},
	}
}

func pinnedPathReaders() []pinnedPathReaderCase {
	return []pinnedPathReaderCase{
		{
			name: "read-file-under-limit",
			read: func(rootDir, path string) (string, error) {
				data, err := ReadFileUnderLimit(rootDir, path, 0)
				return string(data), err
			},
			rootOpenPath: func(rootDir, _ string) string { return rootDir },
		},
		{
			name: "read-file-limit",
			read: func(_, path string) (string, error) {
				data, err := ReadFileLimit(path, 0)
				return string(data), err
			},
			rootOpenPath: func(_, path string) string { return filepath.Dir(path) },
		},
		{
			name: "open-file",
			read: func(_, path string) (string, error) {
				return openFileContent(path)
			},
			rootOpenPath: func(_, path string) string { return filepath.Dir(path) },
		},
		{
			name: "open-file-within-root",
			read: func(rootDir, path string) (string, error) {
				return openFileWithinRootContent(rootDir, filepath.Base(path))
			},
			rootOpenPath: func(rootDir, _ string) string { return rootDir },
		},
	}
}

func readFileContent(path string) (string, error) {
	content, err := ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func openFileContent(path string) (string, error) {
	file, err := OpenFile(path)
	if err != nil {
		return "", err
	}
	content, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return "", errors.Join(readErr, closeErr)
	}
	return string(content), nil
}

func openFileWithinRootContent(rootDir, relPath string) (string, error) {
	root, err := OpenRootNoFollow(rootDir)
	if err != nil {
		return "", err
	}
	file, err := OpenFileWithinRoot(root, relPath)
	if err != nil {
		closeErr := root.Close()
		return "", errors.Join(err, closeErr)
	}
	content, readErr := io.ReadAll(file)
	closeErr := file.Close()
	rootCloseErr := root.Close()
	if readErr != nil || closeErr != nil || rootCloseErr != nil {
		return "", errors.Join(readErr, closeErr, rootCloseErr)
	}
	return string(content), nil
}

func withPinnedPublicReaderSwap(t *testing.T, rootPath, targetPath string) {
	t.Helper()
	fixture := &pinnedPublicReaderSwapFixture{
		t:          t,
		rootPath:   rootPath,
		targetPath: targetPath,
		targetName: filepath.Base(targetPath),
	}
	withFileSystem(t, &fakeFileSystem{
		openRootNoFollow: fixture.openRootNoFollow,
	})
}

type pinnedPublicReaderSwapFixture struct {
	t          *testing.T
	rootPath   string
	targetPath string
	targetName string
	swapped    bool
}

func (f *pinnedPublicReaderSwapFixture) openRootNoFollow(name string) (Root, error) {
	root, err := (&osFileSystem{}).OpenRootNoFollow(name)
	if err != nil {
		return nil, err
	}
	if name != f.rootPath {
		return root, nil
	}
	return &fakeRoot{
		Root: root,
		open: func(child string) (File, error) {
			return f.open(root, child)
		},
	}, nil
}

func (f *pinnedPublicReaderSwapFixture) open(root Root, child string) (File, error) {
	if child == f.targetName && !f.swapped {
		f.swapped = true
		f.replace()
	}
	return root.Open(child)
}

func (f *pinnedPublicReaderSwapFixture) replace() {
	f.t.Helper()
	if err := os.Rename(f.targetPath, f.targetPath+".original"); err != nil {
		f.t.Fatalf("rename original file: %v", err)
	}
	if err := os.WriteFile(f.targetPath, []byte("replacement"), 0o600); err != nil {
		f.t.Fatalf("write replacement file: %v", err)
	}
}

func assertReadContent(t *testing.T, read func(string) (string, error), path, want string) {
	t.Helper()
	content, err := read(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if content != want {
		t.Fatalf("unexpected content from %s: %q", path, content)
	}
}
