package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestProofCommandOutputAndCancellation(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProofCommandChild$")
			cmd.Env = append(os.Environ(), "LOPPER_PROOF_COMMAND_CHILD="+mode)
			output, err := (&runner{}).executeProofCommand(ctx, cmd)
			assertProofCommandResult(t, mode, ctx.Err(), cmd, output, err)
		})
	}
}

func assertProofCommandResult(t *testing.T, mode string, contextErr error, cmd *exec.Cmd, output []byte, err error) {
	t.Helper()
	switch mode {
	case "success":
		if err != nil || string(output) != "stdout\n" {
			t.Fatalf("success must return only stdout: output=%q err=%v", output, err)
		}
	case "failure":
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || string(output) != "stdout\nstderr\n" {
			t.Fatalf("failure must retain diagnostics and exit error: output=%q err=%v", output, err)
		}
	case "cancel":
		if err == nil || !errors.Is(contextErr, context.DeadlineExceeded) || cmd.ProcessState == nil {
			t.Fatalf("running process must be cancelled and reaped: err=%v context=%v", err, contextErr)
		}
	}
}

func TestProofCommandChild(t *testing.T) {
	mode := os.Getenv("LOPPER_PROOF_COMMAND_CHILD")
	if mode == "" {
		return
	}
	if mode == "cancel" {
		for {
			time.Sleep(time.Second)
		}
	}
	if _, err := fmt.Fprintln(os.Stdout, "stdout"); err != nil {
		os.Exit(2)
	}
	if _, err := fmt.Fprintln(os.Stderr, "stderr"); err != nil {
		os.Exit(2)
	}
	if mode == "failure" {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestProofAllocationCannotCrossRunnerOrRepository(t *testing.T) {
	root, otherRoot := t.TempDir(), t.TempDir()
	r := &runner{execCommand: func(context.Context, string, []string, string, []string) ([]byte, error) { return nil, nil }}
	path, cleanup, err := r.createBaseWorktree(context.Background(), root, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			if err := cleanup(); err != nil {
				t.Error(err)
			}
		}
	})
	for _, runnerRoot := range []struct {
		runner *runner
		root   string
	}{{&runner{}, root}, {r, otherRoot}} {
		if _, err := runnerRoot.runner.runGit(context.Background(), runnerRoot.root, "worktree", "remove", "--force", path); err == nil {
			t.Fatal("allocation was usable outside its owning proof and repository")
		}
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	cleaned = true
	if _, err := r.runGit(context.Background(), root, "worktree", "remove", "--force", path); err == nil {
		t.Fatal("released allocation was still usable")
	}
}

func TestProofGoCommandUsesVerifiedExecutable(t *testing.T) {
	path, err := proofGoBinaryPath()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := newProofGoCommand(context.Background(), path)
	if err != nil || cmd.Err != nil || !filepath.IsAbs(cmd.Path) {
		t.Fatalf("trusted Go constructor must be immediately usable: command=%v err=%v", cmd, err)
	}
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"go.mod": "module example.com/proof\n\ngo 1.23\n", "pkg/main.go": "package pkg\n",
	})
	output, err := (&runner{}).resolvePackage(context.Background(), root, "./pkg")
	if err != nil || output != "example.com/proof/pkg" {
		t.Fatalf("verified compiler did not execute proof operation: output=%q err=%v", output, err)
	}
}

func TestProofGoConstructorRejectsForeignExecutable(t *testing.T) {
	for _, path := range []string{"go", filepath.Join(t.TempDir(), "go"), os.Args[0]} {
		if cmd, err := newProofGoCommand(context.Background(), path); err == nil || cmd != nil {
			t.Fatalf("foreign executable accepted: path=%q command=%v err=%v", path, cmd, err)
		}
	}
}

func TestProofGoEnvPreservesOnlyValidLocalAppData(t *testing.T) {
	cacheRoot := t.TempDir()
	t.Setenv("GOCACHE", "")
	for _, value := range []string{cacheRoot, "", "relative", cacheRoot + "\r", cacheRoot + "\n"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("LOCALAPPDATA", value)
			env, err := proofGoEnv(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(env, "GOCACHE=") {
				t.Fatal("empty cache override must use the platform default")
			}
			found := slices.Contains(env, "LOCALAPPDATA="+value)
			if found != (value == cacheRoot) {
				t.Fatalf("platform cache root presence=%v for %q", found, value)
			}
		})
	}
}
