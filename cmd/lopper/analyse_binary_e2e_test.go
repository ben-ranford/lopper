package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/version"
)

const (
	binaryBuildTimeout = 2 * time.Minute
	binaryRunTimeout   = 30 * time.Second
)

func TestAnalyseBinaryHermeticE2E(t *testing.T) {
	moduleRoot := mustModuleRoot(t)
	fixtureRepo := filepath.Join(moduleRoot, "testdata", "js", "esm")
	workspaceRoot := t.TempDir()
	repoPath := filepath.Join(workspaceRoot, "repo")
	copyDir(t, fixtureRepo, repoPath)

	binDir := filepath.Join(workspaceRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	binaryPath := filepath.Join(binDir, "lopper")
	buildBinary(t, moduleRoot, binaryPath)

	stdout, stderr, exitCode := runHermeticAnalyse(t, workspaceRoot, binaryPath, repoPath, "")
	if exitCode != 0 {
		t.Fatalf("lopper analyse failed with exit code %d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr output, got %q", stderr)
	}

	got := decodeJSON(t, []byte(stdout))
	assertGeneratedAtUTC(t, got)
	normalizeReport(t, got)

	goldenPath := filepath.Join(moduleRoot, "testdata", "cli", expectedGoldenReportName())
	goldenReport, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden report: %v", err)
	}
	want := decodeJSON(t, goldenReport)
	if !reflect.DeepEqual(got, want) {
		gotJSON, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatalf("marshal actual report: %v", err)
		}
		t.Fatalf("unexpected report output\nwant:\n%s\n\ngot:\n%s", string(goldenReport), string(gotJSON))
	}
}

func TestAnalyseBinaryRuntimeTraceWorkBoundsE2E(t *testing.T) {
	moduleRoot := mustModuleRoot(t)
	fixtureRepo := filepath.Join(moduleRoot, "testdata", "js", "esm")
	workspaceRoot := t.TempDir()
	repoPath := filepath.Join(workspaceRoot, "repo")
	copyDir(t, fixtureRepo, repoPath)

	binDir := filepath.Join(workspaceRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	binaryPath := filepath.Join(binDir, "lopper")
	buildBinary(t, moduleRoot, binaryPath)

	tracePath := filepath.Join(workspaceRoot, "trace.ndjson")
	writeWorkAmplifyingRuntimeTrace(t, tracePath)

	stdout, stderr, exitCode := runHermeticAnalyse(t, workspaceRoot, binaryPath, repoPath, tracePath)
	if exitCode != 1 {
		t.Fatalf("expected bounded runtime trace failure exit code 1, got %d stderr=%q stdout=%q", exitCode, stderr, stdout)
	}
	if stdout != "" {
		t.Fatalf("expected fail-closed runtime trace rejection with no stdout, got %q", stdout)
	}
	const wantStderr = "runtime trace exceeds maximum line count of 600000\n"
	if stderr != wantStderr {
		t.Fatalf("stderr = %q, want %q", stderr, wantStderr)
	}
}

func buildBinary(t *testing.T, moduleRoot string, binaryPath string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), binaryBuildTimeout)
	defer cancel()

	args := []string{
		"build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags",
		"-X github.com/ben-ranford/lopper/internal/version.buildChannel=" + version.Current().BuildChannel,
		"-o",
		binaryPath,
		"./cmd/lopper",
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = moduleRoot

	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("go build timed out after %s", binaryBuildTimeout)
	}
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, string(output))
	}
}

func runHermeticAnalyse(t *testing.T, workspaceRoot string, binaryPath string, repoPath string, runtimeTracePath string) (string, string, int) {
	t.Helper()

	homePath := filepath.Join(workspaceRoot, "home")
	xdgConfigPath := filepath.Join(workspaceRoot, "xdg-config")
	xdgCachePath := filepath.Join(workspaceRoot, "xdg-cache")
	for _, dir := range []string{homePath, xdgConfigPath, xdgCachePath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), binaryRunTimeout)
	defer cancel()

	args := []string{
		"analyse", "lodash",
		"--repo", repoPath,
		"--format", "json",
		"--cache=false",
	}
	if runtimeTracePath != "" {
		args = append(args, "--runtime-trace", runtimeTracePath)
	}
	cmd := exec.CommandContext(ctx, binaryPath, args...)
	cmd.Dir = workspaceRoot
	cmd.Env = []string{
		"HOME=" + homePath,
		"TMPDIR=" + workspaceRoot,
		"TMP=" + workspaceRoot,
		"TEMP=" + workspaceRoot,
		"TZ=UTC",
		"LANG=C",
		"LC_ALL=C",
		"TERM=dumb",
		"NO_COLOR=1",
		"CLICOLOR=0",
		"CLICOLOR_FORCE=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(homePath, ".gitconfig"),
		"GIT_TERMINAL_PROMPT=0",
		"XDG_CONFIG_HOME=" + xdgConfigPath,
		"XDG_CACHE_HOME=" + xdgCachePath,
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("lopper analyse timed out after %s", binaryRunTimeout)
	}
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("lopper analyse failed without exit code: %v stderr=%q", err, stderr.String())
	}
	return stdout.String(), stderr.String(), exitErr.ExitCode()
}

func mustModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve module root: %v", err)
	}
	return root
}

func copyDir(t *testing.T, src string, dst string) {
	t.Helper()
	if err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(dst, relPath)
		if entry.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(targetPath, content, 0o644)
	}); err != nil {
		t.Fatalf("copy fixture repo: %v", err)
	}
}

func decodeJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode json: %v\n%s", err, string(data))
	}
	return payload
}

func assertGeneratedAtUTC(t *testing.T, payload map[string]any) {
	t.Helper()
	value, ok := payload["generatedAt"].(string)
	if !ok || value == "" {
		t.Fatalf("expected generatedAt string, got %#v", payload["generatedAt"])
	}
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse generatedAt %q: %v", value, err)
	}
	if timestamp.Location() != time.UTC {
		t.Fatalf("expected generatedAt in UTC, got %q", value)
	}
}

func normalizeReport(t *testing.T, payload map[string]any) {
	t.Helper()
	payload["generatedAt"] = "<generatedAt>"
	payload["repoPath"] = "<repo>"

	cacheValue, ok := payload["cache"].(map[string]any)
	if !ok {
		t.Fatalf("expected cache object, got %#v", payload["cache"])
	}
	cacheValue["path"] = "<repo>/.lopper-cache"
}

func expectedGoldenReportName() string {
	if version.Current().BuildChannel == "rolling" {
		return "analyse_binary_e2e.rolling.golden.json"
	}
	return "analyse_binary_e2e.golden.json"
}

func writeWorkAmplifyingRuntimeTrace(t *testing.T, path string) {
	t.Helper()

	var content strings.Builder
	content.Grow(600000 + 192)
	content.WriteString(strings.Repeat("\n", 600000))
	content.WriteString("{\"kind\":\"resolve\",\"module\":\"lodash/map\",\"resolved\":\"file:///repo/node_modules/lodash/map.js\",\"parent\":\"file:///repo/src/index.js\",\"entrypoint\":\"file:///repo/src/main.js\"}\n")
	if err := os.WriteFile(path, []byte(content.String()), 0o600); err != nil {
		t.Fatalf("write runtime trace: %v", err)
	}
}
