package runtime

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestStartCommandCleanupAcceptsExitedDarwinGroup(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", "exit 0")
	configureRuntimeCommand(cmd)
	cleanup, err := StartCommand(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cmd.Wait(); err != nil {
			t.Errorf("reap command: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		process, err := unix.SysctlKinfoProc("kern.proc.pid", cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if process.Proc.P_stat == darwinProcessZombie {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not become a zombie")
		}
		time.Sleep(time.Millisecond)
	}
	// Darwin excludes zombies from group signalling and reports EPERM even
	// though this process belongs to us and has already exited.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("expected zombie-only group EPERM, got %v", err)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup exited group: %v", err)
	}
}

func TestDarwinCleanupPreservesLiveGroupAndInspectionErrors(t *testing.T) {
	originalMembers := runtimeProcessGroupMembers
	originalKill := runtimeKillProcessGroup
	t.Cleanup(func() { runtimeProcessGroupMembers = originalMembers; runtimeKillProcessGroup = originalKill })
	cases := []struct {
		name       string
		members    []unix.KinfoProc
		inspectErr error
		killErr    error
		wantErr    error
	}{
		{name: "live", members: []unix.KinfoProc{{Proc: unix.ExternProc{P_stat: 2}}}, killErr: syscall.EPERM, wantErr: syscall.EPERM},
		{name: "mixed", members: []unix.KinfoProc{{Proc: unix.ExternProc{P_stat: darwinProcessZombie}}, {Proc: unix.ExternProc{P_stat: 2}}}, killErr: syscall.EPERM, wantErr: syscall.EPERM},
		{name: "inspection failure", inspectErr: syscall.EACCES, killErr: syscall.EPERM, wantErr: syscall.EPERM},
		{name: "empty", killErr: syscall.EPERM},
		{name: "zombie", members: []unix.KinfoProc{{Proc: unix.ExternProc{P_stat: darwinProcessZombie}}}, killErr: syscall.EPERM},
		{name: "other signal error", killErr: syscall.EINVAL, wantErr: syscall.EINVAL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtimeKillProcessGroup = func(pid int, signal syscall.Signal) error {
				if pid != -42 || signal != syscall.SIGKILL {
					t.Fatalf("unexpected signal: %d, %v", pid, signal)
				}
				return tc.killErr
			}
			runtimeProcessGroupMembers = func(name string, args ...int) ([]unix.KinfoProc, error) {
				if name != "kern.proc.pgrp" || len(args) != 1 || args[0] != 42 {
					t.Fatalf("unexpected process query: %s %v", name, args)
				}
				if !errors.Is(tc.killErr, syscall.EPERM) {
					t.Fatal("queried group for unrelated signal error")
				}
				return tc.members, tc.inspectErr
			}
			if err := cleanupRuntimeProcessGroup(42); !errors.Is(err, tc.wantErr) {
				t.Fatalf("cleanup error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
