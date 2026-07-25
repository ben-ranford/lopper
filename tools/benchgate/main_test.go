package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestMainSuccessAndFailureArtifacts(t *testing.T) {
	if runBenchgateMainIfRequested(t) {
		return
	}

	t.Run("success", func(t *testing.T) {
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
	})

	t.Run("missing ref writes status two", func(t *testing.T) {
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
	})

	t.Run("missing base-ref required", func(t *testing.T) {
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
	})

	t.Run("unrelated ref writes status two", func(t *testing.T) {
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
	})

	t.Run("artifact write failure surfaces clearly", func(t *testing.T) {
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
	})
}

func TestResolveBaseCommit(t *testing.T) {
	repo := initGitRepo(t)
	baseCommit := gitCommitFile(t, repo, "base.txt", "base")
	headCommit := gitCommitFile(t, repo, "head.txt", "head")

	got, err := resolveBaseCommit(repo, baseCommit, "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit: %v", err)
	}
	if got != baseCommit {
		t.Fatalf("base commit = %q, want %q (head %q)", got, baseCommit, headCommit)
	}
}

func TestRunMain(t *testing.T) {
	t.Run("success", func(t *testing.T) {
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
	})

	t.Run("failure", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		exitCode := runMain([]string{"-base-ref", "refs/heads/missing"}, t.TempDir(), &stdout, &stderr)
		if exitCode != 2 {
			t.Fatalf("exit code = %d, want 2", exitCode)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
		if !strings.Contains(stderr.String(), `does not resolve to a commit`) {
			t.Fatalf("stderr = %q, want missing ref diagnostic", stderr.String())
		}
	})

	t.Run("stdout write failure", func(t *testing.T) {
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
	})

	t.Run("stderr write failure", func(t *testing.T) {
		var stdout bytes.Buffer
		stderr := &errorWriter{err: errors.New("stderr write failed")}
		exitCode := runMain([]string{"-base-ref", "refs/heads/missing"}, t.TempDir(), &stdout, stderr)
		if exitCode != 2 {
			t.Fatalf("exit code = %d, want 2", exitCode)
		}
		if stderr.calls != 1 {
			t.Fatalf("stderr writes = %d, want 1", stderr.calls)
		}
	})
}

func TestResolveBaseCommitFromArgs(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		repo := initGitRepo(t)
		baseCommit := gitCommitFile(t, repo, "base.txt", "base")
		gitCommitFile(t, repo, "head.txt", "head")

		got, err := resolveBaseCommitFromArgs([]string{"-base-ref", baseCommit}, repo)
		if err != nil {
			t.Fatalf("resolve from args: %v", err)
		}
		if got != baseCommit {
			t.Fatalf("base commit = %q, want %q", got, baseCommit)
		}
	})

	t.Run("missing base-ref writes artifacts", func(t *testing.T) {
		dir := t.TempDir()
		summaryPath := filepath.Join(dir, "summary.md")
		statusPath := filepath.Join(dir, "status.txt")

		_, err := resolveBaseCommitFromArgs([]string{"-summary-out", summaryPath, "-status-out", statusPath}, dir)
		if err == nil || !strings.Contains(err.Error(), "-base-ref is required") {
			t.Fatalf("expected missing base-ref error, got %v", err)
		}
		assertBenchgateFailureArtifacts(t, summaryPath, statusPath, `-base-ref is required`)
	})

	t.Run("invalid flag returns parse error", func(t *testing.T) {
		if _, err := resolveBaseCommitFromArgs([]string{"-nope"}, t.TempDir()); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("expected invalid flag error, got %v", err)
		}
	})

	t.Run("status artifact write failure", func(t *testing.T) {
		repo := initGitRepo(t)
		blocker := filepath.Join(repo, "blocker")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatalf("write blocker: %v", err)
		}

		_, err := resolveBaseCommitFromArgs([]string{"-base-ref", "refs/heads/missing", "-summary-out", filepath.Join(repo, ".artifacts", "summary.md"), "-status-out", filepath.Join(blocker, "status.txt")}, repo)
		if err == nil || !strings.Contains(err.Error(), "write failure artifacts") {
			t.Fatalf("expected artifact write failure, got %v", err)
		}
	})

	t.Run("failure message writes artifacts without base ref", func(t *testing.T) {
		dir := t.TempDir()
		summaryPath := filepath.Join(dir, "summary.md")
		statusPath := filepath.Join(dir, "status.txt")

		_, err := resolveBaseCommitFromArgs([]string{"-summary-out", summaryPath, "-status-out", statusPath, "-failure-message", "memory benchmark head execution failed; comparison could not be evaluated"}, dir)
		if err == nil || !strings.Contains(err.Error(), "memory benchmark head execution failed") {
			t.Fatalf("expected failure message error, got %v", err)
		}
		assertBenchgateFailureArtifacts(t, summaryPath, statusPath, "memory benchmark head execution failed")
	})
}

func TestResolveBaseCommitMissingRef(t *testing.T) {
	repo := initGitRepo(t)
	gitCommitFile(t, repo, "head.txt", "head")

	_, err := resolveBaseCommit(repo, "refs/heads/does-not-exist", "HEAD")
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

	_, err := resolveBaseCommit(repo, otherCommit, "HEAD")
	if err == nil || !strings.Contains(err.Error(), "is unrelated to HEAD") {
		t.Fatalf("expected unrelated history error, got %v", err)
	}
}

func TestResolveBaseCommitIgnoresCallerGitEnvironment(t *testing.T) {
	repo := initGitRepo(t)
	want := gitCommitFile(t, repo, "base.txt", "base")
	gitCommitFile(t, repo, "head.txt", "head")

	attackerRepo := initGitRepo(t)
	gitCommitFile(t, attackerRepo, "only.txt", "only")

	t.Setenv("GIT_DIR", filepath.Join(attackerRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", attackerRepo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "attacker.index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(t.TempDir(), "attacker.objects"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(t.TempDir(), "attacker.alternates"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(t.TempDir(), "attacker.common"))
	t.Setenv("GIT_NAMESPACE", "attacker")

	got, err := resolveBaseCommit(repo, "HEAD~1", "HEAD")
	if err != nil {
		t.Fatalf("resolve base commit with polluted git env: %v", err)
	}
	if got != want {
		t.Fatalf("base commit = %q, want %q", got, want)
	}
}

func TestWriteFailureArtifacts(t *testing.T) {
	dir := t.TempDir()
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

	dir := t.TempDir()
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
	if got := info.Mode().Perm() & 0o007; got != 0 {
		t.Fatalf("artifact dir perms = %o, want no world access", info.Mode().Perm())
	}
}

func TestWriteFailureArtifactsNormalizesPreexistingArtifactPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not stable on Windows")
	}

	dir := t.TempDir()
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

	if err := writeFailureArtifacts(summaryPath, statusPath, "missing base ref"); err != nil {
		t.Fatalf("write failure artifacts: %v", err)
	}

	assertBenchgateFailureArtifacts(t, summaryPath, statusPath, "missing base ref")
	assertPathMode(t, artifactDir, artifactDirMode)
	assertPathMode(t, summaryPath, artifactFileMode)
	assertPathMode(t, statusPath, artifactFileMode)
}

func TestWriteFailureArtifactsCreatesExactPermissionsUnderRestrictiveUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits are not stable on Windows")
	}
	if !supportsProcessUmask() {
		t.Skip("process umask control unavailable on this platform")
	}

	dir := t.TempDir()
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
	statusPath := filepath.Join(t.TempDir(), "status.txt")
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
	summaryPath := filepath.Join(t.TempDir(), "summary.md")
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
	dir := t.TempDir()
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
	dir := t.TempDir()
	blocker := filepath.Join(dir, "summary-blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	summaryPath := filepath.Join(blocker, "summary.md")
	statusPath := filepath.Join(dir, "status.txt")

	err := writeFailureArtifacts(summaryPath, statusPath, "summary write error")
	if err == nil {
		t.Fatal("expected summary write error")
	}
	if !strings.Contains(err.Error(), "summary artifact:") {
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
	dir := t.TempDir()
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
	dir := t.TempDir()
	summaryBlocker := filepath.Join(dir, "summary-blocker")
	statusBlocker := filepath.Join(dir, "status-blocker")
	if err := os.WriteFile(summaryBlocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write summary blocker: %v", err)
	}
	if err := os.WriteFile(statusBlocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write status blocker: %v", err)
	}

	err := writeFailureArtifacts(filepath.Join(summaryBlocker, "summary.md"), filepath.Join(statusBlocker, "status.txt"), "both writes fail")
	if err == nil {
		t.Fatal("expected aggregate write error")
	}
	if !strings.Contains(err.Error(), "summary artifact:") {
		t.Fatalf("error = %v, want summary artifact failure", err)
	}
	if !strings.Contains(err.Error(), "status artifact:") {
		t.Fatalf("error = %v, want status artifact failure", err)
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

func initGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
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
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = isolatedGitEnv(t)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
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

type errorWriter struct {
	err   error
	calls int
}

func (w *errorWriter) Write(_ []byte) (int, error) {
	w.calls++
	return 0, w.err
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
