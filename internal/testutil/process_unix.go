//go:build unix

package testutil

import (
	"errors"
	"syscall"
)

func ProcessGroupTerminated(processID int) (bool, error) {
	if err := syscall.Kill(-processID, 0); errors.Is(err, syscall.ESRCH) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	return false, nil
}
