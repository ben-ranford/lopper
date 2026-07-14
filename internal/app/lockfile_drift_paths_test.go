package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const lockfileRunGitErr = "run git"

func TestLockfileDriftAdditionalPathAndWalkBranches(t *testing.T) {
	if _, err := detectLockfileDrift(context.Background(), "\x00", false); err == nil {
		t.Fatalf("expected detectLockfileDrift to reject invalid repo path")
	}

	parent := t.TempDir()
	child := filepath.Join(parent, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("readdir parent: %v", err)
	}
	if err := os.RemoveAll(child); err != nil {
		t.Fatalf("remove child: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "child" {
			continue
		}
		if processLockfileDir(context.Background(), child, entry, nil, lockfileWalkState{repoPath: parent}) == nil {
			t.Fatalf("expected removed directory to fail when scanning lockfile drift")
		}
	}

	if got := relativeDir("\x00", filepath.Join(parent, "pkg")); got != filepath.Join(parent, "pkg") {
		t.Fatalf("expected relativeDir to fall back to input dir, got %q", got)
	}
	if got := mergeGitPaths(); len(got) != 0 {
		t.Fatalf("expected mergeGitPaths with no groups to return nil, got %#v", got)
	}
}

func TestLockfileDriftGitErrorBranches(t *testing.T) {
	original := resolveGitBinaryPathFn
	defer func() { resolveGitBinaryPathFn = original }()

	repo := t.TempDir()
	fakeGit := writeFakeGitBinary(t)
	resolveGitBinaryPathFn = func() (string, error) { return fakeGit, nil }
	useFakeGitCommandContext(t)

	writeFakeGitMode(t, repo, "lsfail")
	if _, _, err := gitChangedFiles(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "ls-files") {
		t.Fatalf("expected gitChangedFiles to surface ls-files failure, got %v", err)
	}

	writeFakeGitMode(t, repo, "checkattrfail")
	if _, err := gitDiffNameOnly(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "check-attr") {
		t.Fatalf("expected gitDiffNameOnly to surface check-attr failure, got %v", err)
	}

	writeFakeGitMode(t, repo, "difffail-head")
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil || !strings.Contains(err.Error(), lockfileRunGitErr) {
		t.Fatalf("expected gitTrackedChanges HEAD diff failure, got %v", err)
	}

	writeFakeGitMode(t, repo, "difffail-unstaged")
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil || !strings.Contains(err.Error(), lockfileRunGitErr) {
		t.Fatalf("expected gitTrackedChanges unstaged diff failure, got %v", err)
	}

	writeFakeGitMode(t, repo, "difffail-cached")
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil || !strings.Contains(err.Error(), lockfileRunGitErr) {
		t.Fatalf("expected gitTrackedChanges cached diff failure, got %v", err)
	}

	resolveGitBinaryPathFn = func() (string, error) { return "", context.Canceled }
	if _, err := gitDiffNameOnly(context.Background(), repo); err == nil {
		t.Fatalf("expected gitDiffNameOnly to surface git command creation failure")
	}
}

func TestGitCommandContextConstructorError(t *testing.T) {
	originalResolve := resolveGitBinaryPathFn
	originalExec := execGitCommandContextFn
	resolveGitBinaryPathFn = func() (string, error) { return writeFakeGitBinary(t), nil }
	execGitCommandContextFn = func(context.Context, string, ...string) (*exec.Cmd, error) {
		return nil, errors.New("construct git")
	}
	t.Cleanup(func() {
		resolveGitBinaryPathFn = originalResolve
		execGitCommandContextFn = originalExec
	})

	if _, err := gitCommandContext(context.Background(), t.TempDir(), "status"); err == nil || !strings.Contains(err.Error(), "construct git") {
		t.Fatalf("expected gitCommandContext to return constructor error, got %v", err)
	}
}

func TestGitDiffNameOnlyNeutralizesFilterDriversWithoutAttrSource(t *testing.T) {
	repo := configureFakeGitRepo(t, "filterdriver")

	if _, err := gitDiffNameOnly(context.Background(), repo); err != nil {
		t.Fatalf("expected gitDiffNameOnly to construct a portable hardened diff command, got %v", err)
	}
}

func TestGitDiffNameOnlyRejectsMalformedCheckAttrOutput(t *testing.T) {
	cases := []struct {
		name string
		mode string
	}{
		{name: "truncated output", mode: "checkattrtruncated"},
		{name: "wrong field count", mode: "checkattrwrongfieldcount"},
		{name: "wrong attribute", mode: "checkattrwrongattr"},
		{name: "wrong path order", mode: "checkattrwrongorder"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := configureFakeGitRepo(t, tc.mode)

			_, err := gitDiffNameOnly(context.Background(), repo)
			if err == nil {
				t.Fatal("expected malformed check-attr output to fail closed")
			}
			if !strings.Contains(err.Error(), "parse git check-attr --stdin -z filter output") {
				t.Fatalf("expected parse error from malformed check-attr output, got %v", err)
			}
			if strings.Contains(err.Error(), "run git diff") {
				t.Fatalf("expected malformed check-attr output to abort before git diff, got %v", err)
			}
		})
	}
}

func TestGitAttributeCandidatePathsHandlesNilContextAndCommandFailure(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		repo := configureFakeGitRepo(t, "filterdriver")

		paths, err := gitAttributeCandidatePaths(testNilContext(), repo)
		if err != nil {
			t.Fatalf("expected gitAttributeCandidatePaths with nil context to succeed, got %v", err)
		}
		if len(paths) != 1 || paths[0] != "package.json" {
			t.Fatalf("unexpected attribute candidate paths %#v", paths)
		}
	})

	t.Run("command failure", func(t *testing.T) {
		repo := configureFakeGitRepo(t, "lsfilescachedfail")

		if _, err := gitAttributeCandidatePaths(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "ls-files --cached --others --exclude-standard -z") {
			t.Fatalf("expected cached ls-files failure, got %v", err)
		}
	})
}

func TestGitActiveFilterDriversHandlesEmptyPaths(t *testing.T) {
	drivers, err := gitActiveFilterDrivers(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("expected empty path set to skip git check-attr, got %v", err)
	}
	if len(drivers) != 0 {
		t.Fatalf("expected no drivers for empty path set, got %#v", drivers)
	}
}

func TestParseNULTerminatedGitOutput(t *testing.T) {
	got := parseNULTerminatedGitOutput([]byte("a\x00b\x00\x00"))
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("unexpected nul-delimited parse result %#v", got)
	}
}

func TestParseNULTerminatedGitFields(t *testing.T) {
	got := parseNULTerminatedGitFields([]byte("a\x00\x00b\x00"))
	want := []string{"a", "", "b"}
	if len(got) != len(want) {
		t.Fatalf("expected %d nul-delimited fields, got %#v", len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("expected field %q at index %d, got %#v", want[index], index, got)
		}
	}
}

func TestParseGitCheckAttrFilterDrivers(t *testing.T) {
	output := []byte("package.json\x00filter\x00foo.bar\x00package-lock.json\x00filter\x00unspecified\x00nested/package.json\x00filter\x00foo/bar\x00package.json\x00filter\x00foo.bar\x00")
	got, err := parseGitCheckAttrFilterDrivers([]string{"package.json", "package-lock.json", "nested/package.json", "package.json"}, output)
	if err != nil {
		t.Fatalf("expected valid check-attr output to parse, got %v", err)
	}
	want := []string{"foo.bar", "foo/bar"}
	if len(got) != len(want) {
		t.Fatalf("expected %d filter drivers, got %#v", len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("expected filter driver %q at index %d, got %#v", want[index], index, got)
		}
	}

	got, err = parseGitCheckAttrFilterDrivers([]string{"package.json", "package-lock.json", "nested/package.json"}, []byte("package.json\x00filter\x00set\x00package-lock.json\x00filter\x00unset\x00nested/package.json\x00filter\x00 \x00"))
	if err != nil {
		t.Fatalf("expected ignorable filter values to parse, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected set/unset/blank filter entries to be ignored, got %#v", got)
	}

	got, err = parseGitCheckAttrFilterDrivers([]string{"a.json", "package.json"}, []byte("a.json\x00filter\x00\x00package.json\x00filter\x00pwn=drv\x00"))
	if err != nil {
		t.Fatalf("expected empty filter value to preserve later triplets, got %v", err)
	}
	if len(got) != 1 || got[0] != "pwn=drv" {
		t.Fatalf("expected empty filter value to preserve later triplets, got %#v", got)
	}
}

func TestParseGitCheckAttrFilterDriversRejectsMalformedOutput(t *testing.T) {
	cases := []struct {
		name       string
		paths      []string
		output     []byte
		errContain string
	}{
		{
			name:       "truncated output",
			paths:      []string{"package.json"},
			output:     []byte("package.json\x00filter"),
			errContain: "truncated output",
		},
		{
			name:       "wrong field count",
			paths:      []string{"package.json"},
			output:     []byte("package.json\x00filter\x00foo.bar\x00extra\x00"),
			errContain: "expected 3 NUL-delimited fields",
		},
		{
			name:       "wrong attribute name",
			paths:      []string{"package.json"},
			output:     []byte("package.json\x00eol\x00lf\x00"),
			errContain: "attribute 0 mismatch",
		},
		{
			name:       "wrong path",
			paths:      []string{"package.json"},
			output:     []byte("package-lock.json\x00filter\x00foo.bar\x00"),
			errContain: "path 0 mismatch",
		},
		{
			name:       "wrong path order",
			paths:      []string{"package.json", "package-lock.json"},
			output:     []byte("package-lock.json\x00filter\x00foo.bar\x00package.json\x00filter\x00foo.baz\x00"),
			errContain: "path 0 mismatch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGitCheckAttrFilterDrivers(tc.paths, tc.output)
			if err == nil || !strings.Contains(err.Error(), tc.errContain) {
				t.Fatalf("expected error containing %q, got %v", tc.errContain, err)
			}
		})
	}
}

func configureFakeGitRepo(t *testing.T, mode string) string {
	t.Helper()

	original := resolveGitBinaryPathFn
	repo := t.TempDir()
	fakeGit := writeFakeGitBinary(t)
	resolveGitBinaryPathFn = func() (string, error) { return fakeGit, nil }
	t.Cleanup(func() { resolveGitBinaryPathFn = original })
	useFakeGitCommandContext(t)
	writeFakeGitMode(t, repo, mode)
	return repo
}

func writeFakeGitBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "git")
	script := `#!/bin/sh
args="$*"
mode="${FAKE_GIT_MODE}"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-C" ] && [ -n "$2" ] && [ -f "$2/.fake-git-mode" ]; then
    mode="$(cat "$2/.fake-git-mode")"
    break
  fi
  shift
done
if printf '%s' "$args" | grep -q 'rev-parse --is-inside-work-tree'; then
  echo true
  exit 0
fi
if printf '%s' "$args" | grep -q 'rev-parse --verify --quiet HEAD'; then
  if [ "$mode" = "difffail-cached" ] || [ "$mode" = "difffail-unstaged" ]; then
    exit 1
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'ls-files --others --exclude-standard'; then
  if [ "$mode" = "lsfail" ]; then
    echo "ls-files failed" >&2
    exit 1
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'ls-files --cached --others --exclude-standard -z'; then
  if [ "$mode" = "lsfilescachedfail" ]; then
    echo "ls-files cached failed" >&2
    exit 1
  fi
  if [ "$mode" = "checkattrfail" ]; then
    printf 'package.json\000'
    exit 0
  fi
  if [ "$mode" = "checkattrwrongorder" ]; then
    printf 'package.json\000package-lock.json\000'
    exit 0
  fi
  if [ "$mode" = "checkattrtruncated" ] || [ "$mode" = "checkattrwrongfieldcount" ] || [ "$mode" = "checkattrwrongattr" ]; then
    printf 'package.json\000'
    exit 0
  fi
  if [ "$mode" = "filterdriver" ]; then
    printf 'package.json\000'
    exit 0
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'check-attr --stdin -z filter'; then
  if [ "$mode" = "checkattrfail" ]; then
    echo "check-attr failed" >&2
    exit 1
  fi
  if [ "$mode" = "checkattrtruncated" ]; then
    cat >/dev/null
    printf 'package.json\000filter\000foo.bar'
    exit 0
  fi
  if [ "$mode" = "checkattrwrongfieldcount" ]; then
    cat >/dev/null
    printf 'package.json\000filter\000foo.bar\000extra\000'
    exit 0
  fi
  if [ "$mode" = "checkattrwrongattr" ]; then
    cat >/dev/null
    printf 'package.json\000eol\000lf\000'
    exit 0
  fi
  if [ "$mode" = "checkattrwrongorder" ]; then
    cat >/dev/null
    printf 'package-lock.json\000filter\000foo.bar\000package.json\000filter\000foo.baz\000'
    exit 0
  fi
  if [ "$mode" = "filterdriver" ]; then
    cat >/dev/null
    printf 'package.json\000filter\000foo.bar\000'
    exit 0
  fi
  exit 0
fi
if printf '%s' "$args" | grep -q 'diff --no-ext-diff --no-textconv'; then
  if [ "$mode" = "filterdriver" ]; then
    if printf '%s' "$args" | grep -q -- '--attr-source='; then
      echo "unexpected attr-source flag" >&2
      exit 1
    fi
    for expected in \
      'GIT_CONFIG_KEY_5=filter.foo.bar.clean' \
      'GIT_CONFIG_KEY_6=filter.foo.bar.process' \
      'GIT_CONFIG_KEY_7=filter.foo.bar.required' \
      'GIT_CONFIG_VALUE_7=false'; do
      if ! env | grep -q "$expected"; then
        echo "missing filter override env: $expected" >&2
        exit 1
      fi
    done
    exit 0
  fi
  if [ "$mode" = "checkattrtruncated" ] || [ "$mode" = "checkattrwrongfieldcount" ] || [ "$mode" = "checkattrwrongattr" ] || [ "$mode" = "checkattrwrongorder" ]; then
    echo "git diff should not run after malformed check-attr output" >&2
    exit 1
  fi
  if printf '%s' "$args" | grep -q -- '--cached'; then
    if [ "$mode" = "difffail-cached" ]; then
      echo "diff failed" >&2
      exit 1
    fi
    exit 0
  fi
  if [ "$mode" = "difffail-head" ] || [ "$mode" = "difffail-unstaged" ]; then
    echo "diff failed" >&2
    exit 1
  fi
  exit 0
fi
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git script: %v", err)
	}
	return path
}

func writeFakeGitMode(t *testing.T, repo, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, ".fake-git-mode"), []byte(mode), 0o600); err != nil {
		t.Fatalf("write fake git mode: %v", err)
	}
}

func useFakeGitCommandContext(t *testing.T) {
	t.Helper()

	original := execGitCommandContextFn
	execGitCommandContextFn = func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, gitPath, args...), nil
	}
	t.Cleanup(func() {
		execGitCommandContextFn = original
	})
}

func testNilContext() context.Context {
	return nil
}
