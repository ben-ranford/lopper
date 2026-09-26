package ui

import "io"

// The no-flush console reader owns input mode and restores it on Close. Disable
// Bubble Tea input: an stdin-FD adapter would open a second console reader that
// bypasses the adapter's Read method. Bubble Tea still owns output restoration.
func (*staveTerminalInput) terminal() io.Reader { return nil }
