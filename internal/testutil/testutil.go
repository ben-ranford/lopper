package testutil

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func CanceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func MustWriteFile(t *testing.T, path string, content string) {
	MustWriteFileMode(t, path, content, 0o600)
}

func MustWriteFileMode(t *testing.T, path string, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func MustWritePaddedFile(t *testing.T, path string, content string, minBytes int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
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
	MustWriteFileMode(t, path, content, 0o644)
	return path
}

func Chdir(t *testing.T, dir string) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})
}

func ChdirRemovedDir(t *testing.T) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd %s: %v", originalWD, err)
		}
	})

	deadDir := filepath.Join(t.TempDir(), "dead")
	if err := os.MkdirAll(deadDir, 0o750); err != nil {
		t.Fatalf("mkdir dead dir: %v", err)
	}
	if err := os.Chdir(deadDir); err != nil {
		t.Fatalf("chdir dead dir: %v", err)
	}
	if err := os.RemoveAll(deadDir); err != nil {
		t.Fatalf("remove dead dir: %v", err)
	}
}

func MustFirstFileEntry(t *testing.T, dir string) fs.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
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
	t.Helper()
	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("resolve git path: %v", err)
	}
	command, err := gitexec.CommandContext(context.Background(), gitPath, append([]string{"-C", repo}, args...)...)
	if err != nil {
		t.Fatalf("construct git %s: %v", strings.Join(args, " "), err)
	}
	command.Env = gitexec.SanitizedEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}
