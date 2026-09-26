package scripts

import (
	"os/exec"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func TestReleaseNotesRejectInvalidTagsBeforeSubprocess(t *testing.T) {
	command := exec.Command("python3", "-B", repoPath(t, "scripts/release_notes_security_test.py"))
	command.Env = gitexec.SanitizedEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release-note subprocess boundary tests: %v\n%s", err, output)
	}
}
