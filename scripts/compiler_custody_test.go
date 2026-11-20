//go:build linux

package scripts

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCompilerCustodyAdmitsWritableToolcache copies a mode 0777 toolchain into a
// private tree and accepts it only when the copy matches the official content digest.
func TestCompilerCustodyAdmitsWritableToolcache(t *testing.T) {
	runCompilerCustody(t, "admit")
}

// TestCompilerCustodyRejectsTamperedBinary fails closed when the writable
// toolchain's go binary differs from the digest, and deletes the private copy.
func TestCompilerCustodyRejectsTamperedBinary(t *testing.T) {
	runCompilerCustody(t, "tamper")
}

func runCompilerCustody(t *testing.T, mode string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	script := filepath.Join(filepath.Dir(filename), "trace-hook-interruption.py")
	command := exec.Command(python, "-c", compilerCustodyProbe, script, mode)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("compiler custody %s: %v\n%s", mode, err, output)
	}
}

const compilerCustodyProbe = `
import runpy
import sys

api = runpy.run_path(sys.argv[1], run_name="custody_probe")
check = api.get("assert_compiler_custody")
if check is None:
    raise SystemExit("compiler custody seal is missing")
check(sys.argv[2])
`
