package scripts

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func TestReleaseNotesPushPreservesCleanChildShellBoundary(t *testing.T) {
	var workflow workflowConfig
	readYAMLConfig(t, ".github/workflows/release.yml", &workflow)
	step := workflowStepByName(t, workflow.Jobs, "prepare-release", "Push refreshed release notes")
	start := strings.Index(step.Run, "git_network() (")
	end := strings.Index(step.Run, "\nrelease_pr_branch=")
	if start < 0 || end < start {
		t.Fatal("release notes push is missing its network helper")
	}
	for _, exitCode := range []int{0, 29} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) {
			root := t.TempDir()
			gitProbe := filepath.Join(root, "git probe")
			bashProbe := filepath.Join(root, "bash probe")
			writeExecutableFile(t, gitProbe, `#!/bin/bash
set -eu
printf '%s\n' "$GIT_CONFIG_VALUE_4" "$GIT_CONFIG_COUNT" "$GIT_CONFIG_VALUE_0" "$GIT_CONFIG_VALUE_1" "$GIT_CONFIG_VALUE_2" "$GIT_CONFIG_VALUE_3" "$@"
if (: <&3) 2>/dev/null; then exit 92; fi
if (: <&4) 2>/dev/null; then exit 95; fi
/bin/cat
exit `+fmt.Sprint(exitCode)+"\n")
			writeExecutableFile(t, bashProbe, `#!/bin/bash
set -eu
[ "${POISON+x}" != x ] || exit 93
case "$*" in *sentinel-header*) exit 94 ;; esac
exec /bin/bash "$@"
`)
			function := strings.Replace(step.Run[start:end], `exec /usr/bin/git "$@"`, "exec "+shellQuote(gitProbe)+` "$@"`, 1)
			program := "set -eu\ngit_home=" + shellQuote(root) + "\nenv_bin=/usr/bin/env\nbash_bin=" + shellQuote(bashProbe) + "\nauth_header=$1\nshift\n" + function + "\ngit_network \"$@\"\n"
			command := exec.Command("/bin/bash", "-c", program, "test", "sentinel-header $literal `unevaluated`", "push", "origin", "HEAD:branch with * $literal")
			command.Env = append(gitexec.SanitizedEnv(), "POISON=must-not-reach-child")
			command.Stdin = strings.NewReader("preserved stdin\n")
			output, err := command.CombinedOutput()
			if exitCode == 0 && err != nil {
				t.Fatalf("network helper failed: %v\n%s", err, output)
			}
			if exitCode != 0 {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != exitCode {
					t.Fatalf("network helper error = %v, want exit %d\n%s", err, exitCode, output)
				}
			}
			want := "AUTHORIZATION: basic sentinel-header $literal `unevaluated`\n5\n/dev/null\n\nnever\nnever\npush\norigin\nHEAD:branch with * $literal\npreserved stdin\n"
			if string(output) != want {
				t.Fatalf("network helper output = %q, want %q", output, want)
			}
		})
	}
}
