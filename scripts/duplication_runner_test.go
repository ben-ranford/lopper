package scripts

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDuplicationRunnerFailsClosed(t *testing.T) {
	t.Parallel()
	command := exec.Command("python3", "-B", repoPath(t, "scripts/check_duplication_test.py"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("duplication runner regression fixtures failed: %v\n%s", err, output)
	}
}

func TestDuplicationMissingBaseFailsClosed(t *testing.T) {
	command := exec.Command("make", "dup-check", "DUPLICATION_BASE=refs/heads/lopper-nonexistent-duplication-fixture")
	command.Dir = repoPath(t, ".")
	command.Env = withoutGitEnv()
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Cannot compare requested base") || !strings.Contains(string(output), "No fallback comparison was used") {
		t.Fatalf("missing comparison base must fail with recovery instructions: %v\n%s", err, output)
	}
}

func TestDuplicationOccurrencePolicy(t *testing.T) {
	t.Parallel()
	command := exec.Command("python3", "-B", repoPath(t, "scripts/duplication_policy_test.py"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("duplication occurrence policy fixtures failed: %v\n%s", err, output)
	}
}
