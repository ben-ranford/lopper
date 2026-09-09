//go:build unix

package testutil

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func ProcessTerminated(pid int) (bool, error) {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true, nil
	} else if err != nil {
		return false, err
	}

	output, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return true, nil
		}
		return false, err
	}
	return strings.HasPrefix(strings.TrimSpace(string(output)), "Z"), nil
}
