package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestInstalledPreCommitRunsStagedCI(t *testing.T) {
	repoDir := newHookFixture(t)
	sentinel := filepath.Join(repoDir, "ci-ran")
	writeFileMode(t, filepath.Join(repoDir, ".githooks", "pre-commit"), "#!/bin/sh\nexit 99\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@git diff HEAD^ HEAD -- sample.go | grep -F 'return 2'\n\t@printf ci >"+sentinel+"\n")
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repoDir, "add", "sample.go", "Makefile")
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@exit 99\n")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "full CI")
	if err != nil {
		t.Fatalf("commit with staged CI: %v\n%s", err, output)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("staged CI did not run: %v", err)
	}
	assertHookWorktreeCleaned(t, repoDir)
}

func TestInstalledPreCommitBlocksFailedCI(t *testing.T) {
	repoDir := newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@echo fixture-ci-failed; exit 42\n")
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repoDir, "add", "sample.go", "Makefile")
	before := testutil.GitOutput(t, repoDir, "rev-parse", "HEAD")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "failing CI")
	if err == nil || !strings.Contains(output, "fixture-ci-failed") {
		t.Fatalf("expected CI failure to block commit, got %v:\n%s", err, output)
	}
	if testutil.GitOutput(t, repoDir, "rev-parse", "HEAD") != before {
		t.Fatal("failed CI created a commit")
	}
	assertHookWorktreeCleaned(t, repoDir)
}

func TestInstalledPreCommitIgnoresCheckoutHooks(t *testing.T) {
	for _, hookExit := range []string{"0", "42"} {
		t.Run("exit_"+hookExit, func(t *testing.T) {
			repoDir := newHookFixture(t)
			hookDir := strings.TrimSpace(testutil.GitOutput(t, repoDir, "config", "--get", "core.hooksPath"))
			writeFileMode(t, filepath.Join(hookDir, "post-checkout"), "#!/bin/sh\nprintf 'ci:\\n\\t@true\\n' > Makefile\nexit "+hookExit+"\n", 0o755)
			writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@echo staged-ci-failed; exit 42\n")
			testutil.RunGit(t, repoDir, "add", "Makefile")
			before := testutil.GitOutput(t, repoDir, "rev-parse", "HEAD")
			output, err := hookCommand(repoDir, "git", "commit", "-m", "staged CI failure")
			if err == nil || !strings.Contains(output, "staged-ci-failed") {
				t.Fatalf("expected staged CI failure despite checkout hook, got %v:\n%s", err, output)
			}
			if testutil.GitOutput(t, repoDir, "rev-parse", "HEAD") != before {
				t.Fatal("failed staged CI created a commit")
			}
			assertHookWorktreeCleaned(t, repoDir)
		})
	}
}

func assertHookWorktreeCleaned(t *testing.T, repoDir string) {
	t.Helper()
	worktrees := testutil.GitOutput(t, repoDir, "worktree", "list", "--porcelain")
	if strings.Count(worktrees, "worktree ") != 1 {
		t.Fatalf("CI left a temporary worktree registered: %s", worktrees)
	}
}

func TestInstalledPreCommitPreservesAmendParents(t *testing.T) {
	repoDir := newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@test -z \"$${LOPPER_HOOK_AMEND-}\"\n\t@test \"$$(git rev-parse HEAD^)\" = \"$$(git rev-parse before-amend^)\"\n\t@! git merge-base --is-ancestor before-amend HEAD\n")
	testutil.RunGit(t, repoDir, "add", "Makefile")
	testutil.RunGit(t, repoDir, "-c", "core.hooksPath=/dev/null", "commit", "-m", "amend fixture")
	testutil.RunGit(t, repoDir, "tag", "before-amend")
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repoDir, "add", "sample.go")
	if output, err := hookCommandWithEnv(repoDir, []string{"LOPPER_HOOK_AMEND=1"}, "git", "commit", "--amend", "--no-edit"); err != nil {
		t.Fatalf("CI did not model amend parents: %v\n%s", err, output)
	}
	assertHookWorktreeCleaned(t, repoDir)
}

func TestInstalledPreCommitPreservesMergeParents(t *testing.T) {
	repoDir := newHookFixture(t)
	testutil.RunGit(t, repoDir, "checkout", "-b", "incoming")
	writeFile(t, filepath.Join(repoDir, "incoming.txt"), "incoming change\n")
	testutil.RunGit(t, repoDir, "add", "incoming.txt")
	testutil.RunGit(t, repoDir, "-c", "core.hooksPath=/dev/null", "commit", "-m", "incoming change")
	testutil.RunGit(t, repoDir, "checkout", "main")
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@git merge-base --is-ancestor incoming HEAD\n\t@test \"$$(git rev-list --parents -n 1 HEAD | wc -w | tr -d ' ')\" = 3\n")
	testutil.RunGit(t, repoDir, "add", "Makefile")
	testutil.RunGit(t, repoDir, "-c", "core.hooksPath=/dev/null", "commit", "-m", "local change")
	testutil.RunGit(t, repoDir, "merge", "--no-commit", "--no-ff", "incoming")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "merge incoming")
	if err != nil {
		t.Fatalf("merge CI snapshot lost incoming ancestry: %v\n%s", err, output)
	}
	assertHookWorktreeCleaned(t, repoDir)
}

func TestInstalledPreCommitChecksFullTreeFromSparseCheckout(t *testing.T) {
	repoDir := newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, "included", "keep.txt"), "keep\n")
	writeFile(t, filepath.Join(repoDir, "excluded", "required.txt"), "required\n")
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@test -f included/keep.txt\n\t@test -f excluded/required.txt\n")
	testutil.RunGit(t, repoDir, "add", ".")
	testutil.RunGit(t, repoDir, "-c", "core.hooksPath=/dev/null", "commit", "-m", "sparse fixture")
	testutil.RunGit(t, repoDir, "sparse-checkout", "set", "included")
	writeFile(t, filepath.Join(repoDir, "included", "keep.txt"), "updated\n")
	testutil.RunGit(t, repoDir, "add", "included/keep.txt")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "sparse commit")
	if err != nil {
		t.Fatalf("CI did not check the full staged tree: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "excluded", "required.txt")); !os.IsNotExist(err) {
		t.Fatalf("CI changed the caller's sparse checkout: %v", err)
	}
	if patterns := strings.TrimSpace(testutil.GitOutput(t, repoDir, "sparse-checkout", "list")); patterns != "included" {
		t.Fatalf("CI changed the caller's sparse patterns: %q", patterns)
	}
	assertHookWorktreeCleaned(t, repoDir)
}

func TestHooksInstallRefreshesManagedSnapshot(t *testing.T) {
	repoDir := newHookFixture(t)
	hookDir := strings.TrimSpace(testutil.GitOutput(t, repoDir, "config", "--get", "core.hooksPath"))
	managedHook := filepath.Join(hookDir, "pre-commit")
	writeFileMode(t, managedHook, "#!/bin/sh\nexit 99\n", 0o755)
	runCommand(t, repoDir, "make", "hooks-install")
	want, err := os.ReadFile(filepath.Join(repoDir, ".githooks", "pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(managedHook)
	if err != nil || string(got) != string(want) {
		t.Fatalf("installed snapshot was not refreshed: %v", err)
	}
}

func TestInstalledPreCommitRejectsUnformattedStagedGo(t *testing.T) {
	assertInstalledPreCommitRejects(t, "sample.go", "package sample\n\nfunc Value() int {return 2}\n", "unformatted", "gofmt-formatted")
}

func TestInstalledPreCommitRejectsStagedWhitespace(t *testing.T) {
	assertInstalledPreCommitRejects(t, "notes.txt", "trailing space \n", "whitespace", "trailing whitespace")
}

func assertInstalledPreCommitRejects(t *testing.T, path, contents, message, expected string) {
	t.Helper()

	repoDir := newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, path), contents)
	testutil.RunGit(t, repoDir, "add", path)
	output, err := hookCommand(repoDir, "git", "commit", "-m", message)
	if err == nil || !strings.Contains(output, expected) {
		t.Fatalf("expected staged %s rejection, got %v:\n%s", expected, err, output)
	}
}

func TestInstalledPreCommitUsesStagedGoContent(t *testing.T) {
	repoDir := newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repoDir, "add", "sample.go")
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int {return 3}\n")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "staged formatting")
	if err != nil {
		t.Fatalf("commit staged formatting: %v\n%s", err, output)
	}
}

func TestInstalledPreCommitRejectsExternalGofmtLinkIntoCheckout(t *testing.T) {
	repoDir := newHookFixture(t)
	sentinel := filepath.Join(repoDir, "branch-gofmt-ran")
	toolsDir := filepath.Join(repoDir, "tools")
	selectedDir := filepath.Join(filepath.Dir(repoDir), "selected")
	chainDir := filepath.Join(filepath.Dir(repoDir), "chain")
	writeFileMode(t, filepath.Join(toolsDir, "gofmt"), "#!/bin/sh\nprintf branch >"+sentinel+"\n", 0o755)
	if err := os.MkdirAll(selectedDir, 0o755); err != nil {
		t.Fatalf("create selected formatter directory: %v", err)
	}
	if err := os.MkdirAll(chainDir, 0o755); err != nil {
		t.Fatalf("create formatter chain directory: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "chain", "gofmt"), filepath.Join(selectedDir, "gofmt")); err != nil {
		t.Fatalf("create selected formatter link: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "repo", "tools", "gofmt"), filepath.Join(chainDir, "gofmt")); err != nil {
		t.Fatalf("create checkout formatter link: %v", err)
	}
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repoDir, "add", "sample.go")
	hookDir, err := hookCommand(repoDir, "git", "config", "--get", "core.hooksPath")
	if err != nil {
		t.Fatalf("read managed hook path: %v", err)
	}
	env := []string{"PATH=" + selectedDir + ":/usr/bin:/bin"}
	output, err := hookCommandWithEnv(repoDir, env, filepath.Join(strings.TrimSpace(hookDir), "pre-commit"))
	if err == nil || !strings.Contains(output, "checkout-controlled hook tool") {
		t.Fatalf("expected checkout gofmt refusal, got %v:\n%s", err, output)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("checkout-controlled gofmt ran: %v", err)
	}
}

func TestInstalledPreCommitRejectsDirectCheckoutGofmt(t *testing.T) {
	repoDir := newHookFixture(t)
	sentinel := filepath.Join(repoDir, "branch-gofmt-ran")
	toolsDir := filepath.Join(repoDir, "tools")
	writeFileMode(t, filepath.Join(toolsDir, "gofmt"), "#!/bin/sh\nprintf branch >"+sentinel+"\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repoDir, "add", "sample.go")
	hookDir, err := hookCommand(repoDir, "git", "config", "--get", "core.hooksPath")
	if err != nil {
		t.Fatalf("read managed hook path: %v", err)
	}
	output, err := hookCommandWithEnv(repoDir, []string{"PATH=" + toolsDir + ":/usr/bin:/bin"}, filepath.Join(strings.TrimSpace(hookDir), "pre-commit"))
	if err == nil || !strings.Contains(output, "checkout-controlled hook tool") {
		t.Fatalf("expected direct checkout gofmt refusal, got %v:\n%s", err, output)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("direct checkout gofmt ran: %v", err)
	}
}

func TestInstalledPreCommitUsesSelectedExternalGofmt(t *testing.T) {
	repoDir := newHookFixture(t)
	trustedDir := filepath.Join(filepath.Dir(repoDir), "trusted")
	selectedDir := filepath.Join(filepath.Dir(repoDir), "selected")
	staleDir := filepath.Join(filepath.Dir(repoDir), "stale")
	usedMarker := filepath.Join(repoDir, "trusted-gofmt-ran")
	hostGofmt, err := exec.LookPath("gofmt")
	if err != nil {
		t.Fatalf("find host gofmt: %v", err)
	}
	writeFileMode(t, filepath.Join(trustedDir, "gofmt"), "#!/bin/sh\nprintf trusted >"+usedMarker+"\nexec \""+hostGofmt+"\" \"$@\"\n", 0o755)
	writeFileMode(t, filepath.Join(staleDir, "gofmt"), "#!/bin/sh\nexit 99\n", 0o755)
	if err := os.MkdirAll(selectedDir, 0o755); err != nil {
		t.Fatalf("create selected formatter directory: %v", err)
	}
	if err := os.Symlink(filepath.Join("..", "trusted", "gofmt"), filepath.Join(selectedDir, "gofmt")); err != nil {
		t.Fatalf("create trusted formatter link: %v", err)
	}
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repoDir, "add", "sample.go")
	hookDir, err := hookCommand(repoDir, "git", "config", "--get", "core.hooksPath")
	if err != nil {
		t.Fatalf("read managed hook path: %v", err)
	}
	output, err := hookCommandWithEnv(repoDir, []string{"PATH=" + selectedDir + ":" + staleDir + ":/usr/bin:/bin"}, filepath.Join(strings.TrimSpace(hookDir), "pre-commit"))
	if err != nil {
		t.Fatalf("run selected external gofmt: %v\n%s", err, output)
	}
	if _, err := os.Stat(usedMarker); err != nil {
		t.Fatalf("expected selected trusted gofmt to run: %v", err)
	}
}

func TestHooksInstallPreservesCustomPathAndManagedUninstall(t *testing.T) {
	repoDir := newHookFixture(t)
	runCommand(t, repoDir, "make", "hooks-uninstall")
	testutil.RunGit(t, repoDir, "config", "core.hooksPath", "/custom/hooks")
	output, err := hookCommand(repoDir, "make", "hooks-install")
	if err == nil || !strings.Contains(output, "Refusing to replace") {
		t.Fatalf("expected custom hook path refusal, got %v:\n%s", err, output)
	}
	runCommand(t, repoDir, "make", "hooks-uninstall")
	got, getErr := hookCommand(repoDir, "git", "config", "--get-all", "core.hooksPath")
	if getErr != nil || got != "/custom/hooks\n" {
		t.Fatalf("custom hook path changed: %v, %q", getErr, got)
	}
	testutil.RunGit(t, repoDir, "config", "--unset", "core.hooksPath")
	runCommand(t, repoDir, "make", "hooks-install")
	runCommand(t, repoDir, "make", "hooks-uninstall")
	output, err = hookCommand(repoDir, "git", "config", "--get", "core.hooksPath")
	if err == nil || output != "" {
		t.Fatalf("expected managed hook path to be removed, got %v: %q", err, output)
	}
}

func TestHooksUninstallRemovesLegacyManagedPath(t *testing.T) {
	repoDir := newHookFixture(t)
	sentinel := filepath.Join(repoDir, "legacy-hook-ran")
	writeFileMode(t, filepath.Join(repoDir, ".githooks", "pre-commit"), "#!/bin/sh\nprintf legacy >"+sentinel+"\n", 0o755)
	testutil.RunGit(t, repoDir, "config", "--local", "core.hooksPath", ".githooks")

	runCommand(t, repoDir, "make", "hooks-uninstall")
	output, err := hookCommand(repoDir, "git", "config", "--local", "--get", "core.hooksPath")
	if err == nil || output != "" {
		t.Fatalf("expected legacy managed hook path to be removed, got %v: %q", err, output)
	}

	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repoDir, "add", "sample.go")
	output, err = hookCommand(repoDir, "git", "commit", "-m", "uninstall legacy hook")
	if err != nil {
		t.Fatalf("commit after legacy hook uninstall: %v\n%s", err, output)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("legacy checkout hook ran after uninstall: %v", err)
	}
}

func TestHooksInstallRejectsNonExecutableManagedHook(t *testing.T) {
	repoDir := newHookFixture(t)
	hookDir, err := hookCommand(repoDir, "git", "config", "--get", "core.hooksPath")
	if err != nil {
		t.Fatalf("read retained managed hook path: %v", err)
	}
	managedHook := filepath.Join(strings.TrimSpace(hookDir), "pre-commit")
	runCommand(t, repoDir, "make", "hooks-uninstall")
	if err := os.Chmod(managedHook, 0o644); err != nil {
		t.Fatalf("make managed hook non-executable: %v", err)
	}
	output, err := hookCommand(repoDir, "make", "hooks-install")
	if err == nil || !strings.Contains(output, "executable") {
		t.Fatalf("expected non-executable managed hook rejection, got %v:\n%s", err, output)
	}
	if output, err := hookCommand(repoDir, "git", "config", "--local", "--get", "core.hooksPath"); err == nil || output != "" {
		t.Fatalf("expected failed reinstall to leave hooks path unset, got %v: %q", err, output)
	}
}

func TestHooksInstallWorksFromLinkedWorktree(t *testing.T) {
	repoDir := newHookFixture(t)
	runCommand(t, repoDir, "make", "hooks-uninstall")
	linkedDir := filepath.Join(filepath.Dir(repoDir), "linked")
	testutil.RunGit(t, repoDir, "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "make", "hooks-install")
	testutil.RunGit(t, repoDir, "config", "core.bare", "true")
	writeFile(t, filepath.Join(linkedDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, linkedDir, "add", "sample.go")
	output, err := hookCommandWithEnv(linkedDir, []string{"GIT_CONFIG_COUNT=01", "GIT_CONFIG_KEY_0=advice.detachedHead", "GIT_CONFIG_VALUE_0=false"}, "git", "-c", "core.bare=false", "commit", "-m", "linked hook")
	if err != nil {
		t.Fatalf("commit from linked worktree: %v\n%s", err, output)
	}
}

func TestInstalledPreCommitUsesAlternateCommonDirectory(t *testing.T) {
	repoDir := newHookFixture(t)
	commonDir := filepath.Join(t.TempDir(), "common")
	if err := os.CopyFS(commonDir, os.DirFS(filepath.Join(repoDir, ".git"))); err != nil {
		t.Fatal(err)
	}
	env := []string{"GIT_COMMON_DIR=" + commonDir}
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@test -z \"$${GIT_COMMON_DIR-}\"\n\t@git show HEAD:sample.go | grep -F 'return 2'\n")
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	if output, err := hookCommandWithEnv(repoDir, env, "git", "add", "sample.go", "Makefile"); err != nil {
		t.Fatalf("stage with alternate common directory: %v\n%s", err, output)
	}
	if output, err := hookCommandWithEnv(repoDir, env, "git", "commit", "-m", "alternate common directory"); err != nil {
		t.Fatalf("commit with alternate common directory: %v\n%s", err, output)
	}
	assertHookWorktreeCleaned(t, repoDir)
}

func TestInstalledPreCommitUsesAlternateObjectDatabase(t *testing.T) {
	repoDir := newHookFixture(t)
	originalHead := strings.TrimSpace(testutil.GitOutput(t, repoDir, "rev-parse", "HEAD"))
	objects := t.TempDir()
	env := []string{"GIT_OBJECT_DIRECTORY=" + objects, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(repoDir, ".git", "objects")}
	baseCommit, err := hookCommandWithEnv(repoDir, env, "git", "commit-tree", "HEAD^{tree}", "-p", "HEAD", "-m", "alternate CI base")
	if err != nil {
		t.Fatalf("create alternate CI base: %v\n%s", err, baseCommit)
	}
	if output, err := hookCommandWithEnv(repoDir, env, "git", "update-ref", "refs/remotes/ci-base", strings.TrimSpace(baseCommit)); err != nil {
		t.Fatalf("record alternate CI base: %v\n%s", err, output)
	}
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@git rev-parse --verify refs/remotes/ci-base^{commit}\n\t@test -z \"$${GIT_OBJECT_DIRECTORY-}\"\n\t@test -z \"$${GIT_ALTERNATE_OBJECT_DIRECTORIES-}\"\n\t@git show HEAD:sample.go | grep -F 'return 2'\n")
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	if output, err := hookCommandWithEnv(repoDir, env, "git", "add", "sample.go", "Makefile"); err != nil {
		t.Fatalf("stage alternate objects: %v\n%s", err, output)
	}
	hookDir := strings.TrimSpace(testutil.GitOutput(t, repoDir, "config", "--get", "core.hooksPath"))
	if output, err := hookCommandWithEnv(repoDir, env, filepath.Join(hookDir, "pre-commit")); err != nil {
		t.Fatalf("CI lost alternate objects: %v\n%s", err, output)
	}
	packs, err := filepath.Glob(filepath.Join(repoDir, ".git", "objects", "pack", "*.idx"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("expected one materialized snapshot pack, got %v: %v", packs, err)
	}
	packContents := testutil.GitOutput(t, repoDir, "verify-pack", "-v", packs[0])
	if strings.Contains(packContents, originalHead) {
		t.Fatal("snapshot pack duplicated history already in normal storage")
	}
	assertHookWorktreeCleaned(t, repoDir)
}

func TestInstalledPreCommitUsesAlternateIndex(t *testing.T) {
	repoDir := newHookFixture(t)
	indexPath := filepath.Join(repoDir, "alternate-index")
	_, err := hookCommandWithEnv(repoDir, []string{"GIT_INDEX_FILE=" + indexPath}, "git", "read-tree", "HEAD")
	if err != nil {
		t.Fatalf("prepare alternate index: %v", err)
	}
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int {return 2}\n")
	_, err = hookCommandWithEnv(repoDir, []string{"GIT_INDEX_FILE=" + indexPath}, "git", "add", "sample.go")
	if err != nil {
		t.Fatalf("stage alternate index: %v", err)
	}
	hookPath, err := hookCommand(repoDir, "git", "config", "--get", "core.hooksPath")
	if err != nil {
		t.Fatalf("read managed hook path: %v", err)
	}
	hookPath = filepath.Join(strings.TrimSpace(hookPath), "pre-commit")
	output, err := hookCommandWithEnv(repoDir, []string{"GIT_INDEX_FILE=" + indexPath, "GIT_CONFIG_COUNT=01", "GIT_CONFIG_KEY_0=advice.detachedHead", "GIT_CONFIG_VALUE_0=false"}, hookPath)
	if err == nil || !strings.Contains(output, "gofmt-formatted") {
		t.Fatalf("expected alternate index formatting rejection, got %v:\n%s", err, output)
	}
}

func TestInstalledPreCommitHandlesStageLikeGoFileNames(t *testing.T) {
	repoDir := newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, "0:formatted.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repoDir, "add", "0:formatted.go")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "stage-like formatted filename")
	if err != nil {
		t.Fatalf("commit formatted stage-like filename: %v\n%s", err, output)
	}

	writeFile(t, filepath.Join(repoDir, "1:unformatted.go"), "package sample\n\nfunc Value() int {return 3}\n")
	testutil.RunGit(t, repoDir, "add", "1:unformatted.go")
	output, err = hookCommand(repoDir, "git", "commit", "-m", "stage-like unformatted filename")
	if err == nil || !strings.Contains(output, "gofmt-formatted") {
		t.Fatalf("expected unformatted stage-like filename rejection, got %v:\n%s", err, output)
	}

	repoDir = newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, "foo.go"), "package sample\n\nfunc Value() int { return 4 }\n")
	writeFile(t, filepath.Join(repoDir, "0:foo.go"), "package sample\n\nfunc Value() int {return 5}\n")
	testutil.RunGit(t, repoDir, "add", "foo.go", "0:foo.go")
	output, err = hookCommand(repoDir, "git", "commit", "-m", "stage-like sibling filename")
	if err == nil || !strings.Contains(output, "gofmt-formatted") {
		t.Fatalf("expected unformatted stage-like sibling rejection, got %v:\n%s", err, output)
	}
}

func newHookFixture(t *testing.T) string {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	runCommand(t, filepath.Dir(repoDir), "git", "init", "-b", "main", repoDir)
	testutil.RunGit(t, repoDir, "config", "user.name", "Hook Test")
	testutil.RunGit(t, repoDir, "config", "user.email", "hook-test@example.com")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	makefile, err := os.ReadFile(filepath.Join(filepath.Dir(cwd), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	_, installers, ok := strings.Cut(string(makefile), "hooks-install:\n")
	if !ok {
		t.Fatal("missing hook installation targets")
	}
	installers, _, _ = strings.Cut(installers, "\nvscode-extension-install:")
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@test -z \"$${GIT_INDEX_FILE-}\"\n\t@test -z \"$${GIT_CONFIG_COUNT-}\"\nhooks-install:\n"+installers)
	copyHookFixtureFile(t, filepath.Join(filepath.Dir(cwd), ".githooks", "pre-commit"), filepath.Join(repoDir, ".githooks", "pre-commit"), 0o755)
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 1 }\n")
	testutil.RunGit(t, repoDir, "add", ".")
	testutil.RunGit(t, repoDir, "-c", "core.hooksPath=/dev/null", "commit", "-m", "revision A")
	runCommand(t, repoDir, "make", "hooks-install")
	return repoDir
}

func copyHookFixtureFile(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read fixture source %s: %v", source, err)
	}
	writeFileMode(t, destination, string(contents), mode)
}

func hookCommand(dir, name string, args ...string) (string, error) {
	return hookCommandWithEnv(dir, nil, name, args...)
}

func hookCommandWithEnv(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = withoutGitEnv()
	for _, setting := range env {
		name, _, _ := strings.Cut(setting, "=")
		for index, existing := range cmd.Env {
			if strings.HasPrefix(existing, name+"=") {
				cmd.Env[index] = setting
				setting = ""
				break
			}
		}
		if setting != "" {
			cmd.Env = append(cmd.Env, setting)
		}
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func TestInstalledPreCommitExcludesSiblingTools(t *testing.T) {
	for _, direction := range []string{"main-to-linked", "linked-to-main"} {
		for _, tool := range []string{"git", "gofmt", "make"} {
			for _, link := range []bool{false, true} {
				t.Run(direction+"/"+tool+"/"+map[bool]string{false: "direct", true: "symlink"}[link], func(t *testing.T) {
					assertSiblingHookToolExcluded(t, direction, tool, link)
				})
			}
		}
	}
}

func assertSiblingHookToolExcluded(t *testing.T, direction, tool string, link bool) {
	t.Helper()
	main := newHookFixture(t)
	linked := filepath.Join(filepath.Dir(main), "linked\ncheckout")
	testutil.RunGit(t, main, "worktree", "add", "--detach", linked)
	current, foreign := main, linked
	if direction == "linked-to-main" {
		current, foreign = linked, main
	}
	marker := filepath.Join(t.TempDir(), "executed")
	toolDir := filepath.Join(foreign, "tools")
	writeFileMode(t, filepath.Join(toolDir, tool), "#!/bin/sh\ntouch '"+marker+"'\nexit 99\n", 0o755)
	if link {
		external := t.TempDir()
		if err := os.Symlink(filepath.Join(toolDir, tool), filepath.Join(external, tool)); err != nil {
			t.Fatal(err)
		}
		toolDir = external
	}
	writeFile(t, filepath.Join(current, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, current, "add", "sample.go")
	hook := strings.TrimSpace(testutil.GitOutput(t, current, "config", "--get", "core.hooksPath"))
	output, err := hookCommandWithEnv(current, []string{"PATH=" + toolDir + ":" + os.Getenv("PATH")}, filepath.Join(hook, "pre-commit"))
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("checkout-controlled %s executed: %v, %v\n%s", tool, statErr, err, output)
	}
	if link && err == nil {
		t.Fatalf("expected external symlink into checkout to fail closed: %s", output)
	}
}

func TestInstalledPreCommitKeepsFilteredPathInCI(t *testing.T) {
	repo := newHookFixture(t)
	tools := filepath.Join(repo, "tools")
	writeFileMode(t, filepath.Join(tools, "untrusted-probe"), "#!/bin/sh\nexit 99\n", 0o755)
	writeFile(t, filepath.Join(repo, "Makefile"), "ci:\n\t@! command -v untrusted-probe\n")
	testutil.RunGit(t, repo, "add", "Makefile")
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	output, err := hookCommandWithEnv(repo, []string{"PATH=" + tools + ":.:" + os.Getenv("PATH")}, filepath.Join(hook, "pre-commit"))
	if err != nil {
		t.Fatalf("filtered PATH not carried into CI: %v\n%s", err, output)
	}
}

func TestInstalledPreCommitAcceptsExternalPrefixAndMissingWorktree(t *testing.T) {
	repo := newHookFixture(t)
	missing := filepath.Join(filepath.Dir(repo), "missing")
	testutil.RunGit(t, repo, "worktree", "add", "--detach", missing)
	if err := os.RemoveAll(missing); err != nil {
		t.Fatal(err)
	}
	tools := repo + "-external\nlocation"
	host, err := exec.LookPath("gofmt")
	if err != nil {
		t.Fatal(err)
	}
	writeFileMode(t, filepath.Join(tools, "gofmt"), "#!/bin/sh\nexec '"+host+"' \"$@\"\n", 0o755)
	writeFile(t, filepath.Join(repo, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repo, "add", "sample.go")
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	output, err := hookCommandWithEnv(repo, []string{"PATH=" + tools + ":" + os.Getenv("PATH")}, filepath.Join(hook, "pre-commit"))
	if err != nil {
		t.Fatalf("external prefix or missing foreign worktree rejected: %v\n%s", err, output)
	}
	if !strings.Contains(testutil.GitOutput(t, repo, "worktree", "list", "--porcelain"), missing) {
		t.Fatal("hook pruned the missing foreign worktree")
	}
}

func TestInstalledPreCommitRejectsInvalidInventory(t *testing.T) {
	for _, response := range []struct{ name, script string }{
		{"failed", "exit 42"},
		{"empty", "exit 0"},
		{"unterminated", "printf 'worktree /foreign'"},
		{"unknown-field", "printf 'garbage\\0\\0'"},
		{"foreign", "printf 'worktree /foreign\\0HEAD abc\\0\\0'"},
		{"missing-head", "printf 'worktree %s\\0\\0' \"$PWD\""},
		{"incomplete", "printf 'worktree %s\\0HEAD abc\\0' \"$PWD\""},
	} {
		t.Run(response.name, func(t *testing.T) {
			repo := newHookFixture(t)
			hook := filepath.Join(strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath")), "pre-commit")
			contents, err := os.ReadFile(hook)
			if err != nil {
				t.Fatal(err)
			}
			inventoryGit := filepath.Join(t.TempDir(), "inventory-git")
			writeFileMode(t, inventoryGit, "#!/bin/sh\n"+response.script+"\n", 0o755)
			// Alter only the trusted test snapshot, not the production PATH.
			writeFileMode(t, hook, strings.Replace(string(contents), "/usr/bin/git --no-pager", "'"+inventoryGit+"' --no-pager", 1), 0o755)
			output, err := hookCommand(repo, hook)
			if err == nil {
				t.Fatalf("invalid inventory permitted CI: %s", output)
			}
		})
	}
}

func TestInstalledPreCommitChecksEverySymlinkHop(t *testing.T) {
	for _, kind := range []string{"return-to-external", "directory-link", "cycle", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			repo := newHookFixture(t)
			external := t.TempDir()
			linkExternalHookTools(t, external)
			target, err := exec.LookPath("gofmt")
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "return-to-external":
				intermediate := filepath.Join(repo, "hop")
				if err := os.Symlink(target, intermediate); err != nil {
					t.Fatal(err)
				}
				target = intermediate
			case "directory-link":
				writeFileMode(t, filepath.Join(repo, "tools", "gofmt"), "#!/bin/sh\nexit 99\n", 0o755)
				if err := os.Symlink(filepath.Join(repo, "tools"), filepath.Join(external, "directory")); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(external, "directory", "gofmt")
			case "cycle":
				target = filepath.Join(external, "gofmt")
			case "dangling":
				target = filepath.Join(external, "missing")
			}
			if err := os.Symlink(target, filepath.Join(external, "gofmt")); err != nil {
				t.Fatal(err)
			}
			hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
			// No fallback gofmt: invalid links must fail closed.
			output, err := hookCommandWithEnv(repo, []string{"PATH=" + external}, filepath.Join(hook, "pre-commit"))
			if err == nil {
				t.Fatalf("invalid symlink accepted: %s", output)
			}
		})
	}
}

func TestInstalledPreCommitDisablesGitExecutableConfiguration(t *testing.T) {
	repo := newHookFixture(t)
	external := t.TempDir()
	marker := filepath.Join(external, "git-called")
	wrapper := "#!/bin/sh\n" +
		"test \"$1\" = --no-pager || exit 91\nshift\n" +
		"for expected in core.bare=false core.fsmonitor=false core.hooksPath=/dev/null; do\n" +
		"  test \"$1\" = -c && test \"$2\" = \"$expected\" || exit 92\n  shift 2\ndone\n" +
		"printf called >> '" + marker + "'\n" +
		"exec /usr/bin/git --no-pager -c core.bare=false -c core.fsmonitor=false -c core.hooksPath=/dev/null \"$@\"\n"
	writeFileMode(t, filepath.Join(external, "git"), wrapper, 0o755)
	writeFileMode(t, filepath.Join(external, "monitor"), "#!/bin/sh\nexit 99\n", 0o755)
	testutil.RunGit(t, repo, "config", "core.fsmonitor", filepath.Join(external, "monitor"))
	writeFile(t, filepath.Join(repo, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	testutil.RunGit(t, repo, "-c", "core.fsmonitor=false", "add", "sample.go")
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	output, err := hookCommandWithEnv(repo, []string{"PATH=" + external + ":" + os.Getenv("PATH"), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.bare", "GIT_CONFIG_VALUE_0=true"}, filepath.Join(hook, "pre-commit"))
	if err != nil {
		t.Fatalf("hook Git call missing isolation flags: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("selected external Git was not used: %v", err)
	}
}

func TestInstalledPreCommitAllowsInaccessibleForeignWorktree(t *testing.T) {
	repo := newHookFixture(t)
	foreign := filepath.Join(filepath.Dir(repo), "inaccessible")
	testutil.RunGit(t, repo, "worktree", "add", "--detach", foreign)
	if err := os.Chmod(foreign, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(foreign, 0o755); err != nil {
			t.Errorf("restore foreign worktree permissions: %v", err)
		}
	})
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	output, err := hookCommand(repo, filepath.Join(hook, "pre-commit"))
	if err != nil {
		t.Fatalf("unrelated inaccessible registration blocked CI: %v\n%s", err, output)
	}
}

func TestInstalledPreCommitRejectsCheckoutCaseAlias(t *testing.T) {
	repo := newHookFixture(t)
	alias := filepath.Join(filepath.Dir(repo), "REPO")
	if _, err := os.Stat(alias); err != nil {
		t.Skip("filesystem uses case-sensitive paths")
	}
	writeFileMode(t, filepath.Join(repo, "tools", "gofmt"), "#!/bin/sh\nexit 0\n", 0o755)
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	output, err := hookCommandWithEnv(repo, []string{"PATH=" + filepath.Join(alias, "tools") + ":" + os.Getenv("PATH")}, filepath.Join(hook, "pre-commit"))
	if err == nil {
		t.Fatalf("case-aliased checkout tool accepted: %s", output)
	}
}

func TestInstalledPreCommitRejectsSparseInspectionFailure(t *testing.T) {
	repo := newHookFixture(t)
	external := t.TempDir()
	writeFileMode(t, filepath.Join(external, "git"), "#!/bin/sh\ncase \"$*\" in *'config --bool core.sparseCheckout'*) exit 42 ;; esac\nexec /usr/bin/git \"$@\"\n", 0o755)
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	output, err := hookCommandWithEnv(repo, []string{"PATH=" + external + ":" + os.Getenv("PATH")}, filepath.Join(hook, "pre-commit"))
	if err == nil || strings.Contains(output, "running full make ci") {
		t.Fatalf("sparse inspection failure was ignored: %v\n%s", err, output)
	}
	assertHookWorktreeCleaned(t, repo)
}

func TestInstalledPreCommitRejectsAlternateCommonWorktreeTools(t *testing.T) {
	for _, tool := range []string{"git", "gofmt", "make"} {
		t.Run(tool, func(t *testing.T) {
			assertAlternateCommonToolRejected(t, tool)
		})
	}
}

func assertAlternateCommonToolRejected(t *testing.T, tool string) {
	t.Helper()
	repo := newHookFixture(t)
	common := filepath.Join(t.TempDir(), "common")
	if err := os.CopyFS(common, os.DirFS(filepath.Join(repo, ".git"))); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(t.TempDir(), "alternate-sibling")
	env := []string{"GIT_COMMON_DIR=" + common}
	if output, err := hookCommandWithEnv(repo, env, "git", "worktree", "add", "--detach", sibling); err != nil {
		t.Fatalf("create alternate-common sibling: %v\n%s", err, output)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	toolDir := filepath.Join(sibling, "tools")
	writeFileMode(t, filepath.Join(toolDir, tool), "#!/bin/sh\nprintf executed >'"+marker+"'\nexit 99\n", 0o755)
	writeFile(t, filepath.Join(repo, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	if output, err := hookCommandWithEnv(repo, env, "git", "add", "sample.go"); err != nil {
		t.Fatalf("stage alternate-common change: %v\n%s", err, output)
	}
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	env = append(env, "PATH="+toolDir+":"+os.Getenv("PATH"))
	output, err := hookCommandWithEnv(repo, env, filepath.Join(hook, "pre-commit"))
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("alternate-common sibling %s executed: %v\n%s", tool, statErr, output)
	}
	if err == nil || !strings.Contains(output, "checkout-controlled hook tool") {
		t.Fatalf("expected alternate-common tool rejection: %v\n%s", err, output)
	}
}

func TestInstalledPreCommitRejectsUnavailableAlternateCommonInventory(t *testing.T) {
	repo := newHookFixture(t)
	external := t.TempDir()
	marker := filepath.Join(external, "git-executed")
	writeFileMode(t, filepath.Join(external, "git"), "#!/bin/sh\nprintf executed >'"+marker+"'\nexit 99\n", 0o755)
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	env := []string{"GIT_COMMON_DIR=" + filepath.Join(t.TempDir(), "missing"), "PATH=" + external + ":" + os.Getenv("PATH")}
	output, err := hookCommandWithEnv(repo, env, filepath.Join(hook, "pre-commit"))
	if err == nil || !strings.Contains(output, "Cannot inspect hook worktrees") {
		t.Fatalf("expected alternate inventory failure: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("selected Git ran before alternate inventory validation: %v", err)
	}
}

func linkExternalHookTools(t *testing.T, directory string) {
	t.Helper()
	for _, name := range []string{"git", "make"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
}
