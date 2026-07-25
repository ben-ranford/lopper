package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func assertOpenFileNoFollowVerifiedSurfaceRejectsMutation(t *testing.T, mutate func(tracePath string), matchErr func(error) bool) {
	t.Helper()

	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	withOpenFileNoFollowByVerificationBeforeOpen(t, func() {
		mutate(tracePath)
	})

	file, err := OpenFileNoFollow(tracePath)
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
		t.Fatal("expected verified open surface to reject mutated leaf")
	}
	if err == nil || !matchErr(err) {
		t.Fatalf("unexpected mutation rejection error: %v", err)
	}
}
