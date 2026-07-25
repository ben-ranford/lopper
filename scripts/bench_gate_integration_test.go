package scripts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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

func TestMakefileBenchGateHelperBuildFailureWritesArtifactsAndFailsClosed(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	mustPrecreateBenchGateArtifacts(t, summaryPath, statusPath)
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
	assertBenchGateFailureArtifacts(t, summaryPath, statusPath, "memory benchmark helper ./tools/benchgate build failed")
	assertBenchGateArtifactPermissions(t, filepath.Dir(summaryPath), summaryPath, statusPath)

	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateHelperLaunchFailureWritesArtifactsAndFailsClosed(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	mustPrecreateBenchGateArtifacts(t, summaryPath, statusPath)
	before := gitWorktreeListOutput(t, repo)
	fakeGo := filepath.Join(t.TempDir(), "fake-go")
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
	assertBenchGateFailureArtifacts(t, summaryPath, statusPath, "memory benchmark helper ./tools/benchgate launch failed")
	assertBenchGateArtifactPermissions(t, filepath.Dir(summaryPath), summaryPath, statusPath)

	after := gitWorktreeListOutput(t, repo)
	if after != before {
		t.Fatalf("git worktree list changed after cleanup\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestMakefileBenchGateHelperFailureStillWritesStatusWhenSummaryPathIsBlocked(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingNamedBenchmarkSource("BenchmarkHeadFixture"),
	})
	blocker := filepath.Join(t.TempDir(), "summary-blocker")
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
	if !strings.Contains(output, "benchgate summary artifact write failed") {
		t.Fatalf("output = %q, want summary artifact write failure", output)
	}
	if !strings.Contains(output, "benchgate failure artifact fallback encountered write errors") {
		t.Fatalf("output = %q, want fallback artifact write warning", output)
	}
	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}
}

func TestMakefileBenchGateIgnoresPollutedGitEnvironmentForBenchgateAndWorktreeGit(t *testing.T) {
	t.Parallel()

	repo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingBenchmarkSourceWithComment("head fixture"),
	})
	attackerRepo := initBenchGateFixtureRepo(t, benchGateFixture{
		baseBenchmarkSource: passingBenchmarkSource(),
		headBenchmarkSource: passingBenchmarkSourceWithComment("attacker fixture"),
	})
	attackerWorktreesBefore := gitWorktreeListOutput(t, attackerRepo)

	output, exitCode := runBenchGateMake(t, repo, map[string]string{
		"GIT_DIR":                          filepath.Join(attackerRepo, ".git"),
		"GIT_WORK_TREE":                    attackerRepo,
		"GIT_INDEX_FILE":                   filepath.Join(t.TempDir(), "attacker.index"),
		"GIT_OBJECT_DIRECTORY":             filepath.Join(t.TempDir(), "attacker.objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(t.TempDir(), "attacker.alternates"),
		"GIT_COMMON_DIR":                   filepath.Join(t.TempDir(), "attacker.common"),
		"GIT_NAMESPACE":                    "attacker",
		"MEMORY_BENCH_BASE":                "HEAD~1",
		"MEMORY_BENCH_PACKAGES":            "./benchfixture",
		"BENCH_COUNT":                      "1",
		"BENCH_TIME":                       "1x",
	})
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", exitCode, output)
	}
	if !strings.Contains(output, "Running memory benchmark delta against HEAD~1.") {
		t.Fatalf("output = %q, want normal bench-gate execution", output)
	}
	if after := gitWorktreeListOutput(t, attackerRepo); after != attackerWorktreesBefore {
		t.Fatalf("attacker repo worktree list changed\nbefore:\n%s\nafter:\n%s", attackerWorktreesBefore, after)
	}
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

type benchGateFixture struct {
	baseBenchmarkSource string
	headBenchmarkSource string
}

func initBenchGateFixtureRepo(t *testing.T, fixture benchGateFixture) string {
	t.Helper()

	repo := t.TempDir()
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
	runGitCommand(t, repo, "add", ".")
	runGitCommand(t, repo, "commit", "-m", "head benchmark fixture")

	return repo
}

func failingBenchmarkSource(message string) string {
	return "package benchfixture\n\nimport \"testing\"\n\nfunc BenchmarkFixture(b *testing.B) {\n\tb.Fatal(\"" + message + "\")\n}\n"
}

func passingBenchmarkSource() string {
	return "package benchfixture\n\nimport \"testing\"\n\nfunc BenchmarkFixture(b *testing.B) {\n\tfor i := 0; i < b.N; i++ {\n\t}\n}\n"
}

func passingBenchmarkSourceWithComment(comment string) string {
	return "package benchfixture\n\nimport \"testing\"\n\n// " + comment + "\nfunc BenchmarkFixture(b *testing.B) {\n\tfor i := 0; i < b.N; i++ {\n\t}\n}\n"
}

func passingNamedBenchmarkSource(name string) string {
	return "package benchfixture\n\nimport \"testing\"\n\nfunc " + name + "(b *testing.B) {\n\tfor i := 0; i < b.N; i++ {\n\t}\n}\n"
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

func runBenchGateCommand(t *testing.T, cmd *exec.Cmd, repo string, extraEnv map[string]string) (string, int) {
	t.Helper()

	cmd.Dir = repo
	cmd.Env = benchGateCommandEnv(t)
	for key, value := range extraEnv {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output, err := cmd.CombinedOutput()
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

func gitWorktreeListOutput(t *testing.T, repo string) string {
	t.Helper()

	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repo
	cmd.Env = benchGateCommandEnv(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, output)
	}
	return string(output)
}

func runGitCommand(t *testing.T, repo string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = benchGateCommandEnv(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func benchGateCommandEnv(t *testing.T) []string {
	t.Helper()

	return testutil.IsolatedGitEnv(t)
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
		relativePath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(targetDir, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(targetPath, data, 0o600)
	}); err != nil {
		t.Fatalf("copy %s -> %s: %v", sourceDir, targetDir, err)
	}
}

func writeBenchGateFile(t *testing.T, path, contents string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustPrecreateBenchGateArtifacts(t *testing.T, summaryPath, statusPath string) {
	t.Helper()

	artifactDir := filepath.Dir(summaryPath)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(artifactDir, 0o777); err != nil {
			t.Fatalf("chmod artifact dir: %v", err)
		}
	}
	for _, artifactPath := range []string{summaryPath, statusPath} {
		if err := os.WriteFile(artifactPath, []byte("old\n"), 0o666); err != nil {
			t.Fatalf("precreate artifact %s: %v", artifactPath, err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(artifactPath, 0o666); err != nil {
				t.Fatalf("chmod artifact %s: %v", artifactPath, err)
			}
		}
	}
}

func assertBenchGateArtifactPermissions(t *testing.T, artifactDir string, paths ...string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("exact chmod semantics are not stable on Windows")
	}

	if info, err := os.Stat(artifactDir); err != nil {
		t.Fatalf("stat artifact dir: %v", err)
	} else if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("artifact dir perms = %o, want 750", got)
	}

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat artifact %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("artifact perms for %s = %o, want 600", path, got)
		}
	}
}
