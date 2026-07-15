package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectLockfileDriftPropagatesGitContextErrors(t *testing.T) {
	original := resolveGitBinaryPathFn
	defer func() { resolveGitBinaryPathFn = original }()

	resolveGitBinaryPathFn = func() (string, error) { return writeFakeGitBinary(t), nil }
	useFakeGitCommandContext(t)

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, lockfileName), "{}\n")
	writeFakeGitMode(t, repo, "lsfail")

	for _, stopOnFirst := range []bool{false, true} {
		_, err := detectLockfileDrift(context.Background(), repo, stopOnFirst)
		if err == nil || !strings.Contains(err.Error(), "ls-files") {
			t.Fatalf("expected ls-files error with stopOnFirst=%v, got %v", stopOnFirst, err)
		}
	}
}

func TestDetectLockfileDriftStopOnFirstBatchesGitContextAcrossDirectories(t *testing.T) {
	repo := t.TempDir()
	const candidateDirs = gitPathspecBatchPaths/2 + 1
	for index := range candidateDirs {
		dir := filepath.Join(repo, fmt.Sprintf("pkg-%03d", index))
		writeFile(t, filepath.Join(dir, manifestFileName), demoPackageJSON)
		writeFile(t, filepath.Join(dir, lockfileName), "{}\n")
	}
	initGitRepo(t, repo)

	commandGroups := captureLockfileGitCommandGroups(t)
	warnings, err := detectLockfileDrift(context.Background(), repo, true)
	if err != nil {
		t.Fatalf("detect lockfile drift: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected clean candidate directories, got %#v", warnings)
	}

	if got := commandGroups["worktree"]; got != 1 {
		t.Errorf("expected one worktree detection command, got %d", got)
	}
	for _, group := range []string{"check-attr", "head", "diff", "ls-files"} {
		if got := commandGroups[group]; got != 2 {
			t.Errorf("expected two bounded %s command groups for %d candidate directories, got %d", group, candidateDirs, got)
		}
	}
}

func TestDetectLockfileDriftStopOnFirstPropagatesGitDetectionErrors(t *testing.T) {
	t.Run("command construction", func(t *testing.T) {
		originalResolve := resolveGitBinaryPathFn
		forcedErr := errors.New("forced git detection construction failure")
		resolveGitBinaryPathFn = func() (string, error) { return "", forcedErr }
		t.Cleanup(func() { resolveGitBinaryPathFn = originalResolve })

		_, err := detectLockfileDrift(context.Background(), t.TempDir(), true)
		if !errors.Is(err, forcedErr) {
			t.Fatalf("expected git detection construction error, got %v", err)
		}
	})

	t.Run("rev-parse execution", func(t *testing.T) {
		originalResolve := resolveGitBinaryPathFn
		originalExec := execGitCommandContextFn
		resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
		execGitCommandContextFn = func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 23"), nil
		}
		t.Cleanup(func() {
			resolveGitBinaryPathFn = originalResolve
			execGitCommandContextFn = originalExec
		})

		_, err := detectLockfileDrift(context.Background(), t.TempDir(), true)
		if err == nil || !strings.Contains(err.Error(), "rev-parse --is-inside-work-tree") {
			t.Fatalf("expected rev-parse worktree detection error, got %v", err)
		}
	})
}

func captureLockfileGitCommandGroups(t *testing.T) map[string]int {
	t.Helper()

	originalExec := execGitCommandContextFn
	commandGroups := make(map[string]int)
	execGitCommandContextFn = func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		if group := lockfileGitCommandGroup(args); group != "" {
			commandGroups[group]++
		}
		return originalExec(ctx, gitPath, args...)
	}
	t.Cleanup(func() { execGitCommandContextFn = originalExec })
	return commandGroups
}

func lockfileGitCommandGroup(args []string) string {
	for index, arg := range args {
		if arg != "-C" || index+2 >= len(args) {
			continue
		}
		commandArgs := args[index+2:]
		switch commandArgs[0] {
		case "check-attr", "diff", "ls-files":
			return commandArgs[0]
		case gitRevParseSubcommand:
			if len(commandArgs) > 1 && commandArgs[1] == "--verify" {
				return "head"
			}
			return "worktree"
		default:
			return ""
		}
	}
	return ""
}
