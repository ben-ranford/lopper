//go:build !windows

package runtime

import (
	"os"
	"path/filepath"
	"syscall"
)

func trustedRuntimeSearchDirMode(info os.FileInfo) bool {
	perm := info.Mode().Perm()
	if perm&0o002 != 0 {
		return false
	}
	if perm&0o020 == 0 {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return false
	}
	return trustedRuntimeOwnerUID(stat.Uid, int64(os.Geteuid()))
}

func trustedRuntimeOwnerUID(fileUID uint32, effectiveUID int64) bool {
	return int64(fileUID) == effectiveUID
}

func platformRuntimeExecutablePathImmutable(path string) bool {
	effectiveUID := int64(os.Geteuid())
	groups, err := os.Getgroups()
	if err != nil {
		return false
	}
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !trustedRuntimePathEntryImmutable(info, effectiveUID, groups) {
			return false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return true
		}
	}
}

func trustedRuntimePathEntryImmutable(info os.FileInfo, effectiveUID int64, groups []int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || int64(stat.Uid) == effectiveUID {
		return false
	}
	permissions := info.Mode().Perm()
	if permissions&0o002 != 0 {
		return false
	}
	return permissions&0o020 == 0 || !runtimeGroupContains(groups, int64(stat.Gid))
}

func runtimeGroupContains(groups []int, gid int64) bool {
	for _, group := range groups {
		if int64(group) == gid {
			return true
		}
	}
	return false
}

func platformRuntimeWindowsExecutableRoots() []string {
	return nil
}
