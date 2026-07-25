package testutil

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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

type testGitConfigOverride struct {
	key   string
	value string
}

var isolatedGitConfigOverrides = []testGitConfigOverride{
	{key: "core.fsmonitor", value: "false"},
	{key: "core.quotePath", value: "false"},
	{key: "diff.external", value: ""},
	{key: "interactive.diffFilter", value: ""},
	{key: "maintenance.auto", value: "false"},
	{key: "core.pager", value: "cat"},
}

func IsolatedGitEnv(t *testing.T) []string {
	t.Helper()

	homeDir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(homeDir, 0o750); err != nil {
		t.Fatalf("mkdir isolated home: %v", err)
	}
	xdgConfigHome := filepath.Join(homeDir, ".config")
	if err := os.MkdirAll(xdgConfigHome, 0o750); err != nil {
		t.Fatalf("mkdir isolated xdg config: %v", err)
	}

	env := make([]string, 0, len(os.Environ())+10+len(isolatedGitConfigOverrides)*2)
	for _, entry := range os.Environ() {
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
	env = append(env, isolatedGitConfigEnvEntries()...)
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

func isolatedGitConfigEnvEntries() []string {
	entries := make([]string, 0, 1+len(isolatedGitConfigOverrides)*2)
	entries = append(entries, "GIT_CONFIG_COUNT="+strconv.Itoa(len(isolatedGitConfigOverrides)))
	for index, override := range isolatedGitConfigOverrides {
		entries = append(entries, []string{
			"GIT_CONFIG_KEY_" + strconv.Itoa(index) + "=" + override.key,
			"GIT_CONFIG_VALUE_" + strconv.Itoa(index) + "=" + override.value,
		}...)
	}
	return entries
}
