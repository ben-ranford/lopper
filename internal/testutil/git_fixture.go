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
	{"init"}, {"init", "-q"}, {"init", "-b", "@operand"}, {"init", "--bare", "@local"},
	{"status", "--short"}, {"add", "-A"}, {"add", "@operands"},
	{"commit", "-m", "@value"}, {"commit", "-qm", "@value"}, {"commit", "-am", "@value"}, {"commit", "--no-verify", "-m", "@value"},
	{"config", "@config", "@value"}, {"config", "--get", "@config"}, {"config", "--unset", "@config"},
	{"config", "--local", "@config", "@value"}, {"config", "--local", "--get", "@config"}, {"config", "--local", "--add", "@config", "@value"},
	{"checkout", "@operand"}, {"checkout", "-b", "@operand"}, {"checkout", "-b", "@operand", "@operand"}, {"checkout", "--orphan", "@operand"},
	{"branch", "@operand", "@operand"}, {"tag", "@operand"}, {"tag", "@operand", "@operand"},
	{"clone", "@local", "@local"}, {"remote", "add", "origin", "@local"}, {"push", "@local", "@operands"},
	{"merge", "--no-edit", "@operand"}, {"merge", "--no-ff", "--no-edit", "@operand"}, {"merge", "--no-commit", "--no-ff", "@operand"}, {"merge", "--no-ff", "-m", "@value", "@operand"},
	{"mv", "@operand", "@operand"}, {"sparse-checkout", "list"}, {"sparse-checkout", "set", "@operand"},
	{"worktree", "add", "@local"}, {"worktree", "add", "--detach", "@local"}, {"worktree", "list", "--porcelain"},
	{"ls-files"}, {"show", "@operand"}, {"write-tree"}, {"commit-tree", "@operand", "-m", "@value"},
	{"diff", "--cached", "--name-only"}, {"verify-pack", "-v", "@local"},
	{"rev-parse", "HEAD"}, {"rev-parse", "HEAD^{tree}"}, {"rev-parse", "--absolute-git-dir"},
	{"rev-parse", "--git-path", "@operand"}, {"rev-parse", "--path-format=absolute", "--git-common-dir"}, {"rev-parse", "--path-format=absolute", "--git-path", "@operand"},
}

func fixtureMatches(args, pattern []string) bool {
	for index, token := range pattern {
		if index >= len(args) {
			return false
		}
		if token == "@operands" {
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
	case "@value":
		return !strings.ContainsRune(value, 0)
	case "@operand":
		return fixtureOperand(value)
	case "@config":
		return fixtureConfigKey(value)
	case "@local":
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
