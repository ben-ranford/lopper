package app

import (
	"context"
	"errors"
	"fmt"
	"os"
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

func TestDetectLockfileDriftPlainDirectoryWithoutGitBinary(t *testing.T) {
	original := resolveGitBinaryPathFn
	resolveGitBinaryPathFn = func() (string, error) { return "", errors.New(gitExecutableNotFoundErr) }
	t.Cleanup(func() { resolveGitBinaryPathFn = original })

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)

	for _, stopOnFirst := range []bool{false, true} {
		warnings, err := detectLockfileDrift(context.Background(), repo, stopOnFirst)
		assertSingleLockfileDriftWarning(t, warnings, err, "npm in .: package.json exists but no matching lockfile", "npm install")
	}
}

func TestGitWorktreeDetectionMissingBinaryFailsClosedWhenMetadataCannotBeInspected(t *testing.T) {
	original := resolveGitBinaryPathFn
	forcedErr := errors.New(gitExecutableNotFoundErr)
	resolveGitBinaryPathFn = func() (string, error) { return "", forcedErr }
	t.Cleanup(func() { resolveGitBinaryPathFn = original })

	_, err := isGitWorktree(context.Background(), filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, forcedErr) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected binary resolution and metadata inspection errors, got %v", err)
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

		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
		_, err := detectLockfileDrift(context.Background(), repo, true)
		if !errors.Is(err, forcedErr) {
			t.Fatalf("expected git detection construction error, got %v", err)
		}
	})

	t.Run("command factory", func(t *testing.T) {
		originalResolve := resolveGitBinaryPathFn
		originalExec := execGitCommandContextFn
		forcedErr := errors.New("forced git command factory failure")
		resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
		execGitCommandContextFn = func(context.Context, string, ...string) (*exec.Cmd, error) {
			return nil, forcedErr
		}
		t.Cleanup(func() {
			resolveGitBinaryPathFn = originalResolve
			execGitCommandContextFn = originalExec
		})

		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
		_, err := detectLockfileDrift(context.Background(), repo, true)
		if !errors.Is(err, forcedErr) {
			t.Fatalf("expected git command factory error, got %v", err)
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

func TestGitWorktreeDetectionDistinguishesFalseAndMalformedResults(t *testing.T) {
	cases := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{name: "explicit false", output: "false\n"},
		{name: "malformed", output: "indeterminate\n", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			originalResolve := resolveGitBinaryPathFn
			originalExec := execGitCommandContextFn
			resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
			execGitCommandContextFn = func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
				return exec.CommandContext(ctx, "/bin/sh", "-c", "printf '%s' \"$1\"", "git-output", tc.output), nil
			}
			t.Cleanup(func() {
				resolveGitBinaryPathFn = originalResolve
				execGitCommandContextFn = originalExec
			})

			inside, err := isGitWorktree(context.Background(), t.TempDir())
			if (err != nil) != tc.wantErr {
				t.Fatalf("isGitWorktree error = %v, wantErr=%v", err, tc.wantErr)
			}
			if inside {
				t.Fatal("expected false worktree result")
			}
		})
	}
}

func TestLockfileGitSnapshotBatchHonorsLimitsAndDeduplicates(t *testing.T) {
	if batches := gitPathspecBatches(nil); len(batches) != 0 {
		t.Fatalf("expected no pathspec batches for an empty input, got %#v", batches)
	}

	snapshot := lockfileDirSnapshot{relDir: "pkg"}
	batch := lockfileGitSnapshotBatch{}
	batch.add(snapshot, []string{"pkg/package.json", "pkg/package.json"})
	if len(batch.candidatePaths) != 1 {
		t.Fatalf("expected duplicate candidate to be stored once, got %#v", batch.candidatePaths)
	}
	if batch.wouldOverflow([]string{"pkg/package.json"}) {
		t.Fatal("expected an already-buffered candidate not to consume another batch slot")
	}

	batch.snapshots = make([]lockfileDirSnapshot, lockfileGitBatchSnapshots)
	if !batch.wouldOverflow(nil) {
		t.Fatal("expected snapshot count cap to flush the batch")
	}
	batch.snapshots = []lockfileDirSnapshot{snapshot}
	batch.candidatePaths = make([]string, gitPathspecBatchPaths)
	if !batch.wouldOverflow([]string{"pkg/package-lock.json"}) {
		t.Fatal("expected candidate count cap to flush the batch")
	}
	batch.candidatePaths = nil
	batch.candidatePathBytes = gitPathspecBatchBytes
	if !batch.wouldOverflow([]string{"pkg/package-lock.json"}) {
		t.Fatal("expected candidate byte cap to flush the batch")
	}
}

func TestLockfileFailFastBatchScannerPropagatesSnapshotEvaluationErrors(t *testing.T) {
	repo, snapshot := newPoetrySnapshot(t, true)
	forcedErr := errors.New("forced snapshot evaluation failure")

	t.Run("record first", func(t *testing.T) {
		rule := newPoetryLockfileRule(func(string, string) (bool, error) {
			return false, forcedErr
		})
		scanner := lockfileFailFastBatchScanner{repoPath: repo, rules: []lockfileRule{rule}}
		if err := scanner.recordFirst(snapshot, lockfileGitContext{}); !errors.Is(err, forcedErr) {
			t.Fatalf("expected record-first evaluation error, got %v", err)
		}
	})

	t.Run("candidate paths", func(t *testing.T) {
		matcherCalls := 0
		rule := newPoetryLockfileRule(func(string, string) (bool, error) {
			matcherCalls++
			if matcherCalls == 2 {
				return false, forcedErr
			}
			return true, nil
		})
		scanner := lockfileFailFastBatchScanner{repoPath: repo, rules: []lockfileRule{rule}}
		if err := scanner.visit(context.Background(), snapshot); !errors.Is(err, forcedErr) {
			t.Fatalf("expected candidate-path evaluation error, got %v", err)
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
