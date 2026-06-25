package analysis

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/runtime"
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

func TestCaptureRuntimeTraceIfNeededWarningAndReuseBranches(t *testing.T) {
	repo := t.TempDir()
	explicitTrace := filepath.Join(repo, "custom-trace.ndjson")

	explicitReq := Request{
		RuntimeTestCommand:       "foobar test",
		RuntimeTracePath:         explicitTrace,
		RuntimeTracePathExplicit: true,
	}
	warnings, tracePath := captureRuntimeTraceIfNeeded(context.Background(), explicitReq, repo, nil, nil)
	if len(warnings) != 1 || !strings.Contains(warnings[0], runtimeTraceCommandWarningPrefix) {
		t.Fatalf("expected explicit runtime capture warning, got %#v", warnings)
	}
	if tracePath != explicitTrace {
		t.Fatalf("expected explicit trace path to be preserved, got %q", tracePath)
	}

	implicitReq := Request{RuntimeTestCommand: "foobar test"}
	warnings, tracePath = captureRuntimeTraceIfNeeded(context.Background(), implicitReq, repo, nil, nil)
	if len(warnings) != 1 || !strings.Contains(warnings[0], runtimeTraceCommandWarningPrefix) {
		t.Fatalf("expected implicit runtime capture warning, got %#v", warnings)
	}
	if tracePath != "" {
		t.Fatalf("expected implicit trace path to be cleared after failure, got %q", tracePath)
	}

	if warnings, tracePath = captureRuntimeTraceIfNeeded(context.Background(), Request{}, repo, nil, nil); len(warnings) != 0 || tracePath != "" {
		t.Fatalf("expected empty runtime command to skip capture, got warnings=%#v tracePath=%q", warnings, tracePath)
	}

	reuseCases := []struct {
		name  string
		cache *analysisCache
		want  bool
	}{
		{name: "nil cache", cache: nil, want: false},
		{name: "disabled cache", cache: &analysisCache{metadata: report.CacheMetadata{}}, want: false},
		{name: "cache miss", cache: &analysisCache{metadata: report.CacheMetadata{Enabled: true, Misses: 1}}, want: false},
		{name: "cache hit", cache: &analysisCache{metadata: report.CacheMetadata{Enabled: true, Hits: 1}}, want: true},
	}
	for _, testCase := range reuseCases {
		if got := shouldReuseRuntimeTrace(testCase.cache); got != testCase.want {
			t.Fatalf("%s: expected shouldReuseRuntimeTrace=%v, got %v", testCase.name, testCase.want, got)
		}
	}
}

func TestCaptureProviderForPythonRuntimeRequests(t *testing.T) {
	features := mustResolvePythonRuntimeCaptureFeatureSet(t, true)
	pythonCandidate := language.Candidate{Adapter: &stubAdapter{id: "python"}}
	jsCandidate := language.Candidate{Adapter: &stubAdapter{id: "js-ts"}}

	testCases := []struct {
		name       string
		req        Request
		command    string
		candidates []language.Candidate
		want       runtime.CaptureProvider
	}{
		{
			name:    "explicit python language",
			req:     Request{Language: "python", Features: features},
			command: "make test",
			want:    runtime.CaptureProviderPython,
		},
		{
			name:       "auto python command with python candidate",
			req:        Request{Language: "auto", Features: features},
			command:    "pytest",
			candidates: []language.Candidate{pythonCandidate},
			want:       runtime.CaptureProviderPython,
		},
		{
			name:       "python only candidate with make command",
			req:        Request{Features: features},
			command:    "make test",
			candidates: []language.Candidate{pythonCandidate},
			want:       runtime.CaptureProviderPython,
		},
		{
			name:       "mixed repo keeps js command on node provider",
			req:        Request{Language: "all", Features: features},
			command:    "npm test",
			candidates: []language.Candidate{pythonCandidate, jsCandidate},
			want:       runtime.CaptureProviderNode,
		},
		{
			name:       "disabled capture flag keeps node provider",
			req:        Request{Language: "python", Features: mustResolvePythonRuntimeCaptureFeatureSet(t, false)},
			command:    "pytest",
			candidates: []language.Candidate{pythonCandidate},
			want:       runtime.CaptureProviderNode,
		},
	}

	for _, tc := range testCases {
		if got := captureProviderForRequest(tc.req, tc.command, tc.candidates); got != tc.want {
			t.Fatalf("%s: expected provider %q, got %q", tc.name, tc.want, got)
		}
	}
}

func mustResolvePythonRuntimeCaptureFeatureSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()
	options := featureflags.ResolveOptions{Channel: featureflags.ChannelDev}
	if enabled {
		options.Enable = []string{pythonRuntimeCapturePreviewFeature}
	} else {
		options.Disable = []string{pythonRuntimeCapturePreviewFeature}
	}
	resolved, err := featureflags.DefaultRegistry().Resolve(options)
	if err != nil {
		t.Fatalf("resolve Python runtime capture feature set: %v", err)
	}
	return resolved
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
