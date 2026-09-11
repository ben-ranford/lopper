//go:build unix

package testutil

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func ProcessTerminated(processID int) (bool, error) {
	if err := syscall.Kill(processID, 0); errors.Is(err, syscall.ESRCH) {
		return true, nil
	} else if err != nil {
		return false, err
	}

	output, err := exec.Command("/bin/ps", "-Ao", "pid=,stat=").Output()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != strconv.Itoa(processID) {
			continue
		}
		return strings.HasPrefix(fields[1], "Z"), nil
	}
	return false, nil
}
