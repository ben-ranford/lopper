package scripts

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestSuppressionProvenanceNodeSuite(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required to verify suppression artifact provenance")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--test", "suppression_provenance.test.js")
	command.Dir = repoPath(t, "scripts")
	command.WaitDelay = 5 * time.Second
	if output, err := command.CombinedOutput(); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			t.Fatalf("suppression provenance regressions did not finish: %v (%v)\n%s", contextErr, err, output)
		}
		t.Fatalf("suppression provenance regressions failed: %v\n%s", err, output)
	}
}
