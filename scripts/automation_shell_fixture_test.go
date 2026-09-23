package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func automationShellFixtureCommand(scriptPath string) *exec.Cmd {
	// Read the copied POSIX shell script as data rather than executing its inode.
	return exec.Command("sh", scriptPath)
}

func TestAutomationShellFixtureReadsScriptAsData(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o700} {
		t.Run(mode.String(), func(t *testing.T) {
			checkAutomationShellFixtureMode(t, mode)
		})
	}
}

func checkAutomationShellFixtureMode(t *testing.T, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture with spaces.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env sh\nprintf 'fixture ran\\n'\n"), mode); err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	})
	if runtime.GOOS == "linux" && mode == 0o700 {
		if err := exec.Command(path).Run(); !errors.Is(err, syscall.ETXTBSY) {
			t.Fatalf("expected executable inode lock, got %v", err)
		}
	}
	output, err := automationShellFixtureCommand(path).CombinedOutput()
	if err != nil || string(output) != "fixture ran\n" {
		t.Fatalf("read fixture through interpreter: %v, output %q", err, output)
	}
}

func TestAutomationShellFixturePreservesScriptFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.sh")
	if err := os.WriteFile(path, []byte("#!/usr/bin/env sh\nprintf 'one invocation\\n'\nexit 23\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := automationShellFixtureCommand(path).CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 || string(output) != "one invocation\n" {
		t.Fatalf("script failure changed: %v, output %q", err, output)
	}
}
