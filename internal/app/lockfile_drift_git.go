package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

var resolveGitBinaryPathFn = gitexec.ResolveBinaryPath
var collectLockfileGitContextFn = collectLockfileGitContext
var execGitCommandContextFn = gitexec.CommandContext

const (
	gitObjectFormatSHA1   = "sha1"
	gitObjectFormatSHA256 = "sha256"
	emptyGitTreeSHA1      = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	emptyGitTreePayload   = "tree 0\x00"
)

type lockfileGitContext struct {
	changedFiles  map[string]struct{}
	hasGitContext bool
}

func collectLockfileGitContext(ctx context.Context, repoPath string) (lockfileGitContext, error) {
	changedFiles, hasGitContext, err := gitChangedFiles(ctx, repoPath)
	if err != nil {
		return lockfileGitContext{}, err
	}
	return lockfileGitContext{
		changedFiles:  changedFiles,
		hasGitContext: hasGitContext,
	}, nil
}

func gitChangedFiles(ctx context.Context, repoPath string) (map[string]struct{}, bool, error) {
	if !isGitWorktree(ctx, repoPath) {
		return nil, false, nil
	}

	changed := map[string]struct{}{}
	tracked, err := gitTrackedChanges(ctx, repoPath)
	if err != nil {
		return nil, true, err
	}
	for _, path := range tracked {
		changed[path] = struct{}{}
	}

	untracked, err := gitUntrackedFiles(ctx, repoPath)
	if err != nil {
		return nil, true, err
	}
	for _, path := range untracked {
		changed[path] = struct{}{}
	}

	return changed, true, nil
}

func isGitWorktree(ctx context.Context, repoPath string) bool {
	command, err := gitCommandContext(ctx, repoPath, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	output, err := command.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "true"
}

func gitTrackedChanges(ctx context.Context, repoPath string) ([]string, error) {
	hasHead, err := gitHasVerifiedHead(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	if hasHead {
		return gitDiffNameOnly(ctx, repoPath, "HEAD")
	}
	// Unborn HEAD: derive tracked changes from staged + working tree diffs.
	staged, err := gitDiffNameOnly(ctx, repoPath, "--cached")
	if err != nil {
		return nil, err
	}
	unstaged, err := gitDiffNameOnly(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	return mergeGitPaths(staged, unstaged), nil
}

func gitHasVerifiedHead(ctx context.Context, repoPath string) (bool, error) {
	command, err := gitCommandContext(ctx, repoPath, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return false, err
	}
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("run git rev-parse --verify --quiet HEAD: %w", err)
	}
	return true, nil
}

func gitDiffNameOnly(ctx context.Context, repoPath string, diffArgs ...string) ([]string, error) {
	attrSourceArg, err := gitEmptyTreeAttrSourceArg(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	args := []string{attrSourceArg, "-c", "diff.external=", "diff", "--no-ext-diff", "--no-textconv"}
	args = append(args, diffArgs...)
	args = append(args, "--name-only", "--")
	command, err := gitCommandContext(ctx, repoPath, args...)
	if err != nil {
		return nil, err
	}
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git %s: %w", strings.Join(args, " "), err)
	}
	return parseGitOutputLines(output), nil
}

func mergeGitPaths(groups ...[]string) []string {
	if len(groups) == 0 {
		return nil
	}
	merged := make([]string, 0)
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, path := range group {
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			merged = append(merged, path)
		}
	}
	return merged
}

func gitUntrackedFiles(ctx context.Context, repoPath string) ([]string, error) {
	command, err := gitCommandContext(ctx, repoPath, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run git ls-files --others --exclude-standard: %w", err)
	}
	return parseGitOutputLines(output), nil
}

func gitCommandContext(ctx context.Context, repoPath string, args ...string) (*exec.Cmd, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	gitPath, err := resolveGitBinaryPathFn()
	if err != nil {
		return nil, err
	}
	commandArgs := append(gitexec.SafeConfigArgs(), "-C", repoPath)
	commandArgs = append(commandArgs, args...)
	command, err := execGitCommandContextFn(ctx, gitPath, commandArgs...)
	if err != nil {
		return nil, err
	}
	command.Env = sanitizedGitEnv()
	return command, nil
}

func parseGitOutputLines(output []byte) []string {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	return lines
}

func gitEmptyTreeAttrSourceArg(ctx context.Context, repoPath string) (string, error) {
	objectFormat, err := gitObjectFormat(ctx, repoPath)
	if err != nil {
		return "", err
	}
	objectID, err := emptyTreeObjectID(objectFormat)
	if err != nil {
		return "", err
	}
	return "--attr-source=" + objectID, nil
}

func gitObjectFormat(ctx context.Context, repoPath string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	gitPath, err := resolveGitBinaryPathFn()
	if err != nil {
		return "", err
	}
	commandArgs := append(gitexec.SafeConfigArgs(), "-C", repoPath, "rev-parse", "--show-object-format")
	command, err := execGitCommandContextFn(ctx, gitPath, commandArgs...)
	if err != nil {
		return "", err
	}
	command.Env = sanitizedGitEnv()
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("run git rev-parse --show-object-format: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func emptyTreeObjectID(objectFormat string) (string, error) {
	payload := []byte(emptyGitTreePayload)
	switch strings.ToLower(strings.TrimSpace(objectFormat)) {
	case gitObjectFormatSHA1:
		return emptyGitTreeSHA1, nil
	case gitObjectFormatSHA256:
		sum := sha256.Sum256(payload)
		return hex.EncodeToString(sum[:]), nil
	default:
		return "", fmt.Errorf("unsupported git object format: %q", objectFormat)
	}
}

func sanitizedGitEnv() []string {
	return gitexec.SanitizedEnv()
}
