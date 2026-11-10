package gitexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommandRejectsUnsupportedRequestsBeforeExecution(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{}, {"status"}, {"--exec-path=/tmp/untrusted", "status", "--porcelain"},
		{"-C", repo, "-C", repo, "status", "--porcelain"},
		{"-C", repo, "-c", "alias.injected=!echo untrusted", "injected"},
		{"-C", repo, "-c", "core.sshCommand=untrusted", "fetch", "--prune", "--depth=1", "origin", "HEAD"},
		{"-C", repo, "-c", "core.hooksPath=" + repo, "worktree", "remove", "--force", repo},
		{"-C", repo, "-c", "protocol.file.allow=always", "-c", "protocol.file.allow=always", "clean", "-fdx"},
		{"-C", repo, "--config-env=core.sshCommand=EVIL", "status", "--porcelain"},
		{"-C", repo, "status", "--porcelain", "--ignored"},
		{"-C", repo, "config", "core.sshCommand", "untrusted"},
		{"-C", repo, "fetch", "--prune", "--no-tags", "--depth=1", "origin", "--upload-pack=untrusted"},
		{"-C", repo, "fetch", "--prune", "--no-tags", "--depth=1", "origin", "refs/heads/main:refs/heads/overwritten"},
		{"-C", repo, "rev-parse", "--verify", "--output=/tmp/untrusted"},
		{"-C", repo, "checkout", "--detach", "--force", "--orphan=untrusted"},
		{"-C", repo, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", "--", ":(glob)*"},
		{"clone", "--no-tags", "--depth=1", "--", "ext::untrusted", repo},
		{"clone", "--no-tags", "--depth=1", "--", "ssh://-oProxyCommand=untrusted/repo", repo},
		{"init", "--template=/tmp/untrusted"},
		{"-C", repo, "hash-object", "--path=go.mod", "--stdin"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			assertRequestRejected(t, args)
		})
	}
}

func assertRequestRejected(t *testing.T, args []string) {
	t.Helper()
	command, err := Command(ExecutablePrimary, args...)
	if err == nil || command != nil {
		t.Fatalf("unsupported request reached command construction: %v; command=%v error=%v", args, command, err)
	}
	command, err = CommandContext(context.Background(), ExecutablePrimary, args...)
	if err == nil || command != nil {
		t.Fatalf("unsupported context request reached command construction: %v; command=%v error=%v", args, command, err)
	}
}

func TestCommandPreservesSupportedCallerArguments(t *testing.T) {
	repo := t.TempDir()
	oid := strings.Repeat("a", 40)
	plain := []string{"-C", repo}
	safe := append(SafeConfigArgs(), plain...)
	hooks := append([]string{"-c", "core.hooksPath=" + os.DevNull}, plain...)
	proof := append(SafeConfigArgs(), hooks...)
	for _, tc := range []struct {
		name              string
		prefix, operation []string
	}{
		{"workspace status", plain, []string{"status", "--porcelain"}},
		{"workspace previous diff", plain, []string{"diff", "--no-ext-diff", "--no-textconv", "--name-only", "--diff-filter=ACMRD", "HEAD~1..HEAD"}},
		{"workspace pair", plain, []string{"diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "--find-renames", "--find-copies", "--diff-filter=ACMRD", "base..HEAD"}},
		{"lockfile root", safe, []string{"rev-parse", "--is-inside-work-tree"}},
		{"lockfile head", safe, []string{"rev-parse", "--verify", "--quiet", "HEAD"}},
		{"literal names", safe, []string{"diff", "--no-ext-diff", "--no-textconv", "HEAD", "--name-only", "-z", "--", ":(literal)--strange name;$(false)\nfile"}},
		{"codemod tracked", safe, []string{"diff", "--no-ext-diff", "--no-textconv", "HEAD", "--name-only", "-z", "--"}},
		{"codemod unborn staged", safe, []string{"diff", "--no-ext-diff", "--no-textconv", "--cached", "--name-only", "-z", "--"}},
		{"codemod unborn unstaged", safe, []string{"diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", "--"}},
		{"unborn staged", safe, []string{"diff", "--no-ext-diff", "--no-textconv", "--cached", "--name-only", "-z", "--", ":(literal)go.mod"}},
		{"unborn unstaged", safe, []string{"diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", "--", ":(literal)go.mod"}},
		{"visible files", safe, []string{"ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", ":(literal)go.mod"}},
		{"untracked files", safe, []string{"ls-files", "--others", "--exclude-standard", "-z", "--", ":(literal)go.mod"}},
		{"all untracked NUL", safe, []string{"ls-files", "--others", "--exclude-standard", "-z", "--"}},
		{"legacy untracked", safe, []string{"ls-files", "--others", "--exclude-standard"}},
		{"attributes", safe, []string{"check-attr", "--stdin", "-z", "--all"}},
		{"filter config", safe, []string{"config", "--null", "--includes", "--get-regexp", `^filter\..*\.(clean|process)$`}},
		{"PR root", hooks, []string{"rev-parse", "--show-toplevel"}},
		{"PR prefix", hooks, []string{"rev-parse", "--show-prefix"}},
		{"PR commit", hooks, []string{"rev-parse", "--verify", oid + "^{commit}"}},
		{"PR worktree", hooks, []string{"worktree", "add", "--detach", "--force", filepath.Join(repo, "base"), oid}},
		{"proof worktree", proof, []string{"worktree", "add", "--detach", filepath.Join(repo, "base"), oid}},
		{"proof remove", proof, []string{"worktree", "remove", "--force", filepath.Join(repo, "base")}},
		{"proof base", proof, []string{"merge-base", "--", "HEAD~1", "HEAD"}},
		{"proof changes", proof, []string{"diff", "--name-only", "--diff-filter=ACMR", oid + "..HEAD", "--"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.prefix...), tc.operation...)
			command, err := CommandContext(context.Background(), ExecutablePrimary, args...)
			if err != nil {
				t.Fatal(err)
			}
			want := append([]string{ExecutablePrimary}, args...)
			args[len(args)-1] = "changed after construction"
			if !reflect.DeepEqual(command.Args, want) {
				t.Fatalf("validated arguments changed: got %q; want %q", command.Args, want)
			}
		})
	}
}

func TestCommandCancellationAndSanitizedEnvironmentRemainUsable(t *testing.T) {
	gitPath, err := ResolveBinaryPath()
	if err != nil {
		t.Skip(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command, err := CommandContext(ctx, gitPath, "--version")
	if err != nil {
		t.Fatal(err)
	}
	command.Env = SanitizedEnv()
	if err := command.Run(); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled command result: %v", err)
	}
	command, err = Command(gitPath, "--version")
	if err != nil {
		t.Fatal(err)
	}
	command.Env = SanitizedEnv()
	if output, err := command.Output(); err != nil || !strings.HasPrefix(string(output), "git version ") {
		t.Fatalf("version output=%q, error=%v", output, err)
	}
}
