package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/creack/pty"
)

func TestStaveTUIPersonalPreferenceAcrossRepositories(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	config := t.TempDir()
	for launch, fixture := range []string{filepath.Join(root, "testdata", "js", "esm"), t.TempDir()} {
		checkPersonalPreferenceLaunch(t, root, bin, config, fixture, launch == 0)
	}
}

func checkPersonalPreferenceLaunch(t *testing.T, root, bin, config, fixture string, first bool) {
	t.Helper()
	cmd := exec.Command(bin, "tui", "--repo", fixture, "--language", "js-ts")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+config, "XDG_CONFIG_HOME="+config, "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "CI=")
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer closePTYProcess(t, master, cmd)
	if first {
		output := readPTYUntil(t, master, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Later:") })
		if !strings.Contains(output, "Try the new UI?") {
			t.Fatalf("invitation=%q", output)
		}
		if _, err := master.Write([]byte("try\n")); err != nil {
			t.Fatal(err)
		}
	}
	output := readPTYUntil(t, master, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Stave preview") })
	if strings.Contains(output, "Try the new UI?") {
		t.Fatalf("repeated invitation=%q", output)
	}
	if err := pty.Setsize(master, &pty.Winsize{Cols: 90, Rows: 25}); err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		t.Fatalf("quit=%v output=%q", err, output)
	}
}

func TestStaveTUIPreferencePromptEOFAndInterrupt(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	for _, name := range []string{"eof", "interrupt"} {
		t.Run(name, func(t *testing.T) { checkPreferencePromptProcessExit(t, root, bin, name) })
	}
}

func checkPreferencePromptProcessExit(t *testing.T, root, bin, name string) {
	t.Helper()
	config := t.TempDir()
	cmd := exec.Command(bin, "tui", "--repo", filepath.Join(root, "testdata", "js", "esm"), "--language", "js-ts")
	cmd.Env = append(os.Environ(), "HOME="+config, "XDG_CONFIG_HOME="+config, "TERM=xterm-256color", "NO_COLOR=", "CI=")
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer closePTYProcess(t, master, cmd)
	output := readPTYUntil(t, master, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Later:") })
	if name == "eof" {
		_, err = master.Write([]byte{4})
	} else {
		err = cmd.Process.Signal(os.Interrupt)
	}
	if err != nil {
		t.Fatal(err)
	}
	assertPreferencePromptExitStatus(t, cmd, name, output)
	if strings.Contains(output, "\x1b[?1049h") {
		t.Fatalf("prompt entered alternate screen %q", output)
	}
	if err := filepath.WalkDir(config, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "ui-preferences.json" {
			t.Errorf("exit saved preference %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertPreferencePromptExitStatus(t *testing.T, cmd *exec.Cmd, name, output string) {
	t.Helper()
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		var exit *exec.ExitError
		if name != "interrupt" || !errors.As(err, &exit) {
			t.Fatalf("prompt exit=%v output=%q", err, output)
		}
		status, ok := exit.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGINT {
			t.Fatalf("unexpected signal exit: %v", err)
		}
	}
}
