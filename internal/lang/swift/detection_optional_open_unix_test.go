//go:build !windows

package swift

import (
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

const swiftOptionalProbeOpenEMFILEChildEnv = "LOPPER_SWIFT_OPTIONAL_PROBE_OPEN_EMFILE_CHILD"

func TestSwiftOptionalProbeTreatsRootOpenEMFILEAsInconclusive(t *testing.T) {
	if os.Getenv(swiftOptionalProbeOpenEMFILEChildEnv) == "1" {
		runSwiftOptionalProbeOpenEMFILEChild(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSwiftOptionalProbeTreatsRootOpenEMFILEAsInconclusive$")
	cmd.Env = append(os.Environ(), swiftOptionalProbeOpenEMFILEChildEnv+"=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run child test process: %v\n%s", err, output)
	}
}

func runSwiftOptionalProbeOpenEMFILEChild(t *testing.T) {
	repo := t.TempDir()
	var oldLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &oldLimit); err != nil {
		t.Fatalf("get RLIMIT_NOFILE: %v", err)
	}
	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &oldLimit); err != nil {
			t.Fatalf("restore RLIMIT_NOFILE: %v", err)
		}
	}()
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: 0, Max: oldLimit.Max}); err != nil {
		t.Fatalf("set RLIMIT_NOFILE: %v", err)
	}

	found, entries, err := probeSwiftSourceWithinRoot(context.Background(), repo, 1)
	if err != nil || found || entries != 0 {
		t.Errorf("root open EMFILE result found=%v entries=%d err=%v", found, entries, err)
	}
}
