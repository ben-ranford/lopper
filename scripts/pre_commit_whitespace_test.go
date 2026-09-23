package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestInstalledPreCommitStagedAttributePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, staged, unstaged, nested, info, global, whitespace string
		reject                                                   bool
	}{
		{name: "unstaged allowance", unstaged: "*.txt whitespace=-trailing-space\n", reject: true},
		{name: "staged rejection", staged: "*.txt whitespace=trailing-space\n", unstaged: "*.txt whitespace=-trailing-space\n", reject: true},
		{name: "staged allowance", staged: "*.txt whitespace=-trailing-space\n", unstaged: "*.txt whitespace=trailing-space\n"},
		{name: "nested allowance", staged: "*.txt whitespace=trailing-space\n", nested: "*.txt whitespace=-trailing-space\n"},
		{name: "nested rejection", staged: "*.txt whitespace=-trailing-space\n", nested: "*.txt whitespace=trailing-space\n", reject: true},
		{name: "info precedence", staged: "*.txt whitespace=trailing-space\n", info: "*.txt whitespace=-trailing-space\n"},
		{name: "global fallback", global: "*.txt whitespace=-trailing-space\n"},
		{name: "repository above global", staged: "*.txt whitespace=trailing-space\n", global: "*.txt whitespace=-trailing-space\n", reject: true},
		{name: "configured allowance", whitespace: "-trailing-space"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newHookFixture(t)
			if tc.staged != "" {
				writeFile(t, filepath.Join(repo, ".gitattributes"), tc.staged)
				runCommand(t, repo, "git", "add", ".gitattributes")
			}
			if tc.nested != "" {
				writeFile(t, filepath.Join(repo, "nested", ".gitattributes"), tc.nested)
				runCommand(t, repo, "git", "add", "nested/.gitattributes")
			}
			if tc.unstaged != "" {
				writeFile(t, filepath.Join(repo, ".gitattributes"), tc.unstaged)
			}
			if tc.info != "" {
				writeFile(t, filepath.Join(repo, ".git", "info", "attributes"), tc.info)
			}
			if tc.global != "" {
				path := filepath.Join(t.TempDir(), "attributes")
				writeFile(t, path, tc.global)
				runCommand(t, repo, "git", "config", "core.attributesFile", path)
			}
			if tc.whitespace != "" {
				runCommand(t, repo, "git", "config", "core.whitespace", tc.whitespace)
			}
			writeFile(t, filepath.Join(repo, "nested", "notes.txt"), "allowed or rejected \n")
			runCommand(t, repo, "git", "add", "nested/notes.txt")
			output, err := hookCommand(repo, "git", "commit", "-m", "attribute policy")
			if tc.reject {
				if err == nil || !strings.Contains(output, "trailing whitespace") {
					t.Fatalf("expected whitespace rejection: %v\n%s", err, output)
				}
			} else if err != nil {
				t.Fatalf("expected allowed whitespace: %v\n%s", err, output)
			}
			if tc.unstaged != "" {
				content, err := os.ReadFile(filepath.Join(repo, ".gitattributes"))
				if err != nil || string(content) != tc.unstaged {
					t.Fatalf("working attributes modified: %v", err)
				}
			}
		})
	}
}

func TestInstalledPreCommitAlternateIndexAttributePolicy(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "linked"}[linked], func(t *testing.T) {
			repo := newHookFixture(t)
			if linked {
				linkedRepo := filepath.Join(t.TempDir(), "linked")
				runCommand(t, repo, "git", "worktree", "add", "-b", "linked", linkedRepo)
				repo = linkedRepo
			}
			index := filepath.Join(t.TempDir(), "index")
			env := []string{"GIT_INDEX_FILE=" + index}
			if output, err := hookCommandWithEnv(repo, env, "git", "read-tree", "HEAD"); err != nil {
				t.Fatalf("alternate index: %v\n%s", err, output)
			}
			writeFile(t, filepath.Join(repo, ".gitattributes"), "*.txt whitespace=-trailing-space\n")
			writeFile(t, filepath.Join(repo, "notes.txt"), "allowed \n")
			if output, err := hookCommandWithEnv(repo, env, "git", "add", ".gitattributes", "notes.txt"); err != nil {
				t.Fatalf("stage: %v\n%s", err, output)
			}
			writeFile(t, filepath.Join(repo, ".gitattributes"), "*.txt whitespace=trailing-space\n")
			before := testutil.GitOutput(t, repo, "write-tree")
			hookDir := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
			if output, err := hookCommandWithEnv(repo, env, filepath.Join(hookDir, "pre-commit")); err != nil {
				t.Fatalf("alternate staged attributes: %v\n%s", err, output)
			}
			if after := testutil.GitOutput(t, repo, "write-tree"); after != before {
				t.Fatal("normal index changed")
			}
		})
	}
}

func TestInstalledPreCommitRequiresAttributeSourceSupport(t *testing.T) {
	repo := newHookFixture(t)
	external := t.TempDir()
	writeFileMode(t, filepath.Join(external, "git"), "#!/bin/sh\ncase \"$*\" in *--attr-source=*) echo unsupported-option >&2; exit 129 ;; esac\nexec /usr/bin/git \"$@\"\n", 0o755)
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	output, err := hookCommandWithEnv(repo, []string{"PATH=" + external + ":" + os.Getenv("PATH")}, filepath.Join(hook, "pre-commit"))
	if err == nil || !strings.Contains(output, "Git 2.41 or newer") || strings.Contains(output, "running full make ci") {
		t.Fatalf("unsupported Git did not fail closed: %v\n%s", err, output)
	}
	assertHookWorktreeCleaned(t, repo)
}

func TestInstalledPreCommitIgnoresDeletedHeadAttributes(t *testing.T) {
	repo := newHookFixture(t)
	writeFile(t, filepath.Join(repo, ".gitattributes"), "*.txt whitespace=-trailing-space\n")
	runCommand(t, repo, "git", "add", ".gitattributes")
	runCommand(t, repo, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "HEAD attributes")
	runCommand(t, repo, "git", "rm", "--cached", ".gitattributes")
	writeFile(t, filepath.Join(repo, "notes.txt"), "rejected \n")
	runCommand(t, repo, "git", "add", "notes.txt")
	output, err := hookCommand(repo, "git", "commit", "-m", "deleted attributes")
	if err == nil || !strings.Contains(output, "trailing whitespace") {
		t.Fatalf("HEAD or checkout attributes leaked: %v\n%s", err, output)
	}
}

func TestInstalledPreCommitFreezesAttributeSnapshot(t *testing.T) {
	repo := newHookFixture(t)
	index := filepath.Join(t.TempDir(), "index")
	env := []string{"GIT_INDEX_FILE=" + index, "FIXTURE_INDEX=" + index}
	writeFile(t, filepath.Join(repo, ".gitattributes"), "*.txt whitespace=-trailing-space\n")
	writeFile(t, filepath.Join(repo, "notes.txt"), "allowed \n")
	writeFile(t, filepath.Join(repo, "Makefile"), "ci:\n\t@test -f notes.txt\n\t@git show HEAD:.gitattributes | grep -F -- '-trailing-space'\n")
	if output, err := hookCommandWithEnv(repo, env, "git", "add", "."); err != nil {
		t.Fatalf("stage snapshot: %v\n%s", err, output)
	}
	external := t.TempDir()
	writeFileMode(t, filepath.Join(external, "git"), "#!/bin/sh\ncase \"$*\" in *' read-tree '*) GIT_INDEX_FILE=\"$FIXTURE_INDEX\" /usr/bin/git read-tree HEAD || exit 1 ;; esac\nexec /usr/bin/git \"$@\"\n", 0o755)
	env = append(env, "PATH="+external+":"+os.Getenv("PATH"))
	hook := strings.TrimSpace(testutil.GitOutput(t, repo, "config", "--get", "core.hooksPath"))
	if output, err := hookCommandWithEnv(repo, env, filepath.Join(hook, "pre-commit")); err != nil {
		t.Fatalf("captured snapshot changed with caller index: %v\n%s", err, output)
	}
}
