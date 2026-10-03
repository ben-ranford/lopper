package testutil

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

const (
	fixtureLocalToken      = "@local"
	fixtureOperandToken    = "@operand"
	fixtureOperandsToken   = "@operands"
	fixtureValueToken      = "@value"
	fixtureConfigToken     = "@config"
	fixtureLocalFlag       = "--local"
	fixtureNoFFFlag        = "--no-ff"
	fixtureRevParseCommand = "rev-parse"
)

// Fixture setup intentionally supports writes and hostile config values used
// by security tests. Keep that vocabulary here, separate from the application's
// Git boundary; it cannot select arbitrary executables, globals or options.
func fixtureGitCommand(repo string, args []string) (*exec.Cmd, error) {
	args = slices.Clone(args)
	if !filepath.IsAbs(repo) || strings.ContainsRune(repo, 0) || !validFixtureRequest(args) {
		return nil, fmt.Errorf("unsupported git fixture request")
	}
	path, err := gitexec.ResolveBinaryPath()
	if err != nil {
		return nil, err
	}
	var command *exec.Cmd
	switch path {
	case gitexec.ExecutablePrimary:
		command = exec.CommandContext(context.Background(), "/usr/bin/git")
	case gitexec.ExecutableFallback:
		command = exec.CommandContext(context.Background(), "/bin/git")
	default:
		return nil, fmt.Errorf("unsupported git fixture executable")
	}
	command.Args = append(command.Args, "-C", repo)
	command.Args = append(command.Args, args...)
	return command, nil
}

func validFixtureRequest(args []string) bool {
	seen := make(map[string]bool)
	for len(args) >= 2 && args[0] == "-c" {
		key, value, ok := strings.Cut(args[1], "=")
		if !ok || seen[key] || !fixtureOverride(key, value) {
			return false
		}
		seen[key] = true
		args = args[2:]
	}
	for _, pattern := range fixturePatterns {
		if fixtureMatches(args, pattern) {
			return true
		}
	}
	return false
}

func fixtureOverride(key, value string) bool {
	switch key {
	case "core.hooksPath":
		return value == os.DevNull
	case "core.fsmonitor":
		return value == "false"
	case "user.name", "user.email":
		return value != "" && !strings.ContainsRune(value, 0)
	default:
		return false
	}
}

var fixturePatterns = [][]string{
	{"init"}, {"init", "-q"}, {"init", "-b", fixtureOperandToken}, {"init", "--bare", fixtureLocalToken},
	{"status", "--short"}, {"add", "-A"}, {"add", fixtureOperandsToken},
	{"commit", "-m", fixtureValueToken}, {"commit", "-qm", fixtureValueToken}, {"commit", "-am", fixtureValueToken}, {"commit", "--no-verify", "-m", fixtureValueToken},
	{"config", fixtureConfigToken, fixtureValueToken}, {"config", "--get", fixtureConfigToken}, {"config", "--unset", fixtureConfigToken},
	{"config", fixtureLocalFlag, fixtureConfigToken, fixtureValueToken}, {"config", fixtureLocalFlag, "--get", fixtureConfigToken}, {"config", fixtureLocalFlag, "--add", fixtureConfigToken, fixtureValueToken},
	{"checkout", fixtureOperandToken}, {"checkout", "-b", fixtureOperandToken}, {"checkout", "-b", fixtureOperandToken, fixtureOperandToken}, {"checkout", "--orphan", fixtureOperandToken},
	{"branch", fixtureOperandToken, fixtureOperandToken}, {"tag", fixtureOperandToken}, {"tag", fixtureOperandToken, fixtureOperandToken},
	{"clone", fixtureLocalToken, fixtureLocalToken}, {"remote", "add", "origin", fixtureLocalToken}, {"push", fixtureLocalToken, fixtureOperandsToken},
	{"merge", "--no-edit", fixtureOperandToken}, {"merge", fixtureNoFFFlag, "--no-edit", fixtureOperandToken}, {"merge", "--no-commit", fixtureNoFFFlag, fixtureOperandToken}, {"merge", fixtureNoFFFlag, "-m", fixtureValueToken, fixtureOperandToken},
	{"mv", fixtureOperandToken, fixtureOperandToken}, {"sparse-checkout", "list"}, {"sparse-checkout", "set", fixtureOperandToken},
	{"worktree", "add", fixtureLocalToken}, {"worktree", "add", "--detach", fixtureLocalToken}, {"worktree", "list", "--porcelain"},
	{"ls-files"}, {"show", fixtureOperandToken}, {"write-tree"}, {"commit-tree", fixtureOperandToken, "-m", fixtureValueToken},
	{"diff", "--cached", "--name-only"}, {"verify-pack", "-v", fixtureLocalToken},
	{fixtureRevParseCommand, "HEAD"}, {fixtureRevParseCommand, "HEAD^{tree}"}, {fixtureRevParseCommand, "--absolute-git-dir"},
	{fixtureRevParseCommand, "--git-path", fixtureOperandToken}, {fixtureRevParseCommand, "--path-format=absolute", "--git-common-dir"}, {fixtureRevParseCommand, "--path-format=absolute", "--git-path", fixtureOperandToken},
}

func fixtureMatches(args, pattern []string) bool {
	for index, token := range pattern {
		if index >= len(args) {
			return false
		}
		if token == fixtureOperandsToken {
			for _, arg := range args[index:] {
				if !fixtureOperand(arg) {
					return false
				}
			}
			return true
		}
		if !fixtureToken(args[index], token) {
			return false
		}
	}
	return len(args) == len(pattern)
}

func fixtureToken(value, token string) bool {
	switch token {
	case fixtureValueToken:
		return !strings.ContainsRune(value, 0)
	case fixtureOperandToken:
		return fixtureOperand(value)
	case fixtureConfigToken:
		return fixtureConfigKey(value)
	case fixtureLocalToken:
		return fixtureLocal(value)
	default:
		return value == token
	}
}

func fixtureOperand(value string) bool {
	return value != "" && !strings.HasPrefix(value, "-") && !strings.ContainsRune(value, 0)
}

func fixtureLocal(value string) bool {
	if !fixtureOperand(value) {
		return false
	}
	if filepath.IsAbs(value) {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "file" && parsed.User == nil && parsed.Host == "" &&
		parsed.RawQuery == "" && parsed.Fragment == "" && filepath.IsAbs(parsed.Path)
}

func fixtureConfigKey(value string) bool {
	switch value {
	case "user.name", "user.email", "core.bare", "core.hooksPath", "core.fsmonitor", "include.path":
		return true
	}
	if !strings.HasPrefix(value, "filter.") || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	for _, suffix := range []string{".clean", ".process", ".required"} {
		if strings.HasSuffix(value, suffix) && len(value) > len("filter.")+len(suffix) {
			return true
		}
	}
	return false
}
