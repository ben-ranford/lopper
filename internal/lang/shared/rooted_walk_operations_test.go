package shared

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

// Cross the directory batch boundary through the public pinned callback. Existing
// narrow-tree tests cover traversal alone; this also checks callback-relative IO.
func TestPinnedWalkWideBatchOperationBounds(t *testing.T) {
	for _, shape := range []struct {
		name         string
		depth, width int
	}{
		{"one-batch-boundary", 1, 128},
		{"multiple-batches", 1, 257},
		{"deep-and-wide", 7, 2},
	} {
		t.Run(shape.name, func(t *testing.T) {
			assertPinnedWalkOperationBounds(t, shape.depth, shape.width)
		})
	}
}

func assertPinnedWalkOperationBounds(t *testing.T, depth, width int) {
	t.Helper()
	repo := t.TempDir()
	directories, files := createSharedWalkDeepWideTree(t, repo, depth, width)
	counts := &sharedWalkOperationCounts{}
	root := newCountingSharedWalkRoot(t, repo, counts)
	budget := RootedWalkBudget{MaxTraversalEntries: directories + files, MaxFiles: files, MaxWorkItems: files}
	visited := 0
	err := WalkRepoFilesWithinRootPinned(context.Background(), repo, root, budget, nil, func(file RootedWalkFile) error {
		input, err := file.Parent.Open(file.Leaf)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(input)
		closeErr := input.Close()
		if string(body) != "x" {
			return fmt.Errorf("unexpected pinned callback contents at %s: %q", file.Path, body)
		}
		visited++
		return errors.Join(readErr, closeErr)
	})
	if err != nil {
		t.Fatal(err)
	}
	if visited != files {
		t.Fatalf("visited=%d, want %d", visited, files)
	}
	if counts.openRootCalls != directories-1 || counts.openCalls != directories+files || counts.lstatCalls != 3*directories-1 {
		t.Fatalf("operations=%+v; want child opens=%d, directory plus callback opens=%d, identity checks=%d", counts, directories-1, directories+files, 3*directories-1)
	}
}
