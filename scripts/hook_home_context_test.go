package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestHooksUninstallRetainsOverriddenHomeRoots(t *testing.T) {
	for _, root := range []string{"HOME", "XDG_CONFIG_HOME"} {
		t.Run(root, func(t *testing.T) {
			repo := newHookFixture(t)
			managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
			env := hookHomeConfigEnvironment(t, repo, managed, root)
			overridden := append(append([]string(nil), env...), root+"="+t.TempDir())
			output, err := hookCommandWithEnv(repo, overridden, "make", "hooks-uninstall")
			if err != nil {
				t.Fatalf("uninstall with overridden %s: %v\n%s", root, err, output)
			}
			if output, err := hookCommandWithEnv(repo, overridden, "git", "config", "--local", "--get", "core.hooksPath"); err == nil || output != "" {
				t.Fatalf("local managed setting was not removed: %v\n%s", err, output)
			}
			output, err = hookCommandWithEnv(repo, env, "sh", "-c", "git config --get core.hooksPath")
			if err != nil || strings.TrimSpace(output) != managed {
				t.Fatalf("restored %s lost durable reference: %v\n%s", root, err, output)
			}
			assertHookSnapshotRetention(t, managed, hookReferenceCase{alias: true})
		})
	}
}

func TestHooksCleanupRetainsUninspectableHome(t *testing.T) {
	for _, failure := range []string{"empty HOME", "unavailable account", "unresolved account"} {
		t.Run(failure, func(t *testing.T) {
			repo := newHookFixture(t)
			managed := filepath.Join(testutil.GitOutput(t, repo, "rev-parse", "--path-format=absolute", "--git-common-dir"), "lopper-hooks")
			runCommand(t, repo, "git", "config", "--local", "--unset", "core.hooksPath")
			env := []string{"HOME="}
			if failure != "empty HOME" {
				bin := t.TempDir()
				lookup := "#!/bin/sh\nexit 1\n"
				if failure == "unresolved account" {
					lookup = "#!/bin/sh\nprintf '~x'\n"
				}
				writeFileMode(t, filepath.Join(bin, "bash"), lookup, 0o755)
				env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
			}
			output, err := hookCommandWithEnv(repo, env, "sh", "scripts/cleanup-hook-snapshot.sh")
			if err != nil {
				t.Fatalf("cleanup with %s: %v\n%s", failure, err, output)
			}
			assertHookSnapshotRetention(t, managed, hookReferenceCase{alias: true})
		})
	}
}

// Map normal global configuration into a fixture without changing the account's
// real files. Temporary HOME/XDG overrides still reach Git unchanged.
func hookHomeConfigEnvironment(t *testing.T, repo, managed, root string) []string {
	t.Helper()
	home := t.TempDir()
	config := filepath.Join(home, ".gitconfig")
	if root == "XDG_CONFIG_HOME" {
		config = filepath.Join(home, ".config", "git", "config")
		if err := os.MkdirAll(filepath.Dir(config), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runCommand(t, repo, "git", "config", "--file", config, "core.hooksPath", managed)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeFileMode(t, filepath.Join(bin, "git"), `#!/bin/sh
if [ "${HOME-}" = "$TEST_ACCOUNT_HOME" ]; then
    HOME=$TEST_DURABLE_HOME
    export HOME
fi
if [ "${XDG_CONFIG_HOME-}" = "$TEST_ACCOUNT_HOME/.config" ]; then
    XDG_CONFIG_HOME=$TEST_DURABLE_HOME/.config
    export XDG_CONFIG_HOME
fi
exec "$TEST_REAL_GIT" "$@"
`, 0o755)
	return []string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TEST_REAL_GIT=" + realGit, "TEST_ACCOUNT_HOME=" + os.Getenv("HOME"), "TEST_DURABLE_HOME=" + home,
		"XDG_CONFIG_HOME=" + os.Getenv("HOME") + "/.config",
	}
}
