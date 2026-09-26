package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

// Real commits exercise Git's absolute linked-worktree index and the temporary
// index used for partial commits while CI stages objects in another repository.
func TestInstalledPreCommitIsolatesNestedRepositoryIndex(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			name := "normal/full"
			if linked {
				name = "linked/full"
			}
			if partial {
				name = strings.TrimSuffix(name, "full") + "partial"
			}
			t.Run(name, func(t *testing.T) { checkNestedRepositoryIndexIsolation(t, linked, partial) })
		}
	}
}

func checkNestedRepositoryIndexIsolation(t *testing.T, linked, partial bool) {
	t.Helper()
	repo := newHookFixture(t)
	child := filepath.Join(t.TempDir(), "independent")
	runCommand(t, filepath.Dir(child), "git", "init", child)
	writeFile(t, filepath.Join(child, "child.txt"), "object only in the independent repository\n")
	wantCommitted := "package sample\n\nfunc Value() int { return 2 }\n"
	if partial {
		wantCommitted = "package sample\n\nfunc Value() int { return 3 }\n"
	}
	expected := filepath.Join(t.TempDir(), "expected.go")
	writeFile(t, expected, wantCommitted)
	// Run a real nested add, not just an environment assertion: leaked absolute
	// indexes otherwise allow objects from this repository to corrupt the caller.
	ci := "ci:\n\t@git -C " + shellQuote(child) + " add child.txt\n\t@cmp sample.go " + shellQuote(expected) + "\n\t@git show HEAD:sample.go | cmp - " + shellQuote(expected) + "\n"
	writeFile(t, filepath.Join(repo, "Makefile"), ci)
	runCommand(t, repo, "git", "add", "Makefile")
	runCommand(t, repo, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "nested CI fixture")
	caller := repo
	if linked {
		caller = filepath.Join(t.TempDir(), "linked")
		runCommand(t, repo, "git", "worktree", "add", "-b", "linked-index", caller)
	}
	writeFile(t, filepath.Join(caller, "pending.txt"), "keep staged during a partial commit\n")
	writeFile(t, filepath.Join(caller, "sample.go"), "package sample\n\nfunc Value() int { return 2 }\n")
	runCommand(t, caller, "git", "add", "pending.txt", "sample.go")
	// --only commits the working-tree version, while a full commit uses the index.
	writeFile(t, filepath.Join(caller, "sample.go"), "package sample\n\nfunc Value() int { return 3 }\n")
	writeFile(t, filepath.Join(caller, "pending.txt"), "unstaged pending content\n")
	args := []string{"commit", "-m", "nested repository isolation"}
	if partial {
		args = append(args, "--only", "sample.go")
	}
	if output, err := hookCommand(caller, "git", args...); err != nil {
		t.Fatalf("commit failed: %v\n%s", err, output)
	}
	if got := testutil.GitOutput(t, child, "ls-files"); got != "child.txt" {
		t.Fatalf("nested add missed child index: %q", got)
	}
	if got := testutil.GitOutput(t, child, "show", ":child.txt"); got != "object only in the independent repository" {
		t.Fatalf("nested add lost child contents: %q", got)
	}
	if got := testutil.GitOutput(t, caller, "ls-files"); strings.Contains(got, "child.txt") {
		t.Fatalf("child entry leaked into caller index: %q", got)
	}
	if got := testutil.GitOutput(t, caller, "show", "HEAD:sample.go"); got != strings.TrimSpace(wantCommitted) {
		t.Fatalf("wrong committed content: %q", got)
	}
	wantStaged := ""
	if partial {
		wantStaged = "pending.txt"
		if got := testutil.GitOutput(t, caller, "show", ":pending.txt"); got != "keep staged during a partial commit" {
			t.Fatalf("remaining staged content was changed: %q", got)
		}
	}
	if got := testutil.GitOutput(t, caller, "diff", "--cached", "--name-only"); got != wantStaged {
		t.Fatalf("remaining staged files=%q, want %q", got, wantStaged)
	}
	working, err := os.ReadFile(filepath.Join(caller, "sample.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(working) != "package sample\n\nfunc Value() int { return 3 }\n" {
		t.Fatalf("working content was changed: %q", working)
	}
	runCommand(t, caller, "git", "write-tree")
	runCommand(t, caller, "git", "fsck", "--no-reflogs")
	if linked {
		runCommand(t, repo, "git", "worktree", "remove", "--force", caller)
	}
	assertHookWorktreeCleaned(t, repo)
}
