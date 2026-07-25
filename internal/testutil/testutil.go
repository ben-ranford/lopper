package testutil

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

type helperTB interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(func())
	TempDir() string
}

type rootHandle interface {
	Open(name string) (rootFile, error)
	OpenFile(name string, flag int, perm os.FileMode) (rootFile, error)
	Close() error
}

type rootFile interface {
	io.Reader
	io.StringWriter
	Close() error
}

type osRootHandle struct {
	root *os.Root
}

func (h *osRootHandle) Open(name string) (rootFile, error) {
	return h.root.Open(name)
}

func (h *osRootHandle) OpenFile(name string, flag int, perm os.FileMode) (rootFile, error) {
	return h.root.OpenFile(name, flag, perm)
}

func (h *osRootHandle) Close() error {
	return h.root.Close()
}

var (
	mkdirAll         = os.MkdirAll
	writeFile        = os.WriteFile
	openRoot         = openOSRoot
	readDir          = os.ReadDir
	getwd            = os.Getwd
	chdir            = os.Chdir
	removeAll        = os.RemoveAll
	resolveGitBinary = gitexec.ResolveBinaryPath
	commandContext   = gitexec.CommandContext
	sanitizedGitEnv  = gitexec.SanitizedEnv
	currentEnviron   = os.Environ
)

func openOSRoot(name string) (rootHandle, error) {
	root, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	return &osRootHandle{root: root}, nil
}

func CanceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func MustWriteFile(t *testing.T, path string, content string) {
	MustWriteFileMode(t, path, content, 0o600)
}

func MustWriteFileMode(t *testing.T, path string, content string, perm os.FileMode) {
	mustWriteFileMode(t, path, content, perm)
}

func mustWriteFileMode(t helperTB, path string, content string, perm os.FileMode) {
	t.Helper()
	if err := mkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := writeFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func MustWritePaddedFile(t *testing.T, path string, content string, minBytes int64) {
	mustWritePaddedFile(t, path, content, minBytes)
}

func mustWritePaddedFile(t helperTB, path string, content string, minBytes int64) {
	t.Helper()
	if err := mkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	root, err := openRoot(filepath.Dir(path))
	if err != nil {
		t.Fatalf("open root for %s: %v", path, err)
	}
	file, err := root.OpenFile(filepath.Base(path), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		closeErr := root.Close()
		t.Fatalf("open %s: %v (close root: %v)", path, err, closeErr)
	}
	written, err := file.WriteString(content)
	if err != nil || written != len(content) {
		closeErr := file.Close()
		rootCloseErr := root.Close()
		t.Fatalf("write %s: wrote %d of %d bytes: %v (close: %v; close root: %v)", path, written, len(content), err, closeErr, rootCloseErr)
	}
	padding := strings.Repeat(" ", 1<<20)
	for remaining := minBytes - int64(len(content)); remaining > 0; {
		chunk := padding
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		written, writeErr := file.WriteString(chunk)
		if writeErr != nil || written != len(chunk) {
			closeErr := file.Close()
			rootCloseErr := root.Close()
			t.Fatalf("pad %s: wrote %d of %d bytes: %v (close: %v; close root: %v)", path, written, len(chunk), writeErr, closeErr, rootCloseErr)
		}
		remaining -= int64(written)
	}
	if err := file.Close(); err != nil {
		rootCloseErr := root.Close()
		t.Fatalf("close %s: %v (close root: %v)", path, err, rootCloseErr)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close root for %s: %v", path, err)
	}
}

func WriteNumberedTextFiles(t *testing.T, dir string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		MustWriteFile(t, filepath.Join(dir, fmt.Sprintf("f-%d.txt", i)), "x")
	}
}

func WriteTempFile(t *testing.T, filename string, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	mustWriteFileMode(t, path, content, 0o644)
	return path
}

func Chdir(t *testing.T, dir string) {
	changeDir(t, dir)
}

func changeDir(t helperTB, dir string) {
	t.Helper()
	originalWD, err := getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})
}

func ChdirRemovedDir(t *testing.T) {
	changeToRemovedDir(t)
}

func changeToRemovedDir(t helperTB) {
	t.Helper()
	originalWD, err := getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})

	deadDir := filepath.Join(t.TempDir(), "dead")
	if err := mkdirAll(deadDir, 0o750); err != nil {
		t.Fatalf("mkdir dead dir: %v", err)
	}
	if err := chdir(deadDir); err != nil {
		t.Fatalf("chdir dead dir: %v", err)
	}
	if err := removeAll(deadDir); err != nil {
		t.Fatalf("remove dead dir: %v", err)
	}
}

func MustFirstFileEntry(t *testing.T, dir string) fs.DirEntry {
	return mustFirstFileEntry(t, dir)
}

func mustFirstFileEntry(t helperTB, dir string) fs.DirEntry {
	t.Helper()
	entries, err := readDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return entry
		}
	}
	t.Fatalf("expected file entry in %s", dir)
	return nil
}

func RunGit(t *testing.T, repo string, args ...string) {
	runGit(t, repo, args...)
}

func runGit(t helperTB, repo string, args ...string) {
	t.Helper()
	gitPath, err := resolveGitBinary()
	if err != nil {
		t.Fatalf("resolve git path: %v", err)
	}
	command, err := commandContext(context.Background(), gitPath, append([]string{"-C", repo}, args...)...)
	if err != nil {
		t.Fatalf("construct git %s: %v", strings.Join(args, " "), err)
	}
	command.Env = sanitizedGitEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}

func IsolatedGitEnv(t *testing.T) []string {
	return isolatedGitEnv(t)
}

func isolatedGitEnv(t helperTB) []string {
	t.Helper()

	homeDir := filepath.Join(t.TempDir(), "home")
	if err := mkdirAll(homeDir, 0o750); err != nil {
		t.Fatalf("mkdir isolated home: %v", err)
	}
	xdgConfigHome := filepath.Join(homeDir, ".config")
	if err := mkdirAll(xdgConfigHome, 0o750); err != nil {
		t.Fatalf("mkdir isolated xdg config: %v", err)
	}

	env := make([]string, 0, len(currentEnviron())+5+len(gitexec.SafeConfigEnvEntries()))
	for _, entry := range currentEnviron() {
		key, _, hasKey := strings.Cut(entry, "=")
		if !hasKey || shouldStripIsolatedGitEnvKey(key) {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, []string{
		"HOME=" + homeDir,
		"XDG_CONFIG_HOME=" + xdgConfigHome,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	}...)
	env = append(env, gitexec.SafeConfigEnvEntries()...)
	return env
}

func shouldStripIsolatedGitEnvKey(key string) bool {
	if strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "LD_") || strings.HasPrefix(key, "DYLD_") {
		return true
	}
	switch key {
	case "HOME", "XDG_CONFIG_HOME", "XDG_CONFIG_DIRS", "PAGER", "EDITOR", "VISUAL":
		return true
	default:
		return false
	}
}
