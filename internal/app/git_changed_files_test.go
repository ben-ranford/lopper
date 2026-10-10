package app

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestGitChangedFilesRoundTripUnusualUntrackedNames(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "baseline"), "baseline\n")
	initGitRepo(t, repo)
	names := []string{"new\nline", "tab\tname", "-leading", " space name ", "日本語", ":(glob)*"}
	for _, name := range names {
		writeFile(t, filepath.Join(repo, name), "new\n")
	}
	changed, hasGit, err := gitChangedFilesForCodemod(context.Background(), repo)
	if err != nil || !hasGit {
		t.Fatalf("collect all paths: hasGit=%v err=%v", hasGit, err)
	}
	assertExactGitChanges(t, changed, names)
	selected, err := gitChangedFilesForPaths(context.Background(), repo, names)
	if err != nil {
		t.Fatalf("collect selected paths: %v", err)
	}
	assertExactGitChanges(t, selected, names)
}

func TestGitChangedFilesPreserveHeadAndSelectionPolicies(t *testing.T) {
	for _, hasHead := range []bool{false, true} {
		name := "unborn"
		if hasHead {
			name = "normal HEAD"
		}
		t.Run(name, func(t *testing.T) {
			repo, tracked := gitChangedFilesHeadPolicyFixture(t, hasHead)
			assertGitChangedFilesAllHeadPolicy(t, repo, tracked, hasHead)
			assertGitChangedFilesSelectionAndDirtyPolicies(t, repo, tracked)
		})
	}
}

func gitChangedFilesHeadPolicyFixture(t *testing.T, hasHead bool) (string, []string) {
	t.Helper()
	repo := t.TempDir()
	tracked := []string{"tracked\nname", "tracked\tname", "-tracked", "tracked space", "é-file", ":(glob)tracked*"}
	writeFile(t, filepath.Join(repo, ".gitignore"), "ignored\n")
	for _, path := range tracked {
		writeFile(t, filepath.Join(repo, path), "initial\n")
	}
	if hasHead {
		initGitRepo(t, repo)
	} else {
		runGit(t, repo, "init")
		runGit(t, repo, "add", "--", ".")
	}
	for _, path := range tracked {
		writeFile(t, filepath.Join(repo, path), "modified\n")
	}
	runGit(t, repo, "add", "--", tracked[0])
	writeFile(t, filepath.Join(repo, "untracked"), "new\n")
	writeFile(t, filepath.Join(repo, "ignored"), "ignored\n")
	return repo, tracked
}

func assertGitChangedFilesAllHeadPolicy(t *testing.T, repo string, tracked []string, hasHead bool) {
	t.Helper()
	all, hasGit, err := gitChangedFilesForCodemod(context.Background(), repo)
	if err != nil || !hasGit {
		t.Fatalf("collect all paths: hasGit=%v err=%v", hasGit, err)
	}
	want := append(append([]string{}, tracked...), "untracked")
	if !hasHead {
		want = append(want, ".gitignore")
	}
	assertExactGitChanges(t, all, want)
}

func assertGitChangedFilesSelectionAndDirtyPolicies(t *testing.T, repo string, tracked []string) {
	t.Helper()
	paths := []string{tracked[0], tracked[0], tracked[5], "untracked", "ignored", "missing"}
	selected, err := gitChangedFilesForPaths(context.Background(), repo, paths)
	if err != nil {
		t.Fatalf("collect selected paths: %v", err)
	}
	assertExactGitChanges(t, selected, []string{tracked[0], tracked[5], "untracked"})
	empty, err := gitChangedFilesForPaths(context.Background(), repo, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty selected scope widened: %#v, %v", empty, err)
	}
	if err := ensureCleanWorktreeForCodemod(context.Background(), repo, false); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected dirty worktree error, got %v", err)
	}
}

func assertExactGitChanges(t *testing.T, got map[string]struct{}, want []string) {
	t.Helper()
	paths := make([]string, 0, len(got))
	for path := range got {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	expected := append([]string{}, want...)
	sort.Strings(expected)
	if !reflect.DeepEqual(paths, expected) {
		t.Fatalf("changed paths = %q, want %q", paths, expected)
	}
}

func TestGitChangedFileCommandsUseNULOutput(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "tracked"), "initial\n")
	initGitRepo(t, repo)
	writeFile(t, filepath.Join(repo, "tracked"), "changed\n")
	writeFile(t, filepath.Join(repo, "untracked"), "new\n")
	original := execGitCommandContextFn
	commands := 0
	execGitCommandContextFn = func(ctx context.Context, gitPath string, args ...string) (*exec.Cmd, error) {
		group := lockfileGitCommandGroup(args)
		if group == "diff" || group == "ls-files" {
			commands++
			if !containsString(args, "-z") {
				t.Errorf("filename-bearing command lacks NUL output: %q", args)
			}
		}
		return original(ctx, gitPath, args...)
	}
	t.Cleanup(func() { execGitCommandContextFn = original })
	if _, _, err := gitChangedFilesForCodemod(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if _, err := gitChangedFilesForPaths(context.Background(), repo, []string{"tracked", "untracked"}); err != nil {
		t.Fatal(err)
	}
	if commands != 5 {
		t.Fatalf("expected all and selected filename commands, got %d", commands)
	}
}

func TestGitChangedFileCollectorsPreserveCancellation(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "tracked"), "initial\n")
	initGitRepo(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	all, _, err := gitChangedFilesForCodemod(ctx, repo)
	if !errors.Is(err, context.Canceled) || all != nil {
		t.Fatalf("all-path cancellation = %#v, %v", all, err)
	}
	selected, err := gitChangedFilesForPaths(ctx, repo, []string{"tracked"})
	if !errors.Is(err, context.Canceled) || selected != nil {
		t.Fatalf("selected-path cancellation = %#v, %v", selected, err)
	}
}

func TestGitChangedFileCollectorsReportCleanWorktrees(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "tracked"), "initial\n")
	initGitRepo(t, repo)
	all, hasGit, err := gitChangedFilesForCodemod(context.Background(), repo)
	if err != nil || !hasGit || len(all) != 0 {
		t.Fatalf("clean all-path collection = %#v, %v, %v", all, hasGit, err)
	}
	selected, err := gitChangedFilesForPaths(context.Background(), repo, []string{"tracked"})
	if err != nil || len(selected) != 0 {
		t.Fatalf("clean selected-path collection = %#v, %v", selected, err)
	}
	if err := ensureCleanWorktreeForCodemod(context.Background(), repo, false); err != nil {
		t.Fatalf("clean worktree refused: %v", err)
	}
}
