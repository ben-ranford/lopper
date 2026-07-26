package scripts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestMakefileBenchGateBaseExecutionFailureWritesArtifactsAndCleansUp(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: failingBenchmarkSource("base benchmark failure"),
		headBenchmarkSource: passingBenchmarkSource(),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	before := gitWorktreeListOutput(t, repo)

	output, exitCode := runBenchGateMake(t, repo, map[string]string{
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
	})
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "base benchmark failure") {
		t.Fatalf("output = %q, want base benchmark failure", output)
	}
	if !strings.Contains(output, "memory benchmark base execution failed") {
		t.Fatalf("output = %q, want deterministic base execution failure message", output)
	}
	assertBenchGateFailureArtifacts(t, summaryPath, statusPath, "memory benchmark base execution failed")
	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateHeadExecutionFailureWritesArtifactsAndCleansUp(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: failingBenchmarkSource("head benchmark failure"),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	before := gitWorktreeListOutput(t, repo)

	output, exitCode := runBenchGateMake(t, repo, map[string]string{
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	})
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "head benchmark failure") {
		t.Fatalf("output = %q, want head benchmark failure", output)
	}
	if !strings.Contains(output, "memory benchmark head execution failed") {
		t.Fatalf("output = %q, want deterministic head execution failure message", output)
	}
	assertBenchGateFailureArtifacts(t, summaryPath, statusPath, "memory benchmark head execution failed")
	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateInvalidComparisonFailsClosedEvenWhenEnforcementDisabled(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingNamedBenchmarkSource("BenchmarkBaseFixture"),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	before := gitWorktreeListOutput(t, repo)

	output, exitCode := runBenchGateMake(t, repo, map[string]string{
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	})
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "Comparison status: invalid") {
		t.Fatalf("output = %q, want invalid comparison status", output)
	}
	if !strings.Contains(output, "No overlapping benchmark names were found between base and head.") {
		t.Fatalf("output = %q, want zero-overlap diagnostic", output)
	}
	assertBenchGateFailureArtifacts(t, summaryPath, statusPath, "No overlapping benchmark names were found between base and head.")
	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateMissingBaseRefFailsClosedEvenWhenEnforcementDisabled(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	before := gitWorktreeListOutput(t, repo)

	output, exitCode := runBenchGateMake(t, repo, map[string]string{
		"MEMORY_BENCH_BASE":     "refs/heads/missing",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	})
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, `does not resolve to a commit`) {
		t.Fatalf("output = %q, want missing ref diagnostic", output)
	}
	assertBenchGateFailureArtifacts(t, summaryPath, statusPath, `does not resolve to a commit`)
	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateHelperBuildFailureRejectsSymlinkArtifactDirWithoutTouchingOutsideFiles(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	realArtifactDir := filepath.Join(benchgateCanonicalTempDir(t), "outside-artifacts")
	if err := os.MkdirAll(realArtifactDir, 0o755); err != nil {
		t.Fatalf("mkdir outside artifact dir: %v", err)
	}
	outsideSummaryPath := filepath.Join(realArtifactDir, "memory-bench-summary.md")
	outsideStatusPath := filepath.Join(realArtifactDir, "memory-bench-status.txt")
	writeBenchGateFile(t, outsideSummaryPath, "outside summary\n")
	writeBenchGateFile(t, outsideStatusPath, "outside status\n")
	linkArtifactDir := filepath.Join(repo, ".artifacts")
	if err := os.Symlink(realArtifactDir, linkArtifactDir); err != nil {
		t.Fatalf("symlink artifact dir: %v", err)
	}
	before := gitWorktreeListOutput(t, repo)
	extraEnv := map[string]string{
		"GO":                    "false",
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	}

	output, exitCode := runBenchGateMakeWithUmask(t, repo, extraEnv, "077")
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "memory benchmark helper ./tools/benchgate build failed") {
		t.Fatalf("output = %q, want helper build failure", output)
	}
	assertBenchGateFileContent(t, outsideSummaryPath, "outside summary\n")
	assertBenchGateFileContent(t, outsideStatusPath, "outside status\n")
	assertBenchGatePathIsSymlink(t, linkArtifactDir)

	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateHelperLaunchFailureRejectsSymlinkArtifactTargetWithoutTouchingOutsideFiles(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	if err := os.MkdirAll(filepath.Dir(summaryPath), 0o755); err != nil {
		t.Fatalf("mkdir repo artifact dir: %v", err)
	}
	outsideSummaryPath := filepath.Join(benchgateCanonicalTempDir(t), "outside-summary.md")
	writeBenchGateFile(t, outsideSummaryPath, "outside summary\n")
	if err := os.Symlink(outsideSummaryPath, summaryPath); err != nil {
		t.Fatalf("symlink summary target: %v", err)
	}
	before := gitWorktreeListOutput(t, repo)
	fakeGo := filepath.Join(benchgateCanonicalTempDir(t), "fake-go")
	writeBenchGateFile(t, fakeGo, "#!/bin/sh\nset -eu\nif [ \"$1\" = \"build\" ]; then\n\tshift\n\toutput=\n\twhile [ \"$#\" -gt 0 ]; do\n\t\tif [ \"$1\" = \"-o\" ]; then\n\t\t\toutput=\"$2\"\n\t\t\tshift 2\n\t\t\tcontinue\n\t\tfi\n\t\tshift\n\tdone\n\tprintf 'not-a-binary\\n' > \"$output\"\n\tchmod 755 \"$output\"\n\texit 0\nfi\nprintf 'unexpected fake-go invocation: %s\\n' \"$*\" >&2\nexit 1\n")
	if err := os.Chmod(fakeGo, 0o755); err != nil {
		t.Fatalf("chmod fake go: %v", err)
	}
	extraEnv := map[string]string{
		"GO":                    fakeGo,
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	}

	output, exitCode := runBenchGateMakeWithUmask(t, repo, extraEnv, "077")
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "memory benchmark helper ./tools/benchgate launch failed") {
		t.Fatalf("output = %q, want helper launch failure", output)
	}
	assertBenchGateFileContent(t, outsideSummaryPath, "outside summary\n")
	assertBenchGatePathAbsent(t, statusPath)

	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateWorktreeAddHelperLaunchFailureWritesArtifactsAndExitsTwo(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	before := gitWorktreeListOutput(t, repo)
	fakeGo := filepath.Join(benchgateCanonicalTempDir(t), "fake-go-worktree")
	writeBenchGateFile(t, fakeGo, "#!/bin/sh\nset -eu\nif [ \"$1\" = \"build\" ]; then\n\tshift\n\toutput=\n\twhile [ \"$#\" -gt 0 ]; do\n\t\tif [ \"$1\" = \"-o\" ]; then\n\t\t\toutput=\"$2\"\n\t\t\tshift 2\n\t\t\tcontinue\n\t\tfi\n\t\tshift\n\tdone\n\tcat > \"$output\" <<'EOF'\n#!/bin/sh\nset -eu\ncase \"${1-}\" in\n\t-base-ref)\n\t\tprintf 'deadbeef\\n'\n\t\texit 0\n\t\t;;\n\t-worktree-add)\n\t\tprintf 'worktree add helper launch failed\\n' >&2\n\t\texit 127\n\t\t;;\n\t-worktree-remove)\n\t\texit 0\n\t\t;;\n\t-summary-out)\n\t\tsummary_out=\n\t\tstatus_out=\n\t\tmessage=\n\t\twhile [ \"$#\" -gt 0 ]; do\n\t\t\tcase \"$1\" in\n\t\t\t\t-summary-out)\n\t\t\t\t\tsummary_out=\"$2\"\n\t\t\t\t\tshift 2\n\t\t\t\t\t;;\n\t\t\t\t-status-out)\n\t\t\t\t\tstatus_out=\"$2\"\n\t\t\t\t\tshift 2\n\t\t\t\t\t;;\n\t\t\t\t-failure-message)\n\t\t\t\t\tmessage=\"$2\"\n\t\t\t\t\tshift 2\n\t\t\t\t\t;;\n\t\t\t\t*)\n\t\t\t\t\tshift\n\t\t\t\t\t;;\n\t\t\tesac\n\t\tdone\n\t\tmkdir -p \"$(dirname \"$summary_out\")\" \"$(dirname \"$status_out\")\"\n\t\tprintf '## Memory Benchmarks\\n\\nComparison could not be evaluated.\\n\\n%s\\n' \"$message\" > \"$summary_out\"\n\t\tprintf '2\\n' > \"$status_out\"\n\t\texit 2\n\t\t;;\n\t*)\n\t\tprintf 'unexpected helper args: %s\\n' \"$*\" >&2\n\t\texit 1\n\t\t;;\nesac\nEOF\n\tchmod 755 \"$output\"\n\texit 0\nfi\nprintf 'unexpected fake-go invocation: %s\\n' \"$*\" >&2\nexit 1\n")
	if err := os.Chmod(fakeGo, 0o755); err != nil {
		t.Fatalf("chmod fake go: %v", err)
	}

	env := map[string]string{
		"GO":                    fakeGo,
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	}
	output, exitCode := runBenchGateMakeWithUmask(t, repo, env, "077")
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "memory benchmark helper ./tools/benchgate launch failed") {
		t.Fatalf("output = %q, want helper launch failure", output)
	}
	assertBenchGateFailureArtifacts(t, summaryPath, statusPath, "memory benchmark worktree setup failed")

	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateBenchdeltaBuildFailureWritesArtifactsAndExitsTwo(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingBenchmarkSource(),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	before := gitWorktreeListOutput(t, repo)
	fakeGo := filepath.Join(benchgateCanonicalTempDir(t), "fake-go-benchdelta-build")
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("look up go binary: %v", err)
	}
	writeBenchGateFile(t, fakeGo, "#!/bin/sh\nset -eu\nreal_go="+fmt.Sprintf("%q", realGo)+"\ncase \"${1-}\" in\n\tbuild)\n\t\tshift\n\t\toutput=\n\t\ttarget=\n\t\tprev=\n\t\tfor arg in \"$@\"; do\n\t\t\tif [ \"$prev\" = \"-o\" ]; then\n\t\t\t\toutput=\"$arg\"\n\t\t\t\tprev=\n\t\t\t\tcontinue\n\t\t\tfi\n\t\t\tif [ \"$arg\" = \"-o\" ]; then\n\t\t\t\tprev=\"-o\"\n\t\t\t\tcontinue\n\t\t\tfi\n\t\t\ttarget=\"$arg\"\n\t\tdone\n\t\tcase \"$target\" in\n\t\t\t./tools/benchgate)\n\t\t\t\tcat > \"$output\" <<'EOF'\n#!/bin/sh\nset -eu\ncase \"${1-}\" in\n\t-base-ref)\n\t\tprintf 'deadbeef\\n'\n\t\texit 0\n\t\t;;\n\t-worktree-add)\n\t\tworktree_dir=\n\t\tcommit=\n\t\twhile [ \"$#\" -gt 0 ]; do\n\t\t\tcase \"$1\" in\n\t\t\t\t-worktree-add)\n\t\t\t\t\tworktree_dir=\"$2\"\n\t\t\t\t\tshift 2\n\t\t\t\t\t;;\n\t\t\t\t-worktree-commit)\n\t\t\t\t\tcommit=\"$2\"\n\t\t\t\t\tshift 2\n\t\t\t\t\t;;\n\t\t\t\t*)\n\t\t\t\t\tshift\n\t\t\t\t\t;;\n\t\t\tesac\n\t\t\tdone\n\t\tmkdir -p \"$worktree_dir\"\n\t\t: \"${commit:?}\"\n\t\texit 0\n\t\t;;\n\t-worktree-remove)\n\t\texit 0\n\t\t;;\n\t-summary-out)\n\t\tsummary_out=\n\t\tstatus_out=\n\t\tmessage=\n\t\twhile [ \"$#\" -gt 0 ]; do\n\t\t\tcase \"$1\" in\n\t\t\t\t-summary-out)\n\t\t\t\t\tsummary_out=\"$2\"\n\t\t\t\t\tshift 2\n\t\t\t\t\t;;\n\t\t\t\t-status-out)\n\t\t\t\t\tstatus_out=\"$2\"\n\t\t\t\t\tshift 2\n\t\t\t\t\t;;\n\t\t\t\t-failure-message)\n\t\t\t\t\tmessage=\"$2\"\n\t\t\t\t\tshift 2\n\t\t\t\t\t;;\n\t\t\t\t*)\n\t\t\t\t\tshift\n\t\t\t\t\t;;\n\t\t\tesac\n\t\t\tdone\n\t\tmkdir -p \"$(dirname \"$summary_out\")\" \"$(dirname \"$status_out\")\"\n\t\tprintf '## Memory Benchmarks\\n\\nComparison could not be evaluated.\\n\\n%s\\n' \"$message\" > \"$summary_out\"\n\t\tprintf '2\\n' > \"$status_out\"\n\t\texit 2\n\t\t;;\n\t*)\n\t\tprintf 'unexpected helper args: %s\\n' \"$*\" >&2\n\t\texit 1\n\t\t;;\nesac\nEOF\n\t\t\t\tchmod 755 \"$output\"\n\t\t\t\texit 0\n\t\t\t\t;;\n\t\t\t./tools/benchdelta)\n\t\t\t\tprintf 'benchdelta build failed on purpose\\n' >&2\n\t\t\t\texit 1\n\t\t\t\t;;\n\t\t\t*)\n\t\t\t\tprintf 'unexpected build target: %s\\n' \"$target\" >&2\n\t\t\t\texit 1\n\t\t\t\t;;\n\t\tesac\n\t\t;;\n\ttest)\n\t\tprintf 'goos: fake\\ngoarch: fake\\npkg: github.com/ben-ranford/lopper/benchfixture\\ncpu: fake\\nBenchmarkFixture-8\\t1\\t100 ns/op\\t64 B/op\\t1 allocs/op\\nPASS\\n'\n\t\texit 0\n\t\t;;\n\t*)\n\t\texec \"$real_go\" \"$@\"\n\t\t;;\nesac\n")
	if err := os.Chmod(fakeGo, 0o755); err != nil {
		t.Fatalf("chmod fake go: %v", err)
	}

	env := map[string]string{
		"GO":                    fakeGo,
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	}
	output, exitCode := runBenchGateMakeWithUmask(t, repo, env, "077")
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "benchdelta build failed on purpose") {
		t.Fatalf("output = %q, want captured benchdelta build failure output", output)
	}
	wantMessage := "memory benchmark comparison setup failed; comparison could not be evaluated"
	if !strings.Contains(output, wantMessage) {
		t.Fatalf("output = %q, want deterministic comparison setup failure message", output)
	}
	assertBenchGateFileContent(t, summaryPath, "## Memory Benchmarks\n\nComparison could not be evaluated.\n\n"+wantMessage+"\n")
	assertBenchGateStatus(t, statusPath, "2\n")

	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateHelperSetupFailureSkipsShellArtifactFallback(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	blocker := filepath.Join(benchgateCanonicalTempDir(t), "summary-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write summary blocker: %v", err)
	}
	summaryPath := filepath.Join(blocker, "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	extraEnv := map[string]string{
		"GO":                    "false",
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
		"MEMORY_BENCH_SUMMARY":  summaryPath,
		"MEMORY_BENCH_STATUS":   statusPath,
	}

	output, exitCode := runBenchGateMakeWithUmask(t, repo, extraEnv, "077")
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "memory benchmark helper ./tools/benchgate build failed") {
		t.Fatalf("output = %q, want helper build failure", output)
	}
	if strings.Contains(output, "benchgate failure artifact fallback") {
		t.Fatalf("output = %q, want no shell fallback warning", output)
	}
	assertBenchGatePathAbsent(t, statusPath)
}

func TestMakefileBenchGateIgnoresPollutedGitEnvironmentForBenchgateAndWorktreeGit(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingNamedBenchmarkSource("BenchmarkBaseFixture"),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	attackerRepo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingBenchmarkSourceWithComment("attacker fixture"),
	})
	runGitCommand(t, attackerRepo, "reset", "--hard", "HEAD~1")
	attackerWorktreesBefore := gitWorktreeListOutput(t, attackerRepo)
	attackerHeadBefore := gitRevParseOutput(t, attackerRepo, "HEAD")
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")

	output, exitCode := runBenchGateMake(t, repo, map[string]string{
		"GIT_DIR":                          filepath.Join(attackerRepo, ".git"),
		"GIT_WORK_TREE":                    attackerRepo,
		"GIT_INDEX_FILE":                   filepath.Join(benchgateCanonicalTempDir(t), "attacker.index"),
		"GIT_OBJECT_DIRECTORY":             filepath.Join(benchgateCanonicalTempDir(t), "attacker.objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(benchgateCanonicalTempDir(t), "attacker.alternates"),
		"GIT_COMMON_DIR":                   filepath.Join(benchgateCanonicalTempDir(t), "attacker.common"),
		"GIT_NAMESPACE":                    "attacker",
		"MEMORY_BENCH_BASE":                "HEAD~1",
		"MEMORY_BENCH_PACKAGES":            "./benchfixture",
		"BENCH_COUNT":                      "1",
		"BENCH_TIME":                       "1x",
		"MEMORY_BENCH_ENFORCE":             "0",
	})
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "Running memory benchmark delta against HEAD~1.") {
		t.Fatalf("output = %q, want normal bench-gate execution", output)
	}
	if !strings.Contains(output, "Comparison status: invalid") {
		t.Fatalf("output = %q, want deterministic invalid comparison from target repo", output)
	}
	if strings.Contains(output, "does not resolve to a commit") {
		t.Fatalf("output = %q, want target repo HEAD~1 resolution instead of attacker repo single-commit failure", output)
	}
	if after := gitWorktreeListOutput(t, attackerRepo); after != attackerWorktreesBefore {
		t.Fatalf("attacker repo worktree list changed\nbefore:\n%s\nafter:\n%s", attackerWorktreesBefore, after)
	}
	if attackerHeadAfter := gitRevParseOutput(t, attackerRepo, "HEAD"); attackerHeadAfter != attackerHeadBefore {
		t.Fatalf("attacker repo HEAD changed\nbefore: %s\nafter:  %s", attackerHeadBefore, attackerHeadAfter)
	}
	assertBenchGateFailureArtifacts(t, summaryPath, statusPath, "No overlapping benchmark names were found between base and head.")
}

func TestMakefileBenchGateRejectsPATHHijackedGitBeforeDuringAndAfterBenchgate(t *testing.T) {
	wrapperDir := filepath.Join(benchgateCanonicalTempDir(t), "custom-bin")
	if err := os.MkdirAll(wrapperDir, 0o755); err != nil {
		t.Fatalf("mkdir wrapper dir: %v", err)
	}
	logPath := filepath.Join(wrapperDir, "git.log")
	wrapperPath := filepath.Join(wrapperDir, "git")
	wrapper := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit 99\n", logPath)
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingNamedBenchmarkSource("BenchmarkBaseFixture"),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	assertPathHijackGitNotInvoked(t, logPath, "fixture setup")

	before := gitWorktreeListOutput(t, repo)
	assertPathHijackGitNotInvoked(t, logPath, "pre-benchgate worktree assertion")

	output, exitCode := runBenchGateMake(t, repo, map[string]string{
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	})
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "Comparison status: invalid") {
		t.Fatalf("output = %q, want deterministic invalid comparison", output)
	}
	assertPathHijackGitNotInvoked(t, logPath, "benchgate execution")

	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
	assertPathHijackGitNotInvoked(t, logPath, "post-benchgate worktree assertion")
}

func TestMakefileBenchGateMalformedBenchmarkOutputFailsClosed(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: benchmarkSourceWithMalformedBenchmarkLine(),
		headBenchmarkSource: passingBenchmarkSource(),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	before := gitWorktreeListOutput(t, repo)

	output, exitCode := runBenchGateMake(t, repo, map[string]string{
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	})
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "Comparison status: invalid") {
		t.Fatalf("output = %q, want invalid comparison status", output)
	}
	if !strings.Contains(output, "unknown-package/BenchmarkMalformed") {
		t.Fatalf("output = %q, want malformed benchmark name to be reported as missing", output)
	}
	assertBenchGateFailureArtifacts(t, summaryPath, statusPath, "unknown-package/BenchmarkMalformed")
	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateThresholdRegressionCanBeNonEnforced(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: allocationBenchmarkSource(1),
		headBenchmarkSource: allocationBenchmarkSource(1024),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	before := gitWorktreeListOutput(t, repo)

	output, exitCode := runBenchGateMake(t, repo, map[string]string{
		"MEMORY_BENCH_BASE":     "HEAD~1",
		"MEMORY_BENCH_PACKAGES": "./benchfixture",
		"BENCH_COUNT":           "1",
		"BENCH_TIME":            "1x",
		"MEMORY_BENCH_ENFORCE":  "0",
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0 for a non-enforced threshold regression\n%s", exitCode, output)
	}
	if !strings.Contains(output, "Result: memory benchmark regression detected.") {
		t.Fatalf("output = %q, want threshold regression result", output)
	}

	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "Result: memory benchmark regression detected.") {
		t.Fatalf("summary = %q, want threshold regression result", summary)
	}
	assertBenchGateStatus(t, statusPath, "1\n")

	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateRejectsSymlinkedArtifactDirAcrossPublishOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	for _, scenario := range benchGatePublishScenarios() {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			assertMakefileBenchGateRejectsSymlinkedArtifactDir(t, scenario)
		})
	}
}

func TestMakefileBenchGateRejectsSymlinkedArtifactTargetsAcrossPublishOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	for _, scenario := range benchGatePublishScenarios() {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			for _, target := range benchGateArtifactTargets() {
				target := target
				t.Run(target.fileName, func(t *testing.T) {
					assertMakefileBenchGateRejectsSymlinkedArtifactTarget(t, scenario, target)
				})
			}
		})
	}
}

func TestMakefileBenchGatePassPublishFixturePassesBeforeArtifactTargetRejection(t *testing.T) {
	t.Parallel()

	scenario := benchGatePassPublishScenario()
	repo := initBenchGateFixtureRepo(t, scenario.fixture)
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")

	output, exitCode := runBenchGateMake(t, repo, scenario.extraEnv)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", exitCode, output)
	}
	if !strings.Contains(output, scenario.outputSubstring) {
		t.Fatalf("output = %q, want %q", output, scenario.outputSubstring)
	}
	assertBenchGateStatus(t, statusPath, "0\n")
	summary := benchGateFileContent(t, summaryPath)
	if !strings.Contains(summary, scenario.outputSubstring) {
		t.Fatalf("summary = %q, want %q", summary, scenario.outputSubstring)
	}
}

func TestBenchgateToolFailureArtifactsRejectSymlinkAncestorPathWithoutOutsideWrites(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingBenchmarkSource(),
	})
	outsideDir := filepath.Join(benchgateCanonicalTempDir(t), "outside")
	outsideExistingDir := filepath.Join(outsideDir, "existing")
	if err := os.MkdirAll(outsideExistingDir, 0o755); err != nil {
		t.Fatalf("mkdir outside existing dir: %v", err)
	}
	summaryPath := filepath.Join(repo, "link", "existing", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, "link", "existing", "memory-bench-status.txt")
	outsideSummaryPath := filepath.Join(outsideExistingDir, "memory-bench-summary.md")
	outsideStatusPath := filepath.Join(outsideExistingDir, "memory-bench-status.txt")
	writeBenchGateFile(t, outsideSummaryPath, "outside summary\n")
	writeBenchGateFile(t, outsideStatusPath, "outside status\n")
	if err := os.Symlink(outsideDir, filepath.Join(repo, "link")); err != nil {
		t.Fatalf("symlink link ancestor: %v", err)
	}

	output, exitCode := runBenchGateTool(t, repo, "-failure-message", "synthetic failure", "-summary-out", summaryPath, "-status-out", statusPath)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "artifact parent contains symlink") {
		t.Fatalf("output = %q, want symlink rejection", output)
	}
	assertBenchGateFileContent(t, outsideSummaryPath, "outside summary\n")
	assertBenchGateFileContent(t, outsideStatusPath, "outside status\n")
}

func TestBenchgateToolPublishRejectsSymlinkAncestorPathAcrossOutcomes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	for _, scenario := range benchGatePublishScenarios() {
		scenario := scenario
		t.Run(scenario.name, func(t *testing.T) {
			assertBenchgateToolPublishRejectsSymlinkAncestorPath(t, scenario)
		})
	}
}

func assertMakefileBenchGateRejectsSymlinkedArtifactDir(t *testing.T, scenario benchGatePublishScenario) {
	t.Helper()

	repo := initBenchGateFixtureRepo(t, scenario.fixture)
	outsideDir := filepath.Join(benchgateCanonicalTempDir(t), "outside-artifacts")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside artifacts: %v", err)
	}
	expected := benchGateOutsideArtifactSentinels(t, outsideDir)
	artifactDir := filepath.Join(repo, ".artifacts")
	if err := os.Symlink(outsideDir, artifactDir); err != nil {
		t.Fatalf("symlink artifact dir: %v", err)
	}

	output, exitCode := runBenchGateMake(t, repo, scenario.extraEnv)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, scenario.outputSubstring) {
		t.Fatalf("output = %q, want %q", output, scenario.outputSubstring)
	}
	for path, want := range expected {
		assertBenchGateFileContent(t, path, want)
	}
	assertBenchGatePathIsSymlink(t, artifactDir)
}

func assertMakefileBenchGateRejectsSymlinkedArtifactTarget(t *testing.T, scenario benchGatePublishScenario, target benchGateArtifactTarget) {
	t.Helper()

	repo := initBenchGateFixtureRepo(t, scenario.fixture)
	artifactDir := filepath.Join(repo, ".artifacts")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	outsidePath := filepath.Join(benchgateCanonicalTempDir(t), "outside-"+target.fileName)
	writeBenchGateFile(t, outsidePath, "outside "+target.fileName+"\n")
	targetPath := filepath.Join(artifactDir, target.fileName)
	if err := os.Symlink(outsidePath, targetPath); err != nil {
		t.Fatalf("symlink %s: %v", target.fileName, err)
	}

	output, exitCode := runBenchGateMake(t, repo, scenario.extraEnv)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, scenario.outputSubstring) {
		t.Fatalf("output = %q, want %q", output, scenario.outputSubstring)
	}
	assertBenchGateFileContent(t, outsidePath, "outside "+target.fileName+"\n")
	assertBenchGatePathIsSymlink(t, targetPath)
	assertOtherBenchGateArtifactsAbsent(t, artifactDir, target.fileName)
}

func assertOtherBenchGateArtifactsAbsent(t *testing.T, artifactDir, excludedFileName string) {
	t.Helper()

	for _, other := range benchGateArtifactTargets() {
		if other.fileName == excludedFileName {
			continue
		}
		assertBenchGatePathAbsent(t, filepath.Join(artifactDir, other.fileName))
	}
}

func assertBenchgateToolPublishRejectsSymlinkAncestorPath(t *testing.T, scenario benchGatePublishScenario) {
	t.Helper()

	repo := initBenchGateFixtureRepo(t, scenario.fixture)
	inputDir := filepath.Join(repo, "inputs")
	outsideDir := filepath.Join(benchgateCanonicalTempDir(t), "outside")
	outsideExistingDir := filepath.Join(outsideDir, "existing")
	if err := os.MkdirAll(outsideExistingDir, 0o755); err != nil {
		t.Fatalf("mkdir outside existing dir: %v", err)
	}
	writeBenchGateFile(t, filepath.Join(inputDir, "bench-base.out"), "base "+scenario.name+"\n")
	writeBenchGateFile(t, filepath.Join(inputDir, "bench-head.out"), "head "+scenario.name+"\n")
	writeBenchGateFile(t, filepath.Join(inputDir, "memory-bench-summary.md"), "summary "+scenario.name+"\n")
	expected := benchGateOutsideArtifactSentinels(t, outsideExistingDir)
	if err := os.Symlink(outsideDir, filepath.Join(repo, "link")); err != nil {
		t.Fatalf("symlink link ancestor: %v", err)
	}

	args := []string{
		"-bench-base-input", filepath.Join(inputDir, "bench-base.out"),
		"-bench-base-out", filepath.Join(repo, "link", "existing", "bench-base.out"),
		"-bench-head-input", filepath.Join(inputDir, "bench-head.out"),
		"-bench-head-out", filepath.Join(repo, "link", "existing", "bench-head.out"),
		"-summary-input", filepath.Join(inputDir, "memory-bench-summary.md"),
		"-summary-out", filepath.Join(repo, "link", "existing", "memory-bench-summary.md"),
		"-status-code", fmt.Sprintf("%d", benchGateStatusCodeForScenario(scenario.name)),
		"-status-out", filepath.Join(repo, "link", "existing", "memory-bench-status.txt"),
	}
	output, exitCode := runBenchGateTool(t, repo, args...)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(output, "artifact parent contains symlink") {
		t.Fatalf("output = %q, want symlink rejection", output)
	}
	for path, want := range expected {
		assertBenchGateFileContent(t, path, want)
	}
}

type benchGateFixture struct {
	baseBenchmarkSource string
	headBenchmarkSource string
}

type benchGatePublishScenario struct {
	name            string
	fixture         benchGateFixture
	extraEnv        map[string]string
	outputSubstring string
}

type benchGateArtifactTarget struct {
	fileName string
}

func benchGatePublishScenarios() []benchGatePublishScenario {
	return []benchGatePublishScenario{
		benchGatePassPublishScenario(),
		{
			name: "regression",
			fixture: benchGateFixture{
				baseBenchmarkSource: allocationBenchmarkSource(1),
				headBenchmarkSource: allocationBenchmarkSource(1024),
			},
			extraEnv: map[string]string{
				"MEMORY_BENCH_BASE":     "HEAD~1",
				"MEMORY_BENCH_PACKAGES": "./benchfixture",
				"BENCH_COUNT":           "1",
				"BENCH_TIME":            "1x",
				"MEMORY_BENCH_ENFORCE":  "0",
			},
			outputSubstring: "Result: memory benchmark regression detected.",
		},
		{
			name: "invalid",
			fixture: benchGateFixture{
				baseBenchmarkSource: passingNamedBenchmarkSource("BenchmarkBaseFixture"),
				headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
			},
			extraEnv: map[string]string{
				"MEMORY_BENCH_BASE":     "HEAD~1",
				"MEMORY_BENCH_PACKAGES": "./benchfixture",
				"BENCH_COUNT":           "1",
				"BENCH_TIME":            "1x",
				"MEMORY_BENCH_ENFORCE":  "0",
			},
			outputSubstring: "Comparison status: invalid",
		},
	}
}

func benchGatePassPublishScenario() benchGatePublishScenario {
	return benchGatePublishScenario{
		name: "pass",
		fixture: benchGateFixture{
			baseBenchmarkSource: passingBenchmarkSource(),
			headBenchmarkSource: passingBenchmarkSource(),
		},
		extraEnv: map[string]string{
			"MEMORY_BENCH_BASE":     "HEAD~1",
			"MEMORY_BENCH_PACKAGES": "./benchfixture",
			"BENCH_COUNT":           "1",
			"BENCH_TIME":            "1x",
		},
		outputSubstring: "Result: memory benchmark gate passed.",
	}
}

func benchGateArtifactTargets() []benchGateArtifactTarget {
	return []benchGateArtifactTarget{
		{fileName: "bench-base.out"},
		{fileName: "bench-head.out"},
		{fileName: "memory-bench-summary.md"},
		{fileName: "memory-bench-status.txt"},
	}
}

func benchGateOutsideArtifactSentinels(t *testing.T, dir string) map[string]string {
	t.Helper()

	expected := make(map[string]string, len(benchGateArtifactTargets()))
	for _, target := range benchGateArtifactTargets() {
		path := filepath.Join(dir, target.fileName)
		content := "outside " + target.fileName + "\n"
		writeBenchGateFile(t, path, content)
		expected[path] = content
	}
	return expected
}

func benchGateStatusCodeForScenario(name string) int {
	switch name {
	case "pass":
		return 0
	case "regression":
		return 1
	case "invalid":
		return 2
	default:
		return 2
	}
}

func initBenchGateFixtureRepo(t *testing.T, fixture benchGateFixture) string {
	t.Helper()

	repo := benchgateCanonicalTempDir(t)
	copyFile(t, repoPath(t, "Makefile"), filepath.Join(repo, "Makefile"))
	copyDir(t, repoPath(t, "tools/benchdelta"), filepath.Join(repo, "tools/benchdelta"))
	copyDir(t, repoPath(t, "tools/benchgate"), filepath.Join(repo, "tools/benchgate"))
	copyDir(t, repoPath(t, "internal/gitexec"), filepath.Join(repo, "internal/gitexec"))
	copyDir(t, repoPath(t, "internal/safeio"), filepath.Join(repo, "internal/safeio"))

	writeBenchGateFile(t, filepath.Join(repo, "go.mod"), "module github.com/ben-ranford/lopper\n\ngo 1.26.5\n")
	writeBenchGateFile(t, filepath.Join(repo, "benchfixture", "bench_test.go"), fixture.baseBenchmarkSource)

	runGitCommand(t, repo, "init")
	runGitCommand(t, repo, "config", "user.name", "Test User")
	runGitCommand(t, repo, "config", "user.email", "test@example.com")
	runGitCommand(t, repo, "add", ".")
	runGitCommand(t, repo, "commit", "-m", "base benchmark fixture")

	writeBenchGateFile(t, filepath.Join(repo, "benchfixture", "bench_test.go"), fixture.headBenchmarkSource)
	if fixture.headBenchmarkSource == fixture.baseBenchmarkSource {
		writeBenchGateFile(t, filepath.Join(repo, "benchfixture", "fixture-head.txt"), "head fixture\n")
	}
	runGitCommand(t, repo, "add", ".")
	runGitCommand(t, repo, "commit", "-m", "head benchmark fixture")

	return repo
}

func benchgateCanonicalTempDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("canonicalize temp dir %s: %v", dir, err)
	}
	return canonicalDir
}

func failingBenchmarkSource(message string) string {
	return "package benchfixture\n\nimport \"testing\"\n\nfunc BenchmarkFixture(b *testing.B) {\n\tb.Fatal(\"" + message + "\")\n}\n"
}

func passingBenchmarkSource() string {
	return stablePassingBenchmarkSource("BenchmarkFixture", "")
}

func passingBenchmarkSourceWithComment(comment string) string {
	return stablePassingBenchmarkSource("BenchmarkFixture", comment)
}

func passingNamedBenchmarkSource(name string) string {
	return stablePassingBenchmarkSource(name, "")
}

func stablePassingBenchmarkSource(name, comment string) string {
	commentLine := ""
	if comment != "" {
		commentLine = "// " + comment + "\n"
	}
	return "package benchfixture\n\nimport \"testing\"\n\nvar benchmarkSink int\n\n" + commentLine + "func " + name + "(b *testing.B) {\n\tb.ReportAllocs()\n\tfor i := 0; i < b.N; i++ {\n\t\tbenchmarkSink += i\n\t}\n}\n"
}

func benchmarkSourceWithMalformedBenchmarkLine() string {
	return "package benchfixture\n\nimport (\n\t\"fmt\"\n\t\"testing\"\n)\n\nfunc BenchmarkFixture(b *testing.B) {\n\tfmt.Println(\"BenchmarkMalformed-8 nope 100 ns/op 100 B/op 1 allocs/op\")\n\tfor i := 0; i < b.N; i++ {\n\t}\n}\n"
}

func allocationBenchmarkSource(size int) string {
	return fmt.Sprintf("package benchfixture\n\nimport \"testing\"\n\nvar benchmarkSink []byte\n\nfunc BenchmarkFixture(b *testing.B) {\n\tb.ReportAllocs()\n\tfor i := 0; i < b.N; i++ {\n\t\tbenchmarkSink = make([]byte, %d)\n\t}\n}\n", size)
}

func runBenchGateMake(t *testing.T, repo string, extraEnv map[string]string) (string, int) {
	t.Helper()

	cmd := exec.Command("make", "bench-gate")
	return runBenchGateCommand(t, cmd, repo, extraEnv)
}

func runBenchGateMakeWithUmask(t *testing.T, repo string, extraEnv map[string]string, umask string) (string, int) {
	t.Helper()

	cmd := exec.Command("sh", "-c", "umask \"$1\" && shift && exec \"$@\"", "bench-gate-make", umask, "make", "bench-gate")
	return runBenchGateCommand(t, cmd, repo, extraEnv)
}

func runBenchGateTool(t *testing.T, repo string, args ...string) (string, int) {
	t.Helper()

	binaryPath := filepath.Join(benchgateCanonicalTempDir(t), "benchgate")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "./tools/benchgate")
	buildOutput, err := runBenchGateCommandOutput(t, buildCmd, repo, nil)
	if err != nil {
		t.Fatalf("build benchgate helper: %v\n%s", err, buildOutput)
	}
	cmd := exec.Command(binaryPath, args...)
	return runBenchGateCommand(t, cmd, repo, nil)
}

func runBenchGateCommandOutput(t *testing.T, cmd *exec.Cmd, repo string, extraEnv map[string]string) ([]byte, error) {
	t.Helper()

	cmd.Dir = repo
	cmd.Env = benchGateCommandEnv(t)
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd.CombinedOutput()
}

func runBenchGateCommand(t *testing.T, cmd *exec.Cmd, repo string, extraEnv map[string]string) (string, int) {
	t.Helper()

	output, err := runBenchGateCommandOutput(t, cmd, repo, extraEnv)
	if err == nil {
		return string(output), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run make bench-gate: %v\n%s", err, output)
	}
	return string(output), exitErr.ExitCode()
}

func assertBenchGateFailureArtifacts(t *testing.T, summaryPath, statusPath, wantMessage string) {
	t.Helper()

	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	hasBlockingSummary := strings.Contains(string(summary), "Comparison could not be evaluated.") ||
		strings.Contains(string(summary), "Comparison status: invalid") ||
		strings.Contains(string(summary), "Comparison status: incomplete")
	if !hasBlockingSummary {
		t.Fatalf("summary = %q, want a blocking invalid or incomplete comparison summary", summary)
	}
	if strings.Contains(string(summary), "passed") || strings.Contains(string(summary), "regression") {
		t.Fatalf("summary = %q, want no threshold result language", summary)
	}
	if !strings.Contains(string(summary), wantMessage) {
		t.Fatalf("summary = %q, want it to contain %q", summary, wantMessage)
	}

	assertBenchGateStatus(t, statusPath, "2\n")
}

func assertBenchGateStatus(t *testing.T, statusPath, want string) {
	t.Helper()

	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if string(status) != want {
		t.Fatalf("status = %q, want %q", status, want)
	}
}

func assertBenchGateFileContent(t *testing.T, path, want string) {
	t.Helper()

	got := benchGateFileContent(t, path)
	if got != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func assertBenchGatePathAbsent(t *testing.T, path string) {
	t.Helper()

	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to remain absent, got err=%v", path, err)
	}
}

func assertBenchGatePathIsSymlink(t *testing.T, path string) {
	t.Helper()

	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to remain a symlink", path)
	}
}

func gitWorktreeListOutput(t *testing.T, repo string) string {
	t.Helper()

	return runBenchGateGitOutput(t, repo, "worktree", "list", "--porcelain")
}

func runGitCommand(t *testing.T, repo string, args ...string) {
	t.Helper()

	runBenchGateGit(t, repo, args...)
}

func benchGateCommandEnv(t *testing.T) []string {
	t.Helper()

	return testutil.IsolatedGitEnv(t)
}

func gitRevParseOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()

	return strings.TrimSpace(runBenchGateGitOutput(t, repo, append([]string{"rev-parse"}, args...)...))
}

func runBenchGateGitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()

	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("resolve git path: %v", err)
	}
	command, err := gitexec.CommandContext(context.Background(), gitPath, append([]string{"-C", repo}, args...)...)
	if err != nil {
		t.Fatalf("construct git %s: %v", strings.Join(args, " "), err)
	}
	command.Env = benchGateCommandEnv(t)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func runBenchGateGit(t *testing.T, repo string, args ...string) {
	t.Helper()

	_ = runBenchGateGitOutput(t, repo, args...)
}

func assertPathHijackGitNotInvoked(t *testing.T, logPath, phase string) {
	t.Helper()

	logData, err := os.ReadFile(logPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read git wrapper log for %s: %v", phase, err)
	}
	if len(logData) != 0 {
		t.Fatalf("PATH-hijacked git wrapper executed during %s: %q", phase, string(logData))
	}
}

func copyFile(t *testing.T, sourcePath, targetPath string) {
	t.Helper()

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read %s: %v", sourcePath, err)
	}
	writeBenchGateFile(t, targetPath, string(data))
}

func copyDir(t *testing.T, sourceDir, targetDir string) {
	t.Helper()

	if err := filepath.WalkDir(sourceDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		return copyDirEntry(sourceDir, targetDir, path, entry)
	}); err != nil {
		t.Fatalf("copy %s -> %s: %v", sourceDir, targetDir, err)
	}
}

func copyDirEntry(sourceDir, targetDir, path string, entry fs.DirEntry) error {
	relativePath, err := filepath.Rel(sourceDir, path)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(targetDir, relativePath)
	if entry.IsDir() {
		return os.MkdirAll(targetPath, 0o755)
	}
	return copyDirFile(path, targetPath)
}

func copyDirFile(sourcePath, targetPath string) error {
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(targetPath, data, 0o600)
}

func writeBenchGateFile(t *testing.T, path, contents string) {
	t.Helper()

	writeFileMode(t, path, contents, 0o600)
}

func benchGateFileContent(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
