package scripts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	reportmodel "github.com/ben-ranford/lopper/internal/report/model"
	"github.com/ben-ranford/lopper/internal/runtime"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRuntimeCaptureRefreshesTamperedTraceOnCacheHit(t *testing.T) {
	fixture := newRuntimeCaptureRegressionFixture(t)

	first, err := fixture.service.Analyse(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("first analyse with runtime capture: %v", err)
	}
	if first.Cache == nil || first.Cache.Misses != 1 {
		t.Fatalf("expected first run cache miss, got %#v", first.Cache)
	}
	assertRuntimeCaptureRegressionCounter(t, fixture.counterPath, 1, "expected first runtime capture invocation count 1")

	testutil.MustWriteFile(t, fixture.tracePath, "{\"module\":\"chalk/index\"}\n")

	second, err := fixture.service.Analyse(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("second analyse after tampering runtime trace: %v", err)
	}
	if second.Cache == nil || second.Cache.Hits != 1 || second.Cache.Misses != 0 {
		t.Fatalf("expected second run cache hit metadata, got %#v", second.Cache)
	}
	assertRuntimeCaptureRegressionCounter(t, fixture.counterPath, 2, "expected tampered runtime trace to force recapture")
	assertRuntimeCaptureDependencyRefresh(t, second.Dependencies)
}

func TestRuntimeCaptureRejectsImplicitDefaultArtifactsSymlinkWithoutExternalMutation(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink parent regression is Unix-specific")
	}

	repo, outside := newRuntimeCaptureRepoAndOutsideFixture(t)
	tracePath, sentinelPath, traceBefore, sentinelBefore := newRuntimeCaptureExternalFilesFixture(t, outside, "lopper-runtime.ndjson")
	if err := os.Symlink(outside, filepath.Join(repo, ".artifacts")); err != nil {
		t.Fatalf("symlink implicit artifacts dir: %v", err)
	}
	counterPath := setupRuntimeCaptureRegressionEnv(t, repo)

	service := analysis.NewService()
	_, err := service.Analyse(context.Background(), analysis.Request{
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
	assertRuntimeCaptureExternalFilesUnchanged(t, tracePath, traceBefore, sentinelPath, sentinelBefore)
	if _, statErr := os.Stat(runtime.DefaultTracePath(repo)); statErr != nil {
		t.Fatalf("expected symlinked implicit default trace lookup to keep external file reachable, stat err=%v", statErr)
	}
}

func TestRuntimeCaptureRejectsExplicitExternalTracePathWithoutExternalMutation(t *testing.T) {
	repo, outside := newRuntimeCaptureRepoAndOutsideFixture(t)
	tracePath, sentinelPath, traceBefore, sentinelBefore := newRuntimeCaptureExternalFilesFixture(t, outside, "external-runtime.ndjson")
	counterPath := setupRuntimeCaptureRegressionEnv(t, repo)

	service := analysis.NewService()
	_, err := service.Analyse(context.Background(), analysis.Request{
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
	assertRuntimeCaptureExternalFilesUnchanged(t, tracePath, traceBefore, sentinelPath, sentinelBefore)
}

func setupRuntimeCaptureRegressionTool(t *testing.T) string {
	t.Helper()

	toolDir := t.TempDir()
	testutil.InstallSelfExecutable(t, toolDir, "npm")
	t.Setenv(scriptsRuntimeHelperModeEnv, "count-trace")
	return toolDir
}

func readRuntimeCaptureRegressionCounter(t *testing.T, path string) int {
	t.Helper()
	return testutil.MustReadTrimmedIntFile(t, path)
}

type runtimeCaptureRegressionFixture struct {
	service     *analysis.Service
	request     analysis.Request
	counterPath string
	tracePath   string
}

func newRuntimeCaptureRegressionFixture(t *testing.T) runtimeCaptureRegressionFixture {
	t.Helper()

	repo, _ := newRuntimeCaptureRepoAndOutsideFixture(t)
	counterPath := setupRuntimeCaptureRegressionEnv(t, repo)

	return runtimeCaptureRegressionFixture{
		service: analysis.NewService(),
		request: analysis.Request{
			RepoPath:           repo,
			Language:           "js-ts",
			TopN:               10,
			RuntimeTestCommand: "npm test",
			Cache: &analysis.CacheOptions{
				Enabled: true,
				Path:    filepath.Join(repo, "analysis-cache"),
			},
		},
		counterPath: counterPath,
		tracePath:   runtime.DefaultTracePath(repo),
	}
}

func newRuntimeCaptureRepoAndOutsideFixture(t *testing.T) (repo string, outside string) {
	t.Helper()

	repo = t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "index.js"), "console.log('hello')\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	return repo, t.TempDir()
}

func newRuntimeCaptureExternalFilesFixture(t *testing.T, outside string, traceName string) (tracePath string, sentinelPath string, traceBefore string, sentinelBefore string) {
	t.Helper()

	tracePath = filepath.Join(outside, traceName)
	sentinelPath = filepath.Join(outside, "sentinel.txt")
	testutil.MustWriteFile(t, tracePath, "outside-trace\n")
	testutil.MustWriteFile(t, sentinelPath, "keep\n")
	traceBefore = mustReadRuntimeCaptureFile(t, tracePath, "read outside trace before analyse")
	sentinelBefore = mustReadRuntimeCaptureFile(t, sentinelPath, "read outside sentinel before analyse")
	return tracePath, sentinelPath, traceBefore, sentinelBefore
}

func setupRuntimeCaptureRegressionEnv(t *testing.T, repo string) string {
	t.Helper()

	counterPath := filepath.Join(repo, "runtime-counter.txt")
	t.Setenv("LOPPER_RUNTIME_COUNTER", counterPath)
	t.Setenv("LOPPER_RUNTIME_BIN_DIRS", setupRuntimeCaptureRegressionTool(t))
	return counterPath
}

func assertRuntimeCaptureExternalFilesUnchanged(t *testing.T, tracePath string, traceBefore string, sentinelPath string, sentinelBefore string) {
	t.Helper()

	if traceAfter := mustReadRuntimeCaptureFile(t, tracePath, "read outside trace after analyse"); traceAfter != traceBefore {
		t.Fatalf("expected outside trace to remain unchanged, before=%q after=%q", traceBefore, traceAfter)
	}
	if sentinelAfter := mustReadRuntimeCaptureFile(t, sentinelPath, "read outside sentinel after analyse"); sentinelAfter != sentinelBefore {
		t.Fatalf("expected outside sentinel to remain unchanged, before=%q after=%q", sentinelBefore, sentinelAfter)
	}
}

func mustReadRuntimeCaptureFile(t *testing.T, path string, action string) string {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return string(content)
}

func assertRuntimeCaptureRegressionCounter(t *testing.T, path string, want int, message string) {
	t.Helper()
	if got := readRuntimeCaptureRegressionCounter(t, path); got != want {
		t.Fatalf("%s, got %d", message, got)
	}
}

func assertRuntimeCaptureDependencyRefresh(t *testing.T, dependencies []reportmodel.DependencyReport) {
	t.Helper()

	lodashRuntime := findRuntimeCaptureDependencyUsage(t, dependencies, "lodash")
	if lodashRuntime == nil || lodashRuntime.LoadCount != 1 || lodashRuntime.Correlation != report.RuntimeCorrelationRuntimeOnly {
		t.Fatalf("expected refreshed lodash runtime annotation, got %#v from %#v", lodashRuntime, dependencies)
	}
	for _, module := range lodashRuntime.Modules {
		if module.Module == "lodash/map" && module.Count == 1 {
			return
		}
	}
	t.Fatalf("expected refreshed lodash/map runtime evidence, got %#v", lodashRuntime.Modules)
}

func findRuntimeCaptureDependencyUsage(t *testing.T, dependencies []reportmodel.DependencyReport, dependencyName string) *report.RuntimeUsage {
	t.Helper()

	for _, dependency := range dependencies {
		if dependency.Language == "js-ts" && dependency.Name == "chalk" {
			t.Fatalf("expected tampered trace to be ignored after refresh, got %#v", dependency)
		}
		if dependency.Language == "js-ts" && dependency.Name == dependencyName {
			return dependency.RuntimeUsage
		}
	}
	return nil
}
