//go:build !windows

package ui

import (
	"io"

	"github.com/charmbracelet/x/term"
)

// Keep the caller's terminal raw until both Bubble Tea and our input reader
// stop. Restoring cooked mode earlier can strand the reader in a blocking read.
func enterStaveTerminalRaw(in *staveTerminalInput) (func() error, error) {
	fd := in.source.Fd()
	if !term.IsTerminal(fd) {
		return func() error { return nil }, nil
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return func() error {
		if state == nil {
			return nil
		}
		original := state
		state = nil
		return term.Restore(fd, original)
	}, nil
}

// The EOF adapter preserves terminal detection while keeping Bubble Tea's
// internal parser idle on Unix.
func (in *staveTerminalInput) terminal() io.Reader {
	return &staveTerminalEOF{file: in.source}
}
