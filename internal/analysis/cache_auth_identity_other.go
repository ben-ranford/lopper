//go:build !windows

package analysis

import (
	"fmt"
	"io/fs"
	"syscall"
)

type storageIdentityInteger interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

func storageDirectoryIdentity(_ string, info fs.FileInfo) (string, error) {
	if info == nil || !info.IsDir() {
		return "", fmt.Errorf("storage root is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("unsupported storage directory identity %T", info.Sys())
	}
	return formatStorageDirectoryIdentity(stat.Dev, stat.Ino), nil
}

func formatStorageDirectoryIdentity[Device storageIdentityInteger, Inode storageIdentityInteger](device Device, inode Inode) string {
	return fmt.Sprintf("device:%x;inode:%x", device, inode)
}
