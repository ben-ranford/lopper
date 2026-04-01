package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

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
	restoreRuntimeHookState(t)
	sentinel := errors.New("hook lookup failed")

	runtimeHookPathsOnce = sync.Once{}
	runtimeHookPathsOnce.Do(func() {
		runtimeRequireHookPath = ""
		runtimeLoaderHookPath = ""
		runtimeHookPathsErr = sentinel
	})

	if _, err := runtimeNodeHookOptions(); !errors.Is(err, sentinel) {
		t.Fatalf("expected runtime hook options error %v, got %v", sentinel, err)
	}

	_, err := withRuntimeTraceEnv([]string{"PATH=/usr/bin"}, "/tmp/runtime.ndjson")
	if err == nil || !strings.Contains(err.Error(), "resolve runtime node hooks") {
		t.Fatalf("expected wrapped runtime hook error, got %v", err)
	}

	err = Capture(context.Background(), CaptureRequest{
		RepoPath: t.TempDir(),
		Command:  "make test",
	})
	if err == nil || !strings.Contains(err.Error(), "resolve runtime node hooks") {
		t.Fatalf("expected capture to surface hook resolution error, got %v", err)
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
		cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
		configureRuntimeCommand(cmd)

		err := cmd.Cancel()
		if !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("expected process-done error, got %v", err)
		}
	})

	t.Run("after process exits", func(t *testing.T) {
		cmd := exec.CommandContext(context.Background(), "sh", "-c", "exit 0")
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

func restoreRuntimeHookState(t *testing.T) {
	t.Helper()

	originalRequire := runtimeRequireHookPath
	originalLoader := runtimeLoaderHookPath
	originalErr := runtimeHookPathsErr

	t.Cleanup(func() {
		runtimeHookPathsOnce = sync.Once{}
		runtimeHookPathsOnce.Do(func() {
			runtimeRequireHookPath = originalRequire
			runtimeLoaderHookPath = originalLoader
			runtimeHookPathsErr = originalErr
		})
	})
}

func TestConfigureRuntimeCommandCancelMapsESRCHToProcessDone(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "sleep 5")
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
