package shared

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWalkRepoFilesWithStatus(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(repo, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	visitErr := errors.New("visit failed")
	for _, tc := range []struct {
		name          string
		limit         int
		visitErr      error
		canceled      bool
		wantTruncated bool
		wantVisits    int
		wantErr       error
	}{
		{name: "one beyond cap", limit: 1, wantTruncated: true, wantVisits: 1},
		{name: "exact cap", limit: 2, wantVisits: 2},
		{name: "below cap", limit: 3, wantVisits: 2},
		{name: "unlimited", wantVisits: 2},
		{name: "visitor stop", limit: 1, visitErr: fs.SkipAll, wantVisits: 1},
		{name: "visitor error", limit: 1, visitErr: visitErr, wantVisits: 1, wantErr: visitErr},
		{name: "canceled", limit: 1, canceled: true, wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			visits := 0
			truncated, err := WalkRepoFilesWithStatus(ctx, repo, tc.limit, nil, func(string, fs.DirEntry) error { visits++; return tc.visitErr })
			if truncated != tc.wantTruncated || visits != tc.wantVisits || !errors.Is(err, tc.wantErr) {
				t.Fatalf("walk = (%t, %d visits, %v), want (%t, %d visits, %v)", truncated, visits, err, tc.wantTruncated, tc.wantVisits, tc.wantErr)
			}
		})
	}
}
