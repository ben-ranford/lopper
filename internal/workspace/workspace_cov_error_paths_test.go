package workspace

import "testing"

func TestChangedFilesReturnsNormalizeError(t *testing.T) {
	_, err := ChangedFiles("\x00")
	if err == nil {
		t.Fatalf("expected normalize error for invalid repo path")
	}
}

func TestCurrentCommitSHAReturnsNormalizeError(t *testing.T) {
	_, err := CurrentCommitSHA("\x00")
	if err == nil {
		t.Fatalf("expected normalize error for invalid repo path")
	}
}
