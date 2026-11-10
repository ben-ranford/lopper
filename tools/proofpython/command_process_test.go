package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestJoinedCommandRetainsExitAndStartFailures(t *testing.T) {
	if os.Getenv("LOPPER_PYTHON_OWNER_CHILD") == "1" {
		os.Exit(7)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), executable, "-test.run=^TestJoinedCommandRetainsExitAndStartFailures$")
	cmd.Env = append(os.Environ(), "LOPPER_PYTHON_OWNER_CHILD=1")
	commandErr, joinErr := runJoined(cmd)
	var exit *exec.ExitError
	if !errors.As(commandErr, &exit) || exit.ExitCode() != 7 || joinErr != nil {
		t.Fatalf("command/join outcome lost: %v / %v", commandErr, joinErr)
	}
	absent := exec.CommandContext(context.Background(), filepath.Join(t.TempDir(), "missing-provider-executable"))
	commandErr, joinErr = runJoined(absent)
	if commandErr == nil || joinErr == nil {
		t.Fatalf("unstarted command falsely certified: %v / %v", commandErr, joinErr)
	}
}
