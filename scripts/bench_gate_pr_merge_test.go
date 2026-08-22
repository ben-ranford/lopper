package scripts

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchGateUsesCheckedOutPRMergeBaseForStaleEventBase(t *testing.T) {
	t.Parallel()

	repo, benchVars := newTempBenchGateGoRepo(t)
	writeExecutableFile(t, filepath.Join(repo, "scripts", "bench-gate-pr-base.sh"), readConfig(t, "scripts/bench-gate-pr-base.sh"))
	copyTree(t, repoPath(t, "tools/benchdelta"), filepath.Join(repo, "tools", "benchdelta"))
	copyTree(t, repoPath(t, "internal/safeio"), filepath.Join(repo, "internal", "safeio"))
	writeFile(t, filepath.Join(repo, "benchpkg", "bench_test.go"), benchmarkTestSource("benchpkg", "BenchmarkShared"))
	writeFile(t, filepath.Join(repo, "benchpkg", "harness_test.go"), "package benchpkg\n\nfunc benchmarkHarnessValue() int { return 1 }\n")
	runGitCommand(t, repo, "add", "go.mod", "benchpkg/bench_test.go", "benchpkg/harness_test.go", "tools/benchdelta", "internal/safeio")
	runGitCommand(t, repo, "commit", "-m", "event base")
	eventBase := strings.TrimSpace(runGitCommand(t, repo, "rev-parse", "HEAD"))

	writeFile(t, filepath.Join(repo, "benchpkg", "harness_test.go"), "package benchpkg\n\nfunc benchmarkHarnessValue() int { return 2 }\n")
	runGitCommand(t, repo, "add", "benchpkg/harness_test.go")
	runGitCommand(t, repo, "commit", "-m", "newer checked out base")
	checkedOutBase := strings.TrimSpace(runGitCommand(t, repo, "rev-parse", "HEAD"))

	runGitCommand(t, repo, "checkout", "-b", "pr-head", eventBase)
	writeFile(t, filepath.Join(repo, "README.md"), "branch change\n")
	runGitCommand(t, repo, "add", "README.md")
	runGitCommand(t, repo, "commit", "-m", "branch code change")

	runGitCommand(t, repo, "checkout", "main")
	runGitCommand(t, repo, "merge", "--no-ff", "--no-edit", "pr-head")

	benchVars["MEMORY_BENCH_BASE"] = eventBase
	benchVars["MEMORY_BENCH_PACKAGES"] = "./benchpkg"
	output, exitCode := runMakeTargetInDirExpectExitCode(t, repo, "bench-gate", benchVars, 0)
	if exitCode != 0 {
		t.Fatalf("bench-gate exit code = %d, want 0", exitCode)
	}
	for _, want := range []string{
		"stale PR event base " + eventBase,
		"using checked-out PR merge base " + checkedOutBase,
		"Result: memory benchmark gate passed.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("bench-gate output missing %q:\n%s", want, output)
		}
	}
	assertMemoryBenchArtifacts(t, repo, "0\n", []string{"Result: memory benchmark gate passed."}, []string{"does not match the resolved head harness fingerprint."})
}
