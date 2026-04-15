package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestServiceRuntimeCaptureReusesTraceOnCacheHit(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	svc, adapter := newCacheTestService(t)
	req := newCacheRequest(repo, filepath.Join(repo, cacheTestDirectoryName), false)
	req.RuntimeTestCommand = "npm test"

	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse with runtime capture: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected first run to call adapter once, got %d", adapter.calls)
	}
	if got := readRuntimeCounter(t, counterPath); got != 1 {
		t.Fatalf("expected first runtime capture invocation count 1, got %d", got)
	}
	if first.Cache == nil || first.Cache.Misses != 1 {
		t.Fatalf("expected first run cache miss, got %#v", first.Cache)
	}

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse with runtime capture: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected second run to be cache hit, adapter calls=%d", adapter.calls)
	}
	if got := readRuntimeCounter(t, counterPath); got != 1 {
		t.Fatalf("expected runtime capture reuse on cache hit, got %d", got)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second run cache hit metadata, got %#v", second.Cache)
	}
}

func setupFakeAnalysisRuntimeTool(t *testing.T) string {
	t.Helper()

	toolDir := t.TempDir()
	npmPath := filepath.Join(toolDir, "npm")
	script := "#!/bin/sh\ncount=$(cat \"$LOPPER_RUNTIME_COUNTER\" 2>/dev/null || echo 0)\ncount=$((count + 1))\nprintf '%s' \"$count\" > \"$LOPPER_RUNTIME_COUNTER\"\nprintf '{\"module\":\"lodash/map\"}\\n' > \"$LOPPER_RUNTIME_TRACE\"\n"
	if err := os.WriteFile(npmPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake npm runtime tool: %v", err)
	}
	return toolDir
}

func readRuntimeCounter(t *testing.T, path string) int {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read runtime counter: %v", err)
	}
	value, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		t.Fatalf("parse runtime counter: %v", err)
	}
	return value
}
