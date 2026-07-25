package safeio

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type openNoFollowMutationCase struct {
	name     string
	mutate   func(t *testing.T, tracePath string)
	matchErr func(error) bool
}

func openNoFollowMutationCases() []openNoFollowMutationCase {
	return []openNoFollowMutationCase{
		{
			name: "renamed leaf replaced by symlink to same file",
			mutate: func(t *testing.T, tracePath string) {
				t.Helper()

				renamedPath := filepath.Join(filepath.Dir(tracePath), "trace.real")
				if err := os.Rename(tracePath, renamedPath); err != nil {
					t.Fatalf("rename trace aside: %v", err)
				}
				if err := os.Symlink(filepath.Base(renamedPath), tracePath); err != nil {
					t.Skipf("symlink unsupported: %v", err)
				}
			},
			matchErr: func(err error) bool {
				return errors.Is(err, ErrNoFollowFinalComponent)
			},
		},
		{
			name: "direct leaf swap",
			mutate: func(t *testing.T, tracePath string) {
				t.Helper()

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
			},
			matchErr: func(err error) bool {
				return strings.Contains(err.Error(), "changed while opening")
			},
		},
	}
}

func assertOpenFileNoFollowMutationRejected(t *testing.T, context string, tc openNoFollowMutationCase, open func(t *testing.T, tracePath string) (io.Closer, error)) {
	t.Helper()

	rootDir := t.TempDir()
	tracePath := filepath.Join(rootDir, "trace.ndjson")
	if err := os.WriteFile(tracePath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write trace: %v", err)
	}

	withOpenFileNoFollowByVerificationBeforeOpen(t, func() {
		tc.mutate(t, tracePath)
	})

	file, err := open(t, tracePath)
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Fatalf("close unexpected file: %v", closeErr)
		}
		t.Fatalf("expected %s to reject mutated leaf", context)
	}
	if err == nil || !tc.matchErr(err) {
		t.Fatalf("unexpected mutation rejection error: %v", err)
	}
}
