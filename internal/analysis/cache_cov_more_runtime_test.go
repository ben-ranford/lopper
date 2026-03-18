package analysis

import (
	"os"
	"testing"
)

func TestCacheCloseIfPresentReturnsUnexpectedCloseError(t *testing.T) {
	if err := closeIfPresent(&os.File{}); err == nil {
		t.Fatalf("expected closeIfPresent to return a non-benign close error")
	}
}
