//go:build unix

package gitexec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValidateUnixExecutablePathAcceptsRootOwnedNonWritableTree(t *testing.T) {
	path := "/secure/bin/git"
	snapshots := trustedUnixSnapshots(path)

	if err := validateUnixExecutablePath(path, unixSnapshotInspector(snapshots)); err != nil {
		t.Fatalf("validate secure Unix Git path: %v", err)
	}
}

func TestValidateUnixExecutablePathRejectsUntrustedProvenance(t *testing.T) {
	path := "/secure/bin/git"
	for _, tc := range []struct {
		name   string
		target string
		mutate func(*unixExecutableSnapshot)
		want   string
	}{
		{
			name:   "non-root owner",
			target: "/secure",
			mutate: func(snapshot *unixExecutableSnapshot) { snapshot.uid = 501 },
			want:   "not root-owned",
		},
		{
			name:   "writable ancestor",
			target: "/secure/bin",
			mutate: func(snapshot *unixExecutableSnapshot) { snapshot.mode = fs.ModeDir | 0o775 },
			want:   "writable by untrusted users",
		},
		{
			name:   "symlink executable",
			target: path,
			mutate: func(snapshot *unixExecutableSnapshot) { snapshot.mode = fs.ModeSymlink | 0o777 },
			want:   "contains symlink",
		},
		{
			name:   "non-executable file",
			target: path,
			mutate: func(snapshot *unixExecutableSnapshot) { snapshot.mode = 0o644 },
			want:   "not executable",
		},
		{
			name:   "non-directory ancestor",
			target: "/secure/bin",
			mutate: func(snapshot *unixExecutableSnapshot) { snapshot.mode = 0o755 },
			want:   "ancestor is not a directory",
		},
		{
			name:   "non-regular executable",
			target: path,
			mutate: func(snapshot *unixExecutableSnapshot) { snapshot.mode = fs.ModeNamedPipe | 0o755 },
			want:   "not a regular file",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshots := trustedUnixSnapshots(path)
			snapshot := snapshots[tc.target]
			tc.mutate(&snapshot)
			snapshots[tc.target] = snapshot

			err := validateUnixExecutablePath(path, unixSnapshotInspector(snapshots))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateUnixExecutablePathRejectsReplacement(t *testing.T) {
	path := "/secure/bin/git"
	snapshots := trustedUnixSnapshots(path)
	finalInspections := 0
	inspect := func(current string) (unixExecutableSnapshot, error) {
		snapshot := snapshots[current]
		if current == path {
			finalInspections++
			if finalInspections == 2 {
				snapshot.identity += ":replacement"
			}
		}
		return snapshot, nil
	}

	err := validateUnixExecutablePath(path, inspect)
	if err == nil || !strings.Contains(err.Error(), "changed during validation") {
		t.Fatalf("expected replacement rejection, got %v", err)
	}
}

func TestValidateUnixExecutablePathPropagatesRepeatedInspectionError(t *testing.T) {
	path := "/secure/bin/git"
	snapshots := trustedUnixSnapshots(path)
	expectedErr := errors.New("replacement inspection failure")
	inspections := 0
	inspect := func(current string) (unixExecutableSnapshot, error) {
		inspections++
		if inspections > len(snapshots) {
			return unixExecutableSnapshot{}, expectedErr
		}
		return snapshots[current], nil
	}

	err := validateUnixExecutablePath(path, inspect)
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected repeated inspection error, got %v", err)
	}
}

func TestValidateUnixExecutablePathRejectsUnsafePathForms(t *testing.T) {
	inspect := unixSnapshotInspector(trustedUnixSnapshots("/secure/bin/git"))
	for _, path := range []string{"git", "/secure/../secure/bin/git", "/opt/homebrew/bin/git"} {
		if err := validateUnixExecutablePath(path, inspect); err == nil {
			t.Fatalf("expected unsafe path %q to be rejected", path)
		}
	}
}

func TestUniqueUnixCandidatesDropsEmptyAndDuplicatePaths(t *testing.T) {
	got := uniqueUnixCandidates([]string{"", "/usr/bin/git", "/usr/bin/git", "/bin/git"})
	want := []string{"/usr/bin/git", "/bin/git"}
	if !slices.Equal(got, want) {
		t.Fatalf("unique Unix candidates = %#v, want %#v", got, want)
	}
}

func TestUnixExecutablePathPartsIncludeOnlyFilesystemRootForRootPath(t *testing.T) {
	got := unixExecutablePathParts(string(os.PathSeparator))
	want := []string{string(os.PathSeparator)}
	if !slices.Equal(got, want) {
		t.Fatalf("root path parts = %#v, want %#v", got, want)
	}
}

func TestInspectUnixPathCapturesStatMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write test executable: %v", err)
	}

	snapshot, err := inspectUnixPath(path)
	if err != nil {
		t.Fatalf("inspect Unix path: %v", err)
	}
	if snapshot.path != path {
		t.Fatalf("snapshot path = %q, want %q", snapshot.path, path)
	}
	if !snapshot.mode.IsRegular() {
		t.Fatalf("snapshot mode = %v, want regular file", snapshot.mode)
	}
	if snapshot.identity == "" {
		t.Fatal("snapshot identity = empty, want device/inode identity")
	}
}

func trustedUnixSnapshots(path string) map[string]unixExecutableSnapshot {
	snapshots := make(map[string]unixExecutableSnapshot)
	for index, part := range unixExecutablePathParts(path) {
		mode := fs.ModeDir | 0o755
		if part == path {
			mode = 0o755
		}
		snapshots[part] = unixExecutableSnapshot{
			path:     part,
			mode:     mode,
			uid:      0,
			identity: fmt.Sprintf("1:%d", index+1),
		}
	}
	return snapshots
}

func unixSnapshotInspector(snapshots map[string]unixExecutableSnapshot) unixPathInspector {
	return func(path string) (unixExecutableSnapshot, error) {
		return snapshots[path], nil
	}
}
