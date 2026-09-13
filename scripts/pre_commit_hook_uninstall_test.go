package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestHooksUninstallFailsWhenConfigIsLockedWithoutRemovingManagedHook(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(gitDir, "config.lock"), "locked\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Unable to remove managed core.hooksPath") {
		t.Fatalf("uninstall with config lock = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", managedDir)
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook while config was locked: %v", err)
	}
}

type commonRemovalCase struct {
	name                  string
	dormant, commonLegacy bool
}
type regularConfigFixture struct {
	repoDir, commonConfig, worktreeConfig string
	commonBefore, worktreeBefore          []byte
}

func TestHooksUninstallRestoresWorktreeConfigurationAfterCommonRemovalFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []commonRemovalCase{{name: "active managed common"}, {name: "dormant managed common", dormant: true}, {name: "active legacy common", commonLegacy: true}, {name: "dormant legacy common", dormant: true, commonLegacy: true}} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRegularConfigFixture(t, test)
			writeFile(t, fixture.commonConfig+".lock", "locked\n")
			output, err := runMakeWithEnv(fixture.repoDir, "hooks-uninstall")
			if err == nil || !strings.Contains(string(output), "Unable to remove managed core.hooksPath") {
				t.Fatalf("uninstall with common removal failure = %v\n%s", err, output)
			}
			fixture.assertRestored(t)
		})
	}
}
func newRegularConfigFixture(t *testing.T, test commonRemovalCase) *regularConfigFixture {
	t.Helper()
	repo := newHookTestRepository(t)
	runCommand(t, repo, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repo, "make", "hooks-install")
	runCommand(t, repo, "git", "config", "--worktree", "core.hooksPath", filepath.Dir(managedHookPath(t, repo)))
	if test.commonLegacy {
		runCommand(t, repo, "git", "config", "--local", "core.hooksPath", ".githooks")
	}
	if test.dormant {
		runCommand(t, repo, "git", "config", "--local", "extensions.worktreeConfig", "false")
	}
	gitDir := gitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-dir")
	common := filepath.Join(gitDir, "config")
	worktree := filepath.Join(gitDir, "config.worktree")
	commonBefore := readHookConfigBytes(t, common)
	worktreeBefore := readHookConfigBytes(t, worktree)
	return &regularConfigFixture{repo, common, worktree, commonBefore, worktreeBefore}
}
func readHookConfigBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return data
}
func (f *regularConfigFixture) assertRestored(t *testing.T) {
	t.Helper()
	assertFileEquals(t, f.commonConfig, string(f.commonBefore))
	assertFileEquals(t, f.worktreeConfig, string(f.worktreeBefore))
	assertNoHookConfigBackups(t, f.repoDir)
	if _, err := os.Stat(managedHookPath(t, f.repoDir)); err != nil {
		t.Fatalf("uninstaller removed managed hook after rollback: %v", err)
	}
}

type symlinkedConfigFixture struct {
	repoDir, gitDir string
	configs         []symlinkedConfig
}
type symlinkedConfig struct {
	path, target string
	before       []byte
}

func TestHooksUninstallRestoresSymlinkedConfigurationAfterCommonRemovalFailure(t *testing.T) {
	t.Parallel()
	for _, dormant := range []bool{false, true} {
		t.Run(strconv.FormatBool(dormant), func(t *testing.T) {
			fixture := newSymlinkedConfigFixture(t, dormant)
			writeFile(t, fixture.configs[0].target+".lock", "locked\n")
			if _, err := runMakeWithEnv(fixture.repoDir, "hooks-uninstall"); err == nil {
				t.Fatal("uninstall with locked symlinked common config succeeded")
			}
			fixture.assertRestored(t)
		})
	}
}
func newSymlinkedConfigFixture(t *testing.T, dormant bool) *symlinkedConfigFixture {
	t.Helper()
	repo := newHookTestRepository(t)
	runCommand(t, repo, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repo, "make", "hooks-install")
	runCommand(t, repo, "git", "config", "--worktree", "core.hooksPath", filepath.Dir(managedHookPath(t, repo)))
	if dormant {
		runCommand(t, repo, "git", "config", "--local", "extensions.worktreeConfig", "false")
	}
	gitDir := gitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-dir")
	configs := []symlinkedConfig{newSymlinkedConfig(t, filepath.Join(gitDir, "config")), newSymlinkedConfig(t, filepath.Join(gitDir, "config.worktree"))}
	return &symlinkedConfigFixture{repo, gitDir, configs}
}
func newSymlinkedConfig(t *testing.T, path string) symlinkedConfig {
	t.Helper()
	target := path + ".target"
	if err := os.Rename(path, target); err != nil {
		t.Fatalf("rename %s: %v", path, err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("symlink %s: %v", path, err)
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read config target: %v", err)
	}
	return symlinkedConfig{path, target, before}
}
func (f *symlinkedConfigFixture) assertRestored(t *testing.T) {
	t.Helper()
	for _, c := range f.configs {
		link, err := os.Readlink(c.path)
		if err != nil || link != c.target {
			t.Fatalf("config link=%q err=%v, want %q", link, err, c.target)
		}
		assertFileEquals(t, c.target, string(c.before))
	}
	assertNoHookConfigBackups(t, f.repoDir)
}

func TestHooksUninstallAttemptsWorktreeRestoreAfterCommonRestoreFailure(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", managedDir)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	commonConfig := filepath.Join(gitDir, "config")
	worktreeConfig := filepath.Join(gitDir, "config.worktree")
	commonBefore, err := os.ReadFile(commonConfig)
	if err != nil {
		t.Fatalf("read common config: %v", err)
	}
	worktreeBefore, err := os.ReadFile(worktreeConfig)
	if err != nil {
		t.Fatalf("read worktree config: %v", err)
	}
	writeFile(t, commonConfig+".lock", "locked\n")
	mvPath, err := exec.LookPath("mv")
	if err != nil {
		t.Fatalf("find mv: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "mv"), fmt.Sprintf(`#!/bin/sh
for arg do case "$arg" in *.lopper-hooks-config.worktree.*) ;; *.lopper-hooks-config.*) echo "forced common restore failure" >&2; exit 74;; esac; done
exec %q "$@"
`, mvPath), 0o755)
	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(string(output), "Unable to restore hook configuration completely") || !strings.Contains(string(output), "forced common restore failure") {
		t.Fatalf("uninstall with restore failure = %v\n%s", err, output)
	}
	assertFileEquals(t, commonConfig, string(commonBefore))
	assertFileEquals(t, worktreeConfig, string(worktreeBefore))
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("managed hook missing after rollback failure: %v", err)
	}
	commonBackups, err := filepath.Glob(filepath.Join(gitDir, ".lopper-hooks-config.*"))
	if err != nil || len(commonBackups) != 1 {
		t.Fatalf("common recovery backup = %#v err=%v", commonBackups, err)
	}
	worktreeBackups, err := filepath.Glob(filepath.Join(gitDir, ".lopper-hooks-config.worktree.*"))
	if err != nil || len(worktreeBackups) != 0 {
		t.Fatalf("worktree recovery backups = %#v err=%v", worktreeBackups, err)
	}
}

type snapshotFailureCase struct {
	name          string
	symlink       bool
	command, body string
}
type snapshotFailureFixture struct {
	repoDir, gitDir, configPath, targetPath string
	before                                  []byte
}

func TestHooksUninstallCleansSnapshotsWhenSetupFails(t *testing.T) {
	t.Parallel()
	for _, test := range []snapshotFailureCase{
		{name: "common copy", command: "cp", body: `echo "forced snapshot copy failure" >&2; exit 73`},
		{name: "symlink readlink", symlink: true, command: "readlink", body: `echo "forced snapshot readlink failure" >&2; exit 74`},
	} {
		t.Run(test.name, func(t *testing.T) { assertSnapshotSetupFailure(t, test) })
	}
}

func assertSnapshotSetupFailure(t *testing.T, test snapshotFailureCase) {
	t.Helper()
	fixture := newSnapshotFailureFixture(t, test.symlink)
	toolPath, err := exec.LookPath(test.command)
	if err != nil {
		t.Fatalf("find %s: %v", test.command, err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, test.command), fmt.Sprintf("#!/bin/sh\n%s\nexec %q \"$@\"\n", test.body, toolPath), 0o755)
	if _, err := runMakeWithEnv(fixture.repoDir, "hooks-uninstall", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH")); err == nil {
		t.Fatal("uninstall setup failure succeeded")
	}
	fixture.assertUnchanged(t, test.symlink)
}

func newSnapshotFailureFixture(t *testing.T, symlink bool) *snapshotFailureFixture {
	t.Helper()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	configPath := filepath.Join(gitDir, "config")
	targetPath := configPath
	if symlink {
		targetPath += ".target"
		if err := os.Rename(configPath, targetPath); err != nil {
			t.Fatalf("rename config: %v", err)
		}
		if err := os.Symlink(targetPath, configPath); err != nil {
			t.Fatalf("symlink config: %v", err)
		}
	}
	before, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return &snapshotFailureFixture{repoDir, gitDir, configPath, targetPath, before}
}

func (f *snapshotFailureFixture) assertUnchanged(t *testing.T, symlink bool) {
	t.Helper()
	assertFileEquals(t, f.targetPath, string(f.before))
	if symlink {
		if link, err := os.Readlink(f.configPath); err != nil || link != f.targetPath {
			t.Fatalf("config link=%q err=%v", link, err)
		}
	}
	if backups, err := filepath.Glob(filepath.Join(f.gitDir, ".lopper-hooks-config.*")); err != nil || len(backups) != 0 {
		t.Fatalf("snapshot backups=%#v err=%v", backups, err)
	}
	if _, err := os.Stat(managedHookPath(t, f.repoDir)); err != nil {
		t.Fatalf("managed hook missing: %v", err)
	}
}

func TestHooksUninstallLeavesLocalConfigurationWhenWorktreeConfigIsLocked(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "make", "hooks-install")
	managedDir := filepath.Dir(managedHookPath(t, repoDir))
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", managedDir)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(gitDir, "config.worktree.lock"), "locked\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Unable to remove managed current worktree core.hooksPath") {
		t.Fatalf("uninstall with locked worktree config = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", managedDir)
	assertConfigValues(t, repoDir, "--worktree", managedDir)
	if _, err := os.Stat(managedHookPath(t, repoDir)); err != nil {
		t.Fatalf("uninstaller removed managed hook after worktree-config failure: %v", err)
	}
}

func TestHooksUninstallRemovesDormantCurrentWorktreeLegacyHookPath(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
	runCommand(t, repoDir, "make", "hooks-uninstall")

	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	assertNoHooksPathInFile(t, filepath.Join(gitDir, "config.worktree"))
	if _, err := os.Stat(managedHookPath(t, repoDir)); !os.IsNotExist(err) {
		t.Fatalf("uninstall retained unreferenced managed hook: %v", err)
	}
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "true")
	markerPath := filepath.Join(repoDir, "dormant-uninstall-marker")
	writeFileMode(t, filepath.Join(repoDir, ".githooks", "pre-commit"), "#!/bin/sh\ntouch "+markerPath+"\n", 0o755)
	writeFile(t, filepath.Join(repoDir, "dormant-uninstall.txt"), "safe\n")
	runCommand(t, repoDir, "git", "add", "dormant-uninstall.txt")
	runCommitWithHook(t, repoDir, "dormant worktree uninstall")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("dormant worktree hook executed after uninstall: %v", err)
	}
}

func TestHooksUninstallPreservesStateWhenDormantCurrentWorktreeConfigIsLocked(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(gitDir, "config.worktree.lock"), "locked\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Unable to remove managed current worktree core.hooksPath") {
		t.Fatalf("uninstall with locked dormant worktree config = %v\n%s", err, output)
	}
	assertConfigValues(t, repoDir, "--local", managedDir)
	if got := gitOutput(t, repoDir, "config", "--file", filepath.Join(gitDir, "config.worktree"), "--get", "core.hooksPath"); got != ".githooks" {
		t.Fatalf("dormant worktree core.hooksPath = %q, want .githooks", got)
	}
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook before dormant worktree failure: %v", err)
	}
}

func TestHooksUninstallPreservesManagedHookForDormantForeignWorktreeReference(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", managedDir)
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")

	runCommand(t, repoDir, "make", "hooks-uninstall")
	assertNoHooksPath(t, repoDir, "--local", "local")
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook used by dormant foreign worktree: %v", err)
	}
}

func TestHooksUninstallRejectsMalformedForeignWorktreeConfigBeforeDeletingManagedHook(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "make", "hooks-install")
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	foreignGitDir := gitOutput(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-dir")
	writeFile(t, filepath.Join(foreignGitDir, "config.worktree"), "[broken\n")
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
	configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read local config before malformed foreign worktree refusal: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil {
		t.Fatalf("uninstall with malformed foreign worktree config unexpectedly succeeded:\n%s", output)
	}
	managedHook := managedHookPath(t, repoDir)
	assertConfigValues(t, repoDir, "--local", filepath.Dir(managedHook))
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook before foreign-config refusal: %v", err)
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read local config after malformed foreign worktree refusal: %v", err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatalf("uninstall modified config before malformed foreign worktree refusal")
	}
}

func TestHooksUninstallRejectsMalformedGlobalIncludedConfigBeforeDeletingManagedHook(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	brokenConfig := filepath.Join(t.TempDir(), "broken.gitconfig")
	writeFile(t, globalConfig, "[include]\n\tpath = "+brokenConfig+"\n")
	writeFile(t, brokenConfig, "[broken\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "GIT_CONFIG_GLOBAL="+globalConfig, "GIT_CONFIG_NOSYSTEM=1")
	if err == nil {
		t.Fatalf("uninstall with malformed global included config unexpectedly succeeded:\n%s", output)
	}
	assertConfigValues(t, repoDir, "--local", managedDir)
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook before global-config refusal: %v", err)
	}
}

func TestHooksUninstallPreservesConditionalForeignGlobalInclude(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
	linkedGitDir := gitOutput(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-dir")
	includePath := filepath.Join(t.TempDir(), "linked-hooks.gitconfig")
	writeFile(t, includePath, "[core]\n\thooksPath = "+managedDir+"\n")
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	writeFile(t, globalConfig, "[includeIf \"gitdir:"+filepath.ToSlash(linkedGitDir)+"\"]\n\tpath = "+includePath+"\n")
	env := []string{"GIT_CONFIG_GLOBAL=" + globalConfig, "GIT_CONFIG_NOSYSTEM=1"}

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", env...)
	if err != nil {
		t.Fatalf("uninstall with linked conditional include: %v\n%s", err, output)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook referenced by linked conditional include: %v", err)
	}
	command := exec.Command("git", "config", "--get", "core.hooksPath")
	command.Dir = linkedDir
	command.Env = append(hookTestEnv(), env...)
	linkedOutput, err := command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(linkedOutput)) != managedDir {
		t.Fatalf("linked conditional hooksPath = %q err=%v, want %q", linkedOutput, err, managedDir)
	}
}

func TestHooksInstallRefusesIncludedAndNewlineHooksPathsWithoutMutation(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	includeFile := filepath.Join(t.TempDir(), "hooks.inc")
	writeFile(t, includeFile, "[core]\n\thooksPath = .githooks\n")
	runCommand(t, linkedDir, "git", "config", "--worktree", "includeIf.onbranch:main.path", includeFile)

	command := exec.Command("make", "hooks-install")
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "includes another config") {
		t.Fatalf("included worktree install = %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer created managed hook directory before refusal: %v", err)
	}

	runCommand(t, linkedDir, "git", "config", "--worktree", "--unset-all", "includeIf.onbranch:main.path")
	newlinePath := ".githooks\n" + filepath.Dir(managedHookPath(t, repoDir))
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", newlinePath)
	command = exec.Command("make", "hooks-install")
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Refusing to replace") {
		t.Fatalf("newline hook path install = %v\n%s", err, output)
	}
	if got := gitOutput(t, repoDir, "config", "--local", "--get", "core.hooksPath"); got != newlinePath {
		t.Fatalf("newline core.hooksPath = %q", got)
	}
}

func TestHooksInstallUninstallPreservesCustomMultiValuePathsAndSharedHook(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "make", "hooks-install")
	managedDir := filepath.Dir(managedHookPath(t, repoDir))
	localCustom := filepath.Join(repoDir, "custom local hooks")
	worktreeCustom := filepath.Join(repoDir, "custom worktree hooks")
	runCommand(t, repoDir, "git", "config", "--local", "--add", "core.hooksPath", localCustom)
	runCommand(t, repoDir, "git", "config", "--local", "--add", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "config", "--worktree", "--add", "core.hooksPath", worktreeCustom)
	runCommand(t, repoDir, "git", "config", "--worktree", "--add", "core.hooksPath", ".githooks")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "git", "config", "--worktree", "--replace-all", "core.hooksPath", managedDir)
	includeFile := filepath.Join(t.TempDir(), "shared-hooks.inc")
	writeFile(t, includeFile, "[core]\n\thooksPath = "+managedDir+"\n")
	runCommand(t, linkedDir, "git", "config", "--worktree", "include.path", includeFile)

	command := exec.Command("make", "hooks-uninstall")
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("hooks-uninstall = %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Preserved") || strings.Contains(string(output), "Removed") {
		t.Fatalf("preserved-hook uninstall output = %q", output)
	}
	assertConfigValues(t, repoDir, "--local", localCustom)
	assertConfigValues(t, repoDir, "--worktree", worktreeCustom)
	assertConfigValues(t, linkedDir, "--worktree", managedDir)
	if got := gitOutput(t, linkedDir, "config", "--includes", "--get-all", "core.hooksPath"); !strings.Contains(got, managedDir) {
		t.Fatalf("included shared hook reference = %q", got)
	}
	if _, err := os.Stat(managedHookPath(t, repoDir)); err != nil {
		t.Fatalf("shared managed hook was removed while linked worktree still uses it: %v", err)
	}
}

func TestHooksUninstallPreservesHookForForeignAbsoluteManagedPathAliases(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name      string
		hooksPath func(t *testing.T, managedDir string) string
	}{
		{
			name: "trailing slash",
			hooksPath: func(_ *testing.T, managedDir string) string {
				return managedDir + string(filepath.Separator)
			},
		},
		{
			name: "symlink alias",
			hooksPath: func(t *testing.T, managedDir string) string {
				aliasDir := filepath.Join(t.TempDir(), "managed-hooks-alias")
				if err := os.Symlink(managedDir, aliasDir); err != nil {
					t.Skipf("create managed hook alias: %v", err)
				}
				return aliasDir
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
			runCommand(t, repoDir, "make", "hooks-install")
			managedHook := managedHookPath(t, repoDir)
			managedDir := filepath.Dir(managedHook)
			linkedDir := filepath.Join(t.TempDir(), "linked")
			runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
			runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", testCase.hooksPath(t, managedDir))

			runCommand(t, repoDir, "make", "hooks-uninstall")
			if _, err := os.Stat(managedHook); err != nil {
				t.Fatalf("uninstaller removed managed hook used through %s: %v", testCase.name, err)
			}
		})
	}
}

func TestHooksUninstallPreservesHookForForeignRelativeManagedPath(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	relativeManagedDir, err := filepath.Rel(linkedDir, managedDir)
	if err != nil {
		t.Fatalf("make managed hook path relative: %v", err)
	}
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.hooksPath", relativeManagedDir)

	runCommand(t, repoDir, "make", "hooks-uninstall")
	if _, err := os.Stat(managedHook); err != nil {
		t.Fatalf("uninstaller removed managed hook used through a relative foreign path: %v", err)
	}
}

func TestHooksUninstallRemovesUnreferencedHookWithUnrelatedGlobalAbsolutePath(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	customDir := filepath.Join(t.TempDir(), "custom-hooks")
	if err := os.Mkdir(customDir, 0o755); err != nil {
		t.Fatalf("create unrelated global hook directory: %v", err)
	}
	writeFile(t, globalConfig, "[core]\n\thooksPath = "+customDir+"\n")

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall", "GIT_CONFIG_GLOBAL="+globalConfig, "GIT_CONFIG_NOSYSTEM=1")
	if err != nil {
		t.Fatalf("uninstall with unrelated global hook path = %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Removed managed pre-commit hook") {
		t.Fatalf("uninstall did not report removal: %s", output)
	}
	if _, err := os.Stat(managedHook); !os.IsNotExist(err) {
		t.Fatalf("uninstaller preserved hook for unrelated global path: %v", err)
	}
}

func TestHooksUninstallReportsAbsentManagedHookNeutrally(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err != nil {
		t.Fatalf("uninstall without managed hook = %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "No managed pre-commit hook to remove") || strings.Contains(string(output), "Removed managed") || strings.Contains(string(output), "Preserved managed") {
		t.Fatalf("absent managed hook output = %q", output)
	}
}
