package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/safeio"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestMainSuccessAndFailureArtifacts(t *testing.T) {
	if runBenchgateMainIfRequested(t) {
		return
	}

	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "success", fn: testMainSuccess},
		{name: "missing ref writes status two", fn: testMainMissingRefWritesStatusTwo},
		{name: "missing base-ref required", fn: testMainMissingBaseRefRequired},
		{name: "unrelated ref writes status two", fn: testMainUnrelatedRefWritesStatusTwo},
		{name: "artifact write failure surfaces clearly", fn: testMainArtifactWriteFailureSurfacesClearly},
		{name: "whitespace artifact outputs fail without side effects", fn: testMainRejectsWhitespaceArtifactOutputs},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestResolveBaseCommit(t *testing.T) {
	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	headCommit := gitCommitFile(t, repo, "head.txt", "head")
	gitPath := mustResolveCallerGitPath(t)

	got, err := resolveBaseCommit(repo, gitPath, baseCommit, "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit: %v", err)
	}
	if got != baseCommit {
		t.Fatalf("base commit = %q, want %q (head %q)", got, baseCommit, headCommit)
	}
}

func TestRunMain(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "success", fn: testRunMainSuccess},
		{name: "failure", fn: testRunMainFailure},
		{name: "stdout write failure", fn: testRunMainStdoutWriteFailure},
		{name: "stderr write failure", fn: testRunMainStderrWriteFailure},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestResolveBaseCommitFromArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "success", fn: testResolveBaseCommitFromArgsSuccess},
		{name: "git path output rejects symlink target without outside overwrite", fn: testResolveBaseCommitFromArgsGitPathOutRejectsSymlinkTargetWithoutOutsideOverwrite},
		{name: "git path output requires base ref resolution mode", fn: testResolveBaseCommitFromArgsGitPathOutRequiresBaseRefResolutionMode},
		{name: "git path output rejects whitespace-only value", fn: testResolveBaseCommitFromArgsRejectsWhitespaceGitPathOut},
		{name: "missing base-ref writes artifacts", fn: testResolveBaseCommitFromArgsMissingBaseRefWritesArtifacts},
		{name: "invalid flag returns parse error", fn: testResolveBaseCommitFromArgsInvalidFlagReturnsParseError},
		{name: "status artifact write failure", fn: testResolveBaseCommitFromArgsStatusArtifactWriteFailure},
		{name: "failure message writes artifacts without base ref", fn: testResolveBaseCommitFromArgsFailureMessageWritesArtifactsWithoutBaseRef},
		{name: "failure message rejects base ref before dispatch", fn: testResolveBaseCommitFromArgsRejectsMixedFailureMessageAndBaseRefModes},
		{name: "failure message rejects git path output before dispatch", fn: testResolveBaseCommitFromArgsRejectsMixedFailureMessageAndGitPathOutModes},
		{name: "failure message rejects worktree flags before dispatch", fn: testResolveBaseCommitFromArgsRejectsMixedFailureMessageAndWorktreeModes},
		{name: "publish mode writes artifacts without stdout payload", fn: testResolveBaseCommitFromArgsPublishModeWritesArtifactsWithoutStdoutPayload},
		{name: "publish mode rejects git path output before dispatch", fn: testResolveBaseCommitFromArgsRejectsMixedPublishAndGitPathOutModes},
		{name: "publish mode rejects worktree flags before dispatch", fn: testResolveBaseCommitFromArgsRejectsMixedPublishAndWorktreeModes},
		{name: "worktree mode rejects git path output before dispatch", fn: testResolveBaseCommitFromArgsRejectsMixedWorktreeAndGitPathOutModes},
		{name: "invalid caller git resolution fails closed", fn: testResolveBaseCommitFromArgsInvalidCallerGitResolutionFailsClosed},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestResolveBaseCommitMissingRef(t *testing.T) {
	repo := initGitRepo(t)
	gitCommitFile(t, repo, "head.txt", "head")
	gitPath := mustResolveCallerGitPath(t)

	_, err := resolveBaseCommit(repo, gitPath, "refs/heads/does-not-exist", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "does not resolve to a commit") {
		t.Fatalf("expected missing ref error, got %v", err)
	}
}

func TestResolveBaseCommitUnrelatedHistory(t *testing.T) {
	repo := initGitRepo(t)
	gitCommitFile(t, repo, "main.txt", "main")
	branch := strings.TrimSpace(runGit(t, repo, "branch", "--show-current"))
	runGit(t, repo, "checkout", "--orphan", "other-root")
	removeTrackedFiles(t, repo)
	otherCommit := gitCommitFile(t, repo, "other.txt", "other")
	runGit(t, repo, "checkout", branch)
	gitPath := mustResolveCallerGitPath(t)

	_, err := resolveBaseCommit(repo, gitPath, otherCommit, "HEAD")
	if err == nil || !strings.Contains(err.Error(), "is unrelated to HEAD") {
		t.Fatalf("expected unrelated history error, got %v", err)
	}
}

func TestResolveBaseCommitIgnoresCallerGitEnvironment(t *testing.T) {
	repo := initGitRepo(t)
	want := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")
	gitPath := mustResolveCallerGitPath(t)

	attackerRepo := initGitRepo(t)
	gitCommitFile(t, attackerRepo, "only.txt", "only")

	t.Setenv("GIT_DIR", filepath.Join(attackerRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", attackerRepo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(benchgateCanonicalTempDir(t), "attacker.index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(benchgateCanonicalTempDir(t), "attacker.objects"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(benchgateCanonicalTempDir(t), "attacker.alternates"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(benchgateCanonicalTempDir(t), "attacker.common"))
	t.Setenv("GIT_NAMESPACE", "attacker")

	got, err := resolveBaseCommit(repo, gitPath, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit with polluted git env: %v", err)
	}
	if got != want {
		t.Fatalf("base commit = %q, want %q", got, want)
	}
}

func TestResolveBaseCommitRejectsOptionLikeRefs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		baseRef string
		headRef string
		want    string
	}{
		{name: "base ref long option", baseRef: "--help", headRef: "HEAD", want: "base-ref must not start with '-'"},
		{name: "base ref short option", baseRef: "-C/tmp/attacker", headRef: "HEAD", want: "base-ref must not start with '-'"},
		{name: "head ref option", baseRef: "HEAD", headRef: "--help", want: "head-ref must not start with '-'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreBenchgateSeams(t)
			calls := 0
			gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
				calls++
				return exec.Command("sh", "-c", ":"), nil
			}

			_, err := resolveBaseCommit(benchgateCanonicalTempDir(t), "/usr/bin/git", tc.baseRef, tc.headRef)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
			if calls != 0 {
				t.Fatalf("git command calls = %d, want 0", calls)
			}
		})
	}
}

func TestResolveBaseCommitAndFixturesIgnorePATHGitSentinel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path sentinel shell script is POSIX-only")
	}

	sentinelDir := filepath.Join(benchgateCanonicalTempDir(t), "fake-bin")
	if err := os.MkdirAll(sentinelDir, 0o755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	sentinelPath := filepath.Join(benchgateCanonicalTempDir(t), "fake-git-invoked")
	fakeGitPath := filepath.Join(sentinelDir, "git")
	fakeGit := "#!/bin/sh\n" +
		"echo invoked >" + shellQuotePath(sentinelPath) + "\n" +
		"exit 99\n"
	if err := os.WriteFile(fakeGitPath, []byte(fakeGit), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", sentinelDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")
	gitPath := mustResolveCallerGitPath(t)

	got, err := resolveBaseCommit(repo, gitPath, baseCommit, "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit with fake PATH git: %v", err)
	}
	if got != baseCommit {
		t.Fatalf("base commit = %q, want %q", got, baseCommit)
	}
	if _, err := os.Stat(sentinelPath); !os.IsNotExist(err) {
		t.Fatalf("expected fake PATH git to remain unused, stat err = %v", err)
	}
}

func TestWorktreeMode(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "rejects invalid combinations", fn: testWorktreeModeRejectsInvalidCombinations},
		{name: "add uses trusted git binary", fn: testWorktreeModeAddUsesTrustedGitBinary},
		{name: "remove propagates git stderr", fn: testWorktreeModeRemovePropagatesGitStderr},
		{name: "leading dash path stays an operand", fn: testWorktreeModeLeadingDashPathStaysAnOperand},
		{name: "rejects option-like commit", fn: testWorktreeModeRejectsOptionLikeCommit},
		{name: "resolve trusted git failure propagates", fn: testWorktreeModeResolveTrustedGitFailurePropagates},
		{name: "direct executeWorktree requires an operation", fn: testWorktreeModeDirectExecuteRequiresAnOperation},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestWorktreeAddMaterializesWithoutCheckoutHooksOrIncludedFilters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook and filter sentinel scripts are POSIX-only")
	}

	repo := initGitRepo(t)
	writeBenchgateInputFile(t, filepath.Join(repo, ".gitattributes"), "tracked.txt filter=foo.bar\n")
	writeBenchgateInputFile(t, filepath.Join(repo, "tracked.txt"), "tracked contents\n")
	runGit(t, repo, "add", ".gitattributes", "tracked.txt")
	runGit(t, repo, "commit", "-m", "add filtered fixture")

	filterSentinel := filepath.Join(repo, "filter-sentinel.log")
	filterScript := filepath.Join(repo, "smudge-filter.sh")
	writeBenchgateExecutable(t, filterScript, "#!/bin/sh\nprintf 'smudge\\n' >> "+shellQuotePath(filterSentinel)+"\ncat\n")
	includeConfig := filepath.Join(repo, "included-filters.gitconfig")
	writeBenchgateInputFile(t, includeConfig, fmt.Sprintf("[filter \"foo.bar\"]\n\tsmudge = %s\n\tclean = cat\n\trequired = true\n", filterScript))
	runGit(t, repo, "config", "include.path", "../included-filters.gitconfig")

	hookSentinel := filepath.Join(repo, "hook-sentinel.log")
	writeBenchgateExecutable(t, filepath.Join(repo, ".git", "hooks", "post-checkout"), "#!/bin/sh\nprintf 'post-checkout\\n' >> "+shellQuotePath(hookSentinel)+"\n")

	worktreePath := filepath.Join(benchgateCanonicalTempDir(t), "worktree")
	if _, err := resolveBaseCommitFromArgs([]string{"-worktree-add", worktreePath, "-worktree-commit", "HEAD"}, repo); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	t.Cleanup(func() {
		if _, err := resolveBaseCommitFromArgs([]string{"-worktree-remove", worktreePath}, repo); err != nil {
			t.Errorf("worktree remove cleanup: %v", err)
		}
	})

	assertBenchgateFileContent(t, filepath.Join(worktreePath, "tracked.txt"), "tracked contents\n")
	assertBenchgatePathAbsent(t, filterSentinel)
	assertBenchgatePathAbsent(t, hookSentinel)
}

func TestWorktreeAddNeutralizesIncludedFiltersWithoutPATHHelpers(t *testing.T) {
	t.Setenv("PATH", "")

	repo := initGitRepo(t)
	writeBenchgateInputFile(t, filepath.Join(repo, ".gitattributes"), "tracked.txt filter=foo.bar\n")
	writeBenchgateInputFile(t, filepath.Join(repo, "tracked.txt"), "tracked contents\n")
	runGit(t, repo, "add", ".gitattributes", "tracked.txt")
	runGit(t, repo, "commit", "-m", "add filtered fixture")

	includeConfig := filepath.Join(repo, "included-filters.gitconfig")
	writeBenchgateInputFile(t, includeConfig, "[filter \"foo.bar\"]\n\tsmudge = definitely-missing-filter-helper\n\tprocess = definitely-missing-filter-helper\n\tclean = definitely-missing-filter-helper\n\trequired = true\n")
	runGit(t, repo, "config", "include.path", "../included-filters.gitconfig")

	worktreePath := filepath.Join(benchgateCanonicalTempDir(t), "worktree")
	if _, err := resolveBaseCommitFromArgs([]string{"-worktree-add", worktreePath, "-worktree-commit", "HEAD"}, repo); err != nil {
		t.Fatalf("worktree add with sanitized PATH and included filters: %v", err)
	}
	t.Cleanup(func() {
		if _, err := resolveBaseCommitFromArgs([]string{"-worktree-remove", worktreePath}, repo); err != nil {
			t.Errorf("worktree remove cleanup: %v", err)
		}
	})

	assertBenchgateFileContent(t, filepath.Join(worktreePath, "tracked.txt"), "tracked contents\n")
}

func TestWindowsTrustedGitPathAndWorktreeAddNeutralizeFiltersWithSanitizedPath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only trusted git provenance integration")
	}

	repo := initGitRepo(t)
	writeBenchgateInputFile(t, filepath.Join(repo, ".gitattributes"), "tracked.txt filter=foo.bar\n")
	writeBenchgateInputFile(t, filepath.Join(repo, "tracked.txt"), "tracked contents\n")
	runGit(t, repo, "add", ".gitattributes", "tracked.txt")
	runGit(t, repo, "commit", "-m", "add filtered fixture")

	includeConfig := filepath.Join(repo, "included-filters.gitconfig")
	writeBenchgateInputFile(t, includeConfig, "[filter \"foo.bar\"]\n\tsmudge = definitely-missing-filter-helper.exe\n\tprocess = definitely-missing-filter-helper.exe\n\tclean = definitely-missing-filter-helper.exe\n\trequired = true\n")
	runGit(t, repo, "config", "include.path", "../included-filters.gitconfig")

	t.Setenv("PATH", filepath.Join(benchgateCanonicalTempDir(t), "missing-bin"))

	expectedGitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("resolve trusted git path with provenance validation: %v", err)
	}
	assertWindowsTrustedGitPath(t, expectedGitPath)

	gitPathOut := filepath.Join(benchgateCanonicalTempDir(t), "git-path.txt")

	output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-base-ref", "HEAD", "-git-path-out", gitPathOut)
	if exitCode != 0 {
		t.Fatalf("git path helper exit code = %d, want 0\n%s", exitCode, output)
	}
	assertBenchgateFileContent(t, gitPathOut, expectedGitPath+"\n")
	assertWindowsTrustedGitPath(t, strings.TrimSpace(string(testutilReadFile(t, gitPathOut))))

	worktreePath := filepath.Join(benchgateCanonicalTempDir(t), "worktree")
	output, exitCode = runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-worktree-add", worktreePath, "-worktree-commit", "HEAD")
	if exitCode != 0 {
		t.Fatalf("worktree add with sanitized PATH and neutralized filters exit code = %d, want 0\n%s", exitCode, output)
	}
	t.Cleanup(func() {
		if _, err := resolveBaseCommitFromArgs([]string{"-worktree-remove", worktreePath}, repo); err != nil {
			t.Errorf("worktree remove cleanup: %v", err)
		}
	})

	assertBenchgateFileContent(t, filepath.Join(worktreePath, "tracked.txt"), "tracked contents\n")
}

func assertWindowsTrustedGitPath(t *testing.T, gitPath string) {
	t.Helper()

	if runtime.GOOS != "windows" {
		t.Fatalf("Windows trusted git path assertion requires Windows, got %s", runtime.GOOS)
	}
	if !filepath.IsAbs(gitPath) {
		t.Fatalf("trusted git path = %q, want absolute path", gitPath)
	}
	if filepath.Clean(gitPath) != gitPath {
		t.Fatalf("trusted git path = %q, want clean path", gitPath)
	}
	if !strings.EqualFold(filepath.Ext(gitPath), ".exe") {
		t.Fatalf("trusted git path = %q, want .exe suffix", gitPath)
	}
	allowedRoots := []string{
		filepath.Clean(os.Getenv("ProgramW6432")),
		filepath.Clean(os.Getenv("ProgramFiles")),
		filepath.Clean(os.Getenv("ProgramFiles(x86)")),
	}
	rootMatched := false
	for _, root := range allowedRoots {
		if root == "." || root == "" {
			continue
		}
		rel, err := filepath.Rel(root, gitPath)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			continue
		}
		rootMatched = true
		break
	}
	if !rootMatched {
		t.Fatalf("trusted git path = %q, want Program Files confinement within %#v", gitPath, allowedRoots)
	}
	info, err := os.Lstat(gitPath)
	if err != nil {
		t.Fatalf("stat trusted git path %q: %v", gitPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("trusted git path = %q, want non-reparse regular file", gitPath)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("trusted git path = %q, want regular file", gitPath)
	}
	if !gitexec.ExecutableAvailable(gitPath) {
		t.Fatalf("trusted git path = %q, want provenance-validated executable", gitPath)
	}
}

func testutilReadFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func testMainSuccess(t *testing.T) {
	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")

	output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-base-ref", baseCommit)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0\n%s", exitCode, output)
	}
	if !strings.Contains(string(output), baseCommit) {
		t.Fatalf("output = %q, want it to contain %q", output, baseCommit)
	}
}

func testMainMissingRefWritesStatusTwo(t *testing.T) {
	repo := initGitRepo(t)
	gitCommitFile(t, repo, "head.txt", "head")
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")

	output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-base-ref", "refs/heads/missing", "-summary-out", summaryPath, "-status-out", statusPath)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(string(output), `does not resolve to a commit`) {
		t.Fatalf("expected missing ref diagnostic, got %q", output)
	}
	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, `does not resolve to a commit`)
}

func testMainMissingBaseRefRequired(t *testing.T) {
	repo := initGitRepo(t)
	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")

	output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-summary-out", summaryPath, "-status-out", statusPath)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(string(output), `-base-ref is required`) {
		t.Fatalf("expected missing base-ref diagnostic, got %q", output)
	}
	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, `-base-ref is required`)
}

func testMainUnrelatedRefWritesStatusTwo(t *testing.T) {
	repo := initGitRepo(t)
	gitCommitFile(t, repo, "main.txt", "main")
	branch := strings.TrimSpace(runGit(t, repo, "branch", "--show-current"))
	runGit(t, repo, "checkout", "--orphan", "other-root")
	removeTrackedFiles(t, repo)
	otherCommit := gitCommitFile(t, repo, "other.txt", "other")
	runGit(t, repo, "checkout", branch)

	summaryPath := filepath.Join(repo, ".artifacts", "memory-bench-summary.md")
	statusPath := filepath.Join(repo, ".artifacts", "memory-bench-status.txt")
	output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-base-ref", otherCommit, "-summary-out", summaryPath, "-status-out", statusPath)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(string(output), `is unrelated to HEAD`) {
		t.Fatalf("expected unrelated diagnostic, got %q", output)
	}
	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, `is unrelated to HEAD`)
}

func testMainArtifactWriteFailureSurfacesClearly(t *testing.T) {
	repo := initGitRepo(t)
	blocker := filepath.Join(repo, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	output, exitCode := runBenchgateHelper(t, repo, "TestMainSuccessAndFailureArtifacts", "-base-ref", "refs/heads/missing", "-summary-out", filepath.Join(blocker, "memory-bench-summary.md"), "-status-out", filepath.Join(repo, ".artifacts", "memory-bench-status.txt"))
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	if !strings.Contains(string(output), `write failure artifacts`) {
		t.Fatalf("expected artifact write failure output, got %q", output)
	}
}

func testMainRejectsWhitespaceArtifactOutputs(t *testing.T) {
	for _, tc := range []struct {
		outputFlag string
		inputFlag  string
	}{
		{outputFlag: "summary-out"},
		{outputFlag: "status-out"},
		{outputFlag: "bench-base-out", inputFlag: "bench-base-input"},
		{outputFlag: "bench-head-out", inputFlag: "bench-head-input"},
	} {
		t.Run(tc.outputFlag, func(t *testing.T) { runWhitespaceOutputRejectionCase(t, tc.outputFlag, tc.inputFlag) })
	}
}

func runWhitespaceOutputRejectionCase(t *testing.T, outputFlag, inputFlag string) {
	t.Helper()
	dir := benchgateCanonicalTempDir(t)
	args, allowedEntry := whitespaceOutputArgs(t, dir, outputFlag, inputFlag)

	output, exitCode := runBenchgateHelper(t, dir, "TestMainSuccessAndFailureArtifacts", args...)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2\n%s", exitCode, output)
	}
	assertWhitespaceOutputRejection(t, dir, outputFlag, output, allowedEntry)
}

func whitespaceOutputArgs(t *testing.T, dir, outputFlag, inputFlag string) ([]string, string) {
	t.Helper()
	args := []string{"-" + outputFlag, "   "}
	if inputFlag == "" {
		return args, ""
	}
	allowedEntry := "input.out"
	inputPath := filepath.Join(dir, allowedEntry)
	writeBenchgateInputFile(t, inputPath, "benchmark\n")
	return append([]string{"-" + inputFlag, inputPath}, args...), allowedEntry
}

func assertWhitespaceOutputRejection(t *testing.T, dir, outputFlag string, output []byte, allowedEntry string) {
	t.Helper()
	want := outputFlag + " must not be empty or whitespace"
	if !strings.Contains(string(output), want) {
		t.Fatalf("output = %q, want %q", output, want)
	}
	assertBenchgatePathAbsent(t, filepath.Join(dir, "   "))
	assertOnlyAllowedBenchgateEntry(t, dir, allowedEntry)
}

func assertOnlyAllowedBenchgateEntry(t *testing.T, dir, allowedEntry string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read artifact directory: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != allowedEntry {
			t.Fatalf("unexpected artifact side effect: %s", entry.Name())
		}
	}
}

func testRunMainSuccess(t *testing.T) {
	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")

	var stdout, stderr bytes.Buffer
	exitCode := runMain([]string{"-base-ref", baseCommit}, repo, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if strings.TrimSpace(stdout.String()) != baseCommit {
		t.Fatalf("stdout = %q, want %q", stdout.String(), baseCommit)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func testRunMainFailure(t *testing.T) {
	repo := initGitRepo(t)
	var stdout, stderr bytes.Buffer
	exitCode := runMain([]string{"-base-ref", "refs/heads/missing"}, repo, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `does not resolve to a commit`) {
		t.Fatalf("stderr = %q, want missing ref diagnostic", stderr.String())
	}
}

func testRunMainStdoutWriteFailure(t *testing.T) {
	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")

	stdout := &errorWriter{err: errors.New("stdout write failed")}
	var stderr bytes.Buffer
	exitCode := runMain([]string{"-base-ref", baseCommit}, repo, stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stdout.calls != 1 {
		t.Fatalf("stdout writes = %d, want 1", stdout.calls)
	}
}

func testRunMainStderrWriteFailure(t *testing.T) {
	var stdout bytes.Buffer
	stderr := &errorWriter{err: errors.New("stderr write failed")}
	exitCode := runMain([]string{"-base-ref", "refs/heads/missing"}, benchgateCanonicalTempDir(t), &stdout, stderr)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if stderr.calls != 1 {
		t.Fatalf("stderr writes = %d, want 1", stderr.calls)
	}
}

func testResolveBaseCommitFromArgsSuccess(t *testing.T) {
	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")
	gitPathOut := filepath.Join(repo, "git-path.txt")

	got, err := resolveBaseCommitFromArgs([]string{"-base-ref", baseCommit, "-git-path-out", gitPathOut}, repo)
	if err != nil {
		t.Fatalf("resolve from args: %v", err)
	}
	if got != baseCommit {
		t.Fatalf("base commit = %q, want %q", got, baseCommit)
	}
	gitPathData, err := os.ReadFile(gitPathOut)
	if err != nil {
		t.Fatalf("read git path output: %v", err)
	}
	gitPath := strings.TrimSpace(string(gitPathData))
	if !filepath.IsAbs(gitPath) {
		t.Fatalf("git path output = %q, want absolute path", gitPath)
	}
}

func testResolveBaseCommitFromArgsGitPathOutRejectsSymlinkTargetWithoutOutsideOverwrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")
	outsidePath := filepath.Join(benchgateCanonicalTempDir(t), "outside-git-path.txt")
	writeBenchgateInputFile(t, outsidePath, "outside\n")
	gitPathOut := filepath.Join(repo, "git-path.txt")
	if err := os.Symlink(outsidePath, gitPathOut); err != nil {
		t.Fatalf("symlink git path output: %v", err)
	}

	_, err := resolveBaseCommitFromArgs([]string{"-base-ref", baseCommit, "-git-path-out", gitPathOut}, repo)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	data, readErr := os.ReadFile(outsidePath)
	if readErr != nil {
		t.Fatalf("read outside git path output: %v", readErr)
	}
	if string(data) != "outside\n" {
		t.Fatalf("outside git path output = %q, want unchanged content", data)
	}
	assertBenchgatePathIsSymlink(t, gitPathOut)
}

func testResolveBaseCommitFromArgsGitPathOutRequiresBaseRefResolutionMode(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	gitPathOut := filepath.Join(dir, "git-path.txt")

	_, err := resolveBaseCommitFromArgs([]string{"-git-path-out", gitPathOut}, dir)
	if err == nil || !strings.Contains(err.Error(), "git-path-out requires "+baseRefFlagName+" resolution mode") {
		t.Fatalf("expected git-path-out mode error, got %v", err)
	}
	assertBenchgatePathAbsent(t, gitPathOut)
}

func testResolveBaseCommitFromArgsRejectsWhitespaceGitPathOut(t *testing.T) {
	repo := initGitRepo(t)
	gitCommitFile(t, repo, "head.txt", "head")

	_, err := resolveBaseCommitFromArgs([]string{"-base-ref", "HEAD", "-git-path-out", "   "}, repo)
	if err == nil || !strings.Contains(err.Error(), "git-path-out must not be empty or whitespace") {
		t.Fatalf("expected whitespace git-path-out rejection, got %v", err)
	}
}

func TestValidateExecutionModeRejectsWhitespaceArtifactOutputs(t *testing.T) {
	for _, flagName := range []string{"summary-out", "status-out", "bench-base-out", "bench-head-out"} {
		t.Run(flagName, func(t *testing.T) {
			cfg, err := parseArgs([]string{"-" + flagName, "   "})
			if err != nil {
				t.Fatalf("parse args: %v", err)
			}

			err = validateExecutionMode(cfg)
			want := flagName + " must not be empty or whitespace"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("expected %q, got %v", want, err)
			}
		})
	}
}

func testResolveBaseCommitFromArgsMissingBaseRefWritesArtifacts(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")

	_, err := resolveBaseCommitFromArgs([]string{"-summary-out", summaryPath, "-status-out", statusPath}, dir)
	if err == nil || !strings.Contains(err.Error(), "-base-ref is required") {
		t.Fatalf("expected missing base-ref error, got %v", err)
	}
	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, `-base-ref is required`)
}

func testResolveBaseCommitFromArgsInvalidFlagReturnsParseError(t *testing.T) {
	if _, err := resolveBaseCommitFromArgs([]string{"-nope"}, benchgateCanonicalTempDir(t)); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("expected invalid flag error, got %v", err)
	}
}

func testResolveBaseCommitFromArgsStatusArtifactWriteFailure(t *testing.T) {
	repo := initGitRepo(t)
	blocker := filepath.Join(repo, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	_, err := resolveBaseCommitFromArgs([]string{"-base-ref", "refs/heads/missing", "-summary-out", filepath.Join(repo, ".artifacts", "summary.md"), "-status-out", filepath.Join(blocker, "status.txt")}, repo)
	if err == nil || !strings.Contains(err.Error(), "write failure artifacts") {
		t.Fatalf("expected artifact write failure, got %v", err)
	}
}

func testResolveBaseCommitFromArgsFailureMessageWritesArtifactsWithoutBaseRef(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")

	_, err := resolveBaseCommitFromArgs([]string{"-summary-out", summaryPath, "-status-out", statusPath, "-failure-message", "memory benchmark head execution failed; comparison could not be evaluated"}, dir)
	if err == nil || !strings.Contains(err.Error(), "memory benchmark head execution failed") {
		t.Fatalf("expected failure message error, got %v", err)
	}
	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, "memory benchmark head execution failed")
}

func testResolveBaseCommitFromArgsRejectsMixedFailureMessageAndBaseRefModes(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")

	args := []string{
		"-base-ref", "HEAD",
		"-summary-out", summaryPath,
		"-status-out", statusPath,
		"-failure-message", "memory benchmark head execution failed; comparison could not be evaluated",
	}
	_, err := resolveBaseCommitFromArgs(args, dir)
	if err == nil || !strings.Contains(err.Error(), "failure-message cannot be combined with base-ref") {
		t.Fatalf("expected mixed mode error, got %v", err)
	}
	assertBenchgatePathAbsent(t, summaryPath)
	assertBenchgatePathAbsent(t, statusPath)
}

func testResolveBaseCommitFromArgsRejectsMixedFailureMessageAndGitPathOutModes(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	gitPathOut := filepath.Join(dir, "git-path.txt")

	args := []string{
		"-failure-message", "memory benchmark head execution failed; comparison could not be evaluated",
		"-git-path-out", gitPathOut,
	}
	_, err := resolveBaseCommitFromArgs(args, dir)
	if err == nil || !strings.Contains(err.Error(), "failure-message cannot be combined with base-ref or git-path-out") {
		t.Fatalf("expected mixed mode error, got %v", err)
	}
	assertBenchgatePathAbsent(t, gitPathOut)
}

func testResolveBaseCommitFromArgsRejectsMixedFailureMessageAndWorktreeModes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "worktree add",
			args: []string{
				"-failure-message", "memory benchmark head execution failed; comparison could not be evaluated",
				"-worktree-add", filepath.Join(t.TempDir(), "worktree"),
				"-worktree-commit", "HEAD",
			},
		},
		{
			name: "worktree remove",
			args: []string{
				"-failure-message", "memory benchmark head execution failed; comparison could not be evaluated",
				"-worktree-remove", filepath.Join(t.TempDir(), "worktree"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveBaseCommitFromArgs(tc.args, benchgateCanonicalTempDir(t))
			if err == nil || !strings.Contains(err.Error(), "worktree mode cannot be combined with publish, base-ref, git-path-out, or failure-message") {
				t.Fatalf("expected mixed mode error, got %v", err)
			}
		})
	}
}

func testResolveBaseCommitFromArgsPublishModeWritesArtifactsWithoutStdoutPayload(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	headInput := filepath.Join(dir, "head-input.out")
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, baseInput, "base\n")
	writeBenchgateInputFile(t, headInput, "head\n")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	args := []string{
		"-bench-base-input", baseInput,
		"-bench-base-out", filepath.Join(dir, ".artifacts", "bench-base.out"),
		"-bench-head-input", headInput,
		"-bench-head-out", filepath.Join(dir, ".artifacts", "bench-head.out"),
		"-summary-input", summaryInput,
		"-summary-out", filepath.Join(dir, ".artifacts", "memory-bench-summary.md"),
		"-status-code", "0",
		"-status-out", filepath.Join(dir, ".artifacts", "memory-bench-status.txt"),
	}
	got, err := resolveBaseCommitFromArgs(args, dir)
	if err != nil {
		t.Fatalf("publish mode resolve: %v", err)
	}
	if got != "" {
		t.Fatalf("publish mode output = %q, want empty", got)
	}
}

func testResolveBaseCommitFromArgsRejectsMixedPublishAndGitPathOutModes(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	gitPathOut := filepath.Join(dir, "git-path.txt")
	writeBenchgateInputFile(t, baseInput, "base\n")

	args := []string{
		"-bench-base-input", baseInput,
		"-bench-base-out", filepath.Join(dir, ".artifacts", "bench-base.out"),
		"-git-path-out", gitPathOut,
	}
	_, err := resolveBaseCommitFromArgs(args, dir)
	if err == nil || !strings.Contains(err.Error(), "publish mode cannot be combined with base-ref, git-path-out, or failure-message") {
		t.Fatalf("expected mixed mode error, got %v", err)
	}
	assertBenchgatePathAbsent(t, filepath.Join(dir, ".artifacts", "bench-base.out"))
	assertBenchgatePathAbsent(t, gitPathOut)
}

func testResolveBaseCommitFromArgsRejectsMixedPublishAndWorktreeModes(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	writeBenchgateInputFile(t, baseInput, "base\n")
	summaryOut := filepath.Join(dir, ".artifacts", "memory-bench-summary.md")

	args := []string{
		"-bench-base-input", baseInput,
		"-bench-base-out", filepath.Join(dir, ".artifacts", "bench-base.out"),
		"-worktree-remove", filepath.Join(dir, "worktree"),
		"-summary-out", summaryOut,
	}
	_, err := resolveBaseCommitFromArgs(args, dir)
	if err == nil || !strings.Contains(err.Error(), "publish mode cannot be combined with worktree mode") {
		t.Fatalf("expected mixed mode error, got %v", err)
	}
	assertBenchgatePathAbsent(t, filepath.Join(dir, ".artifacts", "bench-base.out"))
	assertBenchgatePathAbsent(t, summaryOut)
}

func testResolveBaseCommitFromArgsRejectsMixedWorktreeAndGitPathOutModes(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	gitPathOut := filepath.Join(dir, "git-path.txt")

	args := []string{
		"-worktree-remove", filepath.Join(dir, "worktree"),
		"-git-path-out", gitPathOut,
	}
	_, err := resolveBaseCommitFromArgs(args, dir)
	if err == nil || !strings.Contains(err.Error(), "worktree mode cannot be combined with publish, base-ref, git-path-out, or failure-message") {
		t.Fatalf("expected mixed mode error, got %v", err)
	}
	assertBenchgatePathAbsent(t, gitPathOut)
}

func testResolveBaseCommitFromArgsInvalidCallerGitResolutionFailsClosed(t *testing.T) {
	restoreBenchgateSeams(t)
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")
	resolveGitBinaryPath = func() (string, error) { return "", errors.New("git executable not found in trusted locations") }

	_, err := resolveBaseCommitFromArgs([]string{"-base-ref", "HEAD", "-summary-out", summaryPath, "-status-out", statusPath}, dir)
	if err == nil || !strings.Contains(err.Error(), "trusted locations") {
		t.Fatalf("expected invalid git resolution error, got %v", err)
	}
	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, "trusted locations")
}

func testWorktreeModeRejectsInvalidCombinations(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "requires commit for add", args: []string{"-worktree-add", "/tmp/base-tree"}, want: "worktree add requires worktree-commit"},
		{name: "commit requires add", args: []string{"-worktree-commit", "HEAD"}, want: "worktree-commit requires worktree-add"},
		{name: "add and remove cannot be combined", args: []string{"-worktree-add", "/tmp/base-tree", "-worktree-commit", "HEAD", "-worktree-remove", "/tmp/base-tree"}, want: "worktree add and remove cannot be combined"},
		{name: "worktree mode cannot be combined with base-ref", args: []string{"-base-ref", "HEAD", "-worktree-remove", "/tmp/base-tree"}, want: "worktree mode cannot be combined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveBaseCommitFromArgs(tc.args, benchgateCanonicalTempDir(t))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func testWorktreeModeAddUsesTrustedGitBinary(t *testing.T) {
	restoreBenchgateSeams(t)
	repo := benchgateCanonicalTempDir(t)
	resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }
	calls := stubTrustedWorktreeAddCommands(t, "/tmp/base-tree")

	if _, err := resolveBaseCommitFromArgs([]string{"-worktree-add", "/tmp/base-tree", "-worktree-commit", "deadbeef"}, repo); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if *calls != 2 {
		t.Fatalf("git command calls = %d, want 2", *calls)
	}
}

func testWorktreeModeRemovePropagatesGitStderr(t *testing.T) {
	restoreBenchgateSeams(t)
	resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }
	gitCommandContext = func(_ context.Context, path string, args ...string) (*exec.Cmd, error) {
		if path != "/usr/bin/git" {
			t.Fatalf("git path = %q, want trusted git", path)
		}
		if !reflect.DeepEqual(args, []string{"worktree", "remove", "--force", "--", "/tmp/base-tree"}) {
			t.Fatalf("git args = %#v", args)
		}
		return exec.Command("sh", "-c", "echo removal failed >&2; exit 3"), nil
	}

	_, err := resolveBaseCommitFromArgs([]string{"-worktree-remove", "/tmp/base-tree"}, benchgateCanonicalTempDir(t))
	if err == nil || !strings.Contains(err.Error(), "removal failed") {
		t.Fatalf("expected propagated git stderr, got %v", err)
	}
}

func testWorktreeModeLeadingDashPathStaysAnOperand(t *testing.T) {
	restoreBenchgateSeams(t)
	resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }
	stubTrustedWorktreeAddCommands(t, "-tmp/base-tree")

	if _, err := resolveBaseCommitFromArgs([]string{"-worktree-add", "-tmp/base-tree", "-worktree-commit", "deadbeef"}, benchgateCanonicalTempDir(t)); err != nil {
		t.Fatalf("worktree add with leading dash path: %v", err)
	}
}

func testWorktreeModeRejectsOptionLikeCommit(t *testing.T) {
	restoreBenchgateSeams(t)
	resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }
	calls := 0
	gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
		calls++
		return exec.Command("sh", "-c", ":"), nil
	}

	_, err := resolveBaseCommitFromArgs([]string{"-worktree-add", "/tmp/base-tree", "-worktree-commit", "--help"}, benchgateCanonicalTempDir(t))
	if err == nil || !strings.Contains(err.Error(), "worktree-commit must not start with '-'") {
		t.Fatalf("expected worktree commit validation error, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("git command calls = %d, want 0", calls)
	}
}

func testWorktreeModeResolveTrustedGitFailurePropagates(t *testing.T) {
	restoreBenchgateSeams(t)
	expectedErr := errors.New("git executable not found in trusted locations")
	resolveGitBinaryPath = func() (string, error) { return "", expectedErr }

	_, err := resolveBaseCommitFromArgs([]string{"-worktree-remove", "/tmp/base-tree"}, benchgateCanonicalTempDir(t))
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected resolver error, got %v", err)
	}
}

func testWorktreeModeDirectExecuteRequiresAnOperation(t *testing.T) {
	restoreBenchgateSeams(t)
	resolveGitBinaryPath = func() (string, error) { return "/usr/bin/git", nil }

	err := executeWorktree(benchgateCanonicalTempDir(t), config{statusCode: -1})
	if err == nil || !strings.Contains(err.Error(), "worktree mode requires worktree-add or worktree-remove") {
		t.Fatalf("expected missing worktree operation error, got %v", err)
	}
}

func TestExecuteWorktreeRejectsGitPathOutputConflict(t *testing.T) {
	err := executeWorktree(benchgateCanonicalTempDir(t), config{
		gitPathOut: "/tmp/git.txt",
		statusCode: -1,
	})
	if err == nil || !strings.Contains(err.Error(), "worktree mode cannot be combined with publish, base-ref, git-path-out, or failure-message") {
		t.Fatalf("expected worktree/git-path conflict, got %v", err)
	}
}

func testValidateArtifactTargetMissingTarget(t *testing.T) {
	root := &benchgateStubRoot{lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist }}
	if err := validateArtifactTarget(root, "artifact.txt", "/tmp/artifact.txt"); err != nil {
		t.Fatalf("expected missing target to be allowed, got %v", err)
	}
}

func testValidateArtifactTargetNonRegularTarget(t *testing.T) {
	dirInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
	root := &benchgateStubRoot{lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil }}
	err := validateArtifactTarget(root, "artifact.txt", "/tmp/artifact.txt")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular target error, got %v", err)
	}
}

func testValidateArtifactTargetRegularExistingFile(t *testing.T) {
	path := filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt")
	writeBenchgateInputFile(t, path, "artifact\n")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat regular file: %v", err)
	}
	root := &benchgateStubRoot{lstat: func(string) (fs.FileInfo, error) { return info, nil }}
	if err := validateArtifactTarget(root, "artifact.txt", path); err != nil {
		t.Fatalf("expected regular file validation success, got %v", err)
	}
}

func testValidateArtifactTargetSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}
	dir := benchgateCanonicalTempDir(t)
	targetPath := filepath.Join(dir, "target.txt")
	linkPath := filepath.Join(dir, "link.txt")
	writeBenchgateInputFile(t, targetPath, "target\n")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("symlink target: %v", err)
	}
	info, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	root := &benchgateStubRoot{lstat: func(string) (fs.FileInfo, error) { return info, nil }}
	err = validateArtifactTarget(root, "artifact.txt", linkPath)
	if err == nil || !strings.Contains(err.Error(), "artifact target is a symlink") {
		t.Fatalf("expected symlink target error, got %v", err)
	}
}

func testValidateArtifactTargetLstatError(t *testing.T) {
	expectedErr := errors.New("lstat failure")
	root := &benchgateStubRoot{lstat: func(string) (fs.FileInfo, error) { return nil, expectedErr }}
	err := validateArtifactTarget(root, "artifact.txt", "/tmp/artifact.txt")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected lstat error, got %v", err)
	}
}

func testArtifactWriterWriteAndClosePathsChmodError(t *testing.T) {
	restoreBenchgateSeams(t)
	writer := &artifactWriter{roots: make(map[string]safeio.Root), targets: make(map[string]artifactHandle)}
	path := filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt")
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			chmod: func(name string, perm os.FileMode) error {
				if name != "artifact.txt" || perm != artifactFileMode {
					t.Fatalf("unexpected chmod %q %#o", name, perm)
				}
				return errors.New("chmod failure")
			},
		}, "artifact.txt", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return nil }

	err := writer.Write(artifactSpec{path: path, content: []byte("artifact")})
	if err == nil || !strings.Contains(err.Error(), "chmod failure") {
		t.Fatalf("expected chmod failure, got %v", err)
	}
	if _, ok := writer.written[path]; !ok {
		t.Fatal("expected chmod-failed write to remain rollback-tracked")
	}
}

func testArtifactWriterWriteAndClosePathsSuccess(t *testing.T) {
	restoreBenchgateSeams(t)
	writer := &artifactWriter{roots: make(map[string]safeio.Root), targets: make(map[string]artifactHandle)}
	chmodCalls := 0
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			chmod: func(string, os.FileMode) error {
				chmodCalls++
				return nil
			},
		}, "artifact.txt", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return nil }

	if err := writer.Write(artifactSpec{path: filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"), content: []byte("artifact")}); err != nil {
		t.Fatalf("write success path: %v", err)
	}
	if chmodCalls != 1 {
		t.Fatalf("chmod calls = %d, want 1", chmodCalls)
	}
}

func testArtifactWriterWriteAndClosePathsPrepareError(t *testing.T) {
	expectedErr := errors.New("open artifact dir failure")
	restoreBenchgateSeams(t)
	writer := &artifactWriter{roots: make(map[string]safeio.Root), targets: make(map[string]artifactHandle)}
	openArtifactDirFn = func(string) (safeio.Root, string, error) { return nil, "", expectedErr }

	err := writer.Write(artifactSpec{path: filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"), content: []byte("artifact")})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected prepare error, got %v", err)
	}
}

func testArtifactWriterWriteAndClosePathsCloseJoinsNonNilRoots(t *testing.T) {
	err := (&artifactWriter{
		roots: map[string]safeio.Root{
			"/tmp/ok":  nil,
			"/tmp/bad": &benchgateStubRoot{close: func() error { return errors.New("close failure") }},
		},
	}).Close()
	if err == nil || !strings.Contains(err.Error(), "/tmp/bad") {
		t.Fatalf("expected close failure with path context, got %v", err)
	}
}

func testOpenArtifactDirErrorPathsTargetResolutionError(t *testing.T) {
	expectedErr := errors.New("resolve artifact target failure")
	restoreBenchgateSeams(t)
	resolveArtifactPathAbs = func(string) (string, error) { return "", expectedErr }

	_, _, err := openArtifactDir("summary.md")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected target-resolution error, got %v", err)
	}
}

func testOpenArtifactDirErrorPathsAncestorError(t *testing.T) {
	expectedErr := errors.New("ancestor root failure")
	restoreBenchgateSeams(t)
	openArtifactAncestorFn = func(string) (safeio.Root, string, []string, error) { return nil, "", nil, expectedErr }

	_, _, err := openArtifactDir(filepath.Join(benchgateCanonicalTempDir(t), "summary.md"))
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected ancestor-root error, got %v", err)
	}
}

func testOpenArtifactDirErrorPathsOpenChildErrorClosesAncestorRoot(t *testing.T) {
	openErr := errors.New("open child failure")
	closeErr := errors.New("ancestor close failure")
	restoreBenchgateSeams(t)
	openArtifactAncestorFn = func(string) (safeio.Root, string, []string, error) {
		return &benchgateStubRoot{close: func() error { return closeErr }}, "/tmp", []string{"nested"}, nil
	}
	openOrCreateArtifactFn = func(safeio.Root, string, string) (safeio.Root, bool, error) { return nil, false, openErr }

	_, _, err := openArtifactDir(filepath.Join(benchgateCanonicalTempDir(t), "summary.md"))
	if !errors.Is(err, openErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined child-open and ancestor-close errors, got %v", err)
	}
}

func testOpenArtifactDirErrorPathsCloseCurrentAfterChildOpen(t *testing.T) {
	currentCloseErr := errors.New("current close failure")
	nextCloseErr := errors.New("next close failure")
	restoreBenchgateSeams(t)
	openArtifactAncestorFn = func(string) (safeio.Root, string, []string, error) {
		return &benchgateStubRoot{close: func() error { return currentCloseErr }}, "/tmp", []string{"nested"}, nil
	}
	openOrCreateArtifactFn = func(safeio.Root, string, string) (safeio.Root, bool, error) {
		return &benchgateStubRoot{close: func() error { return nextCloseErr }}, false, nil
	}

	_, _, err := openArtifactDir(filepath.Join(benchgateCanonicalTempDir(t), "summary.md"))
	if !errors.Is(err, currentCloseErr) || !errors.Is(err, nextCloseErr) {
		t.Fatalf("expected joined current and next close errors, got %v", err)
	}
}

func testOpenArtifactDirErrorPathsChmodErrorClosesCurrentRoot(t *testing.T) {
	chmodErr := errors.New("chmod failure")
	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	openArtifactAncestorFn = func(string) (safeio.Root, string, []string, error) {
		return &benchgateStubRoot{close: func() error { return nil }}, "/tmp", []string{"nested"}, nil
	}
	openOrCreateArtifactFn = func(safeio.Root, string, string) (safeio.Root, bool, error) {
		return &benchgateStubRoot{
			chmod: func(name string, perm os.FileMode) error {
				if name != "." || perm != artifactDirMode {
					t.Fatalf("unexpected chmod %q %#o", name, perm)
				}
				return chmodErr
			},
			close: func() error { return closeErr },
		}, true, nil
	}

	_, _, err := openArtifactDir(filepath.Join(benchgateCanonicalTempDir(t), "summary.md"))
	if !errors.Is(err, chmodErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined chmod and close errors, got %v", err)
	}
}

func testOpenArtifactAncestorRootRejectsFileParent(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	filePath := filepath.Join(dir, "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file parent: %v", err)
	}

	_, _, _, err := openArtifactAncestorRoot(filePath)
	if err == nil || !strings.Contains(err.Error(), "artifact parent is not a directory") {
		t.Fatalf("expected file-parent rejection, got %v", err)
	}
}

func testOpenArtifactAncestorRootRejectsSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}
	dir := benchgateCanonicalTempDir(t)
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real dir: %v", err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink parent: %v", err)
	}

	_, _, _, err := openArtifactAncestorRoot(linkDir)
	if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
		t.Fatalf("expected symlink-parent rejection, got %v", err)
	}
}

func testOpenArtifactAncestorRootRejectsSymlinkAncestorOnArtifactPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}
	dir := benchgateCanonicalTempDir(t)
	outsideDir := filepath.Join(dir, "outside")
	if err := os.MkdirAll(filepath.Join(outsideDir, "existing"), 0o755); err != nil {
		t.Fatalf("mkdir outside existing dir: %v", err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(outsideDir, linkDir); err != nil {
		t.Fatalf("symlink ancestor: %v", err)
	}

	_, _, err := openArtifactDir(filepath.Join(linkDir, "existing", "file"))
	if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
		t.Fatalf("expected symlink-ancestor rejection, got %v", err)
	}
}

func testOpenArtifactAncestorRootInternalErrorPathsOpenChildError(t *testing.T) {
	volumeRoot := filepath.VolumeName(benchgateCanonicalTempDir(t)) + string(os.PathSeparator)
	dirInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
	expectedErr := errors.New("open child failure")
	restoreBenchgateSeams(t)
	openCanonicalArtifactRoot = func(path string) (safeio.Root, error) {
		if path != volumeRoot {
			t.Fatalf("open root path = %q, want %q", path, volumeRoot)
		}
		return &benchgateStubRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if name != "tmp" {
					t.Fatalf("unexpected lstat %q", name)
				}
				return dirInfo, nil
			},
			openRoot: func(name string) (safeio.Root, error) {
				if name != "tmp" {
					t.Fatalf("unexpected open root %q", name)
				}
				return nil, expectedErr
			},
		}, nil
	}

	_, _, _, err := openArtifactAncestorRoot(filepath.Join(volumeRoot, "tmp"))
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected open-child error, got %v", err)
	}
}

func testOpenArtifactAncestorRootInternalErrorPathsChildStatError(t *testing.T) {
	volumeRoot := filepath.VolumeName(benchgateCanonicalTempDir(t)) + string(os.PathSeparator)
	dirInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
	expectedErr := errors.New("child stat failure")
	restoreBenchgateSeams(t)
	openCanonicalArtifactRoot = stubArtifactRootWithChild(dirInfo, func(string) (fs.FileInfo, error) { return nil, expectedErr })

	_, _, _, err := openArtifactAncestorRoot(filepath.Join(volumeRoot, "tmp"))
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected child-stat error, got %v", err)
	}
}

func testOpenArtifactAncestorRootInternalErrorPathsChildChangedWhileOpening(t *testing.T) {
	volumeRoot := filepath.VolumeName(benchgateCanonicalTempDir(t)) + string(os.PathSeparator)
	dirInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
	restoreBenchgateSeams(t)
	changedInfo := statBenchgatePath(t, filepath.Dir(benchgateCanonicalTempDir(t)))
	openCanonicalArtifactRoot = stubArtifactRootWithChild(dirInfo, func(string) (fs.FileInfo, error) { return changedInfo, nil })

	_, _, _, err := openArtifactAncestorRoot(filepath.Join(volumeRoot, "tmp"))
	if err == nil || !strings.Contains(err.Error(), "artifact parent changed while opening") {
		t.Fatalf("expected changed-while-opening error, got %v", err)
	}
}

func testOpenArtifactAncestorRootInternalErrorPathsCurrentCloseError(t *testing.T) {
	volumeRoot := filepath.VolumeName(benchgateCanonicalTempDir(t)) + string(os.PathSeparator)
	dirInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
	expectedErr := errors.New("current close failure")
	restoreBenchgateSeams(t)
	openCanonicalArtifactRoot = func(string) (safeio.Root, error) {
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
			openRoot: func(string) (safeio.Root, error) {
				return &benchgateStubRoot{lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil }}, nil
			},
			close: func() error { return expectedErr },
		}, nil
	}

	_, _, _, err := openArtifactAncestorRoot(filepath.Join(volumeRoot, "tmp"))
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected current-close error, got %v", err)
	}
}

func testOpenOrCreateArtifactDirErrorPathsMkdirError(t *testing.T) {
	mkdirErr := errors.New("mkdir failure")
	root := &benchgateStubRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
		mkdir: func(string, os.FileMode) error { return mkdirErr },
	}

	next, _, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
	if next != nil {
		t.Fatal("expected mkdir failure to keep child root nil")
	}
	if !errors.Is(err, mkdirErr) {
		t.Fatalf("expected mkdir error, got %v", err)
	}
}

func testOpenOrCreateArtifactDirErrorPathsMkdirRacedWithConcurrentCreate(t *testing.T) {
	dirInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
	child := &benchgateStubRoot{lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil }}
	lstatCalls := 0
	root := &benchgateStubRoot{
		lstat: func(string) (fs.FileInfo, error) {
			lstatCalls++
			if lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return dirInfo, nil
		},
		mkdir:    func(string, os.FileMode) error { return fs.ErrExist },
		openRoot: func(string) (safeio.Root, error) { return child, nil },
	}

	next, created, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
	if err != nil {
		t.Fatalf("expected concurrent create success, got %v", err)
	}
	if created {
		t.Fatal("created = true, want false when another process won the race")
	}
	if next == nil {
		t.Fatal("expected child root on concurrent create")
	}
	if err := next.Close(); err != nil {
		t.Fatalf("close child root: %v", err)
	}
}

func testOpenOrCreateArtifactDirErrorPathsLookupAfterMkdirError(t *testing.T) {
	expectedErr := errors.New("lookup after mkdir failure")
	lstatCalls := 0
	root := &benchgateStubRoot{
		lstat: func(string) (fs.FileInfo, error) {
			lstatCalls++
			if lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return nil, expectedErr
		},
		mkdir: func(string, os.FileMode) error { return nil },
	}

	next, _, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
	if next != nil {
		t.Fatal("expected lookup-after-mkdir failure to keep child root nil")
	}
	if err == nil || !strings.Contains(err.Error(), "lookup after mkdir failure") {
		t.Fatalf("expected lookup-after-mkdir error, got %v", err)
	}
}

func testOpenOrCreateArtifactDirErrorPathsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}
	dir := benchgateCanonicalTempDir(t)
	targetDir := filepath.Join(dir, "target")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	linkPath := filepath.Join(dir, "link")
	if err := os.Symlink(targetDir, linkPath); err != nil {
		t.Fatalf("symlink child: %v", err)
	}
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	root := &benchgateStubRoot{lstat: func(string) (fs.FileInfo, error) { return linkInfo, nil }}

	next, _, err := openOrCreateArtifactDir(root, "link", linkPath)
	if next != nil {
		t.Fatal("expected symlink child root to remain nil")
	}
	if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func testOpenOrCreateArtifactDirErrorPathsNonDirectory(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	filePath := filepath.Join(dir, "file")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write child file: %v", err)
	}
	fileInfo, err := os.Lstat(filePath)
	if err != nil {
		t.Fatalf("lstat file: %v", err)
	}
	root := &benchgateStubRoot{lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil }}

	next, _, err := openOrCreateArtifactDir(root, "file", filePath)
	if next != nil {
		t.Fatal("expected non-directory child root to remain nil")
	}
	if err == nil || !strings.Contains(err.Error(), "artifact parent is not a directory") {
		t.Fatalf("expected non-directory rejection, got %v", err)
	}
}

func testOpenOrCreateArtifactDirErrorPathsOpenRootError(t *testing.T) {
	expectedInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
	openErr := errors.New("open root failure")
	root := &benchgateStubRoot{
		lstat:    func(string) (fs.FileInfo, error) { return expectedInfo, nil },
		openRoot: func(string) (safeio.Root, error) { return nil, openErr },
	}

	next, _, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
	if next != nil {
		t.Fatal("expected open-root failure to keep child root nil")
	}
	if !errors.Is(err, openErr) {
		t.Fatalf("expected open-root error, got %v", err)
	}
}

func testOpenOrCreateArtifactDirErrorPathsChangedWhileOpening(t *testing.T) {
	expectedInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
	changedInfo := statBenchgatePath(t, filepath.Dir(benchgateCanonicalTempDir(t)))
	childClosed := false
	root := &benchgateStubRoot{
		lstat: func(string) (fs.FileInfo, error) { return expectedInfo, nil },
		openRoot: func(string) (safeio.Root, error) {
			return &benchgateStubRoot{
				lstat: func(string) (fs.FileInfo, error) { return changedInfo, nil },
				close: func() error {
					childClosed = true
					return nil
				},
			}, nil
		},
	}

	next, _, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
	if next != nil {
		t.Fatal("expected changed directory rejection")
	}
	if err == nil || !strings.Contains(err.Error(), "artifact parent changed while opening") {
		t.Fatalf("expected changed-directory error, got %v", err)
	}
	if !childClosed {
		t.Fatal("expected changed child root to be closed")
	}
}

func testOpenOrCreateArtifactDirErrorPathsChildStatAndCloseErrorsJoin(t *testing.T) {
	expectedInfo := statBenchgatePath(t, benchgateCanonicalTempDir(t))
	childStatErr := errors.New("child stat failure")
	childCloseErr := errors.New("child close failure")
	root := &benchgateStubRoot{
		lstat: func(string) (fs.FileInfo, error) { return expectedInfo, nil },
		openRoot: func(string) (safeio.Root, error) {
			return &benchgateStubRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, childStatErr },
				close: func() error { return childCloseErr },
			}, nil
		},
	}

	next, _, err := openOrCreateArtifactDir(root, "artifacts", "/tmp/artifacts")
	if next != nil {
		t.Fatal("expected failed child root to remain nil")
	}
	if !errors.Is(err, childStatErr) || !errors.Is(err, childCloseErr) {
		t.Fatalf("expected joined child stat and close errors, got %v", err)
	}
}

func TestGitOutputPropagatesCommandErrors(t *testing.T) {
	t.Run("command context", func(t *testing.T) {
		expectedErr := errors.New("command creation failure")
		restoreBenchgateSeams(t)
		gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
			return nil, expectedErr
		}

		_, err := gitOutput(benchgateCanonicalTempDir(t), "/custom/git", "status")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected command creation error, got %v", err)
		}
	})
}

func TestResolveBaseCommitPreservesWrappedGitErrors(t *testing.T) {
	expectedErr := errors.New("command creation failure")
	restoreBenchgateSeams(t)
	gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return nil, expectedErr
	}

	_, err := resolveBaseCommit(benchgateCanonicalTempDir(t), "/custom/git", "HEAD", "HEAD")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected wrapped git command error, got %v", err)
	}
}

func TestResolveBaseCommitWrapsUnexpectedMergeBaseErrors(t *testing.T) {
	restoreBenchgateSeams(t)
	calls := 0
	gitCommandContext = func(_ context.Context, _ string, _ ...string) (*exec.Cmd, error) {
		calls++
		if calls == 1 {
			return exec.Command("sh", "-c", "exit 0"), nil
		}
		return exec.Command("sh", "-c", "exit 2"), nil
	}

	_, err := resolveBaseCommit(benchgateCanonicalTempDir(t), "/usr/bin/git", "HEAD", "HEAD")
	if err == nil || !strings.Contains(err.Error(), `resolve merge-base for "HEAD" and HEAD`) {
		t.Fatalf("expected wrapped merge-base error, got %v", err)
	}
}

func TestRunGitCommandPropagatesEmptyOutputErrors(t *testing.T) {
	restoreBenchgateSeams(t)
	expectedErr := errors.New("command creation failure")
	gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return nil, expectedErr
	}

	err := runGitCommand(benchgateCanonicalTempDir(t), "/usr/bin/git", "worktree", "remove", "--force", "/tmp/base-tree")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected command creation failure, got %v", err)
	}

	restoreBenchgateSeams(t)
	gitCommandContext = func(_ context.Context, path string, args ...string) (*exec.Cmd, error) {
		if path != "/usr/bin/git" {
			t.Fatalf("git path = %q, want trusted git", path)
		}
		return exec.Command("sh", "-c", "exit 4"), nil
	}

	err = runGitCommand(benchgateCanonicalTempDir(t), "/usr/bin/git", "worktree", "remove", "--force", "/tmp/base-tree")
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected raw exit error without wrapped stderr, got %T %v", err, err)
	}
}

func TestWriteFailureArtifacts(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "artifacts", "summary.md")
	statusPath := filepath.Join(dir, "artifacts", "status.txt")

	if err := writeFailureArtifacts(summaryPath, statusPath, "memory benchmark base ref \"missing\" does not resolve to a commit"); err != nil {
		t.Fatalf("write failure artifacts: %v", err)
	}

	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "Comparison could not be evaluated.") || strings.Contains(string(summary), "Result: memory benchmark gate passed.") {
		t.Fatalf("unexpected summary: %q", summary)
	}

	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}
}

func TestWriteFailureArtifactsCreatesRestrictedArtifactDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not stable on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	artifactDir := filepath.Join(dir, "artifacts")
	summaryPath := filepath.Join(artifactDir, "summary.md")
	statusPath := filepath.Join(artifactDir, "status.txt")

	if err := writeFailureArtifacts(summaryPath, statusPath, "missing base ref"); err != nil {
		t.Fatalf("write failure artifacts: %v", err)
	}

	info, err := os.Stat(artifactDir)
	if err != nil {
		t.Fatalf("stat artifact dir: %v", err)
	}
	got := info.Mode().Perm() & 0o007
	if got != 0 {
		t.Fatalf("artifact dir perms = %o, want no world access", info.Mode().Perm())
	}
}

func TestWriteFailureArtifactsRejectsUnsafePreexistingArtifactRootWithoutMutatingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not stable on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	artifactDir := filepath.Join(dir, ".artifacts")
	summaryPath := filepath.Join(artifactDir, "memory-bench-summary.md")
	statusPath := filepath.Join(artifactDir, "memory-bench-status.txt")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	if err := os.Chmod(artifactDir, 0o777); err != nil {
		t.Fatalf("chmod artifact dir: %v", err)
	}
	if err := os.WriteFile(summaryPath, []byte("stale summary\n"), 0o666); err != nil {
		t.Fatalf("write stale summary: %v", err)
	}
	if err := os.Chmod(summaryPath, 0o666); err != nil {
		t.Fatalf("chmod stale summary: %v", err)
	}
	if err := os.WriteFile(statusPath, []byte("1\n"), 0o666); err != nil {
		t.Fatalf("write stale status: %v", err)
	}
	if err := os.Chmod(statusPath, 0o666); err != nil {
		t.Fatalf("chmod stale status: %v", err)
	}

	err := writeFailureArtifacts(summaryPath, statusPath, "missing base ref")
	if err == nil || !strings.Contains(err.Error(), "artifact root has unsafe permissions") {
		t.Fatalf("expected unsafe artifact root error, got %v", err)
	}
	assertBenchgateFileContent(t, summaryPath, "stale summary\n")
	assertBenchgateFileContent(t, statusPath, "1\n")
	assertPathMode(t, artifactDir, 0o777)
	assertPathMode(t, summaryPath, 0o666)
	assertPathMode(t, statusPath, 0o666)
}

func TestWriteFailureArtifactsCreatesExactPermissionsUnderRestrictiveUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not stable on Windows")
	}
	if !supportsProcessUmask() {
		t.Skip("process umask control unavailable on this platform")
	}

	dir := benchgateCanonicalTempDir(t)
	artifactDir := filepath.Join(dir, ".artifacts")
	summaryPath := filepath.Join(artifactDir, "memory-bench-summary.md")
	statusPath := filepath.Join(artifactDir, "memory-bench-status.txt")

	withProcessUmask(t, 0o077, func() {
		if err := writeFailureArtifacts(summaryPath, statusPath, "missing base ref"); err != nil {
			t.Fatalf("write failure artifacts under restrictive umask: %v", err)
		}
	})

	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, "missing base ref")
	assertPathMode(t, artifactDir, artifactDirMode)
	assertPathMode(t, summaryPath, artifactFileMode)
	assertPathMode(t, statusPath, artifactFileMode)
}

func TestWriteFailureArtifactsStatusOnly(t *testing.T) {
	statusPath := filepath.Join(benchgateCanonicalTempDir(t), "status.txt")
	if err := writeFailureArtifacts("", statusPath, "no summary"); err != nil {
		t.Fatalf("write status-only failure artifacts: %v", err)
	}

	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}
}

func TestWriteFailureArtifactsSummaryOnly(t *testing.T) {
	summaryPath := filepath.Join(benchgateCanonicalTempDir(t), "summary.md")
	if err := writeFailureArtifacts(summaryPath, "", "summary only"); err != nil {
		t.Fatalf("write summary-only failure artifacts: %v", err)
	}

	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "summary only") {
		t.Fatalf("summary = %q, want it to contain %q", summary, "summary only")
	}
}

func TestWriteFailureArtifactsStatusWriteError(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	statusPath := filepath.Join(dir, "status-dir")
	if err := os.Mkdir(statusPath, 0o755); err != nil {
		t.Fatalf("mkdir status path: %v", err)
	}

	err := writeFailureArtifacts("", statusPath, "status write error")
	if err == nil {
		t.Fatal("expected status write error")
	}
}

func TestWriteFailureArtifactsSummaryWriteErrorStillWritesStatus(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")
	summaryWriteErr := errors.New("summary write failure")
	restoreBenchgateSeams(t)
	writeArtifactWithinRoot = func(root safeio.Root, name string, data []byte, mode os.FileMode) error {
		if name == filepath.Base(summaryPath) {
			return summaryWriteErr
		}
		return safeio.WriteFileReplacingWithinRoot(root, name, data, mode)
	}

	err := writeFailureArtifacts(summaryPath, statusPath, "summary write error")
	if err == nil {
		t.Fatal("expected summary write error")
	}
	if !strings.Contains(err.Error(), "summary artifact:") || !errors.Is(err, summaryWriteErr) {
		t.Fatalf("error = %v, want summary artifact failure", err)
	}
	status, readErr := os.ReadFile(statusPath)
	if readErr != nil {
		t.Fatalf("read status: %v", readErr)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}
}

func TestWriteFailureArtifactsStatusWriteErrorStillWritesSummary(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status-dir")
	if err := os.Mkdir(statusPath, 0o755); err != nil {
		t.Fatalf("mkdir status path: %v", err)
	}

	err := writeFailureArtifacts(summaryPath, statusPath, "status write error")
	if err == nil {
		t.Fatal("expected status write error")
	}
	if !strings.Contains(err.Error(), "status artifact:") {
		t.Fatalf("error = %v, want status artifact failure", err)
	}
	summary, readErr := os.ReadFile(summaryPath)
	if readErr != nil {
		t.Fatalf("read summary: %v", readErr)
	}
	if !strings.Contains(string(summary), "status write error") {
		t.Fatalf("summary = %q, want it to contain %q", summary, "status write error")
	}
}

func TestWriteFailureArtifactsAggregatesWriteErrors(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")
	summaryWriteErr := errors.New("summary write failure")
	statusWriteErr := errors.New("status write failure")
	restoreBenchgateSeams(t)
	writeArtifactWithinRoot = func(root safeio.Root, name string, data []byte, mode os.FileMode) error {
		switch name {
		case filepath.Base(summaryPath):
			return summaryWriteErr
		case filepath.Base(statusPath):
			return statusWriteErr
		default:
			return safeio.WriteFileReplacingWithinRoot(root, name, data, mode)
		}
	}

	err := writeFailureArtifacts(summaryPath, statusPath, "both writes fail")
	if err == nil {
		t.Fatal("expected aggregate write error")
	}
	if !strings.Contains(err.Error(), "summary artifact:") || !errors.Is(err, summaryWriteErr) {
		t.Fatalf("error = %v, want summary artifact failure", err)
	}
	if !strings.Contains(err.Error(), "status artifact:") || !errors.Is(err, statusWriteErr) {
		t.Fatalf("error = %v, want status artifact failure", err)
	}
}

func TestWriteFailureArtifactsRejectsSymlinkArtifactDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	realDir := filepath.Join(dir, "real-artifacts")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real artifact dir: %v", err)
	}
	linkDir := filepath.Join(dir, "link-artifacts")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink artifact dir: %v", err)
	}

	err := writeFailureArtifacts(filepath.Join(linkDir, "summary.md"), filepath.Join(linkDir, "status.txt"), "blocked by symlink")
	if err == nil {
		t.Fatal("expected symlink artifact directory error")
	}
	if !strings.Contains(err.Error(), "artifact parent contains symlink") {
		t.Fatalf("error = %v, want symlink parent failure", err)
	}
}

func TestWriteFailureArtifactsRejectsSymlinkSummaryTargetStillWritesStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")
	outsidePath := filepath.Join(dir, "outside-summary.md")
	if err := os.WriteFile(outsidePath, []byte("outside\n"), 0o644); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	if err := os.Symlink(outsidePath, summaryPath); err != nil {
		t.Fatalf("symlink summary target: %v", err)
	}

	err := writeFailureArtifacts(summaryPath, statusPath, "blocked by symlink target")
	if err == nil {
		t.Fatal("expected symlink summary target error")
	}
	if !strings.Contains(err.Error(), "summary artifact:") {
		t.Fatalf("error = %v, want summary artifact failure", err)
	}
	if !strings.Contains(err.Error(), "target path is a symlink") {
		t.Fatalf("error = %v, want symlink target failure", err)
	}

	status, readErr := os.ReadFile(statusPath)
	if readErr != nil {
		t.Fatalf("read status: %v", readErr)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}

	outside, readErr := os.ReadFile(outsidePath)
	if readErr != nil {
		t.Fatalf("read outside target: %v", readErr)
	}
	if string(outside) != "outside\n" {
		t.Fatalf("outside target = %q, want unchanged content", outside)
	}
}

func TestWriteFailureArtifactsRejectsNormalizedOutputCollisionBeforeWriting(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "nested", "..", "shared.txt")
	statusPath := filepath.Join(dir, "shared.txt")

	err := writeFailureArtifacts(summaryPath, statusPath, "collision")
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("expected output collision, got %v", err)
	}
	assertBenchgatePathAbsent(t, filepath.Join(dir, "shared.txt"))
}

func TestValidateDistinctArtifactOutputsRejectsSymlinkAncestorWithoutTouchingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	repoDir := filepath.Join(dir, "repo")
	outsideDir := filepath.Join(dir, "outside", "existing")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("mkdir repo dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	sentinelPath := filepath.Join(outsideDir, "sentinel")
	writeBenchgateInputFile(t, sentinelPath, "untouched\n")
	sentinelTime := time.Unix(1_234_567, 0)
	if err := os.Chtimes(outsideDir, sentinelTime, sentinelTime); err != nil {
		t.Fatalf("set outside sentinel timestamp: %v", err)
	}
	linkDir := filepath.Join(repoDir, "link")
	if err := os.Symlink(filepath.Dir(outsideDir), linkDir); err != nil {
		t.Fatalf("symlink outside dir: %v", err)
	}

	err := validateDistinctArtifactOutputs(artifactOutput{
		label: "summary",
		path:  filepath.Join(linkDir, filepath.Base(outsideDir), "artifact.out"),
	})
	if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}

	entries, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(sentinelPath) {
		t.Fatalf("outside dir changed: %#v", entries)
	}
	info, err := os.Stat(outsideDir)
	if err != nil {
		t.Fatalf("stat outside dir: %v", err)
	}
	if !info.ModTime().Equal(sentinelTime) {
		t.Fatalf("outside dir was touched: modtime=%v want=%v", info.ModTime(), sentinelTime)
	}
}

func TestValidateDistinctArtifactOutputsRejectsWhitespacePaths(t *testing.T) {
	if err := validateDistinctArtifactOutputs(artifactOutput{label: "status", path: ""}); err != nil {
		t.Fatalf("absent output path should be ignored, got %v", err)
	}

	err := validateDistinctArtifactOutputs(artifactOutput{label: "summary", path: "   "})
	if err == nil || !strings.Contains(err.Error(), "summary-out must not be empty or whitespace") {
		t.Fatalf("expected whitespace output rejection, got %v", err)
	}
}

func TestValidateDistinctArtifactOutputsWrapsResolutionError(t *testing.T) {
	expectedErr := errors.New("absolute path failure")
	restoreBenchgateSeams(t)
	resolveArtifactPathAbs = func(string) (string, error) {
		return "", expectedErr
	}

	err := validateDistinctArtifactOutputs(artifactOutput{label: "summary", path: "summary.md"})
	if err == nil || !strings.Contains(err.Error(), "summary artifact:") {
		t.Fatalf("expected wrapped resolution error, got %v", err)
	}
}

func TestPublishArtifactsWritesBenchOutputsSummaryAndStatus(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	headInput := filepath.Join(dir, "head-input.out")
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, baseInput, "base\n")
	writeBenchgateInputFile(t, headInput, "head\n")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	cfg := config{
		benchBaseInput: baseInput,
		benchBaseOut:   filepath.Join(dir, ".artifacts", "bench-base.out"),
		benchHeadInput: headInput,
		benchHeadOut:   filepath.Join(dir, ".artifacts", "bench-head.out"),
		summaryInput:   summaryInput,
		summaryOut:     filepath.Join(dir, ".artifacts", "memory-bench-summary.md"),
		statusCode:     1,
		statusOut:      filepath.Join(dir, ".artifacts", "memory-bench-status.txt"),
	}

	if err := publishArtifacts(cfg); err != nil {
		t.Fatalf("publish artifacts: %v", err)
	}

	assertBenchgateFileContent(t, cfg.benchBaseOut, "base\n")
	assertBenchgateFileContent(t, cfg.benchHeadOut, "head\n")
	assertBenchgateFileContent(t, cfg.summaryOut, "summary\n")
	assertBenchgateFileContent(t, cfg.statusOut, "1\n")
}

func TestPublishArtifactsStatusOnlyWritesFinalStatus(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	statusOut := filepath.Join(dir, "status.txt")

	if err := publishArtifacts(config{statusCode: 1, statusOut: statusOut}); err != nil {
		t.Fatalf("publish status-only artifact: %v", err)
	}
	assertBenchgateFileContent(t, statusOut, "1\n")
}

func TestPublishArtifactsJoinsRollbackErrorsWhenProvisionalStatusWriteFails(t *testing.T) {
	restoreBenchgateSeams(t)
	writeErr := errors.New("provisional status write failure")
	cleanupErr := errors.New("cleanup failure")
	cleanupCloseErr := errors.New("cleanup close failure")
	writerCloseErr := errors.New("writer close failure")
	openCalls := 0
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		openCalls++
		if openCalls == 2 {
			return &benchgateStubRoot{
				remove: func(string) error { return cleanupErr },
				close:  func() error { return cleanupCloseErr },
			}, "status.txt", nil
		}
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			close: func() error { return writerCloseErr },
		}, "status.txt", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return writeErr }

	err := publishArtifacts(config{statusCode: 1, statusOut: filepath.Join(benchgateCanonicalTempDir(t), "status.txt")})
	for _, expectedErr := range []error{writeErr, cleanupErr, cleanupCloseErr, writerCloseErr} {
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected joined sentinel %v, got %v", expectedErr, err)
		}
	}
	if openCalls != 2 {
		t.Fatalf("artifact root opens = %d, want prepare plus cleanup", openCalls)
	}
}

func TestPublishArtifactsJoinsRollbackErrorsWhenProvisionalStatusChmodFails(t *testing.T) {
	restoreBenchgateSeams(t)
	chmodErr := errors.New("provisional status chmod failure")
	cleanupErr := errors.New("cleanup failure")
	cleanupCloseErr := errors.New("cleanup close failure")
	writerCloseErr := errors.New("writer close failure")
	openCalls := 0
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		openCalls++
		if openCalls == 2 {
			return &benchgateStubRoot{
				remove: func(string) error { return cleanupErr },
				close:  func() error { return cleanupCloseErr },
			}, "status.txt", nil
		}
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			chmod: func(string, os.FileMode) error {
				return chmodErr
			},
			close: func() error { return writerCloseErr },
		}, "status.txt", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return nil }

	err := publishArtifacts(config{statusCode: 1, statusOut: filepath.Join(benchgateCanonicalTempDir(t), "status.txt")})
	for _, expectedErr := range []error{chmodErr, cleanupErr, cleanupCloseErr, writerCloseErr} {
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected joined sentinel %v, got %v", expectedErr, err)
		}
	}
	if openCalls != 2 {
		t.Fatalf("artifact root opens = %d, want prepare plus cleanup", openCalls)
	}
}

func TestPublishArtifactsRemovesProvisionalStatusAfterChmodFailure(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	statusOut := filepath.Join(dir, "status.txt")
	chmodErr := errors.New("provisional status chmod failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(path string) (safeio.Root, string, error) {
		root, fileName, err := openArtifactDir(path)
		if err != nil {
			return nil, "", err
		}
		return &benchgateChmodErrorRoot{Root: root, err: chmodErr}, fileName, nil
	}

	err := publishArtifacts(config{statusCode: 1, statusOut: statusOut})
	if !errors.Is(err, chmodErr) {
		t.Fatalf("expected provisional status chmod failure, got %v", err)
	}
	assertBenchgatePathAbsent(t, statusOut)
}

func TestPublishArtifactsForcesFailureStatusAfterWriteError(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	headInput := filepath.Join(dir, "head-input.out")
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, baseInput, "base\n")
	writeBenchgateInputFile(t, headInput, "head\n")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	artifactDir := filepath.Join(dir, ".artifacts")
	restoreBenchgateSeams(t)
	writeArtifactWithinRoot = func(root safeio.Root, name string, data []byte, mode os.FileMode) error {
		if name == "bench-head.out" {
			return errors.New("synthetic write failure")
		}
		return safeio.WriteFileReplacingWithinRoot(root, name, data, mode)
	}
	cfg := config{
		benchBaseInput: baseInput,
		benchBaseOut:   filepath.Join(artifactDir, "bench-base.out"),
		benchHeadInput: headInput,
		benchHeadOut:   filepath.Join(artifactDir, "bench-head.out"),
		summaryInput:   summaryInput,
		summaryOut:     filepath.Join(artifactDir, "memory-bench-summary.md"),
		statusCode:     1,
		statusOut:      filepath.Join(artifactDir, "memory-bench-status.txt"),
	}

	err := publishArtifacts(cfg)
	if err == nil || !strings.Contains(err.Error(), "bench-head artifact") || !strings.Contains(err.Error(), "synthetic write failure") {
		t.Fatalf("expected bench-head publish failure, got %v", err)
	}
	assertBenchgatePathAbsent(t, cfg.benchBaseOut)
	assertBenchgatePathAbsent(t, cfg.benchHeadOut)
	assertBenchgatePathAbsent(t, cfg.summaryOut)
	assertBenchgateFileContent(t, cfg.statusOut, "2\n")
}

func TestPublishArtifactsRejectsSymlinkTargetBeforeWritingOthers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	headInput := filepath.Join(dir, "head-input.out")
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, baseInput, "base\n")
	writeBenchgateInputFile(t, headInput, "head\n")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	artifactDir := filepath.Join(dir, ".artifacts")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	outsideSummary := filepath.Join(dir, "outside-summary.md")
	writeBenchgateInputFile(t, outsideSummary, "outside summary\n")
	summaryOut := filepath.Join(artifactDir, "memory-bench-summary.md")
	if err := os.Symlink(outsideSummary, summaryOut); err != nil {
		t.Fatalf("symlink summary target: %v", err)
	}

	cfg := config{
		benchBaseInput: baseInput,
		benchBaseOut:   filepath.Join(artifactDir, "bench-base.out"),
		benchHeadInput: headInput,
		benchHeadOut:   filepath.Join(artifactDir, "bench-head.out"),
		summaryInput:   summaryInput,
		summaryOut:     summaryOut,
		statusCode:     0,
		statusOut:      filepath.Join(artifactDir, "memory-bench-status.txt"),
	}

	err := publishArtifacts(cfg)
	if err == nil || !strings.Contains(err.Error(), "summary artifact: artifact target is a symlink") {
		t.Fatalf("expected publish symlink rejection, got %v", err)
	}
	assertBenchgateFileContent(t, outsideSummary, "outside summary\n")
	assertBenchgatePathAbsent(t, cfg.benchBaseOut)
	assertBenchgatePathAbsent(t, cfg.benchHeadOut)
	assertBenchgatePathAbsent(t, cfg.statusOut)
}

func TestBuildArtifactSpecsValidation(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	baseInput := filepath.Join(dir, "base-input.out")
	headInput := filepath.Join(dir, "head-input.out")
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, baseInput, "base\n")
	writeBenchgateInputFile(t, headInput, "head\n")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	t.Run("success", func(t *testing.T) {
		specs, err := buildArtifactSpecs(config{
			benchBaseInput: baseInput,
			benchBaseOut:   filepath.Join(dir, "bench-base.out"),
			benchHeadInput: headInput,
			benchHeadOut:   filepath.Join(dir, "bench-head.out"),
			summaryInput:   summaryInput,
			summaryOut:     filepath.Join(dir, "summary.md"),
			statusCode:     2,
			statusOut:      filepath.Join(dir, "status.txt"),
		})
		if err != nil {
			t.Fatalf("build artifact specs: %v", err)
		}
		if len(specs) != 4 {
			t.Fatalf("spec count = %d, want 4", len(specs))
		}
		if string(specs[3].content) != "2\n" {
			t.Fatalf("status content = %q, want %q", specs[3].content, "2\n")
		}
	})

	for _, tt := range []struct {
		name string
		cfg  config
		want string
	}{
		{
			name: "publish with base ref",
			cfg: config{
				baseRef:      "HEAD~1",
				summaryInput: summaryInput,
				summaryOut:   filepath.Join(dir, "summary.md"),
				statusCode:   0,
				statusOut:    filepath.Join(dir, "status.txt"),
			},
			want: "publish mode cannot be combined",
		},
		{
			name: "publish with failure message",
			cfg: config{
				failureMessage: "failed",
				summaryInput:   summaryInput,
				summaryOut:     filepath.Join(dir, "summary.md"),
			},
			want: "publish mode cannot be combined",
		},
		{
			name: "base missing output",
			cfg: config{
				benchBaseInput: baseInput,
			},
			want: "bench-base publish requires both input and output paths",
		},
		{
			name: "head missing input",
			cfg: config{
				benchHeadOut: filepath.Join(dir, "bench-head.out"),
			},
			want: "bench-head publish requires both input and output paths",
		},
		{
			name: "summary missing output",
			cfg: config{
				summaryInput: summaryInput,
			},
			want: "summary publish requires both input and output paths",
		},
		{
			name: "status missing code",
			cfg: config{
				statusOut:  filepath.Join(dir, "status.txt"),
				statusCode: -1,
			},
			want: "status publish requires both status-code and status-out",
		},
		{
			name: "status missing output",
			cfg: config{
				statusCode: 0,
			},
			want: "status publish requires both status-code and status-out",
		},
		{
			name: "status code too large",
			cfg: config{
				statusCode: 3,
				statusOut:  filepath.Join(dir, "status.txt"),
			},
			want: "status-code must be between 0 and 2",
		},
		{
			name: "summary input read error",
			cfg: config{
				summaryInput: filepath.Join(dir, "missing-summary.md"),
				summaryOut:   filepath.Join(dir, "summary.md"),
				statusCode:   -1,
			},
			want: "read summary input",
		},
		{
			name: "no artifacts",
			cfg:  config{statusCode: -1},
			want: "publish mode requires at least one artifact",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := buildArtifactSpecs(tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q, got %v", tt.want, err)
			}
		})
	}
}

func TestBuildArtifactSpecsRejectsNormalizedOutputCollision(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	_, err := buildArtifactSpecs(config{
		summaryInput: summaryInput,
		summaryOut:   filepath.Join(dir, "nested", "..", "shared.txt"),
		statusCode:   1,
		statusOut:    filepath.Join(dir, "shared.txt"),
	})
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("expected publish output collision, got %v", err)
	}
}

func TestReadArtifactInputErrorPaths(t *testing.T) {
	t.Run("open error", func(t *testing.T) {
		expectedErr := errors.New("open failure")
		restoreBenchgateSeams(t)
		openArtifactInputFile = func(string) (io.ReadCloser, error) {
			return nil, expectedErr
		}

		_, err := readArtifactInput("artifact.txt")
		if !errors.Is(err, expectedErr) {
			t.Fatalf("expected open error, got %v", err)
		}
	})

	t.Run("read and close errors join", func(t *testing.T) {
		readErr := errors.New("read failure")
		closeErr := errors.New("close failure")
		restoreBenchgateSeams(t)
		openArtifactInputFile = func(string) (io.ReadCloser, error) {
			return &benchgateStubFile{
				read:  func([]byte) (int, error) { return 0, readErr },
				close: func() error { return closeErr },
			}, nil
		}

		_, err := readArtifactInput("artifact.txt")
		if !errors.Is(err, readErr) || !errors.Is(err, closeErr) {
			t.Fatalf("expected joined read and close errors, got %v", err)
		}
	})

	t.Run("close error after successful read", func(t *testing.T) {
		closeErr := errors.New("close failure")
		restoreBenchgateSeams(t)
		openArtifactInputFile = func(string) (io.ReadCloser, error) {
			readCalls := 0
			return &benchgateStubFile{
				read: func(p []byte) (int, error) {
					if readCalls > 0 {
						return 0, io.EOF
					}
					readCalls++
					copy(p, []byte("ok"))
					return 2, io.EOF
				},
				close: func() error { return closeErr },
			}, nil
		}

		_, err := readArtifactInput("artifact.txt")
		if !errors.Is(err, closeErr) {
			t.Fatalf("expected close error, got %v", err)
		}
	})
}

func TestPublishArtifactsJoinsWriteAndCloseErrors(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	input := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, input, "summary\n")

	writeErr := errors.New("write failure")
	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			close: func() error { return closeErr },
		}, "summary.md", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error {
		return writeErr
	}

	err := publishArtifacts(config{
		summaryInput: input,
		summaryOut:   filepath.Join(dir, "summary.md"),
		statusCode:   -1,
	})
	if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined write and close errors, got %v", err)
	}
}

func TestValidateArtifactTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "missing target", fn: testValidateArtifactTargetMissingTarget},
		{name: "non-regular target", fn: testValidateArtifactTargetNonRegularTarget},
		{name: "regular existing file", fn: testValidateArtifactTargetRegularExistingFile},
		{name: "symlink target", fn: testValidateArtifactTargetSymlinkTarget},
		{name: "lstat error", fn: testValidateArtifactTargetLstatError},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestArtifactWriterWriteAndClosePaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "chmod error", fn: testArtifactWriterWriteAndClosePathsChmodError},
		{name: "success", fn: testArtifactWriterWriteAndClosePathsSuccess},
		{name: "prepare error", fn: testArtifactWriterWriteAndClosePathsPrepareError},
		{name: "close joins non-nil roots", fn: testArtifactWriterWriteAndClosePathsCloseJoinsNonNilRoots},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestArtifactWriterPrepareCacheAndMismatch(t *testing.T) {
	t.Run("cache hit reuses handle", func(t *testing.T) {
		restoreBenchgateSeams(t)
		writer := &artifactWriter{
			roots:   make(map[string]safeio.Root),
			targets: make(map[string]artifactHandle),
		}
		opened := 0
		openArtifactDirFn = func(string) (safeio.Root, string, error) {
			opened++
			return &benchgateStubRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			}, "artifact.txt", nil
		}

		path := filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt")
		first, err := writer.prepare(path)
		if err != nil {
			t.Fatalf("first prepare: %v", err)
		}
		second, err := writer.prepare(path)
		if err != nil {
			t.Fatalf("second prepare: %v", err)
		}
		if opened != 1 {
			t.Fatalf("open count = %d, want 1", opened)
		}
		if first.fileName != second.fileName || first.root != second.root {
			t.Fatal("expected cached prepare result")
		}
	})

	t.Run("mismatched target name closes root", func(t *testing.T) {
		closeErr := errors.New("close failure")
		restoreBenchgateSeams(t)
		writer := &artifactWriter{
			roots:   make(map[string]safeio.Root),
			targets: make(map[string]artifactHandle),
		}
		openArtifactDirFn = func(string) (safeio.Root, string, error) {
			return &benchgateStubRoot{close: func() error { return closeErr }}, "wrong.txt", nil
		}

		_, err := writer.prepare(filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"))
		if err == nil || !strings.Contains(err.Error(), "artifact target changed while opening") || !errors.Is(err, closeErr) {
			t.Fatalf("expected mismatched target error with close failure, got %v", err)
		}
	})
}

func TestArtifactWriterPreparePropagatesTargetResolutionError(t *testing.T) {
	expectedErr := errors.New("absolute path failure")
	restoreBenchgateSeams(t)
	resolveArtifactPathAbs = func(string) (string, error) {
		return "", expectedErr
	}

	writer := &artifactWriter{
		roots:   make(map[string]safeio.Root),
		targets: make(map[string]artifactHandle),
	}
	_, err := writer.prepare("summary.md")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected target-resolution error, got %v", err)
	}
}

func TestArtifactWriterPrepareAllClosesRootsOnFailure(t *testing.T) {
	prepareErr := errors.New("prepare failure")
	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	writer := &artifactWriter{
		roots: map[string]safeio.Root{
			"/tmp/artifacts": &benchgateStubRoot{close: func() error { return closeErr }},
		},
		targets: make(map[string]artifactHandle),
	}
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return nil, "", prepareErr
	}

	err := writer.PrepareAll([]artifactSpec{{label: "summary", path: filepath.Join(benchgateCanonicalTempDir(t), "summary.md")}})
	if !errors.Is(err, prepareErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined prepare and close errors, got %v", err)
	}
}

func TestPublishArtifactsReturnsCloseErrorAfterSuccessfulWrite(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	input := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, input, "summary\n")

	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
			close: func() error { return closeErr },
		}, "summary.md", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return nil }

	err := publishArtifacts(config{
		summaryInput: input,
		summaryOut:   filepath.Join(dir, "summary.md"),
		statusCode:   -1,
	})
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected close error after successful publish, got %v", err)
	}
}

func TestPublishArtifactsRejectsEmptyRequests(t *testing.T) {
	err := publishArtifacts(config{statusCode: -1})
	if err == nil || !strings.Contains(err.Error(), "publish mode requires at least one artifact") {
		t.Fatalf("expected empty publish rejection, got %v", err)
	}
}

func TestPublishArtifactsRollsBackArtifactAfterChmodFailure(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	input := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, input, "summary\n")

	restoreBenchgateSeams(t)
	written := make(map[string][]byte)
	regularInfo := statBenchgatePath(t, input)
	openArtifactDirFn = func(path string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			lstat: func(name string) (fs.FileInfo, error) {
				if _, ok := written[name]; ok {
					return regularInfo, nil
				}
				return nil, os.ErrNotExist
			},
			chmod: func(name string, perm os.FileMode) error {
				if name == "summary.md" && perm == artifactFileMode {
					return errors.New("chmod failure")
				}
				return nil
			},
			remove: func(name string) error {
				delete(written, name)
				return nil
			},
		}, filepath.Base(path), nil
	}
	writeArtifactWithinRoot = func(root safeio.Root, name string, data []byte, mode os.FileMode) error {
		written[name] = slices.Clone(data)
		return nil
	}

	err := publishArtifacts(config{
		summaryInput: input,
		summaryOut:   filepath.Join(dir, "summary.md"),
		statusCode:   -1,
	})
	if err == nil || !strings.Contains(err.Error(), "chmod failure") {
		t.Fatalf("expected chmod failure, got %v", err)
	}
	if _, ok := written["summary.md"]; ok {
		t.Fatal("expected chmod-failed artifact to be rolled back")
	}
}

func TestPublishArtifactsRestoresFailureStatusAfterFinalStatusWriteFailure(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	restoreBenchgateSeams(t)
	writeArtifactWithinRoot = func(root safeio.Root, name string, data []byte, mode os.FileMode) error {
		if string(data) == "1\n" {
			return errors.New("final status write failure")
		}
		return safeio.WriteFileReplacingWithinRoot(root, name, data, mode)
	}

	summaryOut := filepath.Join(dir, "summary.md")
	statusOut := filepath.Join(dir, "status.txt")
	err := publishArtifacts(config{
		summaryInput: summaryInput,
		summaryOut:   summaryOut,
		statusCode:   1,
		statusOut:    statusOut,
	})
	if err == nil || !strings.Contains(err.Error(), "final status write failure") {
		t.Fatalf("expected final status failure, got %v", err)
	}
	assertBenchgatePathAbsent(t, summaryOut)
	assertBenchgateFileContent(t, statusOut, "2\n")
}

func TestPublishArtifactsJoinsRestoreFailureAfterFinalStatusWriteFailure(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryInput := filepath.Join(dir, "summary-input.md")
	writeBenchgateInputFile(t, summaryInput, "summary\n")

	restoreBenchgateSeams(t)
	statusWrites := 0
	writeArtifactWithinRoot = func(root safeio.Root, name string, data []byte, mode os.FileMode) error {
		if name == "status.txt" {
			statusWrites++
		}
		switch {
		case name == "status.txt" && string(data) == "1\n":
			return errors.New("final status write failure")
		case name == "status.txt" && string(data) == "2\n" && statusWrites > 1:
			return errors.New("restore failure status write failure")
		default:
			return safeio.WriteFileReplacingWithinRoot(root, name, data, mode)
		}
	}

	err := publishArtifacts(config{
		summaryInput: summaryInput,
		summaryOut:   filepath.Join(dir, "summary.md"),
		statusCode:   1,
		statusOut:    filepath.Join(dir, "status.txt"),
	})
	if err == nil || !strings.Contains(err.Error(), "final status write failure") || !strings.Contains(err.Error(), "restore failure status write failure") {
		t.Fatalf("expected joined final and restore status failures, got %v", err)
	}
}

func TestWriteArtifactFileJoinsWriteAndCloseErrors(t *testing.T) {
	writeErr := errors.New("write failure")
	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{close: func() error { return closeErr }}, "artifact.txt", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error {
		return writeErr
	}

	err := writeArtifactFile(filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"), []byte("artifact"))
	if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined write and close errors, got %v", err)
	}
}

func TestWriteArtifactFileJoinsChmodAndCloseErrors(t *testing.T) {
	chmodErr := errors.New("chmod failure")
	closeErr := errors.New("close failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			chmod: func(name string, perm os.FileMode) error {
				if name != "artifact.txt" || perm != artifactFileMode {
					t.Fatalf("unexpected chmod %q %#o", name, perm)
				}
				return chmodErr
			},
			close: func() error { return closeErr },
		}, "artifact.txt", nil
	}
	writeArtifactWithinRoot = func(safeio.Root, string, []byte, os.FileMode) error { return nil }

	err := writeArtifactFile(filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"), []byte("artifact"))
	if !errors.Is(err, chmodErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined chmod and close errors, got %v", err)
	}
}

func TestResolveArtifactTargetNormalizesRelativePath(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := benchgateCanonicalTempDir(t)
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd %s: %v", wd, err)
		}
	})
	wantDir, err := filepath.Abs("nested")
	if err != nil {
		t.Fatalf("resolve expected dir: %v", err)
	}

	target, err := resolveArtifactTarget(filepath.Join("nested", "summary.md"))
	if err != nil {
		t.Fatalf("resolve artifact target: %v", err)
	}
	if target.dir != wantDir {
		t.Fatalf("target dir = %q, want %q", target.dir, wantDir)
	}
	if target.fileName != "summary.md" {
		t.Fatalf("target file = %q, want summary.md", target.fileName)
	}
}

func TestResolveArtifactTargetRejectsRootPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("root-path semantics vary on Windows volumes")
	}

	_, err := resolveArtifactTarget(string(os.PathSeparator))
	if err == nil || !strings.Contains(err.Error(), "must name a file") {
		t.Fatalf("expected root-path rejection, got %v", err)
	}
}

func TestResolveArtifactTargetPropagatesAbsError(t *testing.T) {
	expectedErr := errors.New("absolute path failure")
	restoreBenchgateSeams(t)
	resolveArtifactPathAbs = func(string) (string, error) {
		return "", expectedErr
	}

	_, err := resolveArtifactTarget("summary.md")
	if err == nil || !strings.Contains(err.Error(), "resolve artifact path") {
		t.Fatalf("expected absolute-path resolution error, got %v", err)
	}
}

func TestResolveArtifactCollisionKeyPropagatesTargetError(t *testing.T) {
	expectedErr := errors.New("absolute path failure")
	restoreBenchgateSeams(t)
	resolveArtifactPathAbs = func(string) (string, error) {
		return "", expectedErr
	}

	_, err := resolveArtifactCollisionKey("summary.md")
	if err == nil || !strings.Contains(err.Error(), "resolve artifact path") {
		t.Fatalf("expected collision-key resolution error, got %v", err)
	}
}

func TestResolveArtifactCollisionKeyPropagatesUnexpectedAncestorErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "permission", err: fs.ErrPermission},
		{name: "transient", err: errors.New("transient ancestor failure")},
		{name: "probe", err: errors.New("probe failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreBenchgateSeams(t)
			targetPath := filepath.Join(string(os.PathSeparator), "tmp", "artifacts", "summary.md")
			resolveArtifactPathAbs = func(string) (string, error) {
				return targetPath, nil
			}
			openArtifactAncestorFn = func(string) (safeio.Root, string, []string, error) {
				return nil, "", nil, tc.err
			}

			_, err := resolveArtifactCollisionKey("summary.md")
			if !errors.Is(err, tc.err) {
				t.Fatalf("expected ancestor error %v, got %v", tc.err, err)
			}
		})
	}
}

func TestResolveArtifactCollisionKeyRejectsSymlinkAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real dir: %v", err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("symlink real dir: %v", err)
	}

	_, err := resolveArtifactCollisionKey(filepath.Join(linkDir, "shared.txt"))
	if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
		t.Fatalf("expected symlink ancestor rejection, got %v", err)
	}
}

func TestResolveArtifactCollisionKeyHonorsFilesystemCaseEquivalence(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	keyA, err := resolveArtifactCollisionKey(filepath.Join(dir, "Artifact.out"))
	if err != nil {
		t.Fatalf("resolve mixed-case key A: %v", err)
	}
	keyB, err := resolveArtifactCollisionKey(filepath.Join(dir, "artifact.out"))
	if err != nil {
		t.Fatalf("resolve mixed-case key B: %v", err)
	}

	if benchgateDirCaseInsensitive(t, dir) {
		if keyA != keyB {
			t.Fatalf("case-insensitive collision keys differ: %q vs %q", keyA, keyB)
		}
		return
	}
	if keyA == keyB {
		t.Fatalf("case-sensitive collision keys unexpectedly matched: %q", keyA)
	}
}

func TestResolveArtifactCollisionKeyRejectsBrokenSymlinkAncestor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	linkPath := filepath.Join(dir, "broken")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), linkPath); err != nil {
		t.Fatalf("create broken symlink: %v", err)
	}

	_, err := resolveArtifactCollisionKey(filepath.Join(linkPath, "file.txt"))
	if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
		t.Fatalf("expected broken symlink rejection, got %v", err)
	}
}

func TestResolveArtifactCollisionKeyRejectsSymlinkLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink loop behavior is environment-dependent on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	loopPath := filepath.Join(dir, "loop")
	if err := os.Symlink(loopPath, loopPath); err != nil {
		t.Fatalf("create symlink loop: %v", err)
	}

	_, err := resolveArtifactCollisionKey(filepath.Join(loopPath, "shared.txt"))
	if err == nil || !strings.Contains(err.Error(), "artifact parent contains symlink") {
		t.Fatalf("expected symlink-loop rejection, got %v", err)
	}
}

func TestResolveArtifactCollisionKeyPropagatesLeafCanonicalizationError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics are not stable on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	if probe, err := os.CreateTemp(dir, ".benchgate-probe-*"); err == nil {
		if closeErr := probe.Close(); closeErr != nil {
			t.Fatalf("close probe: %v", closeErr)
		}
		if removeErr := os.Remove(probe.Name()); removeErr != nil {
			t.Fatalf("remove probe: %v", removeErr)
		}
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir read-only: %v", err)
	}
	probePath := filepath.Join(dir, "permission-probe.txt")
	probe, probeErr := os.OpenFile(probePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, artifactFileMode)
	if probeErr == nil {
		if err := probe.Close(); err != nil {
			t.Fatalf("close writability probe: %v", err)
		}
		if err := os.Remove(probePath); err != nil {
			t.Fatalf("remove writability probe: %v", err)
		}
		t.Skip("effective privileges bypass directory permission restrictions")
	}
	if !os.IsPermission(probeErr) {
		t.Skipf("directory permission restrictions are not testable: %v", probeErr)
	}

	_, err := resolveArtifactCollisionKey(filepath.Join(dir, "summary.txt"))
	if err == nil {
		t.Fatal("expected leaf canonicalization error")
	}
}

func TestAppendArtifactPathParts(t *testing.T) {
	got := appendArtifactPathParts(filepath.Join("root", "existing"), []string{"missing", "nested"})
	want := filepath.Join("root", "existing", "missing", "nested")
	if got != want {
		t.Fatalf("artifact path = %q, want %q", got, want)
	}
}

func TestRollbackPublishedArtifactsCleansTrackedOutputsAndRestoresFailureStatus(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	summaryPath := filepath.Join(dir, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")
	writeBenchgateInputFile(t, summaryPath, "summary\n")
	writeBenchgateInputFile(t, statusPath, "1\n")

	writer := &artifactWriter{
		written: map[string]struct{}{
			summaryPath: {},
			statusPath:  {},
		},
	}

	cause := errors.New("publish failure")
	if err := rollbackPublishedArtifacts(writer, &artifactSpec{path: statusPath}, cause); !errors.Is(err, cause) {
		t.Fatalf("expected rollback cause, got %v", err)
	}

	assertBenchgatePathAbsent(t, summaryPath)
	assertBenchgateFileContent(t, statusPath, "2\n")
}

func TestOpenArtifactDirCreatesMissingParents(t *testing.T) {
	rootDir := benchgateCanonicalTempDir(t)
	path := filepath.Join(rootDir, "nested", "artifacts", "summary.md")

	root, fileName, err := openArtifactDir(path)
	if err != nil {
		t.Fatalf("open artifact dir: %v", err)
	}
	if fileName != "summary.md" {
		t.Fatalf("fileName = %q, want summary.md", fileName)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close artifact root: %v", err)
	}
	assertPathMode(t, filepath.Join(rootDir, "nested"), artifactDirMode)
	assertPathMode(t, filepath.Join(rootDir, "nested", "artifacts"), artifactDirMode)
}

func TestFinalizeArtifactDirModeRejectsUnsafeExistingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not stable on Windows")
	}

	dir := benchgateCanonicalTempDir(t)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	root, err := safeio.OpenCanonicalRoot(dir)
	if err != nil {
		t.Fatalf("open canonical root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	err = finalizeArtifactDirMode(root, dir, false)
	if err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
		t.Fatalf("expected unsafe directory rejection, got %v", err)
	}
}

func TestFinalizeArtifactDirModeAllowsSafeExistingDirectory(t *testing.T) {
	dir := benchgateCanonicalTempDir(t)
	root, err := safeio.OpenCanonicalRoot(dir)
	if err != nil {
		t.Fatalf("open canonical root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := finalizeArtifactDirMode(root, dir, false); err != nil {
		t.Fatalf("safe existing directory rejected: %v", err)
	}
}

func TestFinalizeArtifactDirModeSkipsUnsafePermissionRejectionWhenDisabled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows already exercises the disabled permission check in production")
	}

	restoreBenchgateSeams(t)
	enforceArtifactDirPerms = false

	dir := benchgateCanonicalTempDir(t)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	root, err := safeio.OpenCanonicalRoot(dir)
	if err != nil {
		t.Fatalf("open canonical root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close root: %v", closeErr)
		}
	}()

	if err := finalizeArtifactDirMode(root, dir, false); err != nil {
		t.Fatalf("expected disabled permission check to allow existing directory, got %v", err)
	}
}

func TestFinalizeArtifactDirModeChmodsCreatedDirectory(t *testing.T) {
	chmodCalls := 0
	root := &benchgateStubRoot{
		chmod: func(name string, perm os.FileMode) error {
			chmodCalls++
			if name != "." || perm != artifactDirMode {
				t.Fatalf("unexpected chmod %q %#o", name, perm)
			}
			return nil
		},
	}

	if err := finalizeArtifactDirMode(root, "/tmp/artifacts", true); err != nil {
		t.Fatalf("created directory chmod failed: %v", err)
	}
	if chmodCalls != 1 {
		t.Fatalf("chmod calls = %d, want 1", chmodCalls)
	}
}

func TestFinalizeArtifactDirModePropagatesCreatedDirectoryChmodError(t *testing.T) {
	chmodErr := errors.New("chmod failure")
	root := &benchgateStubRoot{
		chmod: func(string, os.FileMode) error { return chmodErr },
	}

	err := finalizeArtifactDirMode(root, "/tmp/artifacts", true)
	if !errors.Is(err, chmodErr) {
		t.Fatalf("expected created directory chmod error, got %v", err)
	}
}

func TestFinalizeArtifactDirModePropagatesExistingDirectoryLstatError(t *testing.T) {
	lstatErr := errors.New("lstat failure")
	root := &benchgateStubRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, lstatErr },
	}

	err := finalizeArtifactDirMode(root, "/tmp/artifacts", false)
	if !errors.Is(err, lstatErr) {
		t.Fatalf("expected existing directory lstat error, got %v", err)
	}
}

func TestArtifactRootCaseInsensitivePropagatesProbeErrors(t *testing.T) {
	expectedErr := errors.New("probe create failure")
	root := &benchgateStubRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return nil, expectedErr
		},
	}

	_, err := artifactRootCaseInsensitive(root)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected probe creation error, got %v", err)
	}
}

func TestFilterDriverNameFromConfigKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		want string
		ok   bool
	}{
		{name: "smudge", key: "filter.foo.bar.smudge", want: "foo.bar", ok: true},
		{name: "required", key: "filter.alpha.required", want: "alpha", ok: true},
		{name: "missing prefix", key: "core.hooksPath", ok: false},
		{name: "missing suffix", key: "filter.alpha.extra", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := filterDriverNameFromConfigKey(tc.key)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("filterDriverNameFromConfigKey(%q) = %q, %v; want %q, %v", tc.key, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestOpenArtifactDirErrorPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "target resolution error", fn: testOpenArtifactDirErrorPathsTargetResolutionError},
		{name: "ancestor error", fn: testOpenArtifactDirErrorPathsAncestorError},
		{name: "open child error closes ancestor root", fn: testOpenArtifactDirErrorPathsOpenChildErrorClosesAncestorRoot},
		{name: "close current after child open", fn: testOpenArtifactDirErrorPathsCloseCurrentAfterChildOpen},
		{name: "chmod error closes current root", fn: testOpenArtifactDirErrorPathsChmodErrorClosesCurrentRoot},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestOpenArtifactAncestorRootRejectsFileAndSymlinkParents(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "file", fn: testOpenArtifactAncestorRootRejectsFileParent},
		{name: "symlink", fn: testOpenArtifactAncestorRootRejectsSymlinkParent},
		{name: "symlink ancestor on original artifact path", fn: testOpenArtifactAncestorRootRejectsSymlinkAncestorOnArtifactPath},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestOpenArtifactAncestorRootReturnsVolumeRootForRootPath(t *testing.T) {
	volumeRoot := filepath.VolumeName(benchgateCanonicalTempDir(t)) + string(os.PathSeparator)

	root, rootAbs, relParts, err := openArtifactAncestorRoot(volumeRoot)
	if err != nil {
		t.Fatalf("open artifact ancestor root: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("close artifact root: %v", err)
	}
	if rootAbs != volumeRoot {
		t.Fatalf("rootAbs = %q, want %q", rootAbs, volumeRoot)
	}
	if len(relParts) != 0 {
		t.Fatalf("relParts = %v, want none", relParts)
	}
}

func TestOpenArtifactAncestorRootInternalErrorPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "open child error", fn: testOpenArtifactAncestorRootInternalErrorPathsOpenChildError},
		{name: "child stat error", fn: testOpenArtifactAncestorRootInternalErrorPathsChildStatError},
		{name: "child changed while opening", fn: testOpenArtifactAncestorRootInternalErrorPathsChildChangedWhileOpening},
		{name: "current close error", fn: testOpenArtifactAncestorRootInternalErrorPathsCurrentCloseError},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestOpenArtifactAncestorRootWrapsOpenError(t *testing.T) {
	expectedErr := errors.New("open canonical root failure")
	restoreBenchgateSeams(t)
	openCanonicalArtifactRoot = func(string) (safeio.Root, error) {
		return nil, expectedErr
	}

	_, _, _, err := openArtifactAncestorRoot(benchgateCanonicalTempDir(t))
	if err == nil || !errors.Is(err, expectedErr) || !strings.Contains(err.Error(), "open artifact root") {
		t.Fatalf("expected wrapped canonical-root error, got %v", err)
	}
}

func TestOpenArtifactAncestorRootPropagatesLstatError(t *testing.T) {
	_, _, _, err := openArtifactAncestorRoot(string([]byte{0}))
	if err == nil {
		t.Fatal("expected invalid-path lstat error")
	}
}

func TestSplitArtifactPathFiltersEmptyAndDotComponents(t *testing.T) {
	parts := splitArtifactPath(filepath.Join("nested", ".", "artifacts") + string(os.PathSeparator) + string(os.PathSeparator) + "summary.md")
	if !reflect.DeepEqual(parts, []string{"nested", "artifacts", "summary.md"}) {
		t.Fatalf("parts = %v, want %v", parts, []string{"nested", "artifacts", "summary.md"})
	}
	if got := splitArtifactPath("."); len(got) != 0 {
		t.Fatalf("root-relative dot path = %v, want empty", got)
	}
}

func TestOpenOrCreateArtifactDirErrorPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{name: "mkdir error", fn: testOpenOrCreateArtifactDirErrorPathsMkdirError},
		{name: "mkdir raced with concurrent create", fn: testOpenOrCreateArtifactDirErrorPathsMkdirRacedWithConcurrentCreate},
		{name: "lookup after mkdir error", fn: testOpenOrCreateArtifactDirErrorPathsLookupAfterMkdirError},
		{name: "symlink", fn: testOpenOrCreateArtifactDirErrorPathsSymlink},
		{name: "non-directory", fn: testOpenOrCreateArtifactDirErrorPathsNonDirectory},
		{name: "open root error", fn: testOpenOrCreateArtifactDirErrorPathsOpenRootError},
		{name: "changed while opening", fn: testOpenOrCreateArtifactDirErrorPathsChangedWhileOpening},
		{name: "child stat and close errors join", fn: testOpenOrCreateArtifactDirErrorPathsChildStatAndCloseErrorsJoin},
	} {
		t.Run(tc.name, tc.fn)
	}
}

func TestCloseRootWithErrorJoinsCloseFailure(t *testing.T) {
	rootErr := errors.New("root failure")
	closeErr := errors.New("close failure")
	err := closeRootWithError(&benchgateStubRoot{close: func() error { return closeErr }}, rootErr)
	if !errors.Is(err, rootErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected joined root and close errors, got %v", err)
	}
}

func TestParseArgs(t *testing.T) {
	cfg, err := parseArgs([]string{
		"-base-ref", "origin/main",
		"-head-ref", "HEAD~1",
		"-summary-out", "summary.md",
		"-status-out", "status.txt",
	})
	if err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if cfg.baseRef != "origin/main" || cfg.headRef != "HEAD~1" || cfg.summaryOut != "summary.md" || cfg.statusOut != "status.txt" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if !flagWasProvided(cfg, baseRefFlagName) || !flagWasProvided(cfg, "summary-out") || !flagWasProvided(cfg, "status-out") {
		t.Fatalf("expected explicit flags to be tracked, got %#v", cfg.explicitFlags)
	}
}

func TestParseArgsFailureMessage(t *testing.T) {
	cfg, err := parseArgs([]string{
		"-summary-out", "summary.md",
		"-status-out", "status.txt",
		"-failure-message", "memory benchmark base execution failed",
	})
	if err != nil {
		t.Fatalf("parse args: %v", err)
	}
	if cfg.failureMessage != "memory benchmark base execution failed" {
		t.Fatalf("failure message = %q, want %q", cfg.failureMessage, "memory benchmark base execution failed")
	}
}

func TestCleanWorktreeConfigArgs(t *testing.T) {
	repo := initGitRepo(t)
	includeConfig := filepath.Join(repo, "filters.gitconfig")
	writeBenchgateInputFile(t, includeConfig, "[filter \"foo.bar\"]\n\tsmudge = foo-smudge\n\tclean = foo-clean\n\trequired = true\n")
	runGit(t, repo, "config", "include.path", "../filters.gitconfig")
	runGit(t, repo, "config", "filter.beta.process", "beta-process")

	args, err := cleanWorktreeConfigArgs(repo, mustResolveCallerGitPath(t))
	if err != nil {
		t.Fatalf("clean worktree config args: %v", err)
	}
	wantEntries := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "filter.foo.bar.smudge=",
		"-c", "filter.foo.bar.clean=",
		"-c", "filter.foo.bar.process=",
		"-c", "filter.foo.bar.required=false",
		"-c", "filter.beta.process=",
		"-c", "filter.beta.required=false",
	}
	for _, want := range wantEntries {
		if !slices.Contains(args, want) {
			t.Fatalf("expected clean config args to contain %q, got %#v", want, args)
		}
	}
}

func TestParseArgsRejectsPositionalArguments(t *testing.T) {
	_, err := parseArgs([]string{"-base-ref", "HEAD", "extra"})
	if err == nil || !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("expected positional argument rejection, got %v", err)
	}
}

func TestValidateGitRevisionOperandRejectsWhitespace(t *testing.T) {
	err := validateGitRevisionOperand(baseRefFlagName, "   ")
	if err == nil || !strings.Contains(err.Error(), "base-ref is required") {
		t.Fatalf("expected whitespace operand rejection, got %v", err)
	}
}

func TestAddWorktreeWithCleanConfigPropagatesFilterConfigReadError(t *testing.T) {
	expectedErr := errors.New("config command creation failure")
	restoreBenchgateSeams(t)
	gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return nil, expectedErr
	}

	err := addWorktreeWithCleanConfig(benchgateCanonicalTempDir(t), "/usr/bin/git", filepath.Join(t.TempDir(), "worktree"), "HEAD")
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected clean-config read failure, got %v", err)
	}
}

func TestCleanWorktreeConfigArgsPropagatesParseErrors(t *testing.T) {
	restoreBenchgateSeams(t)
	gitCommandContext = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return exec.Command("sh", "-c", `printf 'filter.foo.bar.smudge\ncat'`), nil
	}

	_, err := cleanWorktreeConfigArgs(benchgateCanonicalTempDir(t), "/usr/bin/git")
	if err == nil || !strings.Contains(err.Error(), "parse effective filter configuration") {
		t.Fatalf("expected filter config parse error, got %v", err)
	}
}

func TestCleanWorktreeConfigArgsWithoutLocalFilters(t *testing.T) {
	repo := initGitRepo(t)
	args, err := cleanWorktreeConfigArgs(repo, mustResolveCallerGitPath(t))
	if err != nil {
		t.Fatalf("clean worktree config args without filters: %v", err)
	}
	if !slices.Equal(args, append(gitexec.SafeConfigArgs(), "-c", "core.hooksPath=/dev/null")) {
		t.Fatalf("clean config args without filters = %#v", args)
	}
}

func TestConfiguredFilterNamesIgnoresNoiseAndDuplicates(t *testing.T) {
	output := []byte("filter.foo.bar.smudge\ncat\x00filter.foo.bar.clean\ncat\x00filter.beta.process\ncat\x00filter.beta.required\ntrue\x00")
	got, err := configuredFilterNames(output)
	if err != nil {
		t.Fatalf("configured filter names: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"foo.bar", "beta"}) {
		t.Fatalf("configured filter names = %#v, want %#v", got, []string{"foo.bar", "beta"})
	}
}

func TestConfiguredFilterNamesRejectsMalformedOutput(t *testing.T) {
	_, err := configuredFilterNames([]byte("filter.foo.bar.smudge\ncat"))
	if err == nil || !strings.Contains(err.Error(), "missing trailing NUL terminator") {
		t.Fatalf("expected malformed output rejection, got %v", err)
	}

	_, err = configuredFilterNames([]byte("filter.foo.bar.smudge\x00"))
	if err == nil || !strings.Contains(err.Error(), "malformed record") {
		t.Fatalf("expected malformed-record rejection, got %v", err)
	}
}

func TestConfiguredFilterNamesSkipsEmptyRecordsAndRejectsUnexpectedKeys(t *testing.T) {
	got, err := configuredFilterNames([]byte("filter.foo.bar.smudge\ncat\x00\x00"))
	if err != nil {
		t.Fatalf("configured filter names with empty record: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"foo.bar"}) {
		t.Fatalf("configured filter names with empty record = %#v", got)
	}

	_, err = configuredFilterNames([]byte("core.hooksPath\n/dev/null\x00"))
	if err == nil || !strings.Contains(err.Error(), "unexpected key") {
		t.Fatalf("expected unexpected-key rejection, got %v", err)
	}
}

func TestRemoveArtifactFileRemovesExistingTarget(t *testing.T) {
	path := filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt")
	writeBenchgateInputFile(t, path, "artifact\n")

	if err := removeArtifactFile(path); err != nil {
		t.Fatalf("remove artifact file: %v", err)
	}
	assertBenchgatePathAbsent(t, path)
}

func TestRemoveArtifactFileJoinsCloseError(t *testing.T) {
	restoreBenchgateSeams(t)
	closeErr := errors.New("close failure")
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return &benchgateStubRoot{
			remove: func(string) error { return nil },
			close:  func() error { return closeErr },
		}, "artifact.txt", nil
	}

	err := removeArtifactFile(filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"))
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected remove close error, got %v", err)
	}
}

func TestRemoveArtifactFilePropagatesOpenError(t *testing.T) {
	expectedErr := errors.New("open artifact dir failure")
	restoreBenchgateSeams(t)
	openArtifactDirFn = func(string) (safeio.Root, string, error) {
		return nil, "", expectedErr
	}

	err := removeArtifactFile(filepath.Join(benchgateCanonicalTempDir(t), "artifact.txt"))
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected remove open error, got %v", err)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := benchgateCanonicalTempDir(t)
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "config", "user.email", "test@example.com")
	return repo
}

func gitCommitFile(t *testing.T, repo, name, content string) string {
	t.Helper()
	path := filepath.Join(repo, name)
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, repo, "add", name)
	runGit(t, repo, "commit", "-m", "add "+name)
	return strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
}

func removeTrackedFiles(t *testing.T, repo string) {
	t.Helper()
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		path := filepath.Join(repo, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			t.Fatalf("remove %s: %v", entry.Name(), err)
		}
	}
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	gitPath, err := gitexec.ResolveBinaryPath()
	if err != nil {
		t.Fatalf("resolve git path: %v", err)
	}
	cmd, err := gitexec.CommandContext(context.Background(), gitPath, args...)
	if err != nil {
		t.Fatalf("construct git %s: %v", strings.Join(args, " "), err)
	}
	cmd.Dir = repo
	cmd.Env = isolatedGitEnv(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func shellQuotePath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

func runBenchgateMainIfRequested(t *testing.T) bool {
	t.Helper()
	if os.Getenv("GO_WANT_BENCHGATE_HELPER") != "1" {
		return false
	}

	argsIndex := slices.Index(os.Args, "--")
	if argsIndex < 0 {
		t.Fatal("missing helper args separator")
	}
	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = append([]string{oldArgs[0]}, os.Args[argsIndex+1:]...)
	main()
	return true
}

func runBenchgateHelper(t *testing.T, repo, testName string, args ...string) ([]byte, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run="+testName, "--")
	cmd.Args = append(cmd.Args, args...)
	cmd.Dir = repo
	cmd.Env = make([]string, 0, len(os.Environ())+1)
	for _, entry := range isolatedGitEnv(t) {
		if strings.HasPrefix(entry, "GO_WANT_BENCHGATE_HELPER=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, "GO_WANT_BENCHGATE_HELPER=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected subprocess exit error or success, got %v\n%s", err, output)
	}
	return output, exitErr.ExitCode()
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

type errorWriter struct {
	err   error
	calls int
}

type benchgateStubRoot struct {
	open     func(string) (safeio.File, error)
	openFile func(string, int, os.FileMode) (safeio.File, error)
	openRoot func(string) (safeio.Root, error)
	lstat    func(string) (fs.FileInfo, error)
	mkdir    func(string, os.FileMode) error
	chmod    func(string, os.FileMode) error
	mkdirAll func(string, os.FileMode) error
	link     func(string, string) error
	rename   func(string, string) error
	remove   func(string) error
	close    func() error
}

type benchgateChmodErrorRoot struct {
	safeio.Root
	err error
}

type benchgateStubFile struct {
	read  func([]byte) (int, error)
	write func([]byte) (int, error)
	close func() error
	stat  func() (fs.FileInfo, error)
	chmod func(os.FileMode) error
}

func (r *benchgateChmodErrorRoot) Chmod(string, os.FileMode) error {
	return r.err
}

func (f *benchgateStubFile) Read(p []byte) (int, error) {
	if f.read != nil {
		return f.read(p)
	}
	return 0, io.EOF
}

func (f *benchgateStubFile) Write(p []byte) (int, error) {
	if f.write != nil {
		return f.write(p)
	}
	return len(p), nil
}

func (f *benchgateStubFile) Close() error {
	if f.close != nil {
		return f.close()
	}
	return nil
}

func (f *benchgateStubFile) Stat() (fs.FileInfo, error) {
	if f.stat != nil {
		return f.stat()
	}
	return nil, errors.New("unexpected Stat call")
}

func (f *benchgateStubFile) Chmod(perm os.FileMode) error {
	if f.chmod != nil {
		return f.chmod(perm)
	}
	return nil
}

func (r *benchgateStubRoot) Open(name string) (safeio.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return nil, errors.New("unexpected Open call")
}

func (r *benchgateStubRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	if r.openFile != nil {
		return r.openFile(name, flag, perm)
	}
	return nil, errors.New("unexpected OpenFile call")
}

func (r *benchgateStubRoot) OpenRoot(name string) (safeio.Root, error) {
	if r.openRoot != nil {
		return r.openRoot(name)
	}
	return nil, errors.New("unexpected OpenRoot call")
}

func (r *benchgateStubRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(name)
	}
	return nil, errors.New("unexpected Lstat call")
}

func (r *benchgateStubRoot) Mkdir(name string, perm os.FileMode) error {
	if r.mkdir != nil {
		return r.mkdir(name, perm)
	}
	return errors.New("unexpected Mkdir call")
}

func (r *benchgateStubRoot) Chmod(name string, perm os.FileMode) error {
	if r.chmod != nil {
		return r.chmod(name, perm)
	}
	return nil
}

func (r *benchgateStubRoot) MkdirAll(name string, perm os.FileMode) error {
	if r.mkdirAll != nil {
		return r.mkdirAll(name, perm)
	}
	return errors.New("unexpected MkdirAll call")
}

func (r *benchgateStubRoot) Link(oldName, newName string) error {
	if r.link != nil {
		return r.link(oldName, newName)
	}
	return errors.New("unexpected Link call")
}

func (r *benchgateStubRoot) Rename(oldName, newName string) error {
	if r.rename != nil {
		return r.rename(oldName, newName)
	}
	return errors.New("unexpected Rename call")
}

func (r *benchgateStubRoot) Remove(name string) error {
	if r.remove != nil {
		return r.remove(name)
	}
	return errors.New("unexpected Remove call")
}

func (r *benchgateStubRoot) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

func (w *errorWriter) Write(_ []byte) (int, error) {
	w.calls++
	return 0, w.err
}

func restoreBenchgateSeams(t *testing.T) {
	t.Helper()
	originalResolveGitBinaryPath := resolveGitBinaryPath
	originalGitCommandContext := gitCommandContext
	originalOpenArtifactDirFn := openArtifactDirFn
	originalOpenArtifactAncestorFn := openArtifactAncestorFn
	originalOpenOrCreateArtifactFn := openOrCreateArtifactFn
	originalOpenCanonicalArtifactRoot := openCanonicalArtifactRoot
	originalOpenArtifactInputFile := openArtifactInputFile
	originalResolveArtifactPathAbs := resolveArtifactPathAbs
	originalWriteArtifactWithinRoot := writeArtifactWithinRoot
	originalEnforceArtifactDirPerms := enforceArtifactDirPerms
	t.Cleanup(func() {
		resolveGitBinaryPath = originalResolveGitBinaryPath
		gitCommandContext = originalGitCommandContext
		openArtifactDirFn = originalOpenArtifactDirFn
		openArtifactAncestorFn = originalOpenArtifactAncestorFn
		openOrCreateArtifactFn = originalOpenOrCreateArtifactFn
		openCanonicalArtifactRoot = originalOpenCanonicalArtifactRoot
		openArtifactInputFile = originalOpenArtifactInputFile
		resolveArtifactPathAbs = originalResolveArtifactPathAbs
		writeArtifactWithinRoot = originalWriteArtifactWithinRoot
		enforceArtifactDirPerms = originalEnforceArtifactDirPerms
	})
}

func mustResolveCallerGitPath(t *testing.T) string {
	t.Helper()
	gitPath, err := resolveGitBinaryPath()
	if err != nil {
		t.Fatalf("resolve caller git path: %v", err)
	}
	return gitPath
}

func statBenchgatePath(t *testing.T, path string) fs.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func stubArtifactRootWithChild(dirInfo fs.FileInfo, childLstat func(string) (fs.FileInfo, error)) func(string) (safeio.Root, error) {
	return func(string) (safeio.Root, error) {
		return &benchgateStubRoot{
			lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
			openRoot: func(string) (safeio.Root, error) {
				return &benchgateStubRoot{lstat: childLstat}, nil
			},
		}, nil
	}
}

func writeBenchgateInputFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeBenchgateExecutable(t *testing.T, path, content string) {
	t.Helper()
	writeBenchgateInputFile(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func stubTrustedWorktreeAddCommands(t *testing.T, worktreePath string) *int {
	t.Helper()
	calls := 0
	gitCommandContext = func(_ context.Context, path string, args ...string) (*exec.Cmd, error) {
		calls++
		if path != "/usr/bin/git" {
			t.Fatalf("git path = %q, want trusted git", path)
		}
		if calls == 1 && !reflect.DeepEqual(args, append(gitexec.SafeConfigArgs(), "config", "--null", "--includes", "--get-regexp", `^filter\..*\.(smudge|process|clean|required)$`)) {
			t.Fatalf("config args = %#v", args)
		}
		if calls == 2 && !reflect.DeepEqual(args, append(gitexec.SafeConfigArgs(), "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", "--", worktreePath, "deadbeef")) {
			t.Fatalf("worktree args = %#v", args)
		}
		return exec.Command("sh", "-c", ":"), nil
	}
	return &calls
}

func assertBenchgateFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, string(data), want)
	}
}

func assertBenchgatePathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to remain absent, got %v", path, err)
	}
}

func assertBenchgatePathIsSymlink(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to remain a symlink, mode=%v", path, info.Mode())
	}
}

func benchgateDirCaseInsensitive(t *testing.T, dir string) bool {
	t.Helper()
	caseInsensitive, err := artifactDirCaseInsensitive(dir)
	if err != nil {
		t.Fatalf("probe filesystem case sensitivity for %s: %v", dir, err)
	}
	return caseInsensitive
}

func isolatedGitEnv(t *testing.T) []string {
	t.Helper()

	return testutil.IsolatedGitEnv(t)
}

func assertBenchgateFailureArtifacts(t *testing.T, summaryPath, statusPath, message string) {
	t.Helper()
	summary, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(summary), "Comparison could not be evaluated.") || !strings.Contains(string(summary), message) {
		t.Fatalf("unexpected summary: %q", summary)
	}
	if strings.Contains(string(summary), "Result: memory benchmark gate passed.") || strings.Contains(string(summary), "Result: memory benchmark regression detected.") {
		t.Fatalf("invalid comparison summary must not contain threshold result: %q", summary)
	}

	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if string(status) != "2\n" {
		t.Fatalf("status = %q, want %q", status, "2\n")
	}
}

func assertPathMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s perms = %o, want %o", path, got, want)
	}
}
