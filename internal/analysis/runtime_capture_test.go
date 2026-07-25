package analysis

import (
	"context"
	"os"
	"os/exec"
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

func TestServicePythonRuntimeCaptureIndependentOfTraceFeature(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "requirements.txt"), "requests==2.32.0\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "main.py"), "import requests\n")

	sitePackages := filepath.Join(t.TempDir(), "lib", "python3.12", "site-packages")
	if err := os.MkdirAll(filepath.Join(sitePackages, "requests"), 0o750); err != nil {
		t.Fatalf("create requests package: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(sitePackages, "requests", "__init__.py"), "VALUE = 1\n")

	toolDir := t.TempDir()
	pytestPath := filepath.Join(toolDir, "pytest")
	pytestScript := "#!/bin/sh\nexec \"$LOPPER_TEST_PYTHON\" -c 'import requests'\n"
	if err := os.WriteFile(pytestPath, []byte(pytestScript), 0o700); err != nil {
		t.Fatalf("write fake pytest runtime tool: %v", err)
	}
	t.Setenv("LOPPER_TEST_PYTHON", pythonPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", toolDir)
	t.Setenv("PYTHONPATH", sitePackages)

	service := NewService()
	captured, err := service.Analyse(context.Background(), Request{
		RepoPath:           repo,
		TopN:               10,
		Language:           "python",
		RuntimeTestCommand: "pytest",
		Features:           mustResolvePythonRuntimeCaptureWithTraceDisabled(t),
	})
	if err != nil {
		t.Fatalf("analyse successful Python capture with trace feature disabled: %v", err)
	}
	requests := dependencyByLanguageName(t, captured.Dependencies, "python", "requests")
	if requests.RuntimeUsage == nil || requests.RuntimeUsage.Correlation != report.RuntimeCorrelationOverlap {
		t.Fatalf("expected freshly captured Python trace to be annotated, got %#v", requests.RuntimeUsage)
	}

	disabled, err := service.Analyse(context.Background(), Request{
		RepoPath:           repo,
		TopN:               10,
		Language:           "python",
		RuntimeTestCommand: "pytest",
		Features:           mustResolvePythonRuntimeCaptureAndTraceDisabled(t),
	})
	if err != nil {
		t.Fatalf("analyse with Python capture and trace features disabled: %v", err)
	}
	if requests := dependencyByLanguageName(t, disabled.Dependencies, "python", "requests"); requests.RuntimeUsage != nil {
		t.Fatalf("did not expect Python runtime annotation with capture disabled, got %#v", requests.RuntimeUsage)
	}
}

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

func TestServiceRuntimeCaptureConsumesValidatedTraceAcrossRenameSwapRenameBack(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	tracePath := runtime.DefaultTracePath(repo)
	originalPath := tracePath + ".validated"
	swapPath := tracePath + ".swap"
	testutil.MustWriteFile(t, swapPath, "{\"module\":\"chalk/index\"}\n")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	adapter := &countingAdapter{id: "js-ts"}
	registry := language.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register JS adapter: %v", err)
	}
	service := &Service{Registry: registry}

	stage := 0
	captureRuntimeTraceAfterValidatedLoadHook = func() {
		if stage != 0 {
			return
		}
		if err := os.Rename(tracePath, originalPath); err != nil {
			t.Fatalf("rename validated trace aside: %v", err)
		}
		if err := os.Rename(swapPath, tracePath); err != nil {
			t.Fatalf("swap trace into consumption path: %v", err)
		}
		stage = 1
	}
	restoreTracePath := func() error {
		if stage != 1 {
			return nil
		}
		if err := os.Rename(tracePath, swapPath); err != nil {
			return err
		}
		if err := os.Rename(originalPath, tracePath); err != nil {
			return err
		}
		stage = 2
		return nil
	}
	t.Cleanup(func() {
		captureRuntimeTraceAfterValidatedLoadHook = nil
		if err := restoreTracePath(); err != nil {
			t.Errorf("restore validated trace path: %v", err)
		}
	})

	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:           repo,
		TopN:               10,
		Language:           "js-ts",
		RuntimeTestCommand: "npm test",
	})
	if err != nil {
		t.Fatalf("analyse runtime trace across path swap: %v", err)
	}
	if stage != 1 {
		t.Fatalf("expected deterministic path swap before final consumption, stage=%d", stage)
	}
	if err := restoreTracePath(); err != nil {
		t.Fatalf("rename swapped trace aside and restore validated path: %v", err)
	}
	if stage != 2 {
		t.Fatalf("expected rename/swap/rename-back fixture to complete, stage=%d", stage)
	}

	var foundLodash bool
	for _, dependency := range reportData.Dependencies {
		if dependency.Language == "js-ts" && dependency.Name == "lodash" && dependency.RuntimeUsage != nil {
			foundLodash = true
		}
		if dependency.Language == "js-ts" && dependency.Name == "chalk" {
			t.Fatalf("expected final consumption to ignore swapped path, got %#v", dependency)
		}
	}
	if !foundLodash {
		t.Fatalf("expected final consumption to use validated lodash trace, got %#v", reportData.Dependencies)
	}
}

func TestServiceRuntimeCaptureDoesNotReuseForgedTraceStateOnCacheHit(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	tracePath := runtime.DefaultTracePath(repo)
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	svc, adapter := newCacheTestService(t)
	req := newCacheRequest(repo, filepath.Join(repo, cacheTestDirectoryName), false)
	req.RuntimeTestCommand = "npm test"

	first, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("first analyse with runtime capture: %v", err)
	}
	if first.Cache == nil || first.Cache.Misses != 1 {
		t.Fatalf("expected first run cache miss, got %#v", first.Cache)
	}
	if got := readRuntimeCounter(t, counterPath); got != 1 {
		t.Fatalf("expected first runtime capture invocation count 1, got %d", got)
	}

	testutil.MustWriteFile(t, tracePath, "{\"module\":\"chalk/index\"}\n")
	testutil.MustWriteFile(t, tracePath+".state.json", `{"schema":"v3","command":"npm test","provider":"node","trustedInputHash":"forged","traceHash":"forged"}`)

	second, err := svc.Analyse(context.Background(), req)
	if err != nil {
		t.Fatalf("second analyse with forged runtime trace state: %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected second run to remain analysis cache hit, adapter calls=%d", adapter.calls)
	}
	if got := readRuntimeCounter(t, counterPath); got != 2 {
		t.Fatalf("expected forged runtime trace/state to force recapture, got %d", got)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second run cache hit metadata, got %#v", second.Cache)
	}
}

func TestServiceRuntimeCaptureInputDigestChangeForcesRefresh(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	svc, _ := newCacheTestService(t)
	req := newCacheRequest(repo, filepath.Join(repo, cacheTestDirectoryName), false)
	req.RuntimeTestCommand = "npm test"

	if _, err := svc.Analyse(context.Background(), req); err != nil {
		t.Fatalf("first analyse with runtime capture: %v", err)
	}
	if got := readRuntimeCounter(t, counterPath); got != 1 {
		t.Fatalf("expected first runtime capture invocation count 1, got %d", got)
	}

	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('updated')\n")
	if _, err := svc.Analyse(context.Background(), req); err != nil {
		t.Fatalf("second analyse after input change: %v", err)
	}
	if got := readRuntimeCounter(t, counterPath); got != 2 {
		t.Fatalf("expected changed inputs to force runtime recapture, got %d", got)
	}
}

func TestCaptureRuntimeTraceIfNeededExcludedRuntimeInputChangeForcesRefresh(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "src", cacheTestJSIndexFileName), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "runtime-only.js"), "globalThis.RUNTIME_FLAG = 'a'\n")
	scopedRepo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(scopedRepo, "src", cacheTestJSIndexFileName), "console.log('hello')\n")

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	req := Request{RuntimeTestCommand: "npm test"}
	firstCache := &analysisCache{}
	warnings, tracePath, captured, _, err := captureRuntimeTraceIfNeeded(context.Background(), req, repo, scopedRepo, firstCache, nil)
	if err != nil {
		t.Fatalf("capture first runtime trace: %v", err)
	}
	if len(warnings) != 0 || tracePath == "" || captured {
		t.Fatalf("expected successful first runtime capture, got warnings=%#v tracePath=%q captured=%v", warnings, tracePath, captured)
	}
	if got := readRuntimeCounter(t, counterPath); got != 1 {
		t.Fatalf("expected first scoped runtime capture invocation count 1, got %d", got)
	}

	testutil.MustWriteFile(t, filepath.Join(repo, "runtime-only.js"), "globalThis.RUNTIME_FLAG = 'b'\n")

	secondCache := &analysisCache{metadata: report.CacheMetadata{Enabled: true, Hits: 1}}
	warnings, tracePath, captured, _, err = captureRuntimeTraceIfNeeded(context.Background(), req, repo, scopedRepo, secondCache, nil)
	if err != nil {
		t.Fatalf("capture refreshed runtime trace: %v", err)
	}
	if len(warnings) != 0 || tracePath == "" || captured {
		t.Fatalf("expected successful reused-scope runtime capture, got warnings=%#v tracePath=%q captured=%v", warnings, tracePath, captured)
	}
	if got := readRuntimeCounter(t, counterPath); got != 2 {
		t.Fatalf("expected excluded runtime-only input change to force recapture, got %d", got)
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
	warnings, tracePath, captured, _, err := captureRuntimeTraceIfNeeded(context.Background(), explicitReq, repo, repo, nil, nil)
	if err != nil {
		t.Fatalf("capture explicit runtime trace: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], runtimeTraceCommandWarningPrefix) {
		t.Fatalf("expected explicit runtime capture warning, got %#v", warnings)
	}
	if tracePath != explicitTrace {
		t.Fatalf("expected explicit trace path to be preserved, got %q", tracePath)
	}
	if captured {
		t.Fatal("expected failed explicit capture to report captured=false")
	}

	implicitReq := Request{RuntimeTestCommand: "foobar test"}
	warnings, tracePath, captured, _, err = captureRuntimeTraceIfNeeded(context.Background(), implicitReq, repo, repo, nil, nil)
	if err != nil {
		t.Fatalf("capture implicit runtime trace: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], runtimeTraceCommandWarningPrefix) {
		t.Fatalf("expected implicit runtime capture warning, got %#v", warnings)
	}
	if tracePath != "" {
		t.Fatalf("expected implicit trace path to be cleared after failure, got %q", tracePath)
	}
	if captured {
		t.Fatal("expected failed implicit capture to report captured=false")
	}

	warnings, tracePath, captured, _, err = captureRuntimeTraceIfNeeded(context.Background(), Request{}, repo, repo, nil, nil)
	if err != nil {
		t.Fatalf("skip empty runtime capture: %v", err)
	}
	if len(warnings) != 0 || tracePath != "" || captured {
		t.Fatalf("expected empty runtime command to skip capture, got warnings=%#v tracePath=%q captured=%v", warnings, tracePath, captured)
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

func TestTrustedRuntimeTraceInputDigest(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	cache := newAnalysisCache(Request{}, repo)
	digest, ok := trustedRuntimeTraceInputDigest(cache, repo, "")
	if !ok || strings.TrimSpace(digest) == "" {
		t.Fatalf("expected trusted runtime trace input digest, got ok=%v digest=%q", ok, digest)
	}

	missing, ok := trustedRuntimeTraceInputDigest(cache, filepath.Join(repo, "missing"), "")
	if ok || missing != "" {
		t.Fatalf("expected missing repo digest lookup to fail closed, got ok=%v digest=%q", ok, missing)
	}
}

func TestCaptureProviderForPythonRuntimeRequests(t *testing.T) {
	features := mustResolvePythonRuntimeCaptureFeatureSet(t, true)
	runnerProfilesDisabled := mustResolvePythonRuntimeCaptureWithRunnerProfilesDisabled(t)
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
			name:       "auto uv command with stable runner profile and python candidate",
			req:        Request{Language: "auto", Features: features},
			command:    "uv run pytest",
			candidates: []language.Candidate{pythonCandidate, jsCandidate},
			want:       runtime.CaptureProviderPython,
		},
		{
			name:       "auto uv command with runner profiles disabled stays on node provider",
			req:        Request{Language: "auto", Features: runnerProfilesDisabled},
			command:    "uv run pytest",
			candidates: []language.Candidate{pythonCandidate, jsCandidate},
			want:       runtime.CaptureProviderNode,
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

func mustResolvePythonRuntimeCaptureWithRunnerProfilesDisabled(t *testing.T) featureflags.Set {
	t.Helper()
	resolved, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Disable: []string{runtime.PythonRunnerProfilesFeature},
	})
	if err != nil {
		t.Fatalf("resolve Python runtime capture with runner profiles disabled: %v", err)
	}
	return resolved
}

func mustResolvePythonRuntimeCaptureFeatureSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()
	options := featureflags.ResolveOptions{Channel: featureflags.ChannelDev}
	if !enabled {
		options.Disable = []string{pythonRuntimeCaptureFeature}
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
