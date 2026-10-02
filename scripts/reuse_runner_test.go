package scripts

import (
	"os/exec"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func TestReuseRunnerTrustBoundaries(t *testing.T) {
	command := exec.Command("python3", "-B", repoPath(t, "scripts/check_reuse_test.py"))
	command.Env = gitexec.SanitizedEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reuse runner trust-boundary tests: %v\n%s", err, output)
	}
}

func TestReuseSuppressionNodeSuite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to test reuse suppression evidence")
	}
	command := exec.Command(node, "--test", "reuse_suppression.test.js")
	command.Dir = repoPath(t, "scripts")
	command.Env = gitexec.SanitizedEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reuse suppression node tests: %v\n%s", err, output)
	}
}
