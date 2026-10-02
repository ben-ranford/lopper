package runtime

import (
	"errors"
	"syscall"

	"golang.org/x/sys/unix"
)

// SZOMB from Darwin's sys/proc.h: exited, awaiting collection by its parent.
const darwinProcessZombie = 5

var runtimeProcessGroupMembers = unix.SysctlKinfoProcSlice

func runtimeProcessGroupExited(processID int, signalErr error) bool {
	if !errors.Is(signalErr, syscall.EPERM) {
		return false
	}
	// Darwin killpg1 excludes zombies, then returns EPERM when no signalable
	// members remain. Confirm that condition instead of hiding permission
	// failures for live processes or failures to inspect the group.
	members, err := runtimeProcessGroupMembers("kern.proc.pgrp", processID)
	if err != nil {
		return false
	}
	for _, member := range members {
		if member.Proc.P_stat != darwinProcessZombie {
			return false
		}
	}
	return true
}
