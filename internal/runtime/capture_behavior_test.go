package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDefaultTracePath(t *testing.T) {
	repo := "/tmp/repo"
	if got := DefaultTracePath(repo); got != filepath.Join(repo, defaultTraceRelPath) {
		t.Fatalf("unexpected default trace path: %q", got)
	}
}

func TestCapture(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	err := Capture(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   makeVersionCommand,
	})
	if err != nil {
		t.Fatalf("capture runtime trace: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(tracePath)); err != nil {
		t.Fatalf("expected trace directory to exist: %v", err)
	}
}

func TestCaptureUsesAbsoluteNodeHookPaths(t *testing.T) {
	repo := t.TempDir()
	nodeOptionsPath := filepath.Join(repo, "node-options.txt")
	t.Setenv("LOPPER_CAPTURE_NODE_OPTIONS", nodeOptionsPath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\nprintf '%s' \"$NODE_OPTIONS\" > \"$LOPPER_CAPTURE_NODE_OPTIONS\"\n"))

	err := Capture(context.Background(), CaptureRequest{
		RepoPath: repo,
		Command:  npmTestCommand,
	})
	if err != nil {
		t.Fatalf("capture runtime trace: %v", err)
	}

	gotBytes, err := os.ReadFile(nodeOptionsPath)
	if err != nil {
		t.Fatalf("read node options: %v", err)
	}
	got := string(gotBytes)
	if strings.Contains(got, "./scripts/runtime/") {
		t.Fatalf("expected node hook paths to resolve from lopper, got %q", got)
	}

	requirePath, loaderPath, err := runtimeHookPaths()
	if err != nil {
		t.Fatalf("runtime hook paths: %v", err)
	}
	if !strings.Contains(got, "--require="+requirePath) {
		t.Fatalf("expected absolute require hook path, got %q", got)
	}
	if !strings.Contains(got, "--loader="+loaderPath) {
		t.Fatalf("expected absolute loader hook path, got %q", got)
	}
}

func TestCaptureCommandFailure(t *testing.T) {
	repo := t.TempDir()
	assertCaptureErrorContains(t, CaptureRequest{RepoPath: repo, Command: "make __missing_target__"}, "runtime test command failed")
}

func TestCaptureUnsupportedCommand(t *testing.T) {
	repo := t.TempDir()
	assertCaptureErrorContains(t, CaptureRequest{RepoPath: repo, Command: "foobar test"}, "unsupported runtime test executable")
}

func TestCaptureValidationErrors(t *testing.T) {
	if Capture(context.Background(), CaptureRequest{Command: npmTestCommand}) == nil {
		t.Fatalf("expected missing repo path error")
	}
	if Capture(context.Background(), CaptureRequest{RepoPath: t.TempDir()}) == nil {
		t.Fatalf("expected missing command error")
	}
}

func TestCaptureExecutableNotFound(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, t.TempDir())
	repo := t.TempDir()
	err := Capture(context.Background(), CaptureRequest{
		RepoPath: repo,
		Command:  npmTestCommand,
	})
	if err == nil {
		t.Fatalf("expected executable-not-found capture error")
	}
	if !strings.Contains(err.Error(), "not found in trusted runtime directories") {
		t.Fatalf("unexpected capture executable-not-found error: %v", err)
	}
}

func TestCaptureTracePathSetupErrors(t *testing.T) {
	t.Run("create trace directory", func(t *testing.T) {
		repo := t.TempDir()
		blocker := filepath.Join(repo, "blocked")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatalf("write blocker: %v", err)
		}

		err := Capture(context.Background(), CaptureRequest{
			RepoPath:  repo,
			TracePath: filepath.Join(blocker, runtimeTraceNDJSON),
			Command:   makeVersionCommand,
		})
		if err == nil || !strings.Contains(err.Error(), "create runtime trace directory") {
			t.Fatalf("expected trace directory creation error, got %v", err)
		}
	})

	t.Run("remove previous runtime trace", func(t *testing.T) {
		repo := t.TempDir()
		tracePath := filepath.Join(repo, "traces", runtimeTraceNDJSON)
		if err := os.MkdirAll(tracePath, 0o750); err != nil {
			t.Fatalf("mkdir trace path: %v", err)
		}
		if err := os.WriteFile(filepath.Join(tracePath, "keep.txt"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write trace child: %v", err)
		}

		err := Capture(context.Background(), CaptureRequest{
			RepoPath:  repo,
			TracePath: tracePath,
			Command:   makeVersionCommand,
		})
		if err == nil || !strings.Contains(err.Error(), "remove previous runtime trace") {
			t.Fatalf("expected trace cleanup error, got %v", err)
		}
	})
}

func TestCaptureCommandFailureWithoutOutput(t *testing.T) {
	repo := t.TempDir()
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "make", "#!/bin/sh\nexit 3\n"))

	err := Capture(context.Background(), CaptureRequest{
		RepoPath: repo,
		Command:  "make test",
	})
	if err == nil {
		t.Fatalf("expected silent command failure")
	}
	if !strings.Contains(err.Error(), "runtime test command failed") {
		t.Fatalf("expected runtime command failure error, got %v", err)
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("expected silent failure without command output, got %v", err)
	}
}

func TestCaptureHonorsContextCancellation(t *testing.T) {
	repo := t.TempDir()
	markerPath := filepath.Join(repo, "started.txt")
	t.Setenv("LOPPER_CAPTURE_MARKER", markerPath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "make", "#!/bin/sh\nsleep 5\nprintf started > \"$LOPPER_CAPTURE_MARKER\"\n"))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := Capture(ctx, CaptureRequest{
		RepoPath: repo,
		Command:  "make test",
	})
	if err == nil {
		t.Fatalf("expected capture cancellation error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "signal: killed") {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("expected cancelled command to stop quickly, took %v", elapsed)
	}
	if _, statErr := os.Stat(markerPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected cancelled command to stop before creating marker, stat err = %v", statErr)
	}
}

func TestCaptureReuseIfUnchangedSkipsRepeatedCommand(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", runtimeTraceNDJSON)
	counterPath := filepath.Join(repo, "counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeToolScript(t, "npm", "#!/bin/sh\ncount=$(cat \"$LOPPER_RUNTIME_COUNTER\" 2>/dev/null || echo 0)\ncount=$((count + 1))\nprintf '%s' \"$count\" > \"$LOPPER_RUNTIME_COUNTER\"\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"))

	first := CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   npmTestCommand,
	}
	if err := Capture(context.Background(), first); err != nil {
		t.Fatalf("capture first run: %v", err)
	}
	if got := readCaptureCounter(t, counterPath); got != 1 {
		t.Fatalf("expected first capture execution count 1, got %d", got)
	}

	second := first
	second.ReuseIfUnchanged = true
	if err := Capture(context.Background(), second); err != nil {
		t.Fatalf("capture second run: %v", err)
	}
	if got := readCaptureCounter(t, counterPath); got != 1 {
		t.Fatalf("expected second capture reuse without rerun, got %d", got)
	}

	third := second
	third.Command = "npm run test"
	if err := Capture(context.Background(), third); err != nil {
		t.Fatalf("capture third run command change: %v", err)
	}
	if got := readCaptureCounter(t, counterPath); got != 2 {
		t.Fatalf("expected changed command to rerun capture, got %d", got)
	}
}

func readCaptureCounter(t *testing.T, counterPath string) int {
	t.Helper()
	content, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatalf("read capture counter: %v", err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		t.Fatalf("parse capture counter: %v", err)
	}
	return value
}
