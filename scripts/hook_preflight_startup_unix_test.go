//go:build !windows

package scripts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type preflightStartupCase struct {
	name, notice       string
	noGroup, interrupt bool
	wantStatus         int
	wantReader         bool
}

func TestHookPreflightRequiresOwnedGroupBeforeReader(t *testing.T) {
	for _, tc := range []preflightStartupCase{
		{name: "established group", wantStatus: 7, wantReader: true},
		{name: "verified setpgid diagnostic", notice: "expected", wantStatus: 7, wantReader: true},
		{name: "unknown diagnostic", notice: "unknown", wantStatus: 1},
		{name: "additional diagnostic", notice: "multiline", wantStatus: 1},
		{name: "trailing nul diagnostic", notice: "nul", wantStatus: 1},
		{name: "unterminated diagnostic", notice: "unterminated", wantStatus: 1},
		{name: "other group diagnostic", notice: "other", wantStatus: 1},
		{name: "missing owned group", noGroup: true, wantStatus: 1},
		{name: "diagnostic without owned group", noGroup: true, notice: "expected", wantStatus: 1},
		{name: "interrupted startup", interrupt: true, wantStatus: 124},
		{name: "anchor exits before permit", notice: "exited", wantStatus: 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertPreflightStartup(t, tc) })
	}
}

func assertPreflightStartup(t *testing.T, tc preflightStartupCase) {
	t.Helper()
	tmp := t.TempDir()
	state := filepath.Join(tmp, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, pidFile := filepath.Join(tmp, "reader-started"), filepath.Join(tmp, "anchor-pid")
	source := preflightStartupFixture(t, tc.notice, tc.noGroup, tc.interrupt, pidFile)
	helper := filepath.Join(tmp, "preflight.sh")
	writeFile(t, helper, source)
	sentinel := exec.Command("sleep", "60")
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sentinel.Process.Kill(); _ = sentinel.Wait() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-c", `. "$1"
trap cleanup_preflight_git EXIT
run_preflight_git sh -c 'printf x >"$1"; printf "reader output\n"; printf "reader diagnostic\n" >&2; exit 7' sh "$2"
`, "sh", helper, marker)
	command.Env = append(os.Environ(), "TMPDIR="+state)
	command.WaitDelay = time.Second
	stdout, stderr, status := preflightDiagnosticResult(t, command)
	if ctx.Err() != nil || status != tc.wantStatus {
		t.Fatalf("status=%d want=%d context=%v stderr=%q", status, tc.wantStatus, ctx.Err(), stderr)
	}
	assertPreflightStartupStreams(t, tc, marker, stdout, stderr)
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("anchor %d survived: %v", pid, err)
	}
	if err := sentinel.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated sentinel changed: %v", err)
	}
	if entries, err := os.ReadDir(state); err != nil || len(entries) != 0 {
		t.Fatalf("startup left state: %v %v", entries, err)
	}
	assertPreflightStartupDiagnostic(t, tc, pid, stderr)
}

func assertPreflightStartupStreams(t *testing.T, tc preflightStartupCase, marker, stdout, stderr string) {
	t.Helper()
	_, markerErr := os.Stat(marker)
	if (markerErr == nil) != tc.wantReader {
		t.Fatalf("reader started=%t want=%t: %v", markerErr == nil, tc.wantReader, markerErr)
	}
	if tc.wantReader {
		if stdout != "reader output\n" || stderr != "reader diagnostic\n" {
			t.Fatalf("reader streams changed: stdout=%q stderr=%q", stdout, stderr)
		}
	} else if stdout != "" {
		t.Fatalf("unexpected reader output %q", stdout)
	}
}

func assertPreflightStartupDiagnostic(t *testing.T, tc preflightStartupCase, pid int, stderr string) {
	t.Helper()
	if !tc.wantReader && !tc.interrupt {
		wantErr := preflightStartupNotice(tc.notice, pid)
		if tc.noGroup {
			wantErr += "Could not establish Git preflight process group\n"
		}
		if stderr != wantErr {
			t.Fatalf("startup diagnostic changed: got=%q want=%q", stderr, wantErr)
		}
	}
}

func preflightStartupFixture(t *testing.T, notice string, noGroup, interrupt bool, pidFile string) string {
	t.Helper()
	contents, err := os.ReadFile("hook-config-preflight.sh")
	if err != nil {
		t.Fatal(err)
	}
	source := string(contents)
	// Delay only the launch boundary and inject the kernel diagnostic there.
	// The reader remains real and cannot start until the production guard allows it.
	injection := `	while [ ! -f "$state_dir/test-anchor-pid" ]; do :; done
	while ! read -r test_anchor_pid <"$state_dir/test-anchor-pid"; do :; done
	printf "%s\n" "$test_anchor_pid" >` + shellQuote(pidFile) + "\n"
	switch notice {
	case "expected", "multiline", "nul", "unterminated":
		format := "%s\\n"
		if notice == "unterminated" {
			format = "%s"
		}
		injection += "\tprintf \"" + format + "\" \"--: child setpgid ($test_anchor_pid to $test_anchor_pid): Operation not permitted\" >&2\n"
		if notice == "multiline" {
			injection += "\tprintf \"additional diagnostic\\n\" >&2\n"
		}
		if notice == "nul" {
			injection += "\tprintf \"\\0\" >&2\n"
		}
	case "exited":
		injection += "\texit 1\n"
	case "unknown":
		injection += "\tprintf \"unknown startup diagnostic\\n\" >&2\n"
	case "other":
		injection += "\tprintf \"%s\\n\" \"--: child setpgid ($test_anchor_pid to 1): Operation not permitted\" >&2\n"
	}
	if interrupt {
		injection += "\tkill -TERM \"$supervisor_pid\"\n"
	}
	const restore = "\texec 2>&3 3>&-\n"
	if !strings.Contains(source, restore) {
		t.Fatal("startup diagnostic boundary unavailable")
	}
	source = strings.Replace(source, restore, injection+restore, 1)
	source = strings.Replace(source, ") & reader_group=$!\n", ") & reader_group=$!\nprintf \"%s\\n\" \"$reader_group\" >\"$state_dir/test-anchor-pid\"\n", 1)
	if noGroup {
		// Disable job-control group creation in this fixture. The real kernel
		// negative-PGID probe must fail; no fake kill implementation is involved.
		source = strings.Replace(source, "\nset -m\n", "\nset +m\n", 1)
	}
	return source
}

func preflightStartupNotice(notice string, pid int) string {
	number := strconv.Itoa(pid)
	expected := "--: child setpgid (" + number + " to " + number + "): Operation not permitted"
	switch notice {
	case "expected":
		return expected + "\n"
	case "multiline":
		return expected + "\nadditional diagnostic\n"
	case "nul":
		return expected + "\n\x00"
	case "unterminated":
		return expected
	case "unknown":
		return "unknown startup diagnostic\n"
	case "other":
		return "--: child setpgid (" + number + " to 1): Operation not permitted\n"
	default:
		return ""
	}
}
