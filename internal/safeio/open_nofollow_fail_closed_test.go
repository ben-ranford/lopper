package safeio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func assertOpenFileNoFollowFailsClosed(t *testing.T, platform string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "trace.ndjson")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	file, err := OpenFileNoFollow(path)
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
	}
	if err == nil || !strings.Contains(err.Error(), "no-follow file open unsupported on "+platform) {
		t.Fatalf("expected fail-closed unsupported error, got %v", err)
	}
}
