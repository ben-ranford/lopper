//go:build windows

package safeio

import "errors"

func mkfifoTestPath(string, uint32) error {
	return errors.New("mkfifo unsupported on windows")
}
