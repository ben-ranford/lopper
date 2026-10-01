//go:build !windows

package scripts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestHookPreflightKeepsJobNoticesOutOfGitDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, config string
	}{
		{name: "missing key"},
		{name: "present key", config: "[core]\n hooksPath = custom-hooks\n"},
		{name: "invalid config", config: "[unterminated\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertPreflightGitDiagnostics(t, tc.config)
		})
	}
}

func assertPreflightGitDiagnostics(t *testing.T, config string) {
	t.Helper()
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config")
	writeFile(t, configPath, config)
	args := []string{"config", "--file", configPath, "--get", "core.hooksPath"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	wantOut, wantErr, wantStatus := preflightDiagnosticResult(t, exec.CommandContext(ctx, "git", args...))
	shellArgs := []string{"-c", `. ./hook-config-preflight.sh
trap cleanup_preflight_git EXIT
run_preflight_git git "$@"
`, "sh"}
	command := exec.CommandContext(ctx, "sh", append(shellArgs, args...)...)
	command.Env = append(os.Environ(), "PATH="+preflightNotificationSchedule(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	gotOut, gotErr, gotStatus := preflightDiagnosticResult(t, command)
	if ctx.Err() != nil || gotOut != wantOut || gotErr != wantErr || gotStatus != wantStatus {
		t.Fatalf("preflight altered Git result: status=%d want=%d stdout=%q want=%q stderr=%q want=%q context=%v", gotStatus, wantStatus, gotOut, wantOut, gotErr, wantErr, ctx.Err())
	}
}

func preflightDiagnosticResult(t *testing.T, command *exec.Cmd) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if command.ProcessState == nil {
		t.Fatalf("start diagnostic command: %v", err)
	}
	return stdout.String(), stderr.String(), command.ProcessState.ExitCode()
}

func preflightNotificationSchedule(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	shimDir := t.TempDir()
	prefix := filepath.Join(shimDir, "notification-schedule")
	// Let SIGCHLD reach Bash after the deliberate group kill but before wait.
	// This forces the hosted failure's scheduling boundary without changing
	// the reader, signal, wait command, timeout, or production source.
	writeFile(t, prefix, `trap "case \"\$BASH_COMMAND\" in 'wait \"\$reader_group\"'*) sleep 0.05 ;; esac" DEBUG
`)
	writeFileMode(t, filepath.Join(shimDir, "bash"), fmt.Sprintf(`#!/bin/sh
while [ "$1" != -c ]; do shift; done
shift
code="$(cat %s)
$1"
shift
exec %s --noprofile --norc -p -c "$code" "$@"
`, shellQuote(prefix), shellQuote(bash)), 0o755)
	return shimDir
}
