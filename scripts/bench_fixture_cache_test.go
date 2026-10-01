package scripts

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

const benchFixtureCacheProbeEnv = "LOPPER_BENCH_FIXTURE_CACHE_PROBE"

func TestBenchGateFixtureCacheLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "failure"} {
		t.Run(mode, func(t *testing.T) {
			assertBenchFixtureCacheLifecycle(t, mode)
		})
	}
}

func assertBenchFixtureCacheLifecycle(t *testing.T, mode string) {
	t.Helper()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("locate Go binary: %v", err)
	}
	moduleCache := currentGoModuleCache(t, goBinary)
	root := t.TempDir()
	inheritedCache := filepath.Join(root, "inherited-cache")
	sentinel := filepath.Join(inheritedCache, "sentinel")
	writeFile(t, sentinel, "caller-owned cache\n")
	report := filepath.Join(root, "cache-path")
	output, runErr := runBenchFixtureCacheCommand(t, repoPath(t, "."), map[string]string{
		"GOCACHE":                   inheritedCache,
		"GOMODCACHE":                moduleCache,
		"PATH":                      filepath.Dir(goBinary) + string(os.PathListSeparator) + strings.TrimPrefix(gitexec.SafeSystemPath, "PATH="),
		benchFixtureCacheProbeEnv:   mode,
		"LOPPER_BENCH_CACHE_REPORT": report,
	}, testBinary, "-test.run=^TestBenchGateFixtureCacheProbe$", "-test.v")
	t.Logf("cache lifecycle probe:\n%s", output)
	assertBenchFixtureCacheProbeResult(t, mode, output, runErr)
	assertBenchFixturePathRemoved(t, readFile(t, report))
	entries, err := os.ReadDir(inheritedCache)
	if err != nil {
		t.Fatalf("read inherited cache after child exit: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "sentinel" || readFile(t, sentinel) != "caller-owned cache\n" {
		t.Fatal("fixture modified the caller-owned cache")
	}
}

func assertBenchFixtureCacheProbeResult(t *testing.T, mode, output string, runErr error) {
	t.Helper()
	if mode == "success" {
		if runErr != nil {
			t.Errorf("cache reuse probe failed: %v\n%s", runErr, output)
		}
		return
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 ||
		!strings.Contains(output, "intentional fixture cache lifecycle failure") {
		t.Errorf("expected intentional test failure, got %v\n%s", runErr, output)
	}
}

// A separate test process starts with an empty package cache and lets its parent
// verify cleanup after TestMain returns, including an ordinary failing test.
func TestBenchGateFixtureCacheProbe(t *testing.T) {
	mode := os.Getenv(benchFixtureCacheProbeEnv)
	if mode == "" {
		return
	}
	if mode == "failure" {
		_, vars := newTempBenchGateGoRepo(t)
		writeFile(t, os.Getenv("LOPPER_BENCH_CACHE_REPORT"), vars["GOCACHE"])
		t.Error("intentional fixture cache lifecycle failure")
		return
	}
	if mode != "success" {
		t.Fatalf("unknown cache probe mode %q", mode)
	}
	var previousRepo, previousHome string
	t.Run("first", func(t *testing.T) {
		repo, vars := newTempBenchGateGoRepo(t)
		previousRepo, previousHome = repo, vars["HOME"]
		writeFile(t, os.Getenv("LOPPER_BENCH_CACHE_REPORT"), vars["GOCACHE"])
		output := buildAndRunBenchFixtureProgram(t, repo, vars, "first")
		if !strings.Contains(output, " -p fmt ") {
			t.Fatal("fresh fixture cache did not compile fmt; cache reuse proof was not cold")
		}
	})
	assertBenchFixturePathRemoved(t, previousRepo)
	assertBenchFixturePathRemoved(t, previousHome)
	t.Run("second", func(t *testing.T) {
		repo, vars := newTempBenchGateGoRepo(t)
		if repo == previousRepo || vars["HOME"] == previousHome {
			t.Fatal("fixtures reused their repository or HOME")
		}
		output := buildAndRunBenchFixtureProgram(t, repo, vars, "second")
		if strings.Contains(output, " -p fmt ") {
			t.Fatal("second fixture recompiled fmt instead of reusing compiler artifacts after first fixture cleanup")
		}
	})
}

func buildAndRunBenchFixtureProgram(t *testing.T, repo string, vars map[string]string, marker string) string {
	t.Helper()
	writeFile(t, filepath.Join(repo, "main.go"), fmt.Sprintf(`package main

import (
	"fmt"
	"os"
)

func main() {
	dir, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	fmt.Printf("%%s|%%s|%%s\n", %q, os.Getenv("HOME"), dir)
}
`, marker))
	binary := filepath.Join(repo, "fixture.exe")
	buildOutput, err := runBenchFixtureCacheCommand(t, repo, vars, vars["GO"], "build", "-x", "-o", binary, ".")
	if err != nil {
		t.Fatalf("build fixture program: %v\n%s", err, buildOutput)
	}
	output, err := runBenchFixtureCacheCommand(t, repo, vars, binary)
	if err != nil {
		t.Fatalf("run fixture program: %v\n%s", err, output)
	}
	want := marker + "|" + vars["HOME"] + "|" + canonicalBenchFixturePath(t, repo) + "\n"
	if output != want {
		t.Fatalf("fixture output = %q, want %q", output, want)
	}
	for line := range strings.Lines(buildOutput) {
		if strings.Contains(line, " -p fmt ") {
			t.Logf("fmt compiler command: %s", strings.TrimSpace(line))
		}
	}
	t.Logf("%s fixture output: %q", marker, output)
	return buildOutput
}

func TestBenchGateFixtureCacheIsolatesConcurrentDiscovery(t *testing.T) {
	for _, marker := range []string{"First", "Second"} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			assertBenchFixtureCacheDiscovery(t, marker)
		})
	}
}

func assertBenchFixtureCacheDiscovery(t *testing.T, marker string) {
	t.Helper()
	repo, vars := newTempBenchGateGoRepo(t)
	vars["LOPPER_BENCH_FIXTURE_VALUE"] = marker
	pkg := filepath.Join(repo, "benchpkg")
	writeFile(t, filepath.Join(pkg, "env_test.go"), fmt.Sprintf(`package benchpkg

import (
	"fmt"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.Getwd()
	if err != nil || dir != %q || os.Getenv("HOME") != %q || os.Getenv("LOPPER_BENCH_FIXTURE_VALUE") != %q {
		fmt.Fprintln(os.Stderr, "incorrect fixture environment", dir, err)
		os.Exit(2)
	}
	fmt.Println("fixture:" + os.Getenv("LOPPER_BENCH_FIXTURE_VALUE"))
	os.Exit(m.Run())
}
`, filepath.Join(canonicalBenchFixturePath(t, repo), "benchpkg"), vars["HOME"], marker))
	benchFile := filepath.Join(pkg, "bench_test.go")
	for _, suffix := range []string{"Original", "Changed"} {
		benchmark := "Benchmark" + marker + suffix
		writeFile(t, benchFile, benchmarkTestSource("benchpkg", benchmark))
		output, err := runBenchFixtureCacheCommand(t, repo, vars, vars["GO"], "test", "-run=^$", "-list=^Benchmark", "./benchpkg")
		if err != nil {
			t.Fatalf("discover %s: %v\n%s", benchmark, err, output)
		}
		assertBenchFixtureDiscoveryOutput(t, output, marker, benchmark)
	}
	writeFile(t, benchFile, "package benchpkg\nvar _ = missingFixtureSymbol\n")
	output, err := runBenchFixtureCacheCommand(t, repo, vars, vars["GO"], "test", "-run=^$", "-list=^Benchmark", "./benchpkg")
	if err == nil || !strings.Contains(output, "undefined: missingFixtureSymbol") {
		t.Fatalf("discovery reused success after invalid source mutation: %v\n%s", err, output)
	}
}

func assertBenchFixtureDiscoveryOutput(t *testing.T, output, marker, benchmark string) {
	t.Helper()
	var benchmarks []string
	for line := range strings.Lines(output) {
		if strings.HasPrefix(line, "Benchmark") {
			benchmarks = append(benchmarks, strings.TrimSpace(line))
		}
	}
	if len(benchmarks) != 1 || benchmarks[0] != benchmark || !strings.Contains(output, "fixture:"+marker+"\n") {
		t.Fatalf("discovery returned stale source or another fixture's result:\n%s", output)
	}
}

func runBenchFixtureCacheCommand(t *testing.T, dir string, vars map[string]string, executable string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), executable, args...)
	cmd.Dir = dir
	cmd.Env = append(gitexec.SanitizedEnv(), "GOTOOLCHAIN=local", "GOWORK=off")
	for key, value := range vars {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func canonicalBenchFixturePath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve fixture path: %v", err)
	}
	return resolved
}

func assertBenchFixturePathRemoved(t *testing.T, path string) {
	t.Helper()
	if path == "" {
		t.Fatal("fixture did not report a path to check after cleanup")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("fixture path %q remains after cleanup: %v", path, err)
	}
}
