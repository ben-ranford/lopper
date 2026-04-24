package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWithRuntimeTraceEnv(t *testing.T) {
	tracePath := "/tmp/runtime.ndjson"
	requirePath, loaderPath, err := runtimeHookPaths()
	if err != nil {
		t.Fatalf("runtime hook paths: %v", err)
	}

	env, err := withRuntimeTraceEnv([]string{"NODE_OPTIONS=--max-old-space-size=4096", "PATH=/usr/bin"}, tracePath)
	if err != nil {
		t.Fatalf("with runtime trace env: %v", err)
	}

	assertEnvEntryValue(t, env, "LOPPER_RUNTIME_TRACE", tracePath)
	assertNodeOptionsEntry(t, env, requirePath, loaderPath)
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
