package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestWithRuntimeTraceEnv(t *testing.T) {
	tracePath := "/tmp/runtime.ndjson"
	requirePath, loaderPath, err := runtimeHookPaths()
	if err != nil {
		t.Fatalf("runtime hook paths: %v", err)
	}

	env, err := withRuntimeTraceEnv([]string{"NODE_OPTIONS=--max-old-space-size=4096", "PATH=/usr/bin"}, tracePath, CaptureProviderNode)
	if err != nil {
		t.Fatalf("with runtime trace env: %v", err)
	}

	assertEnvEntryValue(t, env, "LOPPER_RUNTIME_TRACE", tracePath)
	assertNodeOptionsEntry(t, env, requirePath, loaderPath)
}

func TestWithPythonRuntimeTraceEnv(t *testing.T) {
	tracePath := "/tmp/python-runtime.ndjson"
	hookDir, err := runtimePythonHookDirectory()
	if err != nil {
		t.Fatalf("runtime python hook directory: %v", err)
	}

	env, err := withRuntimeTraceEnv([]string{"PYTHONPATH=/existing/path", "NODE_OPTIONS=--max-old-space-size=4096"}, tracePath, CaptureProviderPython)
	if err != nil {
		t.Fatalf("with python runtime trace env: %v", err)
	}

	assertEnvEntryValue(t, env, "LOPPER_RUNTIME_TRACE", tracePath)
	pythonPath, ok := lookupEnvEntry(env, "PYTHONPATH")
	if !ok {
		t.Fatalf("expected PYTHONPATH to be set")
	}
	wantPythonPath := hookDir + string(os.PathListSeparator) + "/existing/path"
	if pythonPath != wantPythonPath {
		t.Fatalf("expected PYTHONPATH %q, got %q", wantPythonPath, pythonPath)
	}
	if nodeOptions, ok := lookupEnvEntry(env, "NODE_OPTIONS"); !ok || nodeOptions != "--max-old-space-size=4096" {
		t.Fatalf("expected NODE_OPTIONS to be preserved without Python changes, got %q", nodeOptions)
	}
}

func TestWithPythonRuntimeTraceEnvWithoutExistingPythonPath(t *testing.T) {
	tracePath := "/tmp/python-runtime.ndjson"
	hookDir, err := runtimePythonHookDirectory()
	if err != nil {
		t.Fatalf("runtime python hook directory: %v", err)
	}

	env, err := withPythonRuntimeTraceEnv([]string{"PATH=/usr/bin"}, tracePath)
	if err != nil {
		t.Fatalf("with python runtime trace env: %v", err)
	}

	assertEnvEntryValue(t, env, "LOPPER_RUNTIME_TRACE", tracePath)
	assertEnvEntryValue(t, env, "PYTHONPATH", hookDir)
}

func TestWithPythonRuntimeTraceEnvSurfacesHookLookupError(t *testing.T) {
	restoreRuntimePythonHookState(t)

	runtimePythonHookDirOnce = sync.Once{}
	runtimePythonHookDirOnce.Do(func() {
		runtimePythonHookDirPath = ""
		runtimePythonHookDirErr = errors.New("python hook lookup failed")
	})

	if _, err := withPythonRuntimeTraceEnv(nil, "/tmp/python-runtime.ndjson"); err == nil || !strings.Contains(err.Error(), "resolve runtime python hook") {
		t.Fatalf("expected wrapped python hook lookup error, got %v", err)
	}
}

func TestRuntimeCaptureProviderValidationBranches(t *testing.T) {
	if got := normalizeCaptureProvider(""); got != CaptureProviderNode {
		t.Fatalf("expected default provider to normalize to node, got %q", got)
	}
	if got := normalizeCaptureProvider(CaptureProviderPython); got != CaptureProviderPython {
		t.Fatalf("expected python provider to normalize to python, got %q", got)
	}
	if got := normalizeCaptureProvider("ruby"); got != "" {
		t.Fatalf("expected unsupported provider to normalize empty, got %q", got)
	}

	if _, err := withRuntimeTraceEnv(nil, "/tmp/runtime.ndjson", "ruby"); err == nil || !strings.Contains(err.Error(), "unsupported runtime capture provider") {
		t.Fatalf("expected unsupported provider env error, got %v", err)
	}
	if _, err := resolveCapturePlan(CaptureRequest{RepoPath: t.TempDir(), Command: "npm test", Provider: "ruby"}); err == nil || !strings.Contains(err.Error(), "unsupported runtime capture provider") {
		t.Fatalf("expected unsupported provider plan error, got %v", err)
	}
}

func assertEnvEntryValue(t *testing.T, env []string, key, want string) {
	t.Helper()

	got, ok := lookupEnvEntry(env, key)
	if !ok {
		t.Fatalf("expected %s to be set", key)
	}
	if got != want {
		t.Fatalf("expected %s=%q, got %q", key, want, got)
	}
}

func assertNodeOptionsEntry(t *testing.T, env []string, requirePath, loaderPath string) {
	t.Helper()

	nodeOptions, ok := lookupEnvEntry(env, "NODE_OPTIONS")
	if !ok {
		t.Fatalf("expected NODE_OPTIONS to be set")
	}
	if !strings.Contains(nodeOptions, "--max-old-space-size=4096") {
		t.Fatalf("expected existing NODE_OPTIONS to be preserved: %q", nodeOptions)
	}
	if strings.Contains(nodeOptions, "./scripts/runtime/") {
		t.Fatalf("expected absolute runtime hook paths, got %q", nodeOptions)
	}
	if !strings.Contains(nodeOptions, "--require="+requirePath) {
		t.Fatalf("expected require hook to be included: %q", nodeOptions)
	}
	if !strings.Contains(nodeOptions, "--loader="+loaderPath) {
		t.Fatalf("expected loader hook to be included: %q", nodeOptions)
	}
}

func lookupEnvEntry(env []string, key string) (string, bool) {
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || parts[0] != key {
			continue
		}
		return parts[1], true
	}
	return "", false
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
		secureA,
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

func TestRuntimeHookSearchRootsAreAnchored(t *testing.T) {
	restoreRuntimeHookPathProviders(t)

	runtimeExecutablePath = func() (string, error) {
		return filepath.Join("/tmp", "plant", "bin", "lopper"), nil
	}
	runtimeCaller = func(skip int) (uintptr, string, int, bool) {
		return 0, filepath.Join("/tmp", "source", "internal", "runtime", "capture_env.go"), 0, true
	}

	roots := runtimeHookSearchRoots()
	want := []string{
		filepath.Clean(filepath.Join("/tmp", "plant", "bin", "share", "lopper")),
		filepath.Clean(filepath.Join("/tmp", "plant", "bin", "..", "share", "lopper")),
		filepath.Clean(filepath.Join("/tmp", "source", "internal", "runtime", "..", "..")),
	}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("expected anchored runtime hook roots %v, got %v", want, roots)
	}
}

func TestRuntimeHookSearchRootsResolveRelativeCallerPaths(t *testing.T) {
	restoreRuntimeHookPathProviders(t)

	runtimeExecutablePath = func() (string, error) {
		return filepath.Join("/tmp", "plant", "bin", "lopper"), nil
	}
	runtimeCaller = func(skip int) (uintptr, string, int, bool) {
		return 0, "capture_env.go", 0, true
	}

	roots := runtimeHookSearchRoots()
	wantRepoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	want := []string{
		filepath.Clean(filepath.Join("/tmp", "plant", "bin", "share", "lopper")),
		filepath.Clean(filepath.Join("/tmp", "plant", "bin", "..", "share", "lopper")),
		wantRepoRoot,
	}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("expected relative runtime hook roots %v, got %v", want, roots)
	}
}

func TestRuntimeHookSearchRootsSkipsEmptyAndRelativeAbsFailures(t *testing.T) {
	restoreRuntimeHookPathProviders(t)

	runtimeExecutablePath = func() (string, error) {
		return "", nil
	}
	runtimeCaller = func(skip int) (uintptr, string, int, bool) {
		return 0, "", 0, true
	}

	roots := runtimeHookSearchRoots()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	wantRepoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs repo root: %v", err)
	}
	want := []string{
		filepath.Clean(filepath.Join(wd, "share", "lopper")),
		filepath.Clean(filepath.Join(wd, "..", "share", "lopper")),
		wantRepoRoot,
	}
	if !reflect.DeepEqual(roots, want) {
		t.Fatalf("expected only rooted fallback hook paths %v, got %v", want, roots)
	}
}

func TestRuntimeHookSearchRootsDeduplicatesCallerRoot(t *testing.T) {
	restoreRuntimeHookPathProviders(t)

	sharedRoot := filepath.Clean(filepath.Join("/tmp", "plant", "share", "lopper"))
	runtimeExecutablePath = func() (string, error) {
		return filepath.Join("/tmp", "plant", "bin", "lopper"), nil
	}
	runtimeCaller = func(skip int) (uintptr, string, int, bool) {
		return 0, filepath.Join(sharedRoot, "internal", "runtime", "capture_env.go"), 0, true
	}

	roots := runtimeHookSearchRoots()
	if len(roots) != 2 {
		t.Fatalf("expected deduplicated roots, got %v", roots)
	}
	if roots[0] != filepath.Clean(filepath.Join("/tmp", "plant", "bin", "share", "lopper")) || roots[1] != sharedRoot {
		t.Fatalf("expected deduplicated shared root ordering, got %v", roots)
	}
}

func TestRuntimeHookSearchRootsSkipsExecutableErrorAndMissingCaller(t *testing.T) {
	restoreRuntimeHookPathProviders(t)

	runtimeExecutablePath = func() (string, error) {
		return "", errors.New("no executable path")
	}
	runtimeCaller = func(skip int) (uintptr, string, int, bool) {
		return 0, "", 0, false
	}

	if roots := runtimeHookSearchRoots(); len(roots) != 0 {
		t.Fatalf("expected no hook roots when executable and caller data are unavailable, got %v", roots)
	}
}

func TestRuntimeHookSearchRootsUsesRelativeCallerWhenExecutableUnavailable(t *testing.T) {
	restoreRuntimeHookPathProviders(t)

	repo := t.TempDir()
	packageDir := filepath.Join(repo, "internal", "runtime")
	if err := os.MkdirAll(packageDir, 0o750); err != nil {
		t.Fatalf("mkdir package dir: %v", err)
	}
	testutil.Chdir(t, packageDir)
	wantRepoRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("eval repo root symlink: %v", err)
	}

	runtimeExecutablePath = func() (string, error) {
		return "", errors.New("no executable path")
	}
	runtimeCaller = func(skip int) (uintptr, string, int, bool) {
		return 0, "capture_env.go", 0, true
	}

	if roots := runtimeHookSearchRoots(); !reflect.DeepEqual(roots, []string{wantRepoRoot}) {
		t.Fatalf("expected caller-derived fallback root %q, got %v", wantRepoRoot, roots)
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

func TestRuntimeNodeHookOptionsReturnsCachedError(t *testing.T) {
	oldRequire := runtimeRequireHookPath
	oldLoader := runtimeLoaderHookPath
	oldErr := runtimeHookPathsErr
	defer func() {
		runtimeHookPathsOnce = sync.Once{}
		runtimeHookPathsOnce.Do(func() {
			runtimeRequireHookPath = oldRequire
			runtimeLoaderHookPath = oldLoader
			runtimeHookPathsErr = oldErr
		})
	}()

	runtimeHookPathsOnce = sync.Once{}
	runtimeHookPathsOnce.Do(func() {
		runtimeRequireHookPath = ""
		runtimeLoaderHookPath = ""
		runtimeHookPathsErr = errors.New("boom")
	})

	_, err := runtimeNodeHookOptions()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected cached runtime hook error, got %v", err)
	}
}

func restoreRuntimeHookPathProviders(t *testing.T) {
	t.Helper()

	originalExecutable := runtimeExecutablePath
	originalCaller := runtimeCaller

	t.Cleanup(func() {
		runtimeExecutablePath = originalExecutable
		runtimeCaller = originalCaller
	})
}

func restoreRuntimePythonHookState(t *testing.T) {
	t.Helper()

	originalPath := runtimePythonHookDirPath
	originalErr := runtimePythonHookDirErr

	t.Cleanup(func() {
		runtimePythonHookDirOnce = sync.Once{}
		runtimePythonHookDirOnce.Do(func() {
			runtimePythonHookDirPath = originalPath
			runtimePythonHookDirErr = originalErr
		})
	})
}

func TestRuntimeNodeHookOptionsQuotesPathsWithSpaces(t *testing.T) {
	restoreRuntimeHookState(t)

	runtimeHookPathsOnce = sync.Once{}
	runtimeHookPathsOnce.Do(func() {
		runtimeRequireHookPath = "/tmp/hooks/require hook.cjs"
		runtimeLoaderHookPath = "/tmp/hooks/loader hook.mjs"
		runtimeHookPathsErr = nil
	})

	got, err := runtimeNodeHookOptions()
	if err != nil {
		t.Fatalf("runtime node hook options: %v", err)
	}
	if !strings.Contains(got, `--require="/tmp/hooks/require hook.cjs"`) {
		t.Fatalf("expected quoted require path, got %q", got)
	}
	if !strings.Contains(got, `--loader="/tmp/hooks/loader hook.mjs"`) {
		t.Fatalf("expected quoted loader path, got %q", got)
	}
}

func TestQuoteNodeOptionPath(t *testing.T) {
	testCases := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain path", in: "/tmp/hook.cjs", want: "/tmp/hook.cjs"},
		{name: "spaces", in: "/tmp/with space/hook.cjs", want: `"/tmp/with space/hook.cjs"`},
		{name: "quotes", in: `/tmp/with"quote"/hook.cjs`, want: `"/tmp/with\"quote\"/hook.cjs"`},
		{name: "windows slashes", in: `C:\Program Files\lopper\hook.cjs`, want: `"C:\\Program Files\\lopper\\hook.cjs"`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteNodeOptionPath(tc.in); got != tc.want {
				t.Fatalf("quote node option path: expected %q, got %q", tc.want, got)
			}
		})
	}
}
