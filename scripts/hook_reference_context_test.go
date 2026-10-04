package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

type hookReferenceCase struct {
	name                              string
	alias, conditional, bare, missing bool
	gitdir                            bool
	direct                            bool
	nonmatching                       bool
}

var hookReferenceCases = []hookReferenceCase{
	{name: "unrelated relative"},
	{name: "managed relative alias", alias: true},
	{name: "managed relative path", alias: true, direct: true},
	{name: "conditional unrelated", conditional: true},
	{name: "gitdir conditional unrelated", conditional: true, gitdir: true},
	{name: "gitdir conditional managed", conditional: true, gitdir: true, alias: true},
	{name: "conditional managed alias", conditional: true, alias: true},
	{name: "nonmatching conditional managed relative", conditional: true, alias: true, direct: true, nonmatching: true},
	{name: "bare common repository", bare: true},
	{name: "missing reference", missing: true},
}

func TestHooksUninstallResolvesOwningWorktree(t *testing.T) {
	t.Parallel()
	for _, tc := range hookReferenceCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertHookReferenceCase(t, tc)
		})
	}
}

func assertHookReferenceCase(t *testing.T, tc hookReferenceCase) {
	t.Helper()
	repo, managed, linked := newHookReferenceWorktree(t, tc)
	configureHookReference(t, linked, managed, tc)
	caller := repo
	if tc.bare {
		caller = linked
	}
	runCommand(t, caller, "make", "hooks-uninstall")
	assertHookSnapshotRetention(t, managed, tc)
}

func newHookReferenceWorktree(t *testing.T, tc hookReferenceCase) (repo, managed, linked string) {
	t.Helper()
	repo = newHookFixture(t)
	managed = filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
	linkedName := "linked space\nroot\n"
	if runtime.GOOS == "windows" {
		linkedName = "linked space"
	}
	linked = filepath.Join(t.TempDir(), linkedName)
	runCommand(t, repo, "git", "worktree", "add", "-b", "other", linked)
	runCommand(t, repo, "git", "config", "extensions.worktreeConfig", "true")
	if tc.bare {
		runCommand(t, repo, "git", "config", "core.bare", "true")
		runCommand(t, linked, "git", "config", "--worktree", "core.bare", "false")
	}
	return repo, managed, linked
}

func configureHookReference(t *testing.T, linked, managed string, tc hookReferenceCase) {
	t.Helper()
	hookPath := createHookReferencePath(t, linked, managed, tc)
	if tc.conditional {
		configureConditionalHookReference(t, linked, hookPath, tc)
	} else {
		runCommand(t, linked, "git", "config", "--worktree", "core.hooksPath", hookPath)
	}
}

func createHookReferencePath(t *testing.T, linked, managed string, tc hookReferenceCase) string {
	t.Helper()
	hookPath := "custom hooks"
	switch {
	case tc.direct:
		var err error
		hookPath, err = filepath.Rel(linked, managed)
		if err != nil {
			t.Fatal(err)
		}
	case tc.alias:
		if err := os.Symlink(managed, filepath.Join(linked, hookPath)); err != nil {
			t.Fatal(err)
		}
	case !tc.missing:
		if err := os.Mkdir(filepath.Join(linked, hookPath), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return hookPath
}

func configureConditionalHookReference(t *testing.T, linked, hookPath string, tc hookReferenceCase) {
	t.Helper()
	included := filepath.Join(t.TempDir(), "include.cfg")
	runCommand(t, linked, "git", "config", "--file", included, "core.hooksPath", hookPath)
	condition := "includeIf.onbranch:other.path"
	if tc.nonmatching {
		condition = "includeIf.onbranch:inactive.path"
	}
	if tc.gitdir {
		condition = "includeIf.gitdir:" + testutil.GitOutput(t, linked, "rev-parse", "--absolute-git-dir") + ".path"
	}
	runCommand(t, linked, "git", "config", "--worktree", condition, included)
}

func assertHookSnapshotRetention(t *testing.T, managed string, tc hookReferenceCase) {
	t.Helper()
	_, err := os.Stat(filepath.Join(managed, "pre-commit"))
	shouldRetain := tc.alias && !tc.nonmatching || tc.missing
	if shouldRetain && err != nil {
		t.Fatalf("referenced/ambiguous snapshot removed: %v", err)
	}
	if !shouldRetain && !os.IsNotExist(err) {
		t.Fatalf("unreferenced snapshot retained: %v", err)
	}
}

func TestHooksUninstallResolvesConfiguredWorktreeRoot(t *testing.T) {
	t.Parallel()
	repo, managed, linked := newHookReferenceWorktree(t, hookReferenceCase{})
	effectiveRoot := t.TempDir()
	const hookPath = "custom hooks"
	if err := os.Mkdir(filepath.Join(linked, hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(managed, filepath.Join(effectiveRoot, hookPath)); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("hook directory links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	runCommand(t, linked, "git", "config", "--worktree", "core.hooksPath", hookPath)
	runCommand(t, linked, "git", "config", "--worktree", "core.worktree", effectiveRoot)
	runCommand(t, repo, "make", "hooks-uninstall")
	assertHookSnapshotRetention(t, managed, hookReferenceCase{alias: true})
}

func TestHooksUninstallRetainsCustomHookFileLinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		link func(string, string) error
	}{
		{name: "symlink", link: os.Symlink},
		{name: "hard link", link: os.Link},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, managed, linked := newHookReferenceWorktree(t, hookReferenceCase{})
			custom := filepath.Join(linked, "custom hooks")
			if err := os.Mkdir(custom, 0o755); err != nil {
				t.Fatal(err)
			}
			managedHook := filepath.Join(managed, "pre-commit")
			customHook := filepath.Join(custom, "pre-commit")
			if err := tc.link(managedHook, customHook); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("hook file links unavailable: %v", err)
				}
				t.Fatal(err)
			}
			runCommand(t, linked, "git", "config", "--worktree", "core.hooksPath", "custom hooks")
			runCommand(t, repo, "make", "hooks-uninstall")
			assertHookSnapshotRetention(t, managed, hookReferenceCase{alias: true})
			if _, err := os.Stat(customHook); err != nil {
				t.Fatalf("custom hook no longer resolves: %v", err)
			}
		})
	}
}

func TestHooksUninstallIgnoresCommandScopedHookPathOverride(t *testing.T) {
	t.Parallel()
	repo, managed, linked := newHookReferenceWorktree(t, hookReferenceCase{})
	runCommand(t, linked, "git", "config", "--worktree", "core.hooksPath", managed)
	env := []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0=/unrelated/hooks",
	}
	output, err := hookCommandWithEnv(repo, env, "make", "hooks-uninstall")
	if err != nil {
		t.Fatalf("uninstall with command-scoped Git config: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(managed, "pre-commit")); err != nil {
		t.Fatalf("durable worktree hook reference was ignored: %v", err)
	}
}

func TestHooksUninstallIgnoresGlobalConfigFileSelector(t *testing.T) {
	t.Parallel()
	repo := newHookFixture(t)
	managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
	env := hookHomeConfigEnvironment(t, repo, managed, "HOME")
	override := filepath.Join(t.TempDir(), "empty.cfg")
	writeFile(t, override, "")
	env = append(env, "GIT_CONFIG_GLOBAL="+override)
	output, err := hookCommandWithEnv(repo, env, "make", "hooks-uninstall")
	if err != nil {
		t.Fatalf("uninstall with global Git config selector: %v\n%s", err, output)
	}
	assertHookSnapshotRetention(t, managed, hookReferenceCase{alias: true})
	if output, err := hookCommandWithEnv(repo, env, "git", "config", "--local", "--get", "core.hooksPath"); err == nil || output != "" {
		t.Fatalf("local managed setting was not removed: %v\n%s", err, output)
	}
}

func TestHooksCleanupIgnoresSystemConfigFileSelector(t *testing.T) {
	t.Parallel()
	repo := newHookFixture(t)
	managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
	runCommand(t, repo, "git", "config", "--local", "--unset", "core.hooksPath")
	override := filepath.Join(t.TempDir(), "system.cfg")
	runCommand(t, repo, "git", "config", "--file", override, "core.hooksPath", managed)
	env := []string{"XDG_CONFIG_HOME=", "GIT_CONFIG_SYSTEM=" + override}
	output, err := hookCommandWithEnv(repo, env, "sh", "scripts/cleanup-hook-snapshot.sh")
	if err != nil {
		t.Fatalf("cleanup with system Git config selector: %v\n%s", err, output)
	}
	assertHookSnapshotRetention(t, managed, hookReferenceCase{})
}

func TestHooksCleanupRetainsOnInspectionFailure(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"missing", "malformed", "replaced"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			repo := newHookFixture(t)
			managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
			linked := filepath.Join(t.TempDir(), "missing")
			runCommand(t, repo, "git", "worktree", "add", "-b", "other", linked)
			runCommand(t, repo, "git", "config", "extensions.worktreeConfig", "true")
			if failure == "malformed" {
				config := filepath.Join(t.TempDir(), "bad.cfg")
				writeFile(t, config, "[broken\n")
				runCommand(t, linked, "git", "config", "--worktree", "include.path", config)
			} else if err := os.RemoveAll(linked); err != nil {
				t.Fatal(err)
			}
			if failure == "replaced" {
				runCommand(t, repo, "git", "init", linked)
			}
			runCommand(t, repo, "make", "hooks-uninstall")
			if _, err := os.Stat(filepath.Join(managed, "pre-commit")); err != nil {
				t.Fatalf("inspection failure removed snapshot: %v", err)
			}
		})
	}
}

func TestHooksCleanupForeignWindowsPathsAreAmbiguous(t *testing.T) {
	t.Parallel()
	for _, reference := range []string{`C:/custom-hooks`, `C:\custom-hooks`, `C:custom-hooks`, `C:`, `\\server\share\hooks`, `//server/share/hooks`} {
		t.Run(reference, func(t *testing.T) {
			t.Parallel()
			repo := newHookFixture(t)
			managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
			runCommand(t, repo, "git", "config", "extensions.worktreeConfig", "true")
			runCommand(t, repo, "git", "config", "--worktree", "core.hooksPath", reference)
			runCommand(t, repo, "make", "hooks-uninstall")
			if _, err := os.Stat(filepath.Join(managed, "pre-commit")); err != nil {
				t.Fatalf("ambiguous Windows reference removed snapshot: %v", err)
			}
		})
	}
}

func TestHooksUninstallRetainsDefaultHookFileLink(t *testing.T) {
	t.Parallel()
	repo, managed, linked := newHookReferenceWorktree(t, hookReferenceCase{})
	runCommand(t, repo, "git", "config", "--local", "--unset", "core.hooksPath")
	defaultHooks := testutil.GitOutput(t, linked, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	defaultHook := filepath.Join(defaultHooks, "pre-commit")
	if err := os.Symlink(filepath.Join(managed, "pre-commit"), defaultHook); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("hook file links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	runCommand(t, repo, "make", "hooks-uninstall")
	assertHookSnapshotRetention(t, managed, hookReferenceCase{alias: true})
	if _, err := os.Stat(defaultHook); err != nil {
		t.Fatalf("default hook no longer resolves: %v", err)
	}
}

func TestHooksCleanupIgnoresNoSystemConfigSelector(t *testing.T) {
	t.Parallel()
	repo := newHookFixture(t)
	managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
	runCommand(t, repo, "git", "config", "--local", "--unset", "core.hooksPath")
	systemConfig := filepath.Join(t.TempDir(), "system.cfg")
	runCommand(t, repo, "git", "config", "--file", systemConfig, "core.hooksPath", managed)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// Model installed system configuration without modifying the host's Git config.
	writeFileMode(t, filepath.Join(bin, "git"), "#!/bin/sh\nGIT_CONFIG_SYSTEM=\"$TEST_SYSTEM_CONFIG\" exec \"$TEST_REAL_GIT\" \"$@\"\n", 0o755)
	env := []string{
		"XDG_CONFIG_HOME=",
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TEST_SYSTEM_CONFIG=" + systemConfig, "TEST_REAL_GIT=" + realGit,
		"GIT_CONFIG_NOSYSTEM=1",
	}
	output, err := hookCommandWithEnv(repo, env, "sh", "scripts/cleanup-hook-snapshot.sh")
	if err != nil {
		t.Fatalf("cleanup with system Git config disabled: %v\n%s", err, output)
	}
	assertHookSnapshotRetention(t, managed, hookReferenceCase{alias: true})
}
