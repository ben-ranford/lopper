package scripts

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestTrustedGitObjectVerifierContracts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for trusted Git object contracts")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--test", "queue_me_git_object_contract.test.js")
	command.Dir = repoPath(t, "scripts")
	command.WaitDelay = 5 * time.Second
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("trusted Git object contracts: %v\n%s", runErr, output)
	}
}
