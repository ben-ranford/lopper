//go:build !windows

package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedHookRejectsToolsFromSiblingWorktree(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	assertIndependentHookFixture(t, repoDir)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "formatted.go"), "package fixture\n\nfunc formatted() {}\n")
	runCommand(t, repoDir, "git", "add", "formatted.go")
	linkedDir := filepath.Join(t.TempDir(), "linked worktree\nwith spaces")
	runCommand(t, repoDir, "git", "worktree", "add", "-b", "linked-tool-test", linkedDir)
	writeFile(t, filepath.Join(linkedDir, "formatted.go"), "package fixture\n\nfunc formatted() {}\n")
	runCommand(t, linkedDir, "git", "add", "formatted.go")
	assertSharedFixtureCommonDir(t, repoDir, linkedDir)

	for _, testCase := range []struct {
		name, hookDir, toolDir, tool string
	}{
		{name: "main git from linked hook", hookDir: linkedDir, toolDir: repoDir, tool: "git"},
		{name: "linked git from main hook", hookDir: repoDir, toolDir: linkedDir, tool: "git"},
		{name: "main gofmt from linked hook", hookDir: linkedDir, toolDir: repoDir, tool: "gofmt"},
		{name: "linked gofmt from main hook", hookDir: repoDir, toolDir: linkedDir, tool: "gofmt"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "sibling-tool-ran")
			wrapperDir := installSiblingToolWrapper(t, testCase.toolDir, testCase.tool, marker)
			command := exec.Command(managedHookPath(t, testCase.hookDir))
			command.Dir = testCase.hookDir
			command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("managed hook = %v\n%s", err, output)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatalf("sibling checkout %s tool executed from %q", testCase.tool, testCase.toolDir)
			} else if !os.IsNotExist(err) {
				t.Fatalf("stat sibling tool marker: %v", err)
			}
		})
	}
}

func TestManagedHookFailsClosedWhenBootstrapWorktreeInventoryFails(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), "#!/bin/sh\necho bootstrap inventory failure >&2\nexit 73\n", 0o755)
	installBootstrapToolBinding(t, repoDir, "system_git", filepath.Join(wrapperDir, "git"))
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = hookTestEnv()
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "bootstrap inventory failure") {
		t.Fatalf("hook with failed bootstrap inventory = %v\n%s", err, output)
	}
}

func TestManagedHookFailsClosedOnEmptyBootstrapWorktreeInventory(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf("#!/bin/sh\nfor arg do [ \"$arg\" = worktree ] && exit 0; done\nexec %q \"$@\"\n", gitPath), 0o755)
	installBootstrapToolBinding(t, repoDir, "system_git", filepath.Join(wrapperDir, "git"))
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = hookTestEnv()
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("hook with empty bootstrap inventory succeeded:\n%s", output)
	}
}

func TestManagedHookFailsClosedWhenWorktreeMembershipCheckFails(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	xargsPath, err := exec.LookPath("xargs")
	if err != nil {
		t.Fatalf("find xargs: %v", err)
	}
	wrapperDir := t.TempDir()
	countPath := filepath.Join(wrapperDir, "calls")
	writeFileMode(t, filepath.Join(wrapperDir, "xargs"), fmt.Sprintf("#!/bin/sh\ncount=0; [ -f %q ] && count=$(cat %q)\ncount=$((count + 1)); printf '%%s' \"$count\" > %q\nif [ \"$count\" -gt 2 ]; then echo membership failure >&2; exit 73; fi\nexec %q \"$@\"\n", countPath, countPath, countPath, xargsPath), 0o755)
	installBootstrapToolBinding(t, repoDir, "system_xargs", filepath.Join(wrapperDir, "xargs"))
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = hookTestEnv()
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "membership failure") {
		t.Fatalf("hook with failed worktree membership check = %v\n%s", err, output)
	}
}

func TestManagedHookRejectsInventoryWithoutExactCurrentWorktree(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	ancestor := filepath.Dir(repoDir)
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf("#!/bin/sh\nfor arg do [ \"$arg\" = worktree ] && { printf 'worktree %%s\\000' %q; exit 0; }; done\nexec %q \"$@\"\n", ancestor, gitPath), 0o755)
	installBootstrapToolBinding(t, repoDir, "system_git", filepath.Join(wrapperDir, "git"))
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = hookTestEnv()
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("hook with ancestor-only inventory succeeded:\n%s", output)
	}
}

func TestManagedHookCleansInventoryTempAfterFirstAllocationSignal(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	mktempPath, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatalf("find mktemp: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "mktemp"), fmt.Sprintf("#!/bin/sh\n%q \"$@\"\nstatus=$?\nkill -TERM \"$PPID\"\nexit \"$status\"\n", mktempPath), 0o755)
	installBootstrapToolBinding(t, repoDir, "system_mktemp", filepath.Join(wrapperDir, "mktemp"))
	tmpDir := t.TempDir()
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "TMPDIR="+tmpDir)
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("hook interrupted after inventory allocation succeeded:\n%s", output)
	}
	leftovers, globErr := filepath.Glob(filepath.Join(tmpDir, "lopper-pre-commit-worktrees.*"))
	if globErr != nil || len(leftovers) != 0 {
		t.Fatalf("inventory temp files = %#v err=%v", leftovers, globErr)
	}
}

func TestManagedHookDisablesConfiguredFSMonitor(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "formatted.go"), "package fixture\n\nfunc formatted() {}\n")
	runCommand(t, repoDir, "git", "add", "formatted.go")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	wrapperDir := t.TempDir()
	recordPath := filepath.Join(t.TempDir(), "git-arguments")
	writeFileMode(t, filepath.Join(wrapperDir, "git"), fmt.Sprintf("#!/bin/sh\nfor arg do printf 'ARG:%%s\\n' \"$arg\"; done >> %q\nprintf 'END\\n' >> %q\nexec %q \"$@\"\n", recordPath, recordPath, gitPath), 0o755)
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("hook with recorded Git commands = %v\n%s", err, output)
	}
	for _, query := range []string{"diff --cached --check", "diff --cached --quiet", "diff --cached --name-only", "ls-files --stage", "show :0:"} {
		args, ok := recordedGitQuery(t, recordPath, query)
		if !ok || !containsArgument(args, "core.fsmonitor=false") {
			t.Fatalf("Git query %q omitted core.fsmonitor=false: %#v", query, args)
		}
	}
}

func TestManagedHookFailsWhenGofmtReturnsStatusOneWithoutOutput(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "formatted.go"), "package fixture\n\nfunc formatted() {}\n")
	runCommand(t, repoDir, "git", "add", "formatted.go")
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "gofmt"), "#!/bin/sh\nexit 1\n", 0o755)
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("hook with status-one empty gofmt succeeded:\n%s", output)
	}
}

func TestManagedHookRejectsExternalSymlinkToSiblingWorktreeTool(t *testing.T) {
	t.Parallel()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	linkedDir := filepath.Join(t.TempDir(), "linked trailing newline\n")
	runCommand(t, repoDir, "git", "worktree", "add", "-b", "linked-symlink-tool-test", linkedDir)
	assertSharedFixtureCommonDir(t, repoDir, linkedDir)
	marker := filepath.Join(t.TempDir(), "sibling-tool-ran")
	siblingDir := installSiblingToolWrapper(t, linkedDir, "git", marker)
	externalDir := t.TempDir()
	if err := os.Symlink(filepath.Join(siblingDir, "git"), filepath.Join(externalDir, "git")); err != nil {
		t.Fatalf("link external git wrapper: %v", err)
	}
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+externalDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := command.CombinedOutput(); err == nil || !strings.Contains(string(output), "Refusing checkout-controlled hook tool: git") {
		t.Fatalf("hook with external symlink into sibling checkout = %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("sibling tool executed: %v", err)
	}
}

func installBootstrapToolBinding(t *testing.T, repoDir, binding, path string) {
	t.Helper()
	hookPath := managedHookPath(t, repoDir)
	hook := readRepositoryHook(t)
	anchor := binding + "=$(command -v " + strings.TrimPrefix(binding, "system_") + ") || exit 1"
	if strings.Count(hook, anchor) != 1 {
		t.Fatalf("bootstrap binding anchor %q count = %d", anchor, strings.Count(hook, anchor))
	}
	hook = strings.Replace(hook, anchor, binding+"="+path, 1)
	writeFileMode(t, hookPath, hook, 0o755)
}

func assertSharedFixtureCommonDir(t *testing.T, repoDir, linkedDir string) {
	t.Helper()
	commonDir := gitOutput(t, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	linkedCommonDir := gitOutput(t, linkedDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	want, err := filepath.EvalSymlinks(filepath.Join(repoDir, ".git"))
	if err != nil {
		t.Fatalf("resolve fixture common directory: %v", err)
	}
	if commonDir != linkedCommonDir || !filepath.IsAbs(commonDir) || commonDir != want {
		t.Fatalf("fixture common directories = %q and %q, want fixture-owned common dir", commonDir, linkedCommonDir)
	}
}

func installSiblingToolWrapper(t *testing.T, repoDir, tool, marker string) string {
	t.Helper()
	toolPath, err := exec.LookPath(tool)
	if err != nil {
		t.Fatalf("find %s: %v", tool, err)
	}
	wrapperDir := filepath.Join(repoDir, ".sibling-tool-"+tool)
	writeFileMode(t, filepath.Join(wrapperDir, tool), fmt.Sprintf("#!/bin/sh\ntouch %q\nexec %q \"$@\"\n", marker, toolPath), 0o755)
	return wrapperDir
}
