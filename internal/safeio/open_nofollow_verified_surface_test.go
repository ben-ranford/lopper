//go:build darwin || windows

package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenFileNoFollowRejectsRenamedLeafReplacedBySymlinkToSameFile(t *testing.T) {
	mutate := func(tracePath string) {
		renamedPath := filepath.Join(filepath.Dir(tracePath), "trace.real")
		if err := os.Rename(tracePath, renamedPath); err != nil {
			t.Fatalf("rename trace aside: %v", err)
		}
		if err := os.Symlink(filepath.Base(renamedPath), tracePath); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
	}
	matchErr := func(err error) bool {
		return errors.Is(err, ErrNoFollowFinalComponent)
	}
	assertOpenFileNoFollowVerifiedSurfaceRejectsMutation(t, mutate, matchErr)
}

func TestOpenFileNoFollowRejectsDirectLeafSwap(t *testing.T) {
	mutate := func(tracePath string) {
		replacementPath := filepath.Join(filepath.Dir(tracePath), "trace.other")
		if err := os.WriteFile(replacementPath, []byte("after\n"), 0o600); err != nil {
			t.Fatalf("write replacement trace: %v", err)
		}
		if err := os.Remove(tracePath); err != nil {
			t.Fatalf("remove original trace: %v", err)
		}
		if err := os.Rename(replacementPath, tracePath); err != nil {
			t.Fatalf("swap replacement trace into place: %v", err)
		}
	}
	matchErr := func(err error) bool {
		return strings.Contains(err.Error(), "changed while opening")
	}
	assertOpenFileNoFollowVerifiedSurfaceRejectsMutation(t, mutate, matchErr)
}
