//go:build darwin || linux

package safeio

import "syscall"

func lockDirectoryDescriptor(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_EX)
}

func unlockDirectoryDescriptor(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_UN)
}
