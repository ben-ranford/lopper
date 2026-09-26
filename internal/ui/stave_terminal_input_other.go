//go:build !windows

package ui

import "io"

// Bubble Tea owns raw mode and renderer restoration. The EOF adapter preserves
// terminal detection while keeping its internal parser idle on Unix.
func (in *staveTerminalInput) terminal() io.Reader {
	return &staveTerminalEOF{file: in.source}
}
