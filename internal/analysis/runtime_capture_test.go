package analysis

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	reportmodel "github.com/ben-ranford/lopper/internal/report/model"
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
	installAnalysisRuntimeTool(t, toolDir, "pytest", "python-import-requests")
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

func TestServiceRuntimeCaptureRerunsOnAnalysisCacheHit(t *testing.T) {
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
	if got := readRuntimeCounter(t, counterPath); got != 2 {
		t.Fatalf("expected runtime capture to rerun on analysis cache hit, got %d", got)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second run cache hit metadata, got %#v", second.Cache)
	}
}

func TestServiceRuntimeCaptureReplacesTamperedTraceOnAnalysisCacheHit(t *testing.T) {
	fixture := newRuntimeCaptureCacheFixture(t)

	first, err := fixture.service.Analyse(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("first analyse with runtime capture: %v", err)
	}
	if first.Cache == nil || first.Cache.Misses != 1 {
		t.Fatalf("expected first run cache miss, got %#v", first.Cache)
	}
	assertRuntimeCounter(t, fixture.counterPath, 1, "expected first runtime capture invocation count 1")

	testutil.MustWriteFile(t, fixture.tracePath, "{\"module\":\"chalk/index\"}\n")

	second, err := fixture.service.Analyse(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("second analyse after tampering runtime trace: %v", err)
	}
	assertAdapterCallCount(t, fixture.adapter.calls, 1, "expected second run to remain analysis cache hit")
	assertRuntimeCounter(t, fixture.counterPath, 2, "expected tampered runtime trace to force recapture")
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second run cache hit metadata, got %#v", second.Cache)
	}
	missingRuntimeUsage := func(dependency reportmodel.DependencyReport) bool {
		return dependency.RuntimeUsage == nil
	}
	assertDependencyAbsent(t, second.Dependencies, "js-ts", "chalk", "expected tampered trace to be ignored after refresh")
	assertDependency(t, second.Dependencies, "js-ts", "dep", missingRuntimeUsage, "expected refreshed runtime trace to discard tampered runtime-only rows")
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

	stage, restoreTracePath := installRuntimeTraceSwapHook(t, tracePath, swapPath, originalPath, "rename validated trace aside", "swap trace into consumption path")
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
	if *stage != 1 {
		t.Fatalf("expected deterministic path swap before final consumption, stage=%d", *stage)
	}
	if err := restoreTracePath(); err != nil {
		t.Fatalf("rename swapped trace aside and restore validated path: %v", err)
	}
	if *stage != 2 {
		t.Fatalf("expected rename/swap/rename-back fixture to complete, stage=%d", *stage)
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

func TestCaptureRuntimeTraceIfNeededWarningAndNoCommandBranches(t *testing.T) {
	repo := t.TempDir()
	explicitTrace := filepath.Join(repo, "custom-trace.ndjson")

	explicitReq := Request{
		RuntimeTestCommand:       "foobar test",
		RuntimeTracePath:         explicitTrace,
		RuntimeTracePathExplicit: true,
	}
	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), explicitReq, repo, nil)
	if err != nil {
		t.Fatalf("capture explicit runtime trace: %v", err)
	}
	if len(outcome.warnings) != 2 || !strings.Contains(outcome.warnings[0], runtimeTraceCommandWarningPrefix) || outcome.warnings[1] != runtimeTraceMissingWarning {
		t.Fatalf("expected explicit runtime capture warning, got %#v", outcome.warnings)
	}
	wantExplicitTrace := filepath.Join(resolvedTestRepoPath(t, repo), filepath.Base(explicitTrace))
	if outcome.tracePath != wantExplicitTrace {
		t.Fatalf("expected canonical explicit trace path %q, got %q", wantExplicitTrace, outcome.tracePath)
	}
	if !outcome.captureAttempted || outcome.pythonCaptured || outcome.trace != nil {
		t.Fatalf("expected handled failed explicit capture, got %#v", outcome)
	}

	implicitReq := Request{RuntimeTestCommand: "foobar test"}
	outcome, err = captureRuntimeTraceIfNeeded(context.Background(), implicitReq, repo, nil)
	if err != nil {
		t.Fatalf("capture implicit runtime trace: %v", err)
	}
	if len(outcome.warnings) != 1 || !strings.Contains(outcome.warnings[0], runtimeTraceCommandWarningPrefix) {
		t.Fatalf("expected implicit runtime capture warning, got %#v", outcome.warnings)
	}
	wantImplicitTrace := runtime.DefaultTracePath(resolvedTestRepoPath(t, repo))
	if outcome.tracePath != wantImplicitTrace {
		t.Fatalf("expected canonical implicit trace path %q, got %q", wantImplicitTrace, outcome.tracePath)
	}
	if !outcome.captureAttempted || outcome.pythonCaptured || outcome.trace != nil {
		t.Fatalf("expected handled failed implicit capture, got %#v", outcome)
	}

	outcome, err = captureRuntimeTraceIfNeeded(context.Background(), Request{}, repo, nil)
	if err != nil {
		t.Fatalf("skip empty runtime capture: %v", err)
	}
	if len(outcome.warnings) != 0 || outcome.tracePath != "" || outcome.captureAttempted || outcome.pythonCaptured || outcome.trace != nil {
		t.Fatalf("expected empty runtime command to skip capture, got %#v", outcome)
	}
}

func TestMissingExplicitRuntimeFallbackRemainsHandledDuringFinalization(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", "runtime.ndjson")
	req := Request{
		Language:                 "js-ts",
		RuntimeTestCommand:       "foobar test",
		RuntimeTracePath:         tracePath,
		RuntimeTracePathExplicit: true,
	}

	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), req, repo, nil)
	if err != nil {
		t.Fatalf("capture missing explicit runtime fallback: %v", err)
	}
	wantTracePath := filepath.Join(resolvedTestRepoPath(t, repo), ".artifacts", "runtime.ndjson")
	if outcome.tracePath != wantTracePath {
		t.Fatalf("expected canonical missing explicit fallback path %q, got %q", wantTracePath, outcome.tracePath)
	}
	if !outcome.captureAttempted || outcome.pythonCaptured || outcome.trace != nil {
		t.Fatalf("expected handled capture attempt without validated runtime data, got %#v", outcome)
	}

	req.RuntimeTracePath = outcome.tracePath
	t.Run("missing warning remains single", func(t *testing.T) {
		reportData, err := finalizeReport(req, repo, repo, nil, outcome.trace, outcome.captureAttempted || outcome.traceFinalized, report.Report{
			Warnings: append([]string(nil), outcome.warnings...),
		})
		if err != nil {
			t.Fatalf("finalize handled missing runtime fallback: %v", err)
		}
		assertSingleRuntimeTraceMissingWarning(t, reportData.Warnings)
	})

	t.Run("file appearing later is ignored", func(t *testing.T) {
		testutil.MustWriteFile(t, tracePath, "{\"module\":\"lodash/map\"}\n")
		reportData, err := finalizeReport(req, repo, repo, nil, outcome.trace, outcome.captureAttempted || outcome.traceFinalized, report.Report{
			Warnings: append([]string(nil), outcome.warnings...),
		})
		if err != nil {
			t.Fatalf("finalize after runtime trace appears: %v", err)
		}
		assertSingleRuntimeTraceMissingWarning(t, reportData.Warnings)
		if len(reportData.Dependencies) != 0 {
			t.Fatalf("expected late unvalidated runtime trace to be ignored, got %#v", reportData.Dependencies)
		}
	})
}

func TestCaptureRuntimeTraceIfNeededRecapturesOverMalformedExplicitStaleTrace(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", "runtime.ndjson")
	canonicalTracePath := filepath.Join(resolvedTestRepoPath(t, repo), ".artifacts", "runtime.ndjson")
	if err := os.MkdirAll(filepath.Dir(canonicalTracePath), 0o750); err != nil {
		t.Fatalf("mkdir explicit trace dir: %v", err)
	}
	testutil.MustWriteFile(t, canonicalTracePath, "{not-json}\n")

	t.Setenv("LOPPER_RUNTIME_COUNTER", filepath.Join(repo, "runtime-counter.txt"))
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	req := Request{
		Language:                 "js-ts",
		RuntimeTestCommand:       "npm test",
		RuntimeTracePath:         tracePath,
		RuntimeTracePathExplicit: true,
	}
	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), req, repo, nil)
	if err != nil {
		t.Fatalf("recapture over malformed explicit runtime trace: %v", err)
	}
	wantTracePath := filepath.Join(resolvedTestRepoPath(t, repo), ".artifacts", "runtime.ndjson")
	if outcome.tracePath != wantTracePath {
		t.Fatalf("expected canonical explicit trace path %q, got %q", wantTracePath, outcome.tracePath)
	}
	if !outcome.captureAttempted || outcome.trace == nil {
		t.Fatalf("expected malformed explicit trace to be replaced by recapture, got %#v", outcome)
	}
	if len(outcome.warnings) != 0 {
		t.Fatalf("expected successful recapture without warnings, got %#v", outcome.warnings)
	}
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "lodash", 1, "expected recaptured validated trace to load lodash")
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "chalk", 0, "did not expect malformed stale trace rows to survive recapture")
	assertRuntimeCounter(t, filepath.Join(repo, "runtime-counter.txt"), 1, "expected runtime capture command to execute once")
}

func TestCaptureRuntimeTraceIfNeededUsesExplicitTraceAfterCaptureFailure(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", "runtime.ndjson")
	canonicalTracePath := filepath.Join(resolvedTestRepoPath(t, repo), ".artifacts", "runtime.ndjson")
	before := "{\"module\":\"lodash/map\"}\n{\"module\":\"chalk/index\"}\n"
	testutil.MustWriteFile(t, canonicalTracePath, before)
	if _, err := runtime.LoadValidatedTrace(canonicalTracePath); err != nil {
		t.Fatalf("precondition explicit runtime trace loads: %v", err)
	}

	req := Request{
		Language:                 "js-ts",
		RuntimeTestCommand:       "foobar test",
		RuntimeTracePath:         tracePath,
		RuntimeTracePathExplicit: true,
	}
	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), req, repo, nil)
	if err != nil {
		t.Fatalf("use explicit runtime trace after capture failure: %v", err)
	}
	if !outcome.captureAttempted || outcome.trace == nil {
		t.Fatalf("expected capture failure to fall back to explicit runtime trace, got %#v", outcome)
	}
	if len(outcome.warnings) != 1 || !strings.Contains(outcome.warnings[0], runtimeTraceCommandWarningPrefix) {
		t.Fatalf("expected single capture warning with explicit fallback trace, got %#v", outcome.warnings)
	}
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "lodash", 1, "expected explicit fallback trace to load lodash")
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "chalk", 1, "expected explicit fallback trace to load chalk")
	after, err := os.ReadFile(canonicalTracePath)
	if err != nil {
		t.Fatalf("read explicit runtime trace after capture failure: %v", err)
	}
	if string(after) != before {
		t.Fatalf("expected explicit runtime trace to remain unchanged after capture failure, before=%q after=%q", before, after)
	}
}

func TestCaptureRuntimeTraceIfNeededUsesPreloadedExplicitSnapshotAfterCaptureFailureAcrossPathSwap(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", "runtime.ndjson")
	canonicalTracePath := filepath.Join(resolvedTestRepoPath(t, repo), ".artifacts", "runtime.ndjson")
	testutil.MustWriteFile(t, canonicalTracePath, "{\"module\":\"lodash/map\"}\n")
	swapPath := canonicalTracePath + ".swap"
	testutil.MustWriteFile(t, swapPath, "{\"module\":\"chalk/index\"}\n")

	captureRuntimeTraceAfterExplicitFallbackPreloadHook = func() {
		if err := os.Rename(canonicalTracePath, canonicalTracePath+".validated"); err != nil {
			t.Fatalf("rename validated explicit runtime trace aside: %v", err)
		}
		if err := os.Rename(swapPath, canonicalTracePath); err != nil {
			t.Fatalf("swap explicit runtime trace into capture path: %v", err)
		}
	}
	t.Cleanup(func() {
		captureRuntimeTraceAfterExplicitFallbackPreloadHook = nil
		if _, err := os.Stat(canonicalTracePath + ".validated"); err == nil {
			if renameErr := os.Rename(canonicalTracePath, swapPath); renameErr != nil {
				t.Fatalf("restore swapped explicit runtime trace aside: %v", renameErr)
			}
			if renameErr := os.Rename(canonicalTracePath+".validated", canonicalTracePath); renameErr != nil {
				t.Fatalf("restore validated explicit runtime trace path: %v", renameErr)
			}
		}
	})

	req := Request{
		Language:                 "js-ts",
		RuntimeTestCommand:       "foobar test",
		RuntimeTracePath:         tracePath,
		RuntimeTracePathExplicit: true,
	}
	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), req, repo, nil)
	if err != nil {
		t.Fatalf("use preloaded explicit runtime trace after capture failure across path swap: %v", err)
	}
	if !outcome.captureAttempted || outcome.trace == nil {
		t.Fatalf("expected capture failure to fall back to preloaded explicit runtime trace, got %#v", outcome)
	}
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "lodash", 1, "expected preloaded explicit snapshot to preserve original lodash trace")
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "chalk", 0, "expected preloaded explicit snapshot to ignore swapped trace path")
}

func TestHandleRuntimeTraceCaptureErrorSurfacesInvalidExplicitTraceFallback(t *testing.T) {
	repo := t.TempDir()
	canonicalTracePath := filepath.Join(resolvedTestRepoPath(t, repo), ".artifacts", "runtime.ndjson")
	if err := os.MkdirAll(canonicalTracePath, 0o750); err != nil {
		t.Fatalf("mkdir invalid explicit trace path: %v", err)
	}

	captureOutcome := runtimeTraceCaptureOutcome{captureAttempted: true, traceFinalized: true}
	captureErr := errors.New(`unsupported runtime test executable "foobar"; use a direct command like 'npm test'`)
	outcome, err := handleRuntimeTraceCaptureError(captureOutcome, explicitRuntimeTraceFallback{}, true, canonicalTracePath, captureErr)
	if err == nil {
		t.Fatal("expected invalid explicit runtime trace to surface when recapture fails")
	}
	if len(outcome.warnings) != 0 || outcome.trace != nil {
		t.Fatalf("expected failing recapture to avoid fallback warnings/trace output, got %#v", outcome)
	}
	if !outcome.captureAttempted || !outcome.traceFinalized {
		t.Fatalf("expected failing recapture attempt to be recorded, got %#v", outcome)
	}
	if !strings.Contains(err.Error(), runtimeTraceCommandWarningPrefix) {
		t.Fatalf("expected capture warning prefix in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "unsupported runtime test executable") {
		t.Fatalf("expected capture command validation failure in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "must be regular") {
		t.Fatalf("expected invalid explicit trace validation failure in error, got %v", err)
	}
}

func TestCaptureRuntimeTraceIfNeededSkipsUnsupportedExplicitMalformedTraceBeforeValidation(t *testing.T) {
	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", "runtime.ndjson")
	if err := os.MkdirAll(filepath.Dir(tracePath), 0o750); err != nil {
		t.Fatalf("mkdir explicit trace dir: %v", err)
	}
	testutil.MustWriteFile(t, tracePath, "{not-json}\n")

	t.Setenv("LOPPER_RUNTIME_COUNTER", filepath.Join(repo, "runtime-counter.txt"))
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	req := Request{
		Language:                 "python",
		RuntimeTestCommand:       "npm test",
		RuntimeTracePath:         tracePath,
		RuntimeTracePathExplicit: true,
		Features:                 mustResolvePythonRuntimeCaptureAndTraceDisabled(t),
	}
	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), req, repo, nil)
	if err != nil {
		t.Fatalf("skip unsupported malformed explicit runtime trace: %v", err)
	}
	if outcome.tracePath != tracePath && outcome.tracePath != "" {
		t.Fatalf("expected skipped unsupported trace to avoid canonicalized validation path, got %q", outcome.tracePath)
	}
	if outcome.captureAttempted || outcome.traceFinalized || outcome.trace != nil {
		t.Fatalf("expected unsupported runtime trace request to be skipped, got %#v", outcome)
	}
	if len(outcome.warnings) != 0 {
		t.Fatalf("expected unsupported runtime trace skip without warnings, got %#v", outcome.warnings)
	}
	if _, err := os.Stat(filepath.Join(repo, "runtime-counter.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected runtime capture command not to run for unsupported tracing, stat err=%v", err)
	}
}

func TestCaptureRuntimeTraceIfNeededIgnoresRelativeCWDDecoy(t *testing.T) {
	repo := t.TempDir()
	decoyRoot := t.TempDir()
	relativeTracePath := filepath.Join(".artifacts", "runtime.ndjson")
	testutil.MustWriteFile(t, filepath.Join(decoyRoot, relativeTracePath), "{\"module\":\"lodash/map\"}\n")
	testutil.Chdir(t, decoyRoot)

	t.Setenv("LOPPER_RUNTIME_COUNTER", filepath.Join(repo, "runtime-counter.txt"))
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeToolWithoutTrace(t))

	req := Request{
		Language:                 "js-ts",
		RuntimeTestCommand:       "npm test",
		RuntimeTracePath:         relativeTracePath,
		RuntimeTracePathExplicit: true,
	}
	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), req, repo, nil)
	if err != nil {
		t.Fatalf("capture without runtime output: %v", err)
	}
	wantTracePath := filepath.Join(resolvedTestRepoPath(t, repo), relativeTracePath)
	if outcome.tracePath != wantTracePath {
		t.Fatalf("expected repo-resolved trace path %q, got %q", wantTracePath, outcome.tracePath)
	}
	if !outcome.captureAttempted || outcome.trace != nil {
		t.Fatalf("expected handled capture without validated trace, got %#v", outcome)
	}
	assertSingleRuntimeTraceMissingWarning(t, outcome.warnings)
}

func TestCaptureRuntimeTraceIfNeededWithoutCommandIgnoresRelativeCWDDecoy(t *testing.T) {
	repo := t.TempDir()
	relativeTracePath := filepath.Join(".artifacts", "runtime.ndjson")
	testutil.MustWriteFile(t, filepath.Join(repo, relativeTracePath), "{\"module\":\"lodash/map\"}\n")

	decoyRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(decoyRoot, relativeTracePath), "{\"module\":\"chalk/index\"}\n")
	testutil.Chdir(t, decoyRoot)

	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), Request{Language: "js-ts", RuntimeTracePath: relativeTracePath, RuntimeTracePathExplicit: true}, repo, nil)
	if err != nil {
		t.Fatalf("validate explicit runtime trace without command: %v", err)
	}
	wantTracePath := filepath.Join(resolvedTestRepoPath(t, repo), relativeTracePath)
	if outcome.tracePath != wantTracePath {
		t.Fatalf("expected repo-confined trace path %q, got %q", wantTracePath, outcome.tracePath)
	}
	if !outcome.traceFinalized || outcome.captureAttempted || outcome.trace == nil {
		t.Fatalf("expected finalized explicit runtime trace snapshot, got %#v", outcome)
	}
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "lodash", 1, "expected repo trace snapshot to load lodash")
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "chalk", 0, "did not expect cwd decoy trace to load chalk")
}

func TestCaptureRuntimeTraceIfNeededWithoutCommandRejectsExternalAbsolutePath(t *testing.T) {
	repo := t.TempDir()
	outsideTracePath := filepath.Join(t.TempDir(), "runtime.ndjson")
	testutil.MustWriteFile(t, outsideTracePath, "{\"module\":\"lodash/map\"}\n")

	_, err := captureRuntimeTraceIfNeeded(context.Background(), Request{Language: "js-ts", RuntimeTracePath: outsideTracePath, RuntimeTracePathExplicit: true}, repo, nil)
	if err == nil || !strings.Contains(err.Error(), "must stay within repo") {
		t.Fatalf("expected external explicit trace rejection, got %v", err)
	}
}

func TestCaptureRuntimeTraceIfNeededWithoutCommandRejectsSymlinkedExplicitParentEscape(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink parent regression is Unix-specific")
	}

	repo := t.TempDir()
	outsideRoot := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outsideRoot, "runtime.ndjson"), "{\"module\":\"lodash/map\"}\n")

	linkPath := filepath.Join(repo, "trace-link")
	if err := os.Symlink(outsideRoot, linkPath); err != nil {
		t.Fatalf("symlink trace parent: %v", err)
	}

	_, err := captureRuntimeTraceIfNeeded(context.Background(), Request{Language: "js-ts", RuntimeTracePath: filepath.Join(linkPath, "runtime.ndjson"), RuntimeTracePathExplicit: true}, repo, nil)
	if err == nil || !strings.Contains(err.Error(), "must stay within repo") {
		t.Fatalf("expected symlinked explicit parent escape rejection, got %v", err)
	}
}

func TestCaptureRuntimeTraceIfNeededWithoutCommandUsesRealRepoPathForSymlinkedRepo(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("repo symlink regression is Unix-specific")
	}

	realRepo := t.TempDir()
	relativeTracePath := filepath.Join(".artifacts", "runtime.ndjson")
	testutil.MustWriteFile(t, filepath.Join(realRepo, relativeTracePath), "{\"module\":\"lodash/map\"}\n")

	linkedRepo := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(realRepo, linkedRepo); err != nil {
		t.Fatalf("symlink repo: %v", err)
	}

	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), Request{Language: "js-ts", RuntimeTracePath: relativeTracePath, RuntimeTracePathExplicit: true}, linkedRepo, nil)
	if err != nil {
		t.Fatalf("validate explicit runtime trace through symlinked repo: %v", err)
	}
	wantTracePath := filepath.Join(resolvedTestRepoPath(t, realRepo), relativeTracePath)
	if outcome.tracePath != wantTracePath {
		t.Fatalf("expected canonical symlinked repo trace path %q, got %q", wantTracePath, outcome.tracePath)
	}
	if !outcome.traceFinalized || outcome.trace == nil {
		t.Fatalf("expected finalized explicit runtime trace snapshot, got %#v", outcome)
	}
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "lodash", 1, "expected validated lodash trace")
}

func TestExplicitRuntimeTraceWithoutCommandUsesValidatedSnapshotAcrossPathSwap(t *testing.T) {
	repo, tracePath := setupExplicitRuntimeTraceFixture(t)
	swapPath := tracePath + ".swap"
	validatedPath := tracePath + ".validated"
	stage, restoreTracePath := installRuntimeTraceSwapHook(t, tracePath, swapPath, validatedPath, "rename validated explicit trace aside", "swap explicit trace into path")
	t.Cleanup(func() {
		captureRuntimeTraceAfterValidatedLoadHook = nil
		if err := restoreTracePath(); err != nil {
			t.Errorf("restore explicit runtime trace path: %v", err)
		}
	})

	req := Request{
		Language:                 "js-ts",
		RuntimeTracePath:         tracePath,
		RuntimeTracePathExplicit: true,
	}
	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), req, repo, nil)
	if err != nil {
		t.Fatalf("validate explicit runtime trace without command: %v", err)
	}
	if *stage != 1 {
		t.Fatalf("expected path swap after validated explicit load, stage=%d", *stage)
	}
	req.RuntimeTracePath = outcome.tracePath
	reportData, err := finalizeReport(req, repo, repo, nil, outcome.trace, outcome.captureAttempted || outcome.traceFinalized, report.Report{})
	if err != nil {
		t.Fatalf("finalize explicit runtime trace from validated snapshot: %v", err)
	}
	if err := restoreTracePath(); err != nil {
		t.Fatalf("restore swapped explicit runtime trace path: %v", err)
	}
	if *stage != 2 {
		t.Fatalf("expected explicit rename/swap/rename-back fixture to complete, stage=%d", *stage)
	}
	hasRuntimeUsage := func(dependency reportmodel.DependencyReport) bool {
		return dependency.RuntimeUsage != nil
	}
	assertDependencyAbsent(t, reportData.Dependencies, "js-ts", "chalk", "expected validated explicit snapshot to ignore swapped path")
	assertDependency(t, reportData.Dependencies, "js-ts", "lodash", hasRuntimeUsage, "expected validated explicit snapshot to preserve lodash runtime usage")
}

func TestCaptureRuntimeTraceIfNeededUsesRealDefaultForSymlinkedRepo(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("repo symlink regression is Unix-specific")
	}

	realRepo := t.TempDir()
	linkedRepo := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(realRepo, linkedRepo); err != nil {
		t.Fatalf("symlink repo: %v", err)
	}
	t.Setenv("LOPPER_RUNTIME_COUNTER", filepath.Join(realRepo, "runtime-counter.txt"))
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	req := Request{
		Language:           "js-ts",
		RuntimeTestCommand: "npm test",
	}
	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), req, linkedRepo, nil)
	if err != nil {
		t.Fatalf("capture through symlinked repo: %v", err)
	}
	wantTracePath := runtime.DefaultTracePath(resolvedTestRepoPath(t, realRepo))
	if outcome.tracePath != wantTracePath {
		t.Fatalf("expected real default trace path %q, got %q", wantTracePath, outcome.tracePath)
	}
	if !outcome.captureAttempted || outcome.trace == nil {
		t.Fatalf("expected validated trace from symlinked repo capture, got %#v", outcome)
	}
	assertTraceLoadCount(t, outcome.trace.DependencyLoads, "lodash", 1, "expected validated lodash trace")
}

func TestSuccessfulCaptureWithoutOutputIgnoresLateTraceFile(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("LOPPER_RUNTIME_COUNTER", filepath.Join(repo, "runtime-counter.txt"))
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeToolWithoutTrace(t))

	req := Request{
		Language:           "js-ts",
		RuntimeTestCommand: "npm test",
	}
	outcome, err := captureRuntimeTraceIfNeeded(context.Background(), req, repo, nil)
	if err != nil {
		t.Fatalf("capture without runtime output: %v", err)
	}
	if !outcome.captureAttempted || outcome.trace != nil {
		t.Fatalf("expected successful capture attempt without trace output, got %#v", outcome)
	}
	assertSingleRuntimeTraceMissingWarning(t, outcome.warnings)

	testutil.MustWriteFile(t, outcome.tracePath, "{\"module\":\"lodash/map\"}\n")
	req.RuntimeTracePath = outcome.tracePath
	reportData, err := finalizeReport(req, repo, repo, nil, outcome.trace, outcome.captureAttempted || outcome.traceFinalized, report.Report{
		Warnings: append([]string(nil), outcome.warnings...),
	})
	if err != nil {
		t.Fatalf("finalize after late runtime trace creation: %v", err)
	}
	assertSingleRuntimeTraceMissingWarning(t, reportData.Warnings)
	if len(reportData.Dependencies) != 0 {
		t.Fatalf("expected late unvalidated runtime trace to be ignored, got %#v", reportData.Dependencies)
	}
}

func TestCaptureRuntimeTraceIfNeededPropagatesContextTermination(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("LOPPER_RUNTIME_COUNTER", filepath.Join(repo, "runtime-counter.txt"))
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	contextCases := []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc)
		want       error
	}{
		{
			name: "canceled",
			newContext: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			want: context.Canceled,
		},
		{
			name: "deadline exceeded",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Time{})
			},
			want: context.DeadlineExceeded,
		},
	}
	requestCases := []struct {
		name     string
		explicit bool
	}{
		{name: "implicit trace"},
		{name: "explicit trace", explicit: true},
	}

	for _, contextCase := range contextCases {
		for _, requestCase := range requestCases {
			t.Run(contextCase.name+"/"+requestCase.name, func(t *testing.T) {
				tracePath := ""
				if requestCase.explicit {
					tracePath = filepath.Join(repo, strings.ReplaceAll(t.Name(), "/", "-")+".ndjson")
					testutil.MustWriteFile(t, tracePath, "{\"module\":\"lodash/map\"}\n")
				}
				ctx, cancel := contextCase.newContext()
				defer cancel()

				req := Request{
					RuntimeTestCommand:       "npm test",
					RuntimeTracePath:         tracePath,
					RuntimeTracePathExplicit: requestCase.explicit,
				}
				outcome, err := captureRuntimeTraceIfNeeded(ctx, req, repo, nil)
				if !errors.Is(err, contextCase.want) {
					t.Fatalf("expected %v, got outcome=%#v err=%v", contextCase.want, outcome, err)
				}
				if len(outcome.warnings) != 0 {
					t.Fatalf("expected context termination without warnings, got %#v", outcome.warnings)
				}
			})
		}
	}
}

func TestCaptureRuntimeTraceIfNeededPropagatesInFlightCancellation(t *testing.T) {
	repo := t.TempDir()
	t.Setenv("LOPPER_RUNTIME_COUNTER", filepath.Join(repo, "runtime-counter.txt"))
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeToolReadyBlock(t))

	readyListener := mustListenRuntimeHelperReady(t)
	t.Setenv(analysisRuntimeReadyAddrEnv, readyListener.Addr().String())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		outcome runtimeTraceCaptureOutcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := captureRuntimeTraceIfNeeded(ctx, Request{RuntimeTestCommand: "npm test"}, repo, nil)
		done <- result{outcome: outcome, err: err}
	}()

	waitForRuntimeHelperReady(t, readyListener)
	cancel()

	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("expected in-flight cancellation to propagate context.Canceled, got outcome=%#v err=%v", got.outcome, got.err)
		}
		if len(got.outcome.warnings) != 0 {
			t.Fatalf("expected in-flight cancellation without warnings, got %#v", got.outcome.warnings)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime capture cancellation")
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

type runtimeCaptureCacheFixture struct {
	service     *Service
	adapter     *countingAdapter
	request     Request
	counterPath string
	tracePath   string
}

func newRuntimeCaptureCacheFixture(t *testing.T) runtimeCaptureCacheFixture {
	t.Helper()

	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestJSIndexFileName), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, cacheTestPackageJSONFileName), "{\n  \"name\": \"demo\"\n}\n")

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupFakeAnalysisRuntimeTool(t))

	adapter := &countingAdapter{id: "js-ts"}
	registry := language.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatalf("register JS adapter: %v", err)
	}

	return runtimeCaptureCacheFixture{
		service: &Service{Registry: registry},
		adapter: adapter,
		request: Request{
			RepoPath:           repo,
			Language:           "js-ts",
			TopN:               10,
			RuntimeTestCommand: "npm test",
			Cache: &CacheOptions{
				Enabled: true,
				Path:    filepath.Join(repo, cacheTestDirectoryName),
			},
		},
		counterPath: counterPath,
		tracePath:   runtime.DefaultTracePath(repo),
	}
}

func setupExplicitRuntimeTraceFixture(t *testing.T) (string, string) {
	t.Helper()

	repo := t.TempDir()
	tracePath := filepath.Join(repo, ".artifacts", "runtime.ndjson")
	testutil.MustWriteFile(t, tracePath, "{\"module\":\"lodash/map\"}\n")
	testutil.MustWriteFile(t, tracePath+".swap", "{\"module\":\"chalk/index\"}\n")
	return repo, tracePath
}

func setupFakeAnalysisRuntimeTool(t *testing.T) string {
	return setupFakeAnalysisRuntimeToolWithTraceMode(t, true)
}

func setupFakeAnalysisRuntimeToolWithoutTrace(t *testing.T) string {
	t.Helper()
	return setupFakeAnalysisRuntimeToolWithTraceMode(t, false)
}

func setupFakeAnalysisRuntimeToolWithTraceMode(t *testing.T, writeTrace bool) string {
	t.Helper()
	toolDir := t.TempDir()
	mode := "count-only"
	if writeTrace {
		mode = "count-trace"
	}
	installAnalysisRuntimeTool(t, toolDir, "npm", mode)
	return toolDir
}

func readRuntimeCounter(t *testing.T, path string) int {
	t.Helper()
	return testutil.MustReadTrimmedIntFile(t, path)
}

func mustListenRuntimeHelperReady(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for runtime helper readiness: %v", err)
	}
	t.Cleanup(func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close runtime helper readiness listener: %v", err)
		}
	})
	return listener
}

func waitForRuntimeHelperReady(t *testing.T, listener net.Listener) {
	t.Helper()

	acceptDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			acceptDone <- err
			return
		}
		acceptDone <- conn.Close()
	}()

	select {
	case err := <-acceptDone:
		if err != nil {
			t.Fatalf("accept runtime helper readiness: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for runtime helper readiness")
	}
}

func assertRuntimeCounter(t *testing.T, path string, want int, message string) {
	t.Helper()
	if got := readRuntimeCounter(t, path); got != want {
		t.Fatalf("%s, got %d", message, got)
	}
}

func assertAdapterCallCount(t *testing.T, got int, want int, message string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s, adapter calls=%d", message, got)
	}
}

func assertTraceLoadCount(t *testing.T, loads map[string]int, module string, want int, message string) {
	t.Helper()
	if loads[module] != want {
		t.Fatalf("%s, got %#v", message, loads)
	}
}

func assertSingleRuntimeTraceMissingWarning(t *testing.T, warnings []string) {
	t.Helper()

	count := 0
	for _, warning := range warnings {
		if warning == runtimeTraceMissingWarning {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one runtime trace missing warning, got %d in %#v", count, warnings)
	}
}

func resolvedTestRepoPath(t *testing.T, repo string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve test repo path %q: %v", repo, err)
	}
	return resolved
}

func setupFakeAnalysisRuntimeToolReadyBlock(t *testing.T) string {
	t.Helper()
	toolDir := t.TempDir()
	installAnalysisRuntimeTool(t, toolDir, "npm", "ready-block")
	return toolDir
}

func installAnalysisRuntimeTool(t *testing.T, toolDir string, name string, mode string) {
	t.Helper()
	testutil.InstallSelfExecutable(t, toolDir, name)
	t.Setenv(analysisRuntimeHelperModeEnv, mode)
}

func assertDependencyAbsent(t *testing.T, dependencies []reportmodel.DependencyReport, languageName string, dependencyName string, message string) {
	t.Helper()
	for _, dependency := range dependencies {
		if dependency.Language == languageName && dependency.Name == dependencyName {
			t.Fatalf("%s, got %#v", message, dependency)
		}
	}
}

func assertDependency(t *testing.T, dependencies []reportmodel.DependencyReport, languageName string, dependencyName string, predicate func(reportmodel.DependencyReport) bool, message string) {
	t.Helper()
	for _, dependency := range dependencies {
		if dependency.Language == languageName && dependency.Name == dependencyName && predicate(dependency) {
			return
		}
	}
	t.Fatalf("%s, got %#v", message, dependencies)
}

func installRuntimeTraceSwapHook(t *testing.T, tracePath string, swapPath string, originalPath string, renameMsg string, swapMsg string) (*int, func() error) {
	t.Helper()

	stage := new(int)
	captureRuntimeTraceAfterValidatedLoadHook = func() {
		if *stage != 0 {
			return
		}
		if err := os.Rename(tracePath, originalPath); err != nil {
			t.Fatalf("%s: %v", renameMsg, err)
		}
		if err := os.Rename(swapPath, tracePath); err != nil {
			t.Fatalf("%s: %v", swapMsg, err)
		}
		*stage = 1
	}

	restore := func() error {
		if *stage != 1 {
			return nil
		}
		if err := os.Rename(tracePath, swapPath); err != nil {
			return err
		}
		if err := os.Rename(originalPath, tracePath); err != nil {
			return err
		}
		*stage = 2
		return nil
	}

	return stage, restore
}
