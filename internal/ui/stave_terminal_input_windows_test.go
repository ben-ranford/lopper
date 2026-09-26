package ui

import (
	"os"
	"testing"
)

func TestStaveTerminalDisablesBubbleTeaConsoleInput(t *testing.T) {
	input := &staveTerminalInput{source: os.Stdin}
	if got := input.terminal(); got != nil {
		t.Fatalf("Bubble Tea input = %T; want nil to prevent a second console reader", got)
	}
}
