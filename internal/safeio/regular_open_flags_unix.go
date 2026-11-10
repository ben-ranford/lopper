//go:build unix

package safeio

import "syscall"

func regularReadOpenFlags() (int, error) {
	return syscall.O_RDONLY | syscall.O_NONBLOCK, nil
}
