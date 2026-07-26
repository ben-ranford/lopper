//go:build !darwin && !linux

package safeio

func lockDirectoryDescriptor(uintptr) error {
	return ErrDirectoryLockUnsupported
}

func unlockDirectoryDescriptor(uintptr) error {
	return nil
}
