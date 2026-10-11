//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestUnsupportedProviderStopsBeforeAction(t *testing.T) {
	for _, mode := range []string{"cache", "copy", "capture", "verify", "joined-command"} {
		if status, err := platformMain("python.exe", []string{mode}); status != 2 || err == nil {
			t.Fatalf("unsupported %s admitted: %d %v", mode, status, err)
		}
	}
}

func TestMainProcessRejectsUnsupportedHost(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stdout := &limitedOutput{limit: maxErrorBytes, cancel: cancel}
	stderr := &limitedOutput{limit: maxErrorBytes, cancel: cancel}
	cmd := exec.CommandContext(ctx, executable, "joined-command", "argument with spaces", "", "-test.invalid-provider-argument")
	cmd.Env = append(os.Environ(), "LOPPER_PYTHON_MAIN_CHILD=1")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	commandErr, joinErr := runJoined(cmd)
	var exit *exec.ExitError
	if !errors.As(commandErr, &exit) || exit.ExitCode() != 2 || joinErr != nil || ctx.Err() != nil {
		t.Fatalf("main status/join/deadline changed: %v / %v / %v", commandErr, joinErr, ctx.Err())
	}
	if len(stdout.bytes()) != 0 || string(stderr.bytes()) != "proof Python provider requires native Windows AMD64\n" {
		t.Fatalf("main output changed: stdout=%q stderr=%q", stdout.bytes(), stderr.bytes())
	}
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
		t.Fatal("main child was not waited")
	}
}
