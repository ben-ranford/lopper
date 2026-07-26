//go:build unix

package gitexec

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

type unixExecutableSnapshot struct {
	path     string
	mode     fs.FileMode
	uid      uint64
	identity string
}

type unixPathInspector func(string) (unixExecutableSnapshot, error)

const platformSafeSystemPath = "PATH=/usr/bin:/bin:/usr/sbin:/sbin"

func platformExecutableCandidates() []string {
	candidates := []string{
		ExecutablePrimary,
		ExecutableFallback,
		"/usr/local/bin/git",
		"/opt/homebrew/bin/git",
		"/opt/local/bin/git",
	}
	if path, err := exec.LookPath("git"); err == nil {
		candidates = append([]string{path}, candidates...)
	}
	return uniqueUnixCandidates(candidates)
}

func uniqueUnixCandidates(candidates []string) []string {
	seen := make(map[string]struct{}, len(candidates))
	unique := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		unique = append(unique, candidate)
	}
	return unique
}

func validatePlatformExecutable(path string) error {
	return validateUnixExecutablePath(path, inspectUnixPath)
}

func validateUnixExecutablePath(path string, inspect unixPathInspector) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("git executable path must be absolute and clean")
	}
	if unixHomebrewPath(path) {
		return fmt.Errorf("user-managed Homebrew path is not trusted")
	}

	parts := unixExecutablePathParts(path)
	first, err := collectUnixSnapshots(parts, inspect)
	if err != nil {
		return err
	}
	second, err := collectUnixSnapshots(parts, inspect)
	if err != nil {
		return err
	}
	if !slices.Equal(first, second) {
		return fmt.Errorf("git executable provenance changed during validation")
	}
	return nil
}

func unixHomebrewPath(path string) bool {
	return path == "/opt/homebrew" ||
		strings.HasPrefix(path, "/opt/homebrew/") ||
		strings.Contains(path, "/Homebrew/") ||
		strings.Contains(path, "/Cellar/")
}

func unixExecutablePathParts(path string) []string {
	parts := []string{string(os.PathSeparator)}
	current := string(os.PathSeparator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), current) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		parts = append(parts, current)
	}
	return parts
}

func collectUnixSnapshots(paths []string, inspect unixPathInspector) ([]unixExecutableSnapshot, error) {
	snapshots := make([]unixExecutableSnapshot, 0, len(paths))
	for index, path := range paths {
		snapshot, err := inspect(path)
		if err != nil {
			return nil, fmt.Errorf("inspect git executable path %s: %w", path, err)
		}
		if err := validateUnixSnapshot(snapshot, index == len(paths)-1); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func inspectUnixPath(path string) (unixExecutableSnapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return unixExecutableSnapshot{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return unixExecutableSnapshot{}, fmt.Errorf("ambiguous ownership metadata")
	}
	return unixExecutableSnapshot{
		path:     path,
		mode:     info.Mode(),
		uid:      uint64(stat.Uid),
		identity: fmt.Sprintf("%v:%v", stat.Dev, stat.Ino),
	}, nil
}

func validateUnixSnapshot(snapshot unixExecutableSnapshot, executable bool) error {
	if snapshot.mode&os.ModeSymlink != 0 {
		return fmt.Errorf("git executable path contains symlink: %s", snapshot.path)
	}
	if snapshot.uid != 0 {
		return fmt.Errorf("git executable path is not root-owned: %s", snapshot.path)
	}
	if snapshot.mode.Perm()&0o022 != 0 {
		return fmt.Errorf("git executable path is writable by untrusted users: %s", snapshot.path)
	}
	if executable {
		return validateUnixExecutableFile(snapshot)
	}
	if !snapshot.mode.IsDir() {
		return fmt.Errorf("git executable ancestor is not a directory: %s", snapshot.path)
	}
	return nil
}

func validateUnixExecutableFile(snapshot unixExecutableSnapshot) error {
	if !snapshot.mode.IsRegular() {
		return fmt.Errorf("git executable is not a regular file: %s", snapshot.path)
	}
	if snapshot.mode.Perm()&0o111 == 0 {
		return fmt.Errorf("git executable is not executable: %s", snapshot.path)
	}
	return nil
}
