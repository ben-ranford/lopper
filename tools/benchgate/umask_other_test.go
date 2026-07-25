//go:build !unix

package main

import "testing"

func supportsProcessUmask() bool {
	return false
}

func withProcessUmask(t *testing.T, _ int, _ func()) {
	t.Helper()
	t.Skip("process umask control unavailable on this platform")
}
