//go:build unix

package main

import (
	"sync"
	"syscall"
	"testing"
)

var processUmaskMu sync.Mutex

func supportsProcessUmask() bool {
	return true
}

func withProcessUmask(t *testing.T, mask int, fn func()) {
	t.Helper()

	processUmaskMu.Lock()
	defer processUmaskMu.Unlock()

	previous := syscall.Umask(mask)
	defer syscall.Umask(previous)

	fn()
}
