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
	runCommand(t, repoDir, "git", "add", "sample.go", "Makefile")
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
	runCommand(t, repoDir, "git", "add", "sample.go", "Makefile")
	before := testutil.GitOutput(t, repoDir, "rev-parse", "HEAD")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "failing CI")
	if err == nil || !strings.Contains(output, "fixture-ci-failed") {
		t.Fatalf("expected CI failure to block commit, got %v:\n%s", err, output)
	}
	if after := testutil.GitOutput(t, repoDir, "rev-parse", "HEAD"); after != before {
		t.Fatal("failed CI created a commit")
	}
	assertHookWorktreeCleaned(t, repoDir)
}

func TestInstalledPreCommitCleansFailedCheckout(t *testing.T) {
	repoDir := newHookFixture(t)
	hookDir := strings.TrimSpace(testutil.GitOutput(t, repoDir, "config", "--get", "core.hooksPath"))
	writeFileMode(t, filepath.Join(hookDir, "post-checkout"), "#!/bin/sh\necho fixture-checkout-failed >&2\nexit 42\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, repoDir, "git", "add", "sample.go")
	before := testutil.GitOutput(t, repoDir, "rev-parse", "HEAD")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "failing checkout")
	if err == nil || !strings.Contains(output, "fixture-checkout-failed") {
		t.Fatalf("expected checkout failure, got %v:\n%s", err, output)
	}
	if after := testutil.GitOutput(t, repoDir, "rev-parse", "HEAD"); after != before {
		t.Fatal("failed checkout created a commit")
	}
	assertHookWorktreeCleaned(t, repoDir)
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
	runCommand(t, repoDir, "git", "add", "Makefile")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "amend fixture")
	runCommand(t, repoDir, "git", "tag", "before-amend")
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, repoDir, "git", "add", "sample.go")
	if output, err := hookCommandWithEnv(repoDir, []string{"LOPPER_HOOK_AMEND=1"}, "git", "commit", "--amend", "--no-edit"); err != nil {
		t.Fatalf("CI did not model amend parents: %v\n%s", err, output)
	}
	assertHookWorktreeCleaned(t, repoDir)
}

func TestInstalledPreCommitPreservesMergeParents(t *testing.T) {
	repoDir := newHookFixture(t)
	runCommand(t, repoDir, "git", "checkout", "-b", "incoming")
	writeFile(t, filepath.Join(repoDir, "incoming.txt"), "incoming change\n")
	runCommand(t, repoDir, "git", "add", "incoming.txt")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "incoming change")
	runCommand(t, repoDir, "git", "checkout", "main")
	writeFile(t, filepath.Join(repoDir, "Makefile"), "ci:\n\t@git merge-base --is-ancestor incoming HEAD\n\t@test \"$$(git rev-list --parents -n 1 HEAD | wc -w | tr -d ' ')\" = 3\n")
	runCommand(t, repoDir, "git", "add", "Makefile")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "local change")
	runCommand(t, repoDir, "git", "merge", "--no-commit", "--no-ff", "incoming")
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
	runCommand(t, repoDir, "git", "add", ".")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "sparse fixture")
	runCommand(t, repoDir, "git", "sparse-checkout", "set", "included")
	writeFile(t, filepath.Join(repoDir, "included", "keep.txt"), "updated\n")
	runCommand(t, repoDir, "git", "add", "included/keep.txt")
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
	runCommand(t, repoDir, "git", "add", path)
	output, err := hookCommand(repoDir, "git", "commit", "-m", message)
	if err == nil || !strings.Contains(output, expected) {
		t.Fatalf("expected staged %s rejection, got %v:\n%s", expected, err, output)
	}
}

func TestInstalledPreCommitUsesStagedGoContent(t *testing.T) {
	repoDir := newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, repoDir, "git", "add", "sample.go")
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
	runCommand(t, repoDir, "git", "add", "sample.go")
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
	runCommand(t, repoDir, "git", "add", "sample.go")
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
	runCommand(t, repoDir, "git", "add", "sample.go")
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
	runCommand(t, repoDir, "git", "config", "core.hooksPath", "/custom/hooks")
	output, err := hookCommand(repoDir, "make", "hooks-install")
	if err == nil || !strings.Contains(output, "Refusing to replace") {
		t.Fatalf("expected custom hook path refusal, got %v:\n%s", err, output)
	}
	runCommand(t, repoDir, "make", "hooks-uninstall")
	got, getErr := hookCommand(repoDir, "git", "config", "--get-all", "core.hooksPath")
	if getErr != nil || got != "/custom/hooks\n" {
		t.Fatalf("custom hook path changed: %v, %q", getErr, got)
	}
	runCommand(t, repoDir, "git", "config", "--unset", "core.hooksPath")
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
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", ".githooks")

	runCommand(t, repoDir, "make", "hooks-uninstall")
	output, err := hookCommand(repoDir, "git", "config", "--local", "--get", "core.hooksPath")
	if err == nil || output != "" {
		t.Fatalf("expected legacy managed hook path to be removed, got %v: %q", err, output)
	}

	writeFile(t, filepath.Join(repoDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, repoDir, "git", "add", "sample.go")
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
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "make", "hooks-install")
	runCommand(t, repoDir, "git", "config", "core.bare", "true")
	writeFile(t, filepath.Join(linkedDir, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, linkedDir, "git", "add", "sample.go")
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
	runCommand(t, repoDir, "git", "add", "0:formatted.go")
	output, err := hookCommand(repoDir, "git", "commit", "-m", "stage-like formatted filename")
	if err != nil {
		t.Fatalf("commit formatted stage-like filename: %v\n%s", err, output)
	}

	writeFile(t, filepath.Join(repoDir, "1:unformatted.go"), "package sample\n\nfunc Value() int {return 3}\n")
	runCommand(t, repoDir, "git", "add", "1:unformatted.go")
	output, err = hookCommand(repoDir, "git", "commit", "-m", "stage-like unformatted filename")
	if err == nil || !strings.Contains(output, "gofmt-formatted") {
		t.Fatalf("expected unformatted stage-like filename rejection, got %v:\n%s", err, output)
	}

	repoDir = newHookFixture(t)
	writeFile(t, filepath.Join(repoDir, "foo.go"), "package sample\n\nfunc Value() int { return 4 }\n")
	writeFile(t, filepath.Join(repoDir, "0:foo.go"), "package sample\n\nfunc Value() int {return 5}\n")
	runCommand(t, repoDir, "git", "add", "foo.go", "0:foo.go")
	output, err = hookCommand(repoDir, "git", "commit", "-m", "stage-like sibling filename")
	if err == nil || !strings.Contains(output, "gofmt-formatted") {
		t.Fatalf("expected unformatted stage-like sibling rejection, got %v:\n%s", err, output)
	}
}

func newHookFixture(t *testing.T) string {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	runCommand(t, filepath.Dir(repoDir), "git", "init", "-b", "main", repoDir)
	runCommand(t, repoDir, "git", "config", "user.name", "Hook Test")
	runCommand(t, repoDir, "git", "config", "user.email", "hook-test@example.com")
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
	runCommand(t, repoDir, "git", "add", ".")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "revision A")
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
