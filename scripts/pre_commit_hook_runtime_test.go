package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedHookChecksTrackedSymlinkReplacedByUnformattedGoFile(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkPath := filepath.Join(repoDir, "replaced.go")
	if err := os.Symlink("tracked.txt", linkPath); err != nil {
		t.Fatalf("create tracked Go symlink: %v", err)
	}
	runCommand(t, repoDir, "git", "add", "replaced.go")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "add Go symlink")
	runCommand(t, repoDir, "make", "hooks-install")

	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("remove Go symlink: %v", err)
	}
	writeFile(t, linkPath, "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "replaced.go")
	assertCommitFails(t, repoDir, "staged Go files must be gofmt-formatted")
}

func TestManagedHookSkipsNonRegularStagedGoEntries(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name  string
		stage func(t *testing.T, repoDir string)
	}{
		{
			name: "symlink",
			stage: func(t *testing.T, repoDir string) {
				goPath := filepath.Join(repoDir, "replacement.go")
				writeFile(t, goPath, "package fixture\n")
				runCommand(t, repoDir, "git", "add", "replacement.go")
				runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "add regular Go file")
				if err := os.Remove(goPath); err != nil {
					t.Fatalf("remove regular Go file: %v", err)
				}
				if err := os.Symlink("tracked.txt", goPath); err != nil {
					t.Fatalf("replace Go file with symlink: %v", err)
				}
				runCommand(t, repoDir, "git", "add", "replacement.go")
			},
		},
		{
			name: "gitlink",
			stage: func(t *testing.T, repoDir string) {
				treeID := gitOutput(t, repoDir, "rev-parse", "HEAD^{tree}")
				runCommand(t, repoDir, "git", "update-index", "--add", "--cacheinfo", "160000,"+treeID+",gitlink.go")
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "make", "hooks-install")
			testCase.stage(t, repoDir)
			runCommitWithHook(t, repoDir, "allow non-regular Go entry")
		})
	}
}

func TestManagedHookFailsClosedWhenStagedEntryQueryFails(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "unformatted.go"), "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "unformatted.go")

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$arg" = "ls-files" ]; then
		echo "forced staged-entry query failure" >&2
		exit 73
	fi
done
exec %q "$@"
`, gitPath), 0o755)

	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "forced staged-entry query failure") {
		t.Fatalf("hook with failed staged-entry query = %v\n%s", err, output)
	}
	if strings.Contains(string(output), "staged Go files must be gofmt-formatted") {
		t.Fatalf("hook treated the failed staged-entry query as a formatting result:\n%s", output)
	}
}

func TestManagedHookFailsClosedWhenGitQueriesFail(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		failure string
		want    string
	}{
		{name: "quiet", failure: "quiet", want: "forced quiet query failure"},
		{name: "names", failure: "names", want: "forced name query failure"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repoDir := newHookTestRepository(t)
			runCommand(t, repoDir, "make", "hooks-install")
			writeFile(t, filepath.Join(repoDir, "unformatted.go"), "package fixture\n\nfunc unformatted(){}\n")
			runCommand(t, repoDir, "git", "add", "unformatted.go")

			gitPath, err := exec.LookPath("git")
			if err != nil {
				t.Fatalf("find git: %v", err)
			}
			wrapperDir := t.TempDir()
			writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$HOOK_TEST_GIT_FAILURE" = quiet ] && [ "$arg" = "--quiet" ]; then
		echo "forced quiet query failure" >&2
		exit 73
	fi
	if [ "$HOOK_TEST_GIT_FAILURE" = names ] && [ "$arg" = "--name-only" ]; then
		echo "forced name query failure" >&2
		exit 74
	fi
done
exec %q "$@"
`, gitPath), 0o755)

			command := exec.Command(managedHookPath(t, repoDir))
			command.Dir = repoDir
			command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"), "HOOK_TEST_GIT_FAILURE="+testCase.failure)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), testCase.want) {
				t.Fatalf("hook with failed %s query = %v\n%s", testCase.name, err, output)
			}
			if strings.Contains(string(output), "staged Go files must be gofmt-formatted") {
				t.Fatalf("hook treated the failed %s query as a formatting result:\n%s", testCase.name, output)
			}
		})
	}
}

func TestManagedHookSkipsGofmtWhenNoGoFilesAreStaged(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "only.txt"), "staged\n")
	runCommand(t, repoDir, "git", "add", "only.txt")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf(`#!/bin/sh
for arg do
	if [ "$arg" = "ls-files" ]; then
		echo "unexpected staged-entry query" >&2
		exit 75
	fi
done
exec %q "$@"
`, gitPath), 0o755)
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook without staged Go files = %v\n%s", err, output)
	}
}

func TestManagedHookUsesExplicitAlternateIndex(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "alternate.go"), "package fixture\n\nfunc alternate() {}\n")
	runCommand(t, repoDir, "git", "add", "alternate.go")
	runCommand(t, repoDir, "git", "-c", "core.hooksPath=/dev/null", "commit", "-m", "formatted main")
	writeFile(t, filepath.Join(repoDir, "alternate.go"), "package fixture\n\nfunc alternate(){}\n")
	indexPath := filepath.Join(t.TempDir(), "alternate.index")
	runCommandWithEnv(t, repoDir, []string{"GIT_INDEX_FILE=" + indexPath}, "git", "read-tree", "HEAD")
	runCommandWithEnv(t, repoDir, []string{"GIT_INDEX_FILE=" + indexPath}, "git", "add", "alternate.go")
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "GIT_INDEX_FILE="+indexPath)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "staged Go files must be gofmt-formatted") {
		t.Fatalf("hook with alternate unformatted index = %v\n%s", err, output)
	}
	command = exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = hookTestEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook with formatted main index = %v\n%s", err, output)
	}

	runCommand(t, repoDir, "git", "add", "alternate.go")
	writeFile(t, filepath.Join(repoDir, "alternate.go"), "package fixture\n\nfunc alternate() {}\n")
	formattedAlternateIndex := filepath.Join(t.TempDir(), "formatted-alternate.index")
	runCommandWithEnv(t, repoDir, []string{"GIT_INDEX_FILE=" + formattedAlternateIndex}, "git", "read-tree", "HEAD")
	runCommandWithEnv(t, repoDir, []string{"GIT_INDEX_FILE=" + formattedAlternateIndex}, "git", "add", "alternate.go")
	writeFile(t, filepath.Join(repoDir, "alternate.go"), "package fixture\n\nfunc alternate(){}\n")
	command = exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "GIT_INDEX_FILE="+formattedAlternateIndex)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook with formatted alternate index = %v\n%s", err, output)
	}
	command = exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = hookTestEnv()
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "staged Go files must be gofmt-formatted") {
		t.Fatalf("hook with unformatted main index = %v\n%s", err, output)
	}
}

func TestManagedHookWorksWhenLinkedWorktreeSetsCoreBare(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	linkedDir := filepath.Join(t.TempDir(), "linked")
	runCommand(t, repoDir, "git", "config", "extensions.worktreeConfig", "true")
	runCommand(t, repoDir, "git", "worktree", "add", linkedDir)
	runCommand(t, linkedDir, "make", "hooks-install")
	writeFile(t, filepath.Join(linkedDir, "linked.go"), "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, linkedDir, "git", "add", "linked.go")
	runCommand(t, linkedDir, "git", "config", "--worktree", "core.bare", "true")

	command := exec.Command(managedHookPath(t, linkedDir))
	command.Dir = linkedDir
	command.Env = hookTestEnv()
	outputBytes, err := command.CombinedOutput()
	output := string(outputBytes)
	if err == nil || !strings.Contains(output, "staged Go files must be gofmt-formatted") {
		t.Fatalf("managed hook with core.bare=true = %v\n%s", err, output)
	}
	if strings.Contains(output, "this operation must be run in a work tree") {
		t.Fatalf("managed hook inherited core.bare=true:\n%s", output)
	}
	runCommand(t, linkedDir, "make", "hooks-uninstall")
	if _, err := os.Stat(managedHookPath(t, linkedDir)); !os.IsNotExist(err) {
		t.Fatalf("uninstall with core.bare=true left managed hook: %v", err)
	}
}

func TestManagedHookDoesNotRunConfiguredDiffHelpers(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	markerPath := filepath.Join(repoDir, "diff-helper-ran")
	helperPath := filepath.Join(repoDir, "hostile-diff-helper")
	writeFileMode(t, helperPath, "#!/bin/sh\ntouch "+markerPath+"\nexit 0\n", 0o755)
	runCommand(t, repoDir, "git", "config", "diff.external", helperPath)
	runCommand(t, repoDir, "git", "config", "diff.hostile.textconv", helperPath)
	writeFile(t, filepath.Join(repoDir, ".gitattributes"), "*.go diff=hostile\n")
	writeFile(t, filepath.Join(repoDir, "hostile.go"), "package fixture\n\nfunc hostile() {}\n")
	runCommand(t, repoDir, "git", "add", ".gitattributes", "hostile.go")
	runCommitWithHook(t, repoDir, "stage hostile diff fixture")
	writeFile(t, filepath.Join(repoDir, "hostile.go"), "package fixture\n\nfunc hostileChanged() {}\n")
	runCommand(t, repoDir, "git", "add", "hostile.go")
	runCommitWithHook(t, repoDir, "commit without diff helper")
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("configured diff helper ran during commit: %v", err)
	}
}
