package scripts

import (
	"os/exec"
	"testing"

	"github.com/ben-ranford/lopper/internal/gitexec"
)

func TestReuseRunnerTrustBoundaries(t *testing.T) {
	t.Parallel()

	command := exec.Command("python3", "-B", repoPath(t, "scripts/check_reuse_test.py"))
	command.Env = gitexec.SanitizedEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reuse runner trust-boundary tests: %v\n%s", err, output)
	}
}

func TestReuseSuppressionNodeSuite(t *testing.T) {
	node := queueLifecycleNode(t)
	command := exec.Command(node, "--test", "reuse_suppression.test.js", "testdata/reuse_suppression/partial_rerun.cjs")
	command.Dir = repoPath(t, "scripts")
	command.Env = gitexec.SanitizedEnv()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reuse suppression node tests: %v\n%s", err, output)
	}
}
