package scripts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRuntimeCaptureRefreshesTamperedTraceOnCacheHit(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "index.js"), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	tracePath := runtime.DefaultTracePath(repo)
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupRuntimeCaptureRegressionTool(t))

	service := analysis.NewService()
	req := analysis.Request{
		RepoPath:           repo,
		Language:           "js-ts",
		TopN:               10,
		RuntimeTestCommand: "npm test",
		Cache: &analysis.CacheOptions{
			Enabled: true,
			Path:    filepath.Join(repo, "analysis-cache"),
		},
	}

	first, err := service.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse with runtime capture: %v", err)
	}
	if first.Cache == nil || first.Cache.Misses != 1 {
		t.Fatalf("expected first run cache miss, got %#v", first.Cache)
	}
	if got := readRuntimeCaptureRegressionCounter(t, counterPath); got != 1 {
		t.Fatalf("expected first runtime capture invocation count 1, got %d", got)
	}

	testutil.MustWriteFile(t, tracePath, "{\"module\":\"chalk/index\"}\n")

	second, err := service.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse after tampering runtime trace: %v", err)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second run cache hit metadata, got %#v", second.Cache)
	}
	if got := readRuntimeCaptureRegressionCounter(t, counterPath); got != 2 {
		t.Fatalf("expected tampered runtime trace to force recapture, got %d", got)
	}

	var lodashRuntime *report.RuntimeUsage
	for _, dependency := range second.Dependencies {
		if dependency.Language == "js-ts" && dependency.Name == "chalk" {
			t.Fatalf("expected tampered trace to be ignored after refresh, got %#v", dependency)
		}
		if dependency.Language == "js-ts" && dependency.Name == "lodash" {
			lodashRuntime = dependency.RuntimeUsage
		}
	}
	if lodashRuntime == nil || lodashRuntime.LoadCount != 1 || lodashRuntime.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected refreshed lodash runtime annotation, got %#v from %#v", lodashRuntime, second.Dependencies)
	}
	var foundLodashMap bool
	for _, module := range lodashRuntime.Modules {
		if module.Module == "lodash/map" && module.Count == 1 {
			foundLodashMap = true
		}
	}
	if !foundLodashMap {
		t.Fatalf("expected refreshed lodash/map runtime evidence, got %#v", lodashRuntime.Modules)
	}
}

func TestRuntimeCaptureRejectsImplicitDefaultArtifactsSymlinkWithoutExternalMutation(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink parent regression is Unix-specific")
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "index.js"), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")

	outside := t.TempDir()
	tracePath := filepath.Join(outside, "lopper-runtime.ndjson")
	sentinelPath := filepath.Join(outside, "sentinel.txt")
	testutil.MustWriteFile(t, tracePath, "outside-trace\n")
	testutil.MustWriteFile(t, sentinelPath, "keep\n")
	if err := os.Symlink(outside, filepath.Join(repo, ".artifacts")); err != nil {
		t.Fatalf("symlink implicit artifacts dir: %v", err)
	}

	traceBefore, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read outside trace before analyse: %v", err)
	}
	sentinelBefore, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read outside sentinel before analyse: %v", err)
	}

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupRuntimeCaptureRegressionTool(t))

	service := analysis.NewService()
	_, err = service.Analyse(context.Background(), analysis.Request{
		RepoPath:           repo,
		Language:           "js-ts",
		TopN:               10,
		RuntimeTestCommand: "npm test",
	})
	if err == nil || !strings.Contains(err.Error(), "must stay within repo") {
		t.Fatalf("expected symlinked implicit artifacts path rejection, got %v", err)
	}
	if _, statErr := os.Stat(counterPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected runtime command not to start, stat err=%v", statErr)
	}
	traceAfter, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read outside trace after analyse: %v", err)
	}
	if string(traceAfter) != string(traceBefore) {
		t.Fatalf("expected outside trace to remain unchanged, before=%q after=%q", traceBefore, traceAfter)
	}
	sentinelAfter, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read outside sentinel after analyse: %v", err)
	}
	if string(sentinelAfter) != string(sentinelBefore) {
		t.Fatalf("expected outside sentinel to remain unchanged, before=%q after=%q", sentinelBefore, sentinelAfter)
	}
	if _, statErr := os.Stat(runtime.DefaultTracePath(repo)); statErr != nil {
		t.Fatalf("expected symlinked implicit default trace lookup to keep external file reachable, stat err=%v", statErr)
	}
}

func TestRuntimeCaptureRejectsExplicitExternalTracePathWithoutExternalMutation(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "index.js"), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")

	outside := t.TempDir()
	tracePath := filepath.Join(outside, "external-runtime.ndjson")
	sentinelPath := filepath.Join(outside, "sentinel.txt")
	testutil.MustWriteFile(t, tracePath, "outside-trace\n")
	testutil.MustWriteFile(t, sentinelPath, "keep\n")

	traceBefore, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read outside trace before analyse: %v", err)
	}
	sentinelBefore, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read outside sentinel before analyse: %v", err)
	}

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupRuntimeCaptureRegressionTool(t))

	service := analysis.NewService()
	_, err = service.Analyse(context.Background(), analysis.Request{
		RepoPath:                 repo,
		Language:                 "js-ts",
		TopN:                     10,
		RuntimeTestCommand:       "npm test",
		RuntimeTracePath:         tracePath,
		RuntimeTracePathExplicit: true,
	})
	if err == nil || !strings.Contains(err.Error(), "must stay within repo") {
		t.Fatalf("expected explicit external trace path rejection, got %v", err)
	}
	if _, statErr := os.Stat(counterPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected runtime command not to start, stat err=%v", statErr)
	}
	traceAfter, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read outside trace after analyse: %v", err)
	}
	if string(traceAfter) != string(traceBefore) {
		t.Fatalf("expected outside trace to remain unchanged, before=%q after=%q", traceBefore, traceAfter)
	}
	sentinelAfter, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("read outside sentinel after analyse: %v", err)
	}
	if string(sentinelAfter) != string(sentinelBefore) {
		t.Fatalf("expected outside sentinel to remain unchanged, before=%q after=%q", sentinelBefore, sentinelAfter)
	}
}

func setupRuntimeCaptureRegressionTool(t *testing.T) string {
	t.Helper()

	toolDir := t.TempDir()
	npmPath := filepath.Join(toolDir, "npm")
	script := "#!/bin/sh\ncount=$(cat \"$LOPPER_RUNTIME_COUNTER\" 2>/dev/null || echo 0)\ncount=$((count + 1))\nprintf '%s' \"$count\" > \"$LOPPER_RUNTIME_COUNTER\"\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"
	testutil.MustWriteFileMode(t, npmPath, script, 0o700)
	return toolDir
}

func readRuntimeCaptureRegressionCounter(t *testing.T, path string) int {
	t.Helper()
	return testutil.MustReadTrimmedIntFile(t, path)
}
