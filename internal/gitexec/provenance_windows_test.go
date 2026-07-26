//go:build windows

package gitexec

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsProgramFilesCandidatesSupportNonstandardVolume(t *testing.T) {
	t.Setenv("ProgramW6432", `D:\Applications`)
	t.Setenv("ProgramFiles", `D:\Applications`)
	t.Setenv("ProgramFiles(x86)", `E:\Applications`)
	t.Setenv("PATH", "")

	roots := windowsProgramFilesRoots()
	if !containsWindowsPath(roots, `D:\Applications`) ||
		!containsWindowsPath(roots, `E:\Applications`) {
		t.Fatalf("Program Files roots = %#v", roots)
	}

	candidates := platformExecutableCandidates()
	for _, want := range []string{
		filepath.Join(`D:\Applications`, "Git", "cmd", windowsGitExecutableName),
		filepath.Join(`D:\Applications`, "Git", "bin", windowsGitExecutableName),
		filepath.Join(`E:\Applications`, "Git", "cmd", windowsGitExecutableName),
		filepath.Join(`E:\Applications`, "Git", "bin", windowsGitExecutableName),
	} {
		if !containsWindowsPath(candidates, want) {
			t.Fatalf("Windows Git candidates = %#v, want %q", candidates, want)
		}
	}
}

func TestValidateWindowsExecutablePathAcceptsTrustedInstall(t *testing.T) {
	for _, subdir := range []string{"cmd", "bin"} {
		t.Run(subdir, func(t *testing.T) {
			path, roots, snapshots := trustedWindowsSnapshots(t, subdir)

			if err := validateWindowsExecutablePath(path, roots, windowsSnapshotInspector(snapshots)); err != nil {
				t.Fatalf("validate secure Windows Git path: %v", err)
			}
		})
	}
}

func TestValidateWindowsExecutablePathRejectsUntrustedProvenance(t *testing.T) {
	path, roots, snapshots := trustedWindowsSnapshots(t, "cmd")
	for _, tc := range []struct {
		name   string
		target string
		mutate func(*windowsExecutableSnapshot)
		want   string
	}{
		{
			name:   "reparse point executable",
			target: path,
			mutate: func(snapshot *windowsExecutableSnapshot) { snapshot.mode |= os.ModeSymlink },
			want:   "contains a reparse point",
		},
		{
			name:   "untrusted owner",
			target: filepath.Dir(path),
			mutate: func(snapshot *windowsExecutableSnapshot) { snapshot.owner = "S-1-5-21-1-2-3-1001" },
			want:   "untrusted Windows owner",
		},
		{
			name:   "untrusted dacl grant",
			target: path,
			mutate: func(snapshot *windowsExecutableSnapshot) {
				snapshot.entries = append(snapshot.entries, windowsAccessEntry{principal: "S-1-5-32-545", mask: windowsFileWriteData})
			},
			want: "has write access",
		},
		{
			name:   "unsafe ancestry",
			target: filepath.Dir(path),
			mutate: func(snapshot *windowsExecutableSnapshot) {
				snapshot.entries = append(snapshot.entries, windowsAccessEntry{principal: "S-1-5-32-545", mask: windowsFileDeleteChild})
			},
			want: "has write access",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := cloneWindowsSnapshots(snapshots)
			snapshot := mutated[tc.target]
			tc.mutate(&snapshot)
			mutated[tc.target] = snapshot

			err := validateWindowsExecutablePath(path, roots, windowsSnapshotInspector(mutated))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q rejection, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateWindowsExecutablePathRejectsReplacementDuringSecondPass(t *testing.T) {
	path, roots, snapshots := trustedWindowsSnapshots(t, "cmd")
	replacementPath := filepath.Join(t.TempDir(), "replacement.exe")
	if err := os.WriteFile(replacementPath, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement executable: %v", err)
	}
	replacementInfo, err := os.Lstat(replacementPath)
	if err != nil {
		t.Fatalf("stat replacement executable: %v", err)
	}

	finalInspections := 0
	inspect := func(current string) (windowsExecutableSnapshot, error) {
		snapshot := snapshots[current]
		if current == path {
			finalInspections++
			if finalInspections == 2 {
				snapshot.info = replacementInfo
			}
		}
		return snapshot, nil
	}

	err = validateWindowsExecutablePath(path, roots, inspect)
	if err == nil || !strings.Contains(err.Error(), "changed during validation") {
		t.Fatalf("expected replacement rejection, got %v", err)
	}
}

func TestValidateWindowsExecutablePathRejectsOutsideProgramFiles(t *testing.T) {
	inspected := false
	inspect := func(string) (windowsExecutableSnapshot, error) {
		inspected = true
		return windowsExecutableSnapshot{}, nil
	}

	err := validateWindowsExecutablePath(
		`C:\Users\runneradmin\bin\`+windowsGitExecutableName,
		[]string{`C:\Program Files`},
		inspect,
	)
	if err == nil {
		t.Fatal("expected user-local Windows Git path rejection")
	}
	if inspected {
		t.Fatal("outside-Program-Files path must be rejected before inspection")
	}
}

func TestWindowsExecutablePathPartsIncludesVolumeRootAndLeaf(t *testing.T) {
	path := `C:\Program Files\Git\cmd\` + windowsGitExecutableName
	parts := windowsExecutablePathParts(`C:\Program Files`, path)
	if parts[0] != `C:\` ||
		parts[1] != `C:\Program Files` ||
		parts[len(parts)-1] != path {
		t.Fatalf("Windows path parts = %#v", parts)
	}
}

func trustedWindowsSnapshots(t *testing.T, subdir string) (string, []string, map[string]windowsExecutableSnapshot) {
	t.Helper()

	programFiles := filepath.Join(t.TempDir(), "Program Files")
	path := filepath.Join(programFiles, "Git", subdir, windowsGitExecutableName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir trusted Windows fixture: %v", err)
	}
	if err := os.WriteFile(path, []byte("git"), 0o600); err != nil {
		t.Fatalf("write trusted Windows fixture: %v", err)
	}

	snapshots := make(map[string]windowsExecutableSnapshot)
	for _, current := range windowsExecutablePathParts(programFiles, path) {
		info, err := os.Lstat(current)
		if err != nil {
			t.Fatalf("stat %s: %v", current, err)
		}
		snapshots[current] = windowsExecutableSnapshot{
			path:  current,
			mode:  trustedWindowsMode(current, path),
			info:  info,
			owner: "S-1-5-18",
			entries: []windowsAccessEntry{
				{principal: "S-1-5-32-545", mask: 0x00120089},
			},
		}
	}
	return path, []string{programFiles}, snapshots
}

func trustedWindowsMode(current, executable string) fs.FileMode {
	if current == executable {
		return 0
	}
	return fs.ModeDir | 0o755
}

func windowsSnapshotInspector(snapshots map[string]windowsExecutableSnapshot) windowsPathInspector {
	return func(path string) (windowsExecutableSnapshot, error) {
		return snapshots[path], nil
	}
}

func cloneWindowsSnapshots(src map[string]windowsExecutableSnapshot) map[string]windowsExecutableSnapshot {
	cloned := make(map[string]windowsExecutableSnapshot, len(src))
	for path, snapshot := range src {
		snapshot.entries = append([]windowsAccessEntry(nil), snapshot.entries...)
		cloned[path] = snapshot
	}
	return cloned
}
