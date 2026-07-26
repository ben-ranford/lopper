//go:build !windows

package safeio

import "syscall"

func mkfifoTestPath(path string, perm uint32) error {
	return syscall.Mkfifo(path, perm)
}
