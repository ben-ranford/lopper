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

func TestManagedHookRejectsExternalToolSymlinksIntoCheckout(t *testing.T) {
	t.Parallel()

	for _, tool := range []string{"git", "mktemp", "xargs", "sh", "awk", "gofmt", "cat", "rm"} {
		t.Run(tool, func(t *testing.T) { assertManagedHookRejectsCheckoutToolSymlink(t, tool) })
	}
}

func assertManagedHookRejectsCheckoutToolSymlink(t *testing.T, tool string) {
	t.Helper()
	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "unformatted.go"), "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "unformatted.go")
	marker := filepath.Join(repoDir, "checkout-tool-ran")
	target := filepath.Join(repoDir, "checkout-tool")
	writeFileMode(t, target, "#!/bin/sh\ntouch "+marker+"\nexit 0\n", 0o755)
	toolDir := t.TempDir()
	if err := os.Symlink(target, filepath.Join(toolDir, tool)); err != nil {
		t.Fatalf("symlink %s into checkout: %v", tool, err)
	}
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Refusing checkout-controlled hook tool: "+tool) {
		t.Fatalf("hook with checkout-targeted %s symlink = %v\n%s", tool, err, output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("checkout-targeted %s executed: %v", tool, err)
	}
}

func TestManagedHookDisablesPagersForEveryGitQuery(t *testing.T) {
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
		t.Fatalf("managed hook = %v\n%s", err, output)
	}
	for _, query := range []string{"diff --cached --check", "diff --cached --quiet", "diff --cached --name-only", "ls-files --stage", "show :0:"} {
		args, ok := recordedGitQuery(t, recordPath, query)
		if !ok {
			t.Fatalf("did not record expected git query %q", query)
		}
		if !containsArgument(args, "--no-pager") {
			t.Fatalf("git query %q omitted separate --no-pager argument: %#v", query, args)
		}
	}
}

func recordedGitQuery(t *testing.T, path, want string) ([]string, bool) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Git argument log: %v", err)
	}
	var args []string
	for _, line := range strings.Split(strings.TrimSuffix(string(contents), "\n"), "\n") {
		if line == "END" {
			if strings.Contains(strings.Join(args, " "), want) {
				return args, true
			}
			args = nil
			continue
		}
		args = append(args, strings.TrimPrefix(line, "ARG:"))
	}
	return nil, false
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestManagedHookCleansFirstTemporaryFileAfterSignal(t *testing.T) {
	t.Parallel()

	repoDir := newHookTestRepository(t)
	runCommand(t, repoDir, "make", "hooks-install")
	writeFile(t, filepath.Join(repoDir, "unformatted.go"), "package fixture\n\nfunc unformatted(){}\n")
	runCommand(t, repoDir, "git", "add", "unformatted.go")
	mktempPath, err := exec.LookPath("mktemp")
	if err != nil {
		t.Fatalf("find mktemp: %v", err)
	}
	wrapperDir := t.TempDir()
	writeFileMode(t, filepath.Join(wrapperDir, "mktemp"), fmt.Sprintf("#!/bin/sh\n%q \"$@\"\nstatus=$?\nkill -TERM \"$PPID\"\nexit \"$status\"\n", mktempPath), 0o755)
	tempDir := t.TempDir()
	command := exec.Command(managedHookPath(t, repoDir))
	command.Dir = repoDir
	command.Env = append(hookTestEnv(), "PATH="+wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+tempDir)
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("hook interrupted during first allocation succeeded:\n%s", output)
	}
	if leftovers, err := filepath.Glob(filepath.Join(tempDir, "lopper-pre-commit.*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files after first allocation signal = %#v err=%v", leftovers, err)
	}
}
