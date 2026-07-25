//go:build darwin

package safeio

import (
	"os"
	"testing"
)

func openAtomicTestFile(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %q: %v", path, err)
	}
	return trackAtomicTestFile(t, file)
}

func removeAtomicTestTarget(t *testing.T, _ *os.File, _, targetPath string) {
	t.Helper()
	if err := os.Remove(targetPath); err != nil {
		t.Fatalf("remove target: %v", err)
	}
}
