package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func TestGitFixtureCommandsHaveTheirOwnClosedBoundary(t *testing.T) {
	if _, err := gitexec.ResolveBinaryPath(); err != nil {
		t.Skip(err)
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init"}, {"status", "--short"}, {"add", "literal name;$(echo data)"},
		{"config", "filter.pwn.clean", "./intentionally-hostile-helper"},
		{"config", "filter.pwn.process", ""}, {"config", "filter.pwn.required", "true"},
		{"config", "core.fsmonitor", filepath.Join(repo, "monitor")},
		{"config", "core.hooksPath", filepath.Join(repo, "hooks")},
		{"config", "include.path", "included-filters"},
		{"-c", "core.hooksPath=" + os.DevNull, "commit", "-m", "fixture"},
		{"-c", "core.fsmonitor=false", "add", "."},
		{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "-m", "fixture"},
		{"clone", "file:////fixture-host/share/repo", filepath.Join(repo, "clone")},
	} {
		command, err := fixtureGitCommand(repo, args)
		if err != nil {
			t.Fatalf("fixture %v: %v", args, err)
		}
		if command.Args[1] != "-C" || command.Args[2] != repo {
			t.Fatalf("fixture lost directory binding: %v", command.Args)
		}
	}
}

func TestGitFixtureRejectsUnreviewedExecutionCapabilities(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{
		{}, {"-c"}, {"--exec-path=/untrusted", "status"},
		{"-c", "user.name", "init"}, {"-c", "user.name=", "init"},
		{"-c", "user.name=One", "-c", "user.name=Two", "init"},
		{"-c", "alias.run=!untrusted", "run"}, {"-c", "core.hooksPath=" + repo, "commit", "-m", "fixture"},
		{"-c", "core.fsmonitor=untrusted", "add", "."},
		{"config", "alias.run", "!untrusted"}, {"config", "filter..clean", "value"}, {"config", "filter.pwn.unknown", "value"}, {"config", "filter.pwn\n.clean", "value"},
		{"config", "core.hooksPath", "a\x00b"}, {"init", "--template=/untrusted"},
		{"add", "--chmod=+x", "."}, {"add", ""}, {"status", "--short", "extra"},
		{"clone", "https://example.test/repo", repo}, {"clone", "file://foreign/tmp/repo", repo}, {"clone", "file:///tmp/repo?query", repo}, {"clone", "relative", repo}, {"clone", "", repo},
	} {
		if command, err := fixtureGitCommand(repo, args); err == nil || command != nil {
			t.Fatalf("unreviewed fixture reached executable: %v", args)
		}
	}
	for _, repo := range []string{"relative", "a\x00b"} {
		if _, err := fixtureGitCommand(repo, []string{"init"}); err == nil {
			t.Fatal("invalid fixture directory accepted")
		}
	}
	if fixtureOperand(strings.Repeat("x", 3) + "\x00") {
		t.Fatal("NUL operand accepted")
	}
}
