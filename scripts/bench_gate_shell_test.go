package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBenchGatePreservesArtifactDirectoryNames(t *testing.T) {
	t.Parallel()

	repo, benchVars := newBenchGateShellFixture(t)
	benchVars["MEMORY_BENCH_BASE"] = "refs/heads/does-not-exist"
	benchVars["BENCH_BASE_OUTPUT"] = "bench artifacts/base.out"
	benchVars["BENCH_HEAD_OUTPUT"] = "bench\tartifacts/head.out"
	benchVars["MEMORY_BENCH_SUMMARY"] = "bench\nartifacts/summary.md"
	benchVars["MEMORY_BENCH_STATUS"] = "bench[xy]*artifacts/status.txt"
	if err := os.Mkdir(filepath.Join(repo, "benchx-other-artifacts"), 0o755); err != nil {
		t.Fatalf("create directory matching artifact glob: %v", err)
	}

	output, _ := runMakeTargetInDirExpectExitCode(t, repo, "bench-gate", benchVars, 2)
	want := "requested base ref 'refs/heads/does-not-exist' is missing or invalid"
	if !strings.Contains(output, want) {
		t.Fatalf("bench-gate output missing %q:\n%s", want, output)
	}
	for _, variable := range []string{"BENCH_BASE_OUTPUT", "BENCH_HEAD_OUTPUT", "MEMORY_BENCH_SUMMARY", "MEMORY_BENCH_STATUS"} {
		dir := filepath.Join(repo, filepath.Dir(benchVars[variable]))
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("artifact directory %q was not created: %v", dir, err)
		}
	}
	assertMemoryBenchArtifactsAtPaths(t, repo, benchVars["MEMORY_BENCH_SUMMARY"], benchVars["MEMORY_BENCH_STATUS"], "2\n", []string{want}, nil)
	assertPathAbsent(t, filepath.Join(repo, "bench"))
	assertPathAbsent(t, filepath.Join(repo, "artifacts"))
}

func TestBenchGatePreservesPackageSplittingAndCleansFailedRun(t *testing.T) {
	t.Parallel()

	repo, benchVars := newBenchGateShellFixture(t)
	for _, name := range []string{"benchpkg with space", "benchpkgone", "benchpkgtwo"} {
		if err := os.Mkdir(filepath.Join(repo, name), 0o755); err != nil {
			t.Fatalf("create package glob fixture: %v", err)
		}
	}
	tempDir := filepath.Join(t.TempDir(), "bench temp '[x]\nfiles")
	if err := os.Mkdir(tempDir, 0o755); err != nil {
		t.Fatalf("create benchmark temporary directory: %v", err)
	}
	benchVars["TMPDIR"] = tempDir
	benchVars["MEMORY_BENCH_PACKAGES"] = " \t./benchpkg*\n./literal'quote\t./back\\slash ./nomatch? \n"
	mktempBin, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatalf("resolve mktemp: %v", err)
	}
	toolDir := t.TempDir()
	tempLog := filepath.Join(repo, "temporary-paths")
	writeExecutableFile(t, filepath.Join(toolDir, "mktemp"), "#!/bin/sh\nset -eu\ncreated=$("+shellQuote(mktempBin)+" \"$@\")\nprintf '%s\\0' \"$created\" >> "+shellQuote(tempLog)+"\nprintf '%s\\n' \"$created\"\n")
	benchVars["PATH"] = toolDir + string(os.PathListSeparator) + os.Getenv("PATH")

	output, _ := runMakeTargetInDirExpectExitCode(t, repo, "bench-gate", benchVars, 2)
	wantDiagnostic := "head benchmark package targets could not be resolved."
	if !strings.Contains(output, wantDiagnostic) {
		t.Fatalf("bench-gate output missing %q:\n%s", wantDiagnostic, output)
	}
	argv, err := os.ReadFile(filepath.Join(repo, "package-argv"))
	if err != nil {
		t.Fatalf("read package arguments: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(argv), "\x00"), "\x00")
	want := []string{"./benchpkg with space", "./benchpkgone", "./benchpkgtwo", "./literal'quote", "./back\\slash", "./nomatch?"}
	if !slices.Equal(got, want) {
		t.Fatalf("package arguments = %q, want %q", got, want)
	}
	assertMemoryBenchArtifacts(t, repo, "2\n", []string{wantDiagnostic}, nil)
	temporaryPaths, err := os.ReadFile(tempLog)
	if err != nil {
		t.Fatalf("read benchmark temporary paths: %v", err)
	}
	paths := strings.Split(strings.TrimSuffix(string(temporaryPaths), "\x00"), "\x00")
	if len(paths) < 5 {
		t.Fatalf("expected benchmark worktree and four temporary files, got %q", paths)
	}
	for _, path := range paths {
		assertPathAbsent(t, path)
	}
}

func newBenchGateShellFixture(t *testing.T) (string, map[string]string) {
	t.Helper()

	repo := newTempBenchGateRepo(t)
	goBin := filepath.Join(repo, "go-fixture")
	writeExecutableFile(t, goBin, `#!/bin/sh
set -eu
case "$1" in
  env) printf 'go1.shell-fixture\n' ;;
  version) printf 'go version go1.shell-fixture test/test\n' ;;
  build) exit 0 ;;
  list)
    shift
    printf '%s\0' "$@" > package-argv
    exit 7
    ;;
  *) exit 9 ;;
esac
`)
	return repo, map[string]string{
		"GO":                    goBin,
		"GO_BIN":                goBin,
		"GO_TOOLCHAIN":          "local",
		"GO_TEST_LDFLAGS":       "",
		"MEMORY_BENCH_BASE":     "HEAD",
		"MEMORY_BENCH_ENFORCE":  "1",
		"MEMORY_BENCH_PACKAGES": "./benchpkg",
		"BENCH_BASE_OUTPUT":     ".artifacts/bench-base.out",
		"BENCH_HEAD_OUTPUT":     ".artifacts/bench-head.out",
		"MEMORY_BENCH_SUMMARY":  ".artifacts/memory-bench-summary.md",
		"MEMORY_BENCH_STATUS":   ".artifacts/memory-bench-status.txt",
		"LC_ALL":                "C",
	}
}
