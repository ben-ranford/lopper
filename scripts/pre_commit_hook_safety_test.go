package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksInstallRefusesManagedHookSymlinkBeforeMutation(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	managedHook := managedHookPath(t, repoDir)
	target := filepath.Join(t.TempDir(), "pre-commit-target")
	writeFileMode(t, target, "#!/bin/sh\nexit 0\n", 0o755)
	if err := os.MkdirAll(filepath.Dir(managedHook), 0o700); err != nil {
		t.Fatalf("create managed hook directory: %v", err)
	}
	if err := os.Symlink(target, managedHook); err != nil {
		t.Fatalf("create managed hook symlink: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Refusing to replace managed pre-commit hook symlink") {
		t.Fatalf("install with managed hook symlink = %v\n%s", err, output)
	}
	if info, err := os.Lstat(managedHook); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("managed hook symlink was modified before refusal: %v", err)
	}
	assertFileEquals(t, target, "#!/bin/sh\nexit 0\n")
	assertNoHooksPath(t, repoDir, "--local", "local")
}

func TestHooksInstallRefusesManagedHookDirectorySymlinkBeforeMutation(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	commonDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	targetDir := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(targetDir, 0o700); err != nil {
		t.Fatalf("create target: %v", err)
	}
	if err := os.Symlink(targetDir, filepath.Join(commonDir, "lopper-hooks")); err != nil {
		t.Fatalf("symlink managed directory: %v", err)
	}
	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Refusing unsafe managed hook directory") {
		t.Fatalf("install with managed directory symlink = %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "pre-commit")); !os.IsNotExist(err) {
		t.Fatalf("installer wrote through managed directory symlink: %v", err)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
}

func TestHooksInstallRefusesCurrentConfigSymlinkBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name           string
		prepare        func(*testing.T, string)
		configFilePath func(*testing.T, string) string
	}{
		{
			name:    "common config",
			prepare: func(*testing.T, string) {},
			configFilePath: func(t *testing.T, repoDir string) string {
				t.Helper()
				return filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir"), "config")
			},
		},
		{
			name: "current worktree config",
			prepare: func(t *testing.T, repoDir string) {
				t.Helper()
				runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
				runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
			},
			configFilePath: func(t *testing.T, repoDir string) string {
				t.Helper()
				return filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config.worktree")
			},
		},
		{
			name: "dormant current worktree config",
			prepare: func(t *testing.T, repoDir string) {
				t.Helper()
				runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
				runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")
				runCommand(t, repoDir, "git", "config", "--local", "extensions.worktreeConfig", "false")
			},
			configFilePath: func(t *testing.T, repoDir string) string {
				t.Helper()
				return filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config.worktree")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			test.prepare(t, repoDir)
			configPath := test.configFilePath(t, repoDir)
			targetPath := configPath + ".target"
			if err := os.Rename(configPath, targetPath); err != nil {
				t.Fatalf("move config to symlink target: %v", err)
			}
			targetBefore, err := os.ReadFile(targetPath)
			if err != nil {
				t.Fatalf("read config target: %v", err)
			}
			if err := os.Symlink(targetPath, configPath); err != nil {
				t.Fatalf("symlink config: %v", err)
			}

			output, err := runMakeWithEnv(repoDir, "hooks-install")
			if err == nil || !strings.Contains(string(output), "unsafe") || !strings.Contains(string(output), "config symlink") {
				t.Fatalf("install with current config symlink = %v\n%s", err, output)
			}
			if info, err := os.Lstat(configPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("config symlink changed before refusal: %v", err)
			}
			assertFileEquals(t, targetPath, string(targetBefore))
			if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
				t.Fatalf("installer created managed hook directory before config-symlink refusal: %v", err)
			}
		})
	}
}

func TestHooksUninstallRefusesManagedHookDirectorySymlinkBeforeMutation(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")

	managedHook := managedHookPath(t, repoDir)
	managedDir := filepath.Dir(managedHook)
	configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hook config before unsafe uninstall: %v", err)
	}
	if err := os.Remove(managedHook); err != nil {
		t.Fatalf("remove managed hook before symlink setup: %v", err)
	}
	if err := os.Remove(managedDir); err != nil {
		t.Fatalf("remove managed hook directory before symlink setup: %v", err)
	}
	victimDir := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(victimDir, 0o700); err != nil {
		t.Fatalf("create external hook directory: %v", err)
	}
	victimHook := filepath.Join(victimDir, "pre-commit")
	writeFileMode(t, victimHook, "#!/bin/sh\nexit 0\n", 0o755)
	if err := os.Symlink(victimDir, managedDir); err != nil {
		t.Fatalf("symlink managed hook directory: %v", err)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Refusing unsafe managed hook directory") {
		t.Fatalf("uninstall with managed directory symlink = %v\n%s", err, output)
	}
	assertFileEquals(t, victimHook, "#!/bin/sh\nexit 0\n")
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hook config after unsafe uninstall: %v", err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatalf("uninstall modified config before unsafe-directory refusal")
	}
}

type unsafeManagedHookTestCase struct {
	name    string
	prepare func(t *testing.T, path string)
	verify  func(t *testing.T, path string)
}

func TestHooksUninstallRefusesUnsafeManagedHookBeforeConfigMutation(t *testing.T) {
	t.Parallel()

	for _, testCase := range []unsafeManagedHookTestCase{
		{
			name: "directory",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatalf("create managed hook directory: %v", err)
				}
			},
			verify: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Lstat(path)
				if err != nil || !info.IsDir() {
					t.Fatalf("managed hook directory was modified before refusal: %v", err)
				}
			},
		},
		{
			name: "FIFO",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				mkfifoPath, err := exec.LookPath("mkfifo")
				if err != nil {
					t.Skip("mkfifo is unavailable on this host")
				}
				if output, err := exec.Command(mkfifoPath, path).CombinedOutput(); err != nil {
					t.Fatalf("create managed hook FIFO: %v\n%s", err, output)
				}
			},
			verify: func(t *testing.T, path string) {
				t.Helper()
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
					t.Fatalf("managed hook FIFO was modified before refusal: %v", err)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assertHooksUninstallRefusesUnsafeManagedHookBeforeConfigMutation(t, testCase)
		})
	}
}

func assertHooksUninstallRefusesUnsafeManagedHookBeforeConfigMutation(t *testing.T, testCase unsafeManagedHookTestCase) {
	t.Helper()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	managedHook := managedHookPath(t, repoDir)
	configPath := filepath.Join(gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir"), "config")
	configBefore, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hook config before unsafe uninstall: %v", err)
	}
	if err := os.Remove(managedHook); err != nil {
		t.Fatalf("remove managed hook before unsafe setup: %v", err)
	}
	testCase.prepare(t, managedHook)

	output, err := runMakeWithEnv(repoDir, "hooks-uninstall")
	if err == nil || !strings.Contains(string(output), "Refusing unsafe managed pre-commit hook") {
		t.Fatalf("uninstall with unsafe managed hook = %v\n%s", err, output)
	}
	testCase.verify(t, managedHook)
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read hook config after unsafe uninstall: %v", err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatalf("uninstall modified config before unsafe-hook refusal")
	}
}

func TestHooksInstallRefusesManagedHookFIFOBeforeMutation(t *testing.T) {
	t.Parallel()

	mkfifoPath, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo is unavailable on this host")
	}
	repoDir := newHookTestRepository(t)
	managedHook := managedHookPath(t, repoDir)
	if err := os.MkdirAll(filepath.Dir(managedHook), 0o700); err != nil {
		t.Fatalf("create managed hook directory: %v", err)
	}
	command := exec.Command(mkfifoPath, managedHook)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create managed hook FIFO: %v\n%s", err, output)
	}

	output, err := runMakeWithEnv(repoDir, "hooks-install")
	if err == nil || !strings.Contains(string(output), "Refusing to replace non-regular managed pre-commit hook") {
		t.Fatalf("install with managed hook FIFO = %v\n%s", err, output)
	}
	info, err := os.Lstat(managedHook)
	if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("managed hook FIFO was modified before refusal: %v", err)
	}
	assertNoHooksPath(t, repoDir, "--local", "local")
}

func TestHooksInstallPreservesMaskedCustomLocalHooksPath(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	customDir := filepath.Join(repoDir, "custom-local-hooks")
	writeFileMode(t, filepath.Join(customDir, "pre-commit"), "#!/bin/sh\nexit 0\n", 0o755)
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "config", "--local", "core.hooksPath", customDir)
	runCommand(t, repoDir, "git", "config", "--worktree", "core.hooksPath", ".githooks")

	command := exec.Command("make", "hooks-install")
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Refusing to replace") {
		t.Fatalf("masked custom hook install = %v\n%s", err, output)
	}
	assertConfigEquals(t, repoDir, ".githooks")
	if got := gitOutput(t, repoDir, "config", "--local", "--get", "core.hooksPath"); got != customDir {
		t.Fatalf("local core.hooksPath = %q, want %q", got, customDir)
	}
	if got := gitOutput(t, repoDir, "config", "--worktree", "--get", "core.hooksPath"); got != ".githooks" {
		t.Fatalf("worktree core.hooksPath = %q, want .githooks", got)
	}
	if _, err := os.Stat(filepath.Join(customDir, "pre-commit")); err != nil {
		t.Fatalf("custom local hook was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("installer created managed hook directory before refusal: %v", err)
	}
}

func TestHooksInstallUsesCommonGitDirectoryForLinkedWorktree(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	command := exec.Command("make", "hooks-install")
	command.Dir = linkedDir
	command.Env = hookTestEnv()
	output, err := command.CombinedOutput()
	if err != nil || strings.Contains(string(output), "cannot be used with multiple working trees") {
		t.Fatalf("linked worktree install = %v\n%s", err, output)
	}

	managedHook := managedHookPath(t, linkedDir)
	assertConfigEquals(t, linkedDir, filepath.Dir(managedHook))
	writeFile(t, filepath.Join(linkedDir, "linked.txt"), "linked\n")
	runCommand(t, linkedDir, "git", "add", "linked.txt")
	runCommitWithHook(t, linkedDir, "linked commit")
	command = exec.Command("make", "hooks-uninstall")
	command.Dir = linkedDir
	command.Env = hookTestEnv()
	output, err = command.CombinedOutput()
	if err != nil || strings.Contains(string(output), "cannot be used with multiple working trees") {
		t.Fatalf("linked worktree uninstall = %v\n%s", err, output)
	}
}
