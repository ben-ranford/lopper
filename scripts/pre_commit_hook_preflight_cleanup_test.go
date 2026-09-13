//go:build !windows

package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHooksPreflightSignalCleansConfigTempsBeforeMutation(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"hooks-install", "hooks-uninstall"} {
		t.Run(target, func(t *testing.T) {
			assertHooksPreflightSignalCleanup(t, target)
		})
	}
}

func assertHooksPreflightSignalCleanup(t *testing.T, target string) {
	t.Helper()
	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	if target == "hooks-uninstall" {
		runCommand(t, repoDir, "make", "hooks-install")
	}
	commonDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	configPath := filepath.Join(commonDir, "config")
	configBefore := readHookConfigBytes(t, configPath)
	tmpDir := t.TempDir()
	wrapperDir := preflightSignalGitWrapper(t)
	output, err := runMakeWithEnv(repoDir, target, "TMPDIR="+tmpDir, "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil {
		t.Fatalf("%s interrupted in preflight succeeded:\n%s", target, output)
	}
	assertFileEquals(t, configPath, string(configBefore))
	assertNoPreflightTemps(t, tmpDir)
	assertPreflightDidNotMutateHook(t, repoDir, target)
}

func assertNoPreflightTemps(t *testing.T, tmpDir string) {
	t.Helper()
	temps, err := filepath.Glob(filepath.Join(tmpDir, "lopper-hooks-config.*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("preflight config temps = %#v err=%v", temps, err)
	}
}

func assertPreflightDidNotMutateHook(t *testing.T, repoDir, target string) {
	t.Helper()
	if target == "hooks-install" {
		if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
			t.Fatalf("installer mutated managed hook state before preflight signal: %v", err)
		}
		return
	}
	if _, err := os.Stat(managedHookPath(t, repoDir)); err != nil {
		t.Fatalf("uninstaller removed managed hook before preflight signal: %v", err)
	}
}

func TestHooksInstallSignalDuringSnapshotHandoffCleansNewManagedDirectory(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	managedDir := filepath.Dir(managedHookPath(t, repoDir))
	mkdirPath, err := exec.LookPath("mkdir")
	if err != nil {
		t.Fatalf("find mkdir: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "mkdir"), fmt.Sprintf("#!/bin/sh\n%q \"$@\"\nstatus=$?\nkill -TERM \"$PPID\"\nexit \"$status\"\n", mkdirPath), 0o755)
	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil {
		t.Fatalf("install interrupted after managed directory creation succeeded:\n%s", output)
	}
	if _, err := os.Stat(managedDir); !os.IsNotExist(err) {
		t.Fatalf("managed directory remains after snapshot handoff signal: %v", err)
	}
}

func preflightSignalGitWrapper(t *testing.T) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$arg" = "--bool" ]; then
		kill -TERM "$PPID"
		exit 73
	fi
done
exec %q "$@"
`, gitPath), 0o755)
	return wrapperDir
}
