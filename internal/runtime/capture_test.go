package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const npmTestCommand = "npm test"

func TestDefaultTracePath(t *testing.T) {
	repo := "/tmp/repo"
	if got := DefaultTracePath(repo); got != filepath.Join(repo, defaultTraceRelPath) {
		t.Fatalf("unexpected default trace path: %q", got)
	}
}

func TestWithRuntimeTraceEnv(t *testing.T) {
	tracePath := "/tmp/runtime.ndjson"
	env := withRuntimeTraceEnv([]string{"NODE_OPTIONS=--max-old-space-size=4096", "PATH=/usr/bin"}, tracePath)

	var hasTrace bool
	var hasNodeOptions bool
	for _, entry := range env {
		if entry == "LOPPER_RUNTIME_TRACE="+tracePath {
			hasTrace = true
		}
		if strings.HasPrefix(entry, "NODE_OPTIONS=") {
			hasNodeOptions = true
			if !strings.Contains(entry, "--max-old-space-size=4096") {
				t.Fatalf("expected existing NODE_OPTIONS to be preserved: %q", entry)
			}
			if !strings.Contains(entry, "--loader=./scripts/runtime/loader.mjs") {
				t.Fatalf("expected loader hook to be included: %q", entry)
			}
		}
	}
	if !hasTrace {
		t.Fatalf("expected LOPPER_RUNTIME_TRACE to be set")
	}
	if !hasNodeOptions {
		t.Fatalf("expected NODE_OPTIONS to be set")
	}
}

func TestCapture(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", "runtime.ndjson")
	err := Capture(context.Background(), CaptureRequest{
		RepoPath:  repo,
		TracePath: tracePath,
		Command:   "make -v",
	})
	if err != nil {
		t.Fatalf("capture runtime trace: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(tracePath)); err != nil {
		t.Fatalf("expected trace directory to exist: %v", err)
	}
}

func TestCaptureCommandFailure(t *testing.T) {
	repo := t.TempDir()
	assertCaptureErrorContains(t, CaptureRequest{
		RepoPath: repo,
		Command:  "make __missing_target__",
	}, "runtime test command failed")
}

func TestCaptureUnsupportedCommand(t *testing.T) {
	repo := t.TempDir()
	assertCaptureErrorContains(t, CaptureRequest{
		RepoPath: repo,
		Command:  "foobar test",
	}, "unsupported runtime test executable")
}

func assertCaptureErrorContains(t *testing.T, req CaptureRequest, wantSubstring string) {
	t.Helper()

	err := Capture(context.Background(), req)
	if err == nil {
		t.Fatalf("expected capture error containing %q", wantSubstring)
	}
	if !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("expected capture error to contain %q, got %v", wantSubstring, err)
	}
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
	t.Setenv("PATH", "")
	repo := t.TempDir()
	err := Capture(context.Background(), CaptureRequest{
		RepoPath: repo,
		Command:  npmTestCommand,
	})
	if err == nil {
		t.Fatalf("expected executable-not-found capture error")
	}
	if !strings.Contains(err.Error(), "runtime test command failed") {
		t.Fatalf("unexpected capture executable-not-found error: %v", err)
	}
}

func TestBuildRuntimeCommandAllowlist(t *testing.T) {
	commands := []string{
		npmTestCommand,
		"pnpm test",
		"yarn test",
		"bun test",
		"npx vitest",
		"node -v",
		"vitest run",
		"jest --runInBand",
		"mocha",
		"ava",
		"deno test",
		"make test",
	}

	for _, command := range commands {
		cmd, err := buildRuntimeCommand(context.Background(), command)
		if err != nil {
			t.Fatalf("expected %q to be allowlisted: %v", command, err)
		}
		if cmd.Path == "" {
			t.Fatalf("expected executable path for command %q", command)
		}
	}
}

func TestBuildRuntimeCommandRequiresInput(t *testing.T) {
	if _, err := buildRuntimeCommand(context.Background(), " "); err == nil {
		t.Fatalf("expected empty command error")
	}
}

func TestMergeEnvAndReadEnvValue(t *testing.T) {
	base := []string{"A=1", "BADENTRY", "NODE_OPTIONS=--max-old-space-size=2048"}
	merged := mergeEnv(base, map[string]string{"A": "2", "B": "3"})
	if got := readEnvValue(merged, "A"); got != "2" {
		t.Fatalf("expected updated A value, got %q", got)
	}
	if got := readEnvValue(merged, "B"); got != "3" {
		t.Fatalf("expected B value, got %q", got)
	}
	if got := readEnvValue(merged, "MISSING"); got != "" {
		t.Fatalf("expected missing env value, got %q", got)
	}
}
