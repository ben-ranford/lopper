package testutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

const testutilGetwdErrFmt = "getwd: %v"

type fakeTB struct {
	t        *testing.T
	tempDir  string
	cleanups []func()
	fatalMsg string
}

func newFakeTB(t *testing.T) *fakeTB {
	t.Helper()
	return &fakeTB{
		t:       t,
		tempDir: t.TempDir(),
	}
}

func (tb *fakeTB) Helper() {}

func (tb *fakeTB) Fatalf(format string, args ...any) {
	tb.fatalMsg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (tb *fakeTB) Cleanup(fn func()) {
	tb.cleanups = append(tb.cleanups, fn)
}

func (tb *fakeTB) TempDir() string {
	return tb.tempDir
}

func (tb *fakeTB) runCleanups() {
	for i := len(tb.cleanups) - 1; i >= 0; i-- {
		tb.cleanups[i]()
	}
}

type stubRoot struct {
	openFunc     func(name string) (rootFile, error)
	openFileFunc func(name string, flag int, perm os.FileMode) (rootFile, error)
	closeFunc    func() error
}

func (r *stubRoot) Open(name string) (rootFile, error) {
	return r.openFunc(name)
}

func (r *stubRoot) OpenFile(name string, flag int, perm os.FileMode) (rootFile, error) {
	return r.openFileFunc(name, flag, perm)
}

func (r *stubRoot) Close() error {
	return r.closeFunc()
}

type stubRootFile struct {
	readFunc        func(p []byte) (int, error)
	writeStringFunc func(s string) (int, error)
	closeFunc       func() error
}

func (f *stubRootFile) Read(p []byte) (int, error) {
	return f.readFunc(p)
}

func (f *stubRootFile) WriteString(s string) (int, error) {
	return f.writeStringFunc(s)
}

func (f *stubRootFile) Close() error {
	return f.closeFunc()
}

func expectFatal(t *testing.T, want string, fn func(tb *fakeTB)) {
	t.Helper()
	tb := newFakeTB(t)
	expectFatalWithTB(t, tb, want, func() {
		fn(tb)
	})
}

func expectFatalWithTB(t *testing.T, tb *fakeTB, want string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
	if tb.fatalMsg == "" {
		t.Fatalf("expected fatal containing %q", want)
	}
	if !strings.Contains(tb.fatalMsg, want) {
		t.Fatalf("fatal %q does not contain %q", tb.fatalMsg, want)
	}
}

func patchTestutilSeams(t *testing.T) {
	t.Helper()

	origMkdirAll := mkdirAll
	origWriteFile := writeFile
	origOpenRoot := openRoot
	origReadDir := readDir
	origGetwd := getwd
	origChdir := chdir
	origRemoveAll := removeAll
	origResolveGitBinary := resolveGitBinary
	origCommandContext := commandContext
	origSanitizedGitEnv := sanitizedGitEnv
	origCurrentEnviron := currentEnviron

	t.Cleanup(func() {
		mkdirAll = origMkdirAll
		writeFile = origWriteFile
		openRoot = origOpenRoot
		readDir = origReadDir
		getwd = origGetwd
		chdir = origChdir
		removeAll = origRemoveAll
		resolveGitBinary = origResolveGitBinary
		commandContext = origCommandContext
		sanitizedGitEnv = origSanitizedGitEnv
		currentEnviron = origCurrentEnviron
	})
}

func TestCanceledContextIsDone(t *testing.T) {
	ctx := CanceledContext()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("expected canceled context")
	}
}

func TestCanceledContext(t *testing.T) {
	ctx := CanceledContext()
	if ctx.Err() == nil {
		t.Fatalf("expected canceled context")
	}
	if ctx.Err() != context.Canceled {
		t.Fatalf("unexpected context error: %v", ctx.Err())
	}
}

func TestWriteHelpers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.txt")

	MustWriteFile(t, path, "hello")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if got := string(data); got != "hello" {
		t.Fatalf("unexpected content: %q", got)
	}

	WriteNumberedTextFiles(t, dir, 3)
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("f-%d.txt", i))
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
	}

	tempPath := WriteTempFile(t, "temp.txt", "x")
	if info, err := os.Stat(tempPath); err != nil {
		t.Fatalf("stat temp file: %v", err)
	} else if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("expected 0644, got %o", got)
	}
}

func TestFileHelpers(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a", "b.txt")
	MustWriteFile(t, p, "hello")
	content, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("unexpected content: %q", content)
	}

	MustWriteFileMode(t, filepath.Join(dir, "mode.txt"), "x", 0o644)
	info, err := os.Stat(filepath.Join(dir, "mode.txt"))
	if err != nil {
		t.Fatalf("stat mode file: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("unexpected mode: %o", info.Mode().Perm())
	}
}

func TestWriteNumberedTextFilesAndFirstEntry(t *testing.T) {
	dir := t.TempDir()
	WriteNumberedTextFiles(t, dir, 3)
	entry := MustFirstFileEntry(t, dir)
	if entry == nil {
		t.Fatalf("expected first file entry")
	}
	if entry.Name() != "f-0.txt" {
		t.Fatalf("expected first file entry to be %q, got %q", "f-0.txt", entry.Name())
	}
}

func TestMustFirstFileEntrySkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	MustWriteFile(t, filepath.Join(dir, "z.txt"), "x")
	entry := MustFirstFileEntry(t, dir)
	if entry == nil {
		t.Fatalf("expected file entry after directory entries")
	}
	if entry.Name() != "z.txt" {
		t.Fatalf("expected first file entry to be %q, got %q", "z.txt", entry.Name())
	}
}

func TestWriteTempFile(t *testing.T) {
	path := WriteTempFile(t, "tmp.txt", "abc")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp file: %v", err)
	}
	if string(content) != "abc" {
		t.Fatalf("unexpected temp file content: %q", content)
	}
}

func TestChdirAndMustFirstFileEntry(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(testutilGetwdErrFmt, err)
	}

	dir := t.TempDir()
	MustWriteFile(t, filepath.Join(dir, "a.txt"), "a")

	t.Run("chdir and first file", func(t *testing.T) {
		Chdir(t, dir)
		cleanDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("eval symlinks for dir: %v", err)
		}
		if cwd, err := os.Getwd(); err != nil {
			t.Fatalf("getwd after chdir: %v", err)
		} else if cwd != dir && cwd != cleanDir {
			t.Fatalf("expected cwd %s (or %s), got %s", dir, cleanDir, cwd)
		}

		entry := MustFirstFileEntry(t, dir)
		if entry.IsDir() {
			t.Fatal("expected file entry")
		}
	})

	if cwd, err := os.Getwd(); err != nil {
		t.Fatalf("getwd after cleanup: %v", err)
	} else if cwd != originalWD {
		t.Fatalf("expected cwd restored to %s, got %s", originalWD, cwd)
	}
}

func TestChdir(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf(testutilGetwdErrFmt, err)
	}
	dir := t.TempDir()
	Chdir(t, dir)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd after chdir: %v", err)
	}
	wdResolved, err := filepath.EvalSymlinks(wd)
	if err != nil {
		t.Fatalf("eval wd symlink: %v", err)
	}
	dirResolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval dir symlink: %v", err)
	}
	if wdResolved != dirResolved {
		t.Fatalf("expected wd %q, got %q", dirResolved, wdResolved)
	}
	t.Cleanup(func() {
		wd2, err := os.Getwd()
		if err == nil && wd2 != original {
			if chdirErr := os.Chdir(original); chdirErr != nil {
				t.Fatalf("restore wd %s: %v", original, chdirErr)
			}
		}
	})
}

func TestChdirRemovedDir(t *testing.T) {
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(testutilGetwdErrFmt, err)
	}

	t.Run("removed cwd", func(t *testing.T) {
		ChdirRemovedDir(t)
	})

	if cwd, err := os.Getwd(); err != nil {
		t.Fatalf("getwd after cleanup: %v", err)
	} else if cwd != originalWD {
		t.Fatalf("expected cwd restored to %s, got %s", originalWD, cwd)
	}
}

func TestRunGit(t *testing.T) {
	if _, err := gitexec.ResolveBinaryPath(); err != nil {
		t.Skip("git binary not available")
	}

	repo := t.TempDir()
	RunGit(t, repo, "init")
	RunGit(t, repo, "status", "--short")

	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("expected git repository to be initialized: %v", err)
	}
}

func TestIsolatedGitEnv(t *testing.T) {
	t.Setenv("KEEP_ME", "1")
	t.Setenv("GIT_DIR", "/tmp/attacker-git")
	t.Setenv("GIT_CONFIG_GLOBAL", "/tmp/attacker-global")
	t.Setenv("LD_PRELOAD", "/tmp/attacker.so")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/attacker.dylib")
	t.Setenv("HOME", "/tmp/attacker-home")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/attacker-xdg")
	t.Setenv("PATH", "/tmp/custom-bin:"+os.Getenv("PATH"))
	t.Setenv("PAGER", "more")

	env := IsolatedGitEnv(t)
	envText := strings.Join(env, "\n")

	for _, blockedEnvEntry := range []string{
		"GIT_DIR=",
		"GIT_CONFIG_GLOBAL=/tmp/attacker-global",
		"LD_PRELOAD=",
		"DYLD_INSERT_LIBRARIES=",
		"HOME=/tmp/attacker-home",
		"XDG_CONFIG_HOME=/tmp/attacker-xdg",
		"PAGER=more",
	} {
		if strings.Contains(envText, blockedEnvEntry) {
			t.Fatalf("expected %q to be stripped from %#v", blockedEnvEntry, env)
		}
	}
	for _, expected := range []string{
		"KEEP_ME=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	} {
		if !strings.Contains(envText, expected) {
			t.Fatalf("expected %q in %#v", expected, env)
		}
	}
	for _, expected := range gitexec.SafeConfigEnvEntries() {
		if !strings.Contains(envText, expected) {
			t.Fatalf("expected shared git hardening entry %q in %#v", expected, env)
		}
	}
	if !strings.Contains(envText, "PATH=/tmp/custom-bin:") {
		t.Fatalf("expected caller PATH to be preserved in %#v", env)
	}
	if homeIndex := indexEnv(env, "HOME="); homeIndex < 0 {
		t.Fatalf("expected isolated HOME in %#v", env)
	} else if xdgIndex := indexEnv(env, "XDG_CONFIG_HOME="); xdgIndex != homeIndex+1 {
		t.Fatalf("expected XDG_CONFIG_HOME immediately after HOME in %#v", env)
	}
}

func indexEnv(env []string, prefix string) int {
	for index, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return index
		}
	}
	return -1
}

func TestMustWriteFileModeReportsMkdirFailures(t *testing.T) {
	dir := t.TempDir()
	parentFile := filepath.Join(dir, "parent")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup parent file: %v", err)
	}

	expectFatal(t, "mkdir", func(tb *fakeTB) {
		mustWriteFileMode(tb, filepath.Join(parentFile, "child.txt"), "x", 0o600)
	})
}

func TestMustWriteFileModeReportsWriteFailures(t *testing.T) {
	dir := t.TempDir()

	expectFatal(t, "write", func(tb *fakeTB) {
		mustWriteFileMode(tb, dir, "x", 0o600)
	})
}

func TestMustWritePaddedFileLeavesContentUnchangedWhenAlreadyLargeEnough(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")

	MustWritePaddedFile(t, path, "already-long", 4)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read padded file: %v", err)
	}
	if string(data) != "already-long" {
		t.Fatalf("padded file = %q, want %q", string(data), "already-long")
	}
}

func TestOpenOSRootReportsErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, err := openOSRoot(file); err == nil {
		t.Fatal("expected openOSRoot to fail for file path")
	}
}

func TestMustWritePaddedFileReportsMkdirFailures(t *testing.T) {
	patchTestutilSeams(t)
	mkdirAll = func(path string, perm os.FileMode) error {
		return errors.New("mkdir failed")
	}

	expectFatal(t, "mkdir", func(tb *fakeTB) {
		mustWritePaddedFile(tb, filepath.Join(t.TempDir(), "nested", "file.txt"), "x", 1)
	})
}

func TestMustWritePaddedFileReportsOpenRootFailures(t *testing.T) {
	patchTestutilSeams(t)
	openRoot = func(name string) (rootHandle, error) {
		return nil, errors.New("boom")
	}

	expectFatal(t, "open root", func(tb *fakeTB) {
		mustWritePaddedFile(tb, filepath.Join(t.TempDir(), "nested", "file.txt"), "x", 1)
	})
}

func TestMustWritePaddedFileReportsOpenFailures(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("setup target dir: %v", err)
	}

	expectFatal(t, "open", func(tb *fakeTB) {
		mustWritePaddedFile(tb, targetDir, "x", 2)
	})
}

func TestMustWritePaddedFileReportsInitialShortWrites(t *testing.T) {
	patchTestutilSeams(t)
	openRoot = func(name string) (rootHandle, error) {
		return &stubRoot{
			openFunc: func(name string) (rootFile, error) { return nil, errors.New("unexpected open") },
			openFileFunc: func(name string, flag int, perm os.FileMode) (rootFile, error) {
				return &stubRootFile{
					readFunc:        func([]byte) (int, error) { return 0, io.EOF },
					writeStringFunc: func(s string) (int, error) { return len(s) - 1, nil },
					closeFunc:       func() error { return nil },
				}, nil
			},
			closeFunc: func() error { return nil },
		}, nil
	}

	expectFatal(t, "wrote 4 of 5 bytes", func(tb *fakeTB) {
		mustWritePaddedFile(tb, filepath.Join(t.TempDir(), "seed.txt"), "hello", 8)
	})
}

func TestMustWritePaddedFileReportsPaddingWriteFailures(t *testing.T) {
	patchTestutilSeams(t)
	writeCalls := 0
	openRoot = func(name string) (rootHandle, error) {
		return &stubRoot{
			openFunc: func(name string) (rootFile, error) { return nil, errors.New("unexpected open") },
			openFileFunc: func(name string, flag int, perm os.FileMode) (rootFile, error) {
				return &stubRootFile{
					readFunc: func([]byte) (int, error) { return 0, io.EOF },
					writeStringFunc: func(s string) (int, error) {
						writeCalls++
						if writeCalls == 1 {
							return len(s), nil
						}
						return 0, errors.New("pad failed")
					},
					closeFunc: func() error { return nil },
				}, nil
			},
			closeFunc: func() error { return nil },
		}, nil
	}

	expectFatal(t, "pad", func(tb *fakeTB) {
		mustWritePaddedFile(tb, filepath.Join(t.TempDir(), "seed.txt"), "x", 2)
	})
}

func TestMustWritePaddedFileReportsCloseFailures(t *testing.T) {
	patchTestutilSeams(t)
	openRoot = func(name string) (rootHandle, error) {
		return &stubRoot{
			openFunc: func(name string) (rootFile, error) { return nil, errors.New("unexpected open") },
			openFileFunc: func(name string, flag int, perm os.FileMode) (rootFile, error) {
				return &stubRootFile{
					readFunc:        func([]byte) (int, error) { return 0, io.EOF },
					writeStringFunc: func(s string) (int, error) { return len(s), nil },
					closeFunc:       func() error { return errors.New("close failed") },
				}, nil
			},
			closeFunc: func() error { return nil },
		}, nil
	}

	expectFatal(t, "close", func(tb *fakeTB) {
		mustWritePaddedFile(tb, filepath.Join(t.TempDir(), "seed.txt"), "x", 1)
	})
}

func TestMustWritePaddedFileReportsRootCloseFailures(t *testing.T) {
	patchTestutilSeams(t)
	openRoot = func(name string) (rootHandle, error) {
		return &stubRoot{
			openFunc: func(name string) (rootFile, error) { return nil, errors.New("unexpected open") },
			openFileFunc: func(name string, flag int, perm os.FileMode) (rootFile, error) {
				return &stubRootFile{
					readFunc:        func([]byte) (int, error) { return 0, io.EOF },
					writeStringFunc: func(s string) (int, error) { return len(s), nil },
					closeFunc:       func() error { return nil },
				}, nil
			},
			closeFunc: func() error { return errors.New("root close failed") },
		}, nil
	}

	expectFatal(t, "close root", func(tb *fakeTB) {
		mustWritePaddedFile(tb, filepath.Join(t.TempDir(), "seed.txt"), "x", 1)
	})
}

func TestChdirReportsMissingDirectories(t *testing.T) {
	expectFatal(t, "chdir", func(tb *fakeTB) {
		changeDir(tb, filepath.Join(t.TempDir(), "missing"))
	})
}

func TestChdirReportsGetwdFailures(t *testing.T) {
	patchTestutilSeams(t)
	getwd = func() (string, error) {
		return "", errors.New("getwd failed")
	}

	expectFatal(t, "getwd", func(tb *fakeTB) {
		changeDir(tb, t.TempDir())
	})
}

func TestChdirReportsRestoreFailures(t *testing.T) {
	patchTestutilSeams(t)
	tb := newFakeTB(t)
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(testutilGetwdErrFmt, err)
	}

	changeDir(tb, t.TempDir())
	chdir = func(path string) error {
		if path == originalWD {
			return errors.New("restore failed")
		}
		return os.Chdir(path)
	}
	defer func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore cwd after test: %v", err)
		}
	}()

	expectFatalWithTB(t, tb, "restore wd", tb.runCleanups)
}

func TestChdirRemovedDirReportsGetwdFailures(t *testing.T) {
	patchTestutilSeams(t)
	getwd = func() (string, error) {
		return "", errors.New("getwd failed")
	}

	expectFatal(t, "getwd", func(tb *fakeTB) {
		changeToRemovedDir(tb)
	})
}

func TestChdirRemovedDirReportsMkdirFailures(t *testing.T) {
	patchTestutilSeams(t)
	mkdirAll = func(path string, perm os.FileMode) error {
		return errors.New("mkdir failed")
	}

	expectFatal(t, "mkdir dead dir", func(tb *fakeTB) {
		changeToRemovedDir(tb)
	})
}

func TestChdirRemovedDirReportsChdirFailures(t *testing.T) {
	patchTestutilSeams(t)
	chdir = func(path string) error {
		if strings.HasSuffix(path, "dead") {
			return errors.New("chdir failed")
		}
		return os.Chdir(path)
	}

	expectFatal(t, "chdir dead dir", func(tb *fakeTB) {
		changeToRemovedDir(tb)
	})
}

func TestChdirRemovedDirReportsRemoveFailures(t *testing.T) {
	patchTestutilSeams(t)
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(testutilGetwdErrFmt, err)
	}
	removeAll = func(path string) error {
		return errors.New("remove failed")
	}
	defer func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore cwd after test: %v", err)
		}
	}()

	expectFatal(t, "remove dead dir", func(tb *fakeTB) {
		changeToRemovedDir(tb)
	})
}

func TestChdirRemovedDirReportsRestoreFailures(t *testing.T) {
	patchTestutilSeams(t)
	tb := newFakeTB(t)
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf(testutilGetwdErrFmt, err)
	}

	changeToRemovedDir(tb)
	chdir = func(path string) error {
		if path == originalWD {
			return errors.New("restore failed")
		}
		return os.Chdir(path)
	}
	defer func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore cwd after test: %v", err)
		}
	}()

	expectFatalWithTB(t, tb, "restore wd", tb.runCleanups)
}

func TestMustFirstFileEntryReportsReadDirFailures(t *testing.T) {
	expectFatal(t, "readdir", func(tb *fakeTB) {
		_ = mustFirstFileEntry(tb, filepath.Join(t.TempDir(), "missing"))
	})
}

func TestMustFirstFileEntryReportsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("setup subdir: %v", err)
	}

	expectFatal(t, "expected file entry", func(tb *fakeTB) {
		_ = mustFirstFileEntry(tb, dir)
	})
}

func TestRunGitReportsResolveFailures(t *testing.T) {
	patchTestutilSeams(t)
	resolveGitBinary = func() (string, error) {
		return "", errors.New("missing git")
	}

	expectFatal(t, "resolve git path", func(tb *fakeTB) {
		runGit(tb, t.TempDir(), "status")
	})
}

func TestRunGitReportsCommandConstructionFailures(t *testing.T) {
	patchTestutilSeams(t)
	commandContext = func(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
		return nil, errors.New("command failed")
	}

	expectFatal(t, "construct git", func(tb *fakeTB) {
		runGit(tb, t.TempDir(), "status")
	})
}

func TestRunGitReportsExecutionFailures(t *testing.T) {
	expectFatal(t, "git status --short", func(tb *fakeTB) {
		runGit(tb, t.TempDir(), "status", "--short")
	})
}

func TestIsolatedGitEnvReportsHomeDirectoryFailures(t *testing.T) {
	patchTestutilSeams(t)
	mkdirAll = func(path string, perm os.FileMode) error {
		if strings.HasSuffix(path, "home") {
			return errors.New("mkdir failed")
		}
		return os.MkdirAll(path, perm)
	}

	expectFatal(t, "mkdir isolated home", func(tb *fakeTB) {
		_ = isolatedGitEnv(tb)
	})
}

func TestIsolatedGitEnvReportsXDGDirectoryFailures(t *testing.T) {
	patchTestutilSeams(t)
	mkdirAll = func(path string, perm os.FileMode) error {
		if strings.HasSuffix(path, ".config") {
			return errors.New("mkdir failed")
		}
		return os.MkdirAll(path, perm)
	}

	expectFatal(t, "mkdir isolated xdg config", func(tb *fakeTB) {
		_ = isolatedGitEnv(tb)
	})
}

func TestIsolatedGitEnvSkipsMalformedEnvironmentEntries(t *testing.T) {
	patchTestutilSeams(t)
	currentEnviron = func() []string {
		return []string{"BROKEN", "KEEP=1"}
	}

	env := isolatedGitEnv(newFakeTB(t))
	if strings.Join(env, "\n") == "" {
		t.Fatal("expected environment entries")
	}
	if strings.Contains(strings.Join(env, "\n"), "BROKEN") {
		t.Fatalf("expected malformed environment entry to be skipped: %#v", env)
	}
}
