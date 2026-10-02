//go:build !regressionproof

package ui

import (
	"context"
	"testing"
)

func TestSummaryTerminalAbandonsPendingAction(t *testing.T) {
	terminal := &summaryTerminal{ctx: context.Background()}
	command := terminal.beginAction()
	action := terminal.action

	action.awaitOrAbandon()
	if action.state.Load() != summaryActionAbandoned {
		t.Fatalf("pending action state = %d, want abandoned", action.state.Load())
	}
	if message := command(); message != nil {
		t.Fatalf("abandoned action returned a message: %#v", message)
	}
	action.awaitOrAbandon()
}
