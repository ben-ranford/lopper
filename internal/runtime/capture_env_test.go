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
			if strings.Contains(entry, "./scripts/runtime/") {
				t.Fatalf("expected absolute runtime hook paths, got %q", entry)
			}
			if !strings.Contains(entry, "--require="+requirePath) {
				t.Fatalf("expected require hook to be included: %q", entry)
			}
			if !strings.Contains(entry, "--loader="+loaderPath) {
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
