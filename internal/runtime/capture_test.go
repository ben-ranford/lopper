package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	npmTestCommand     = "npm test"
	runtimeTraceNDJSON = "runtime.ndjson"
	makeVersionCommand = "make -v"
)

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

func TestCaptureCommandFailure(t *testing.T) {
	repo := t.TempDir()
	assertCaptureErrorContains(t, CaptureRequest{RepoPath: repo, Command: "make __missing_target__"}, "runtime test command failed")
}

func TestCaptureUnsupportedCommand(t *testing.T) {
	repo := t.TempDir()
	assertCaptureErrorContains(t, CaptureRequest{RepoPath: repo, Command: "foobar test"}, "unsupported runtime test executable")
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

func TestBuildRuntimeCommandAllowlist(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeTools(t))

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
		if cmd.Path == "" || !filepath.IsAbs(cmd.Path) {
			t.Fatalf("expected executable path for command %q", command)
		}
	}
}

func TestBuildRuntimeCommandPreservesParsedArgs(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, setupFakeRuntimeTools(t))

	testCases := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "quoted args",
			command: `node -e "console.log('hello world')"`,
			want:    []string{"node", "-e", "console.log('hello world')"},
		},
		{
			name:    "single quoted args",
			command: `node -e 'console.log("hello")'`,
			want:    []string{"node", "-e", `console.log("hello")`},
		},
		{
			name:    "escaped whitespace",
			command: `make test\ target`,
			want:    []string{"make", "test target"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := buildRuntimeCommand(context.Background(), tc.command)
			if err != nil {
				t.Fatalf("build runtime command: %v", err)
			}
			if !slices.Equal(cmd.Args[1:], tc.want[1:]) {
				t.Fatalf("expected args %q, got %q", tc.want[1:], cmd.Args[1:])
			}
			if got := filepath.Base(cmd.Path); got != tc.want[0] {
				t.Fatalf("expected executable %q, got %q", tc.want[0], got)
			}
		})
	}
}

func TestTrustedSearchDirs(t *testing.T) {
	secureA := t.TempDir()
	secureB := t.TempDir()
	insecure := filepath.Join(t.TempDir(), "insecure")
	if err := os.MkdirAll(insecure, 0o700); err != nil {
		t.Fatalf("mkdir insecure: %v", err)
	}
	info, err := os.Stat(insecure)
	if err != nil {
		t.Fatalf("stat insecure: %v", err)
	}
	insecurePerms := info.Mode().Perm() | 0o020
	if err := os.Chmod(insecure, insecurePerms); err != nil {
		t.Fatalf("chmod insecure: %v", err)
	}

	dirEntries := []string{
		"",
		".",
		secureA,
		insecure,
		secureB,
		secureA, // duplicate
	}
	dirListValue := strings.Join(dirEntries, string(os.PathListSeparator))
	got := trustedSearchDirs(dirListValue)
	if len(got) != 2 {
		t.Fatalf("expected 2 trusted dirs, got %d: %v", len(got), got)
	}
	if got[0] != secureA {
		t.Fatalf("expected secureA first, got %q", got[0])
	}
	if got[1] != secureB {
		t.Fatalf("expected secureB second, got %q", got[1])
	}
}

func TestRuntimeSearchDirsDefault(t *testing.T) {
	t.Setenv(runtimeBinDirsEnvKey, "")
	_ = runtimeSearchDirs()
}

func TestBuildRuntimeCommandRequiresInput(t *testing.T) {
	if _, err := buildRuntimeCommand(context.Background(), " "); err == nil {
		t.Fatalf("expected empty command error")
	}
}

func TestBuildRuntimeCommandRejectsMalformedInput(t *testing.T) {
	testCases := []struct {
		name    string
		command string
		wantErr string
	}{
		{
			name:    "unfinished escape",
			command: `npm test\`,
			wantErr: "unfinished escape sequence",
		},
		{
			name:    "unterminated quote",
			command: `node -e "console.log('hello world')`,
			wantErr: "unterminated quote",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildRuntimeCommand(context.Background(), tc.command)
			if err == nil {
				t.Fatalf("expected error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
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

func TestResolveRuntimeExecutablePathSkipsNonExecutableCandidate(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()

	firstPath := filepath.Join(firstDir, "npm")
	if err := os.WriteFile(firstPath, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatalf("write non-executable tool: %v", err)
	}
	secondPath := filepath.Join(secondDir, "npm")
	if err := os.WriteFile(secondPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write executable tool: %v", err)
	}

	got, err := resolveRuntimeExecutablePath("npm", []string{firstDir, secondDir})
	if err != nil {
		t.Fatalf("resolve runtime executable path: %v", err)
	}
	if got != secondPath {
		t.Fatalf("expected executable path %q, got %q", secondPath, got)
	}
}

func TestNewAllowlistedRuntimeCommandRejectsUnsupportedExecutable(t *testing.T) {
	if _, err := newAllowlistedRuntimeCommand(context.Background(), "python"); err == nil {
		t.Fatalf("expected unsupported executable error")
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

func setupFakeRuntimeTools(t *testing.T) string {
	t.Helper()

	toolDir := t.TempDir()
	tools := []string{
		"npm",
		"pnpm",
		"yarn",
		"bun",
		"npx",
		"node",
		"vitest",
		"jest",
		"mocha",
		"ava",
		"deno",
		"make",
	}
	for _, tool := range tools {
		path := filepath.Join(toolDir, tool)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatalf("write fake runtime tool %q: %v", tool, err)
		}
	}
	return toolDir
}

func setupFakeRuntimeToolScript(t *testing.T, tool string, script string) string {
	t.Helper()

	toolDir := setupFakeRuntimeTools(t)
	path := filepath.Join(toolDir, tool)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake runtime tool script %q: %v", tool, err)
	}
	return toolDir
}
