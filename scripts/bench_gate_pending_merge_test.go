package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchGateAcceptsExactPendingMergeHeadAsBase(t *testing.T) {
	t.Parallel()

	repo, benchVars, mergeHead, _ := newPendingMergeBenchGateRepo(t)
	benchVars["MEMORY_BENCH_BASE"] = mergeHead

	output, exitCode := runMakeTargetInDirExpectExitCode(t, repo, "bench-gate", benchVars, 0)
	if exitCode != 0 {
		t.Fatalf("bench-gate exit code = %d, want 0", exitCode)
	}
	for _, want := range []string{
		"matches the pending merge head " + mergeHead,
		"Running memory benchmark delta against " + mergeHead,
		"Result: memory benchmark gate passed.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("bench-gate output missing %q:\n%s", want, output)
		}
	}
	assertMemoryBenchArtifacts(t, repo, "0\n", []string{"Result: memory benchmark gate passed."}, []string{"Comparison status: invalid", "Result: memory benchmark regression detected."})
}

func TestBenchGateRejectsNonMatchingBaseDuringPendingMerge(t *testing.T) {
	t.Parallel()

	repo, benchVars, _, nonMatchingBase := newPendingMergeBenchGateRepo(t)
	benchVars["MEMORY_BENCH_BASE"] = nonMatchingBase
	assertPendingMergeBaseRejected(t, repo, benchVars)
}

func TestBenchGateRejectsMultiplePendingMergeHeads(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		terminator string
	}{
		{name: "terminated", terminator: "\n"},
		{name: "unterminated", terminator: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, benchVars, mergeHead, additionalHead := newPendingMergeBenchGateRepo(t)
			mergeHeadPath := pendingMergeHeadPath(t, repo)
			if err := os.WriteFile(mergeHeadPath, []byte(mergeHead+"\n"+additionalHead+tc.terminator), 0o644); err != nil {
				t.Fatalf("write multiple pending merge heads: %v", err)
			}
			benchVars["MEMORY_BENCH_BASE"] = mergeHead
			assertPendingMergeBaseRejected(t, repo, benchVars)
		})
	}
}

func TestBenchGateRejectsAbsentOrMalformedPendingMergeHead(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "absent",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove pending merge head: %v", err)
				}
			},
		},
		{
			name: "empty",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatalf("empty pending merge head: %v", err)
				}
			},
		},
		{
			name: "malformed",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("not-a-commit\n"), 0o644); err != nil {
					t.Fatalf("write malformed pending merge head: %v", err)
				}
			},
		},
		{
			name: "symbolic",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("main\n"), 0o644); err != nil {
					t.Fatalf("write symbolic pending merge head: %v", err)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, benchVars, mergeHead, _ := newPendingMergeBenchGateRepo(t)
			tc.mutate(t, pendingMergeHeadPath(t, repo))
			benchVars["MEMORY_BENCH_BASE"] = mergeHead
			assertPendingMergeBaseRejected(t, repo, benchVars)
		})
	}
}

func pendingMergeHeadPath(t *testing.T, repo string) string {
	t.Helper()

	mergeHeadPath := strings.TrimSpace(runGitCommand(t, repo, "rev-parse", "--git-path", "MERGE_HEAD"))
	if filepath.IsAbs(mergeHeadPath) {
		return mergeHeadPath
	}
	return filepath.Join(repo, mergeHeadPath)
}

func assertPendingMergeBaseRejected(t *testing.T, repo string, benchVars map[string]string) {
	t.Helper()

	output, exitCode := runMakeTargetInDirExpectExitCode(t, repo, "bench-gate", benchVars, 2)
	if exitCode != 2 {
		t.Fatalf("bench-gate exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(output, "not an ancestor of HEAD; failing closed") {
		t.Fatalf("bench-gate did not reject an invalid pending-merge base:\n%s", output)
	}
	if strings.Contains(output, "Running memory benchmark delta against") {
		t.Fatalf("bench-gate started benchmarking an invalid pending-merge base:\n%s", output)
	}
	assertMemoryBenchArtifacts(t, repo, "2\n", []string{"Comparison status: invalid", "is not an ancestor of HEAD."}, []string{"Result: memory benchmark gate passed.", "Result: memory benchmark regression detected."})
}

func newPendingMergeBenchGateRepo(t *testing.T) (repo string, benchVars map[string]string, mergeHead string, nonMatchingBase string) {
	t.Helper()

	repo, benchVars = newTempBenchGateGoRepo(t)
	copyTree(t, repoPath(t, "tools/benchdelta"), filepath.Join(repo, "tools", "benchdelta"))
	copyTree(t, repoPath(t, "internal/safeio"), filepath.Join(repo, "internal", "safeio"))
	writeFile(t, filepath.Join(repo, "benchpkg", "bench_test.go"), benchmarkTestSource("benchpkg", "BenchmarkPendingMerge"))
	runGitCommand(t, repo, "add", "benchpkg/bench_test.go", "tools/benchdelta", "internal/safeio")
	runGitCommand(t, repo, "commit", "-m", "add benchmark inputs")

	runGitCommand(t, repo, "checkout", "-b", "nonmatching-base")
	writeFile(t, filepath.Join(repo, "nonmatching.txt"), "nonmatching base\n")
	runGitCommand(t, repo, "add", "nonmatching.txt")
	runGitCommand(t, repo, "commit", "-m", "add nonmatching base")
	nonMatchingBase = strings.TrimSpace(runGitCommand(t, repo, "rev-parse", "HEAD"))

	runGitCommand(t, repo, "checkout", "main")
	runGitCommand(t, repo, "checkout", "-b", "pr-head")
	writeFile(t, filepath.Join(repo, "pr.txt"), "pull request\n")
	runGitCommand(t, repo, "add", "pr.txt")
	runGitCommand(t, repo, "commit", "-m", "add pull request change")

	runGitCommand(t, repo, "checkout", "main")
	writeFile(t, filepath.Join(repo, "main.txt"), "main update\n")
	runGitCommand(t, repo, "add", "main.txt")
	runGitCommand(t, repo, "commit", "-m", "advance main")
	mergeHead = strings.TrimSpace(runGitCommand(t, repo, "rev-parse", "HEAD"))

	runGitCommand(t, repo, "checkout", "pr-head")
	runGitCommand(t, repo, "merge", "--no-commit", "--no-ff", "main")
	benchVars["MEMORY_BENCH_PACKAGES"] = "./benchpkg"
	return repo, benchVars, mergeHead, nonMatchingBase
}
