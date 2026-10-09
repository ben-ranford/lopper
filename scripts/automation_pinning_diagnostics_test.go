package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubActionsPinningRejectionReportsEmptyChildOutput(t *testing.T) {
	t.Parallel()

	// Exercise the real rejection assertion in a child test process. Only Ruby
	// is replaced, making the checker fail silently before its policy diagnostic.
	binDir := t.TempDir()
	for _, name := range []string{"sh", "dirname"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("resolve fixture tool %s: %v", name, err)
		}
		if err := os.Symlink(path, filepath.Join(binDir, name)); err != nil {
			t.Fatalf("link fixture tool %s: %v", name, err)
		}
	}
	writeFixtureFileMode(t, binDir, "ruby", "#!/bin/sh\nexit 17\n", 0o755)
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	cmd := exec.Command(executable, "-test.run=^TestGitHubActionsPinningRejectsMutableActionRef$", "-test.count=1")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "PATH=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+binDir)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected deliberate silent checker failure, got success:\n%s", output)
	}
	assertOutputContainsAll(t, string(output), []string{
		`output missing "GitHub Actions pinning check failed"`,
		"child error=\"exit status 17\"",
		"command=", "check-github-actions-pinning.sh", "dir=", "status=\"exit status 17\"",
	})
}

func TestRunAutomationExamplesFixtureReportsLaunchContext(t *testing.T) {
	t.Parallel()

	for _, setup := range []string{"missing executable", "missing working directory"} {
		t.Run(setup, func(t *testing.T) {
			t.Parallel()

			var command *exec.Cmd
			output, err := runAutomationExamplesFixtureWithCommand(t, "pre-commit: {}\n", func(scriptPath string) *exec.Cmd {
				command = exec.Command(scriptPath + ".missing")
				if setup == "missing working directory" {
					if err := os.RemoveAll(filepath.Dir(filepath.Dir(scriptPath))); err != nil {
						t.Fatalf("remove owned fixture directory: %v", err)
					}
					command = exec.Command("sh", scriptPath)
				}
				return command
			})
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("expected original missing-path error, got %v", err)
			}
			assertOutputContainsAll(t, output, []string{
				err.Error(), "command=", command.Path, "dir=", command.Dir, "status=\"not started\"",
			})
		})
	}
}

func TestRunAutomationExamplesFixtureBoundsLaunchContext(t *testing.T) {
	t.Parallel()

	output, err := runAutomationExamplesFixtureWithCommand(t, "pre-commit: {}\n", func(scriptPath string) *exec.Cmd {
		return exec.Command(scriptPath+".missing", strings.Repeat("x", 4096)+"argument-tail")
	})
	if err == nil {
		t.Fatal("expected missing fixture executable to fail")
	}
	if !strings.Contains(output, "[truncated]") || strings.Contains(output, "argument-tail") || len(output) > 4096 {
		t.Fatalf("expected bounded launch context, got %d bytes: %s", len(output), output)
	}
}
