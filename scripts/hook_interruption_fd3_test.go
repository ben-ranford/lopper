//go:build linux

package scripts

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFD3Dup2AcceptsSplitStraceLine keeps the single-line dup2(0x…, 0x3) = 0x3
// requirement and accepts the strace 6.19 unfinished/resumed pair from #1909.
func TestFD3Dup2AcceptsSplitStraceLine(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	script := filepath.Join(filepath.Dir(filename), "fd3_dup2.py")
	command := exec.Command(python, "-c", fd3Dup2Probe, script)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("FD3 dup2 contract: %v\n%s", err, output)
	}
}

const fd3Dup2Probe = `
import re
import runpy
import sys

try:
    check = runpy.run_path(sys.argv[1], run_name="fd3_probe")["re_search_fd3"]
except FileNotFoundError:
    def check(trace):
        return re.search(rb"dup2\(0x[0-9a-f]+, 0x3\)\s+=\s+0x3", trace) is not None
accepted = (
    b"12 1.0 dup2(0x5, 0x3) = 0x3 <0.01>\n",
    b"dup2(0xa, 0x3) = 0x3",
    b"317919 1794362079.615578 dup2(0x5, 0x3 <unfinished ...>\n"
    b"317919 1794362079.615610 <... dup2 resumed>) = 0x3 <0.000024>\n",
    b"dup2(0x5, 0x3 <unfinished ...>\n<... dup2 resumed>) = 0x3 <0.000024>\n",
    b"317919 1794362079.615578 dup2(0x5, 0x3 <unfinished ...>\n"
    b"317920 1794362079.615590 close(0x4) = 0\n"
    b"[pid 317919] 1794362079.615610 <... dup2 resumed>) = 0x3 <0.000024>\n",
)
rejected = (
    b"dup2(0x5, 0x3) = -1 EBADF (Bad file descriptor)\n",
    b"dup2(0x5, 0x4) = 0x4\n",
    b"dup2(0x5, 0x4 <unfinished ...>\n<... dup2 resumed>) = 0x4\n",
    b"dup2(0x5, 0x3 <unfinished ...>\n<... dup2 resumed>) = -1 EINVAL (Invalid argument)\n",
    b"dup2(0x5, 0x3 <unfinished ...>\n<... dup2 resumed>) = 0x5\n",
    b"dup2(0x5, 0x3 <unfinished ...>\n<... wait4 resumed>) = 0x3\n",
    b"<... dup2 resumed>) = 0x3 <0.000024>\n",
    b"dup2(0x5, 0x3 <unfinished ...>\n",
    b"317919 1.0 dup2(0x5, 0x3 <unfinished ...>\n"
    b"317920 1.1 close(0x4) = 0\n"
    b"317920 1.2 <... dup2 resumed>) = 0x3 <0.000024>\n",
    b"317919 1.0 dup2(0x5, 0x3 <unfinished ...>\n"
    b"317920 1.1 close(0x4) = 0\n"
    b"317919 1.2 <... dup2 resumed>) = 0x5\n",
    b"317919 1.0 dup2(0x5, 0x4 <unfinished ...>\n"
    b"317920 1.1 close(0x4) = 0\n"
    b"317919 1.2 <... dup2 resumed>) = 0x4\n",
)
failures = []
for trace in accepted:
    if not check(trace):
        failures.append("rejected " + repr(trace))
for trace in rejected:
    if check(trace):
        failures.append("accepted " + repr(trace))
if failures:
    raise SystemExit("\n".join(failures))
`
