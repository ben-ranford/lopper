//go:build unix

package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const shellPath = "/bin/sh"

func TestParseRuntimeCommandKeepsBackslashesInSingleQuotes(t *testing.T) {
	fields, err := parseRuntimeCommand(`node -e 'const value = "a\b"'`)
	if err != nil {
		t.Fatalf("parse runtime command: %v", err)
	}
	if len(fields) != 3 {
		t.Fatalf("expected 3 fields, got %#v", fields)
	}
	if got := fields[2]; got != `const value = "a\b"` {
		t.Fatalf("expected single-quoted backslash to be preserved, got %q", got)
	}

	var parser runtimeCommandParser
	parser.inSingleQuote = true
	parser.consume('\\')
	if got := parser.current.String(); got != `\` {
		t.Fatalf("expected direct consume to preserve backslash in single quotes, got %q", got)
	}
}

func TestTrustedSearchDirsSkipsNonDirectories(t *testing.T) {
	secureDir := t.TempDir()
	plainFile := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(plainFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write plain file: %v", err)
	}
	missingDir := filepath.Join(t.TempDir(), "missing")

	got := trustedSearchDirs(strings.Join([]string{plainFile, missingDir, secureDir}, string(os.PathListSeparator)))
	if len(got) != 1 || got[0] != secureDir {
		t.Fatalf("expected only secure directory to remain, got %#v", got)
	}
}

func TestRuntimeHookErrorsPropagate(t *testing.T) {
	sentinel := errors.New("hook lookup failed")
	resolver := newFakeRuntimeHookPathResolver(func() (string, error) { return "", nil }, func(skip int) (uintptr, string, int, bool) { return 0, "", 0, false })
	resolver.hookPaths.err = sentinel
	resolver.hookPathsOnce.Do(func() {})

	if _, err := resolver.runtimeNodeHookOptions(); !errors.Is(err, sentinel) {
		t.Fatalf("expected runtime hook options error %v, got %v", sentinel, err)
	}

	_, err := withRuntimeTraceEnvForResolver([]string{"PATH=/usr/bin"}, "/tmp/runtime.ndjson", CaptureProviderNode, "/repo", resolver)
	if err == nil || !strings.Contains(err.Error(), "resolve runtime node hooks") {
		t.Fatalf("expected wrapped runtime hook error, got %v", err)
	}

	req := CaptureRequest{RepoPath: t.TempDir(), Command: "make test"}
	err = captureWithRuntimeHookPathResolver(context.Background(), req, resolver)
	if err == nil || !strings.Contains(err.Error(), "resolve runtime node hooks") {
		t.Fatalf("expected capture to surface hook resolution error, got %v", err)
	}
}

func TestLocateRuntimePythonHookDirectoryInRootsErrorsWhenHookMissing(t *testing.T) {
	root := t.TempDir()
	_, err := locateRuntimePythonHookDirectoryInRoots([]string{root})
	if err == nil || !strings.Contains(err.Error(), "could not locate runtime python hook") {
		t.Fatalf("expected missing python hook error, got %v", err)
	}
}

func TestLocateRuntimeHookPathsInRootsErrorsWhenHooksMissing(t *testing.T) {
	root := t.TempDir()
	_, _, err := locateRuntimeHookPathsInRoots([]string{root})
	if err == nil || !strings.Contains(err.Error(), "could not locate runtime hooks") {
		t.Fatalf("expected missing hook error, got %v", err)
	}
}

func TestConfigureRuntimeCommandCancelBranches(t *testing.T) {
	t.Run("without process", func(t *testing.T) {
		cmd := shellCommand(context.Background(), "-c", "exit 0")
		configureRuntimeCommand(cmd)

		err := cmd.Cancel()
		if !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("expected process-done error, got %v", err)
		}
	})

	t.Run("after process exits", func(t *testing.T) {
		cmd := shellCommand(context.Background(), "-c", "exit 0")
		configureRuntimeCommand(cmd)

		if err := cmd.Start(); err != nil {
			t.Fatalf("start process: %v", err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("wait process: %v", err)
		}

		err := cmd.Cancel()
		if !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("expected process-done error after exit, got %v", err)
		}
	})
}

func TestConfigureRuntimeCommandCancelMapsESRCHToProcessDone(t *testing.T) {
	cmd := shellCommand(context.Background(), "-c", "sleep 5")
	configureRuntimeCommand(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill process group: %v", err)
	}
	if err := cmd.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) && !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("wait process: %v", err)
	}

	err := cmd.Cancel()
	if !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("expected ESRCH to map to os.ErrProcessDone, got %v", err)
	}
}

func shellCommand(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, shellPath, args...)
}
