package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksInstallRemovesNewCommonConfigWhenActivationVerificationFails(t *testing.T) {
	t.Parallel()
	assertHooksInstallRemovesNewCommonConfig(t, false)
}

func TestHooksInstallRemovesNewCommonConfigWhenActivationGetsTerm(t *testing.T) {
	t.Parallel()
	assertHooksInstallRemovesNewCommonConfig(t, true)
}

func assertHooksInstallRemovesNewCommonConfig(t *testing.T, signal bool) {
	t.Helper()
	repoDir := newHookTestRepository(t)
	gitDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-dir")
	configPath := filepath.Join(gitDir, "config")
	if err := os.Remove(configPath); err != nil {
		t.Fatalf("remove common config: %v", err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	activationFailure := "echo \"forced activation verification failure\" >&2\n\t\texit 73"
	if signal {
		activationFailure = "kill -TERM \"$PPID\"\n\t\texit 0"
	}
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$arg" = "--fixed-value" ]; then
		%s
	fi
done
exec %q "$@"
`, activationFailure, gitPath), 0o755)

	output, err := runMakeWithEnv(repoDir, "hooks-install", "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || (!signal && !strings.Contains(string(output), "Managed core.hooksPath was not activated")) {
		t.Fatalf("install with missing common config and activation failure = %v\n%s", err, output)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("activation rollback retained newly-created common config: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(managedHookPath(t, repoDir))); !os.IsNotExist(err) {
		t.Fatalf("activation rollback retained managed hook state: %v", err)
	}
}
