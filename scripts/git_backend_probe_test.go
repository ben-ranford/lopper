package scripts

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestTrustedGitObjectBackendEvidence(t *testing.T) {
	runGitBackendFixture(t, "testdata/git_backend_probe/probe.test.cjs", "--native")
}

func TestTrustedGitObjectBackendContracts(t *testing.T) {
	runGitBackendFixture(t, "--test", "testdata/git_backend_probe/probe.test.cjs")
}

func runGitBackendFixture(t *testing.T, arguments ...string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for Git backend evidence")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, node, arguments...)
	command.Dir = repoPath(t, "scripts")
	command.WaitDelay = 5 * time.Second
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Git backend evidence failed: %v\n%s", err, output)
	}
	t.Logf("Git backend evidence (native admission and modelled contracts are distinct):\n%s", output)
}
