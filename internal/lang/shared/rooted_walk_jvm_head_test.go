package shared

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// These state assertions move with the implementation from JVM's private walk.
func TestRootedWalkMigratedJVMBudgetGuards(t *testing.T) {
	unlimited := rootedRepoWalker{}
	if got := unlimited.traversalReadSize(); got != 128 {
		t.Fatalf("unlimited batch size=%d, want128", got)
	}
	if !unlimited.queueTraversalEntries(1) || !unlimited.dequeueTraversalEntry() {
		t.Fatal("unlimited queue/dequeue failed")
	}
	if unlimited.dequeueTraversalEntry() {
		t.Fatal("empty dequeue succeeded")
	}
	bounded := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 1}}
	if !bounded.queueTraversalEntries(1) {
		t.Fatal("final queue entry rejected")
	}
	if bounded.queueTraversalEntries(1) || bounded.queueTraversalEntries(-1) {
		t.Fatal("overflow/negative queue accepted")
	}
}

func TestRootedWalkMigratedJVMOverflowQueueState(t *testing.T) {
	directory := &sharedWalkTestDirectory{fillEntry: &sharedWalkTestDirEntry{name: "ignored.txt"}, repeatEntries: 4095, overflowEntry: &sharedWalkTestDirEntry{name: "Main.java"}}
	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 4096}}
	if err := walker.countTraversalEntry("repo"); err != nil {
		t.Fatal(err)
	}
	for size := walker.traversalReadSize(); size > 0; size = walker.traversalReadSize() {
		_, done, err := walker.readDirBatch("repo", directory, size)
		if err != nil || done {
			t.Fatalf("unexpected bounded read done=%t err=%v", done, err)
		}
	}
	if err := walker.probeDirectoryLimit(context.Background(), "repo", directory); !errors.Is(err, errRootedWalkTraversalLimit) {
		t.Fatalf("overflow probe: %v", err)
	}
	if walker.state.traversalEntriesSeen != 1 || walker.state.traversalEntriesQueued != 4095 || walker.state.filesSeen != 0 {
		t.Fatalf("overflow state=%#v, want root1 queued4095 candidates0", walker.state)
	}
}

func TestRootedWalkMigratedJVMExactAndCandidateQueueState(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		files, limit, seen, queued, candidates, visits int
	}{
		{"exact-cap", 2, 2, 3, 0, 2, 2}, {"candidate-cap-plus-one", 4, 2, 4, 1, 3, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			createMigratedJVMCandidates(t, repo, tc.files)
			visits := 0
			walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: tc.files + 1, MaxFiles: tc.limit}, skipDir: rootedWalkDirectoryNameSkip(nil), visit: func(RootedWalkFile) error { visits++; return nil }}
			root := openSharedTestRoot(t, repo)
			info, err := root.Lstat(".")
			if err != nil {
				t.Fatal(err)
			}
			err = walker.walk(context.Background(), root, ".", repo, fs.FileInfoToDirEntry(info))
			if (err != nil) != (tc.files > tc.limit) {
				t.Fatalf("walk error=%v", err)
			}
			if walker.state.traversalEntriesSeen != tc.seen || walker.state.traversalEntriesQueued != tc.queued || walker.state.filesSeen != tc.candidates || visits != tc.visits {
				t.Fatalf("state=%#v visits=%d, want %#v", walker.state, visits, tc)
			}
		})
	}
}

func TestRootedWalkMigratedJVMSkipsCountTraversalOnly(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 2, MaxFiles: 1}, skipDir: func(_, name string) bool { return name == "target" }, visit: func(RootedWalkFile) error { return nil }}
	root := openSharedTestRoot(t, repo)
	skipped := fs.FileInfoToDirEntry(&sharedWalkNamedJVMInfo{FileInfo: info, name: "target"})
	if err := walker.walk(context.Background(), root, "target", filepath.Join(repo, "target"), skipped); err != nil {
		t.Fatal(err)
	}
	if walker.state.traversalEntriesSeen != 1 || walker.state.filesSeen != 0 {
		t.Fatalf("skipped-directory state=%#v", walker.state)
	}
	link := fs.FileInfoToDirEntry(&sharedWalkSymlinkJVMInfo{FileInfo: info})
	if err := walker.walk(context.Background(), root, "link", filepath.Join(repo, "link.java"), link); err != nil {
		t.Fatal(err)
	}
	if walker.state.traversalEntriesSeen != 2 || walker.state.filesSeen != 0 {
		t.Fatalf("rejected entries state=%#v", walker.state)
	}
	if err := walker.walk(context.Background(), root, "extra", filepath.Join(repo, "extra.java"), link); !errors.Is(err, errRootedWalkTraversalLimit) {
		t.Fatalf("expected exact rejected-entry cap: %v", err)
	}
}

type sharedWalkNamedJVMInfo struct {
	fs.FileInfo
	name string
}

func (i *sharedWalkNamedJVMInfo) Name() string { return i.name }

type sharedWalkSymlinkJVMInfo struct{ fs.FileInfo }

func (*sharedWalkSymlinkJVMInfo) Mode() fs.FileMode { return fs.ModeSymlink }
func (*sharedWalkSymlinkJVMInfo) IsDir() bool       { return false }

func TestRootedWalkMigratedJVMExactEmptyDirectories(t *testing.T) {
	repo := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(repo, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 3, MaxFiles: 2}, skipDir: rootedWalkDirectoryNameSkip(nil), visit: func(RootedWalkFile) error { return nil }}
	walkMigratedJVMState(t, repo, &walker)
	if walker.state.traversalEntriesSeen != 3 || walker.state.traversalEntriesQueued != 0 {
		t.Fatalf("exact empty-directory state=%#v", walker.state)
	}
}

func TestRootedWalkMigratedJVMDeepWideEntryCount(t *testing.T) {
	repo := t.TempDir()
	dirs, files := createMigratedJVMDeepWideTree(t, repo)
	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 4096}, skipDir: rootedWalkDirectoryNameSkip(nil), visit: func(RootedWalkFile) error { return nil }}
	walkMigratedJVMState(t, repo, &walker)
	if walker.state.traversalEntriesSeen != dirs+files {
		t.Fatalf("entry count=%d want%d", walker.state.traversalEntriesSeen, dirs+files)
	}
}

func TestRootedWalkMigratedJVMBrokenAndEscapingEntryCounts(t *testing.T) {
	repo := t.TempDir()
	outside := filepath.Join(t.TempDir(), "Outside.java")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"Broken.java": filepath.Join(repo, "missing", "Broken.java"), "Escaping.java": outside} {
		if err := os.Symlink(target, filepath.Join(repo, name)); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	root := openSharedTestRoot(t, repo)
	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 8, MaxFiles: 8}, visit: func(RootedWalkFile) error { return nil }}
	for _, entry := range entries {
		if err := walker.walk(context.Background(), root, entry.Name(), filepath.Join(repo, entry.Name()), entry); err != nil {
			t.Fatal(err)
		}
	}
	if walker.state.traversalEntriesSeen != 2 || walker.state.filesSeen != 0 {
		t.Fatalf("link state=%#v", walker.state)
	}
}

func walkMigratedJVMState(t *testing.T, repo string, walker *rootedRepoWalker) {
	t.Helper()
	root := openSharedTestRoot(t, repo)
	info, err := root.Lstat(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := walker.walk(context.Background(), root, ".", repo, fs.FileInfoToDirEntry(info)); err != nil {
		t.Fatal(err)
	}
}

func createMigratedJVMDeepWideTree(t *testing.T, repo string) (int, int) {
	t.Helper()
	current := repo
	dirs, files := 1, 0
	for depth := 0; depth < 20; depth++ {
		for width := 0; width < 7; width++ {
			child := filepath.Join(current, string(rune('a'+width)))
			if err := os.Mkdir(child, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(child, "note.txt"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			dirs++
			files++
		}
		current = filepath.Join(current, "a")
	}
	return dirs, files
}

func createMigratedJVMCandidates(t *testing.T, repo string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		if err := os.WriteFile(filepath.Join(repo, string(rune('a'+i))+".java"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRootedWalkMigratedJVMEntriesAvoidDirectoryOpen(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 2}, skipDir: func(_, name string) bool { return name == "target" }, visit: func(RootedWalkFile) error { return nil }}
	skipped := fs.FileInfoToDirEntry(&sharedWalkNamedJVMInfo{FileInfo: info, name: "target"})
	if err := walker.walk(context.Background(), nil, "target", filepath.Join(repo, "target"), skipped); err != nil {
		t.Fatal(err)
	}
	entry := &sharedWalkTestDirEntry{name: "note.txt"}
	if err := walker.walk(context.Background(), nil, "note.txt", filepath.Join(repo, "note.txt"), entry); err != nil {
		t.Fatal(err)
	}
	if err := walker.walk(context.Background(), nil, "note.txt", filepath.Join(repo, "note.txt"), entry); !errors.Is(err, errRootedWalkTraversalLimit) {
		t.Fatalf("exhausted ordinary file: %v", err)
	}
}

func TestRootedWalkMigratedJVMRejectedEntryFloodCounts(t *testing.T) {
	repo := t.TempDir()
	info, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}
	entry := fs.FileInfoToDirEntry(&sharedWalkSymlinkJVMInfo{FileInfo: info})
	walker := rootedRepoWalker{budget: RootedWalkBudget{MaxTraversalEntries: 2, MaxFiles: 1024}, visit: func(RootedWalkFile) error { return nil }}
	for i := 0; i < 2; i++ {
		if err := walker.walk(context.Background(), nil, "link", filepath.Join(repo, "escape.java"), entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := walker.walk(context.Background(), nil, "link", filepath.Join(repo, "escape.java"), entry); !errors.Is(err, errRootedWalkTraversalLimit) {
		t.Fatalf("rejected flood: %v", err)
	}
	if walker.state.traversalEntriesSeen != 2 || walker.state.filesSeen != 0 {
		t.Fatalf("rejected flood state=%#v", walker.state)
	}
}
