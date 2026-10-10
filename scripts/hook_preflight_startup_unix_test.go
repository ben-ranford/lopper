//go:build !windows

package scripts

import (
	"context"
	"errors"
	"io"
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
		{name: "other group after launch notice", notice: "other prefixed", wantStatus: 1},
		{name: "existing verified launch notice", notice: "expected prefixed", wantStatus: 7, wantReader: true},
		{name: "duplicate verified launch notices", notice: "duplicate", wantStatus: 1},
		{name: "missing owned group", noGroup: true, wantStatus: 1},
		{name: "diagnostic without owned group", noGroup: true, notice: "expected", wantStatus: 1},
		{name: "interrupted startup", interrupt: true, wantStatus: 124},
		{name: "anchor exits before permit", notice: "exited", wantStatus: 1},
		{name: "exited anchor forwards unverified diagnostic", notice: "exited diagnostic", wantStatus: 1},
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
	sentinel := preflightSentinel(t, "startup")
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
	assertPreflightExitWitness(t, tc.notice, pidFile)
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
	prefix := assertPreflightLaunchCapture(t, tc, pidFile, pid)
	assertPreflightStartupDiagnostic(t, tc, pid, prefix, stderr)
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

func assertPreflightStartupDiagnostic(t *testing.T, tc preflightStartupCase, pid int, prefix, stderr string) {
	t.Helper()
	if !tc.wantReader && !tc.interrupt {
		wantErr := prefix + preflightStartupNotice(tc.notice, pid)
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
	pidPath := shellQuote(pidFile)
	if preflightCapturesLaunch(notice, noGroup, interrupt) {
		pidPath = preflightStartupPath(pidFile)
	}
	// Delay only the launch boundary and inject the kernel diagnostic there.
	// The reader remains real and cannot start until the production guard allows it.
	injection := `	while [ ! -f "$state_dir/test-anchor-pid" ]; do :; done
	while ! read -r test_anchor_pid <"$state_dir/test-anchor-pid"; do :; done
	printf "%s\n" "$test_anchor_pid" >` + pidPath + "\n"
	capture := preflightCapturesLaunch(notice, noGroup, interrupt)
	if capture {
		injection += preflightLaunchBarrier(notice, pidFile)
	}
	diagnostic := preflightStartupInjection(notice)
	if capture && (notice == "expected" || notice == "expected prefixed") {
		diagnostic = "\tif [ ! -s " + preflightStartupPath(pidFile+".prefix") + " ]; then\n" + diagnostic + "\tfi\n"
	}
	injection += diagnostic
	exited := notice == "exited" || notice == "exited diagnostic"
	if exited {
		injection += "\tprintf \"%s\\n\" \"$test_anchor_pid\" >" + shellQuote(pidFile+".reached") + "\n\texit 1\n"
	}
	if interrupt {
		injection += "\tkill -TERM \"$supervisor_pid\"\n"
	}
	const restore = "\texec 2>&3 3>&-\n"
	if strings.Count(source, restore) != 1 {
		t.Fatal("startup diagnostic boundary unavailable")
	}
	source = strings.Replace(source, restore, injection+restore, 1)
	source = replacePreflightStartupBoundary(t, source, ") & reader_group=$!\n", ") & reader_group=$!\nprintf \"%s\\n\" \"$reader_group\" >\"$state_dir/test-anchor-pid\"\n")
	if capture {
		source = preflightLaunchObserver(t, source, notice, pidFile)
	}
	if exited {
		source = preflightExitObserver(t, source, pidFile)
	}
	if noGroup || exited {
		// Live no-group fixtures require the real negative-PGID probe to fail.
		// Exited fixtures instead isolate the earlier dead-child rejection.
		source = preflightDisableJobControl(t, source)
	}
	return source
}

func preflightStartupInjection(notice string) string {
	injection := ""
	switch notice {
	case "expected", "multiline", "nul", "unterminated", "exited diagnostic", "expected prefixed", "duplicate":
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
	case "unknown":
		injection += "\tprintf \"unknown startup diagnostic\\n\" >&2\n"
	case "other", "other prefixed":
		injection += "\tprintf \"%s\\n\" \"--: child setpgid ($test_anchor_pid to 1): Operation not permitted\" >&2\n"
	}
	return injection
}

func preflightCapturesLaunch(notice string, noGroup, interrupt bool) bool {
	return notice != "" && !noGroup && !interrupt && notice != "exited" && notice != "exited diagnostic"
}

// The generated observer lives inside the production single-quoted Bash body.
func preflightStartupPath(path string) string {
	return strings.ReplaceAll(shellQuote(path), "'", "'\"'\"'")
}

func replacePreflightStartupBoundary(t *testing.T, source, before, after string) string {
	t.Helper()
	if strings.Count(source, before) != 1 {
		t.Fatalf("unique startup observation boundary unavailable: %q", before)
	}
	return strings.Replace(source, before, after, 1)
}

func preflightLaunchRecords() string {
	return `test_read_capture_record() {
	[ -e "$1" ] || return 1
	[ -f "$1" ] && [ -r "$1" ] || return 3
	test_record=
	test_tail=
	test_nul=
	if IFS= read -r -d "" test_nul <"$1"; then return 2; fi
	{
		IFS= read -r test_record || return 1
		if IFS= read -r test_tail || [ -n "$test_tail" ]; then return 2; fi
	} <"$1" || return 3
	return 0
}
test_wait_capture_record() {
	while :; do
		[ -z "$interrupted" ] && [ -z "$test_barrier_interrupted" ] && [ ! -f "$state_dir/expired" ] && kill -0 "$2" 2>/dev/null || return 3
		test_record_status=0
		test_read_capture_record "$1" || test_record_status=$?
		case "$test_record_status" in 0) return 0 ;; 1) ;; *) return "$test_record_status" ;; esac
	done
}
`
}

func preflightLaunchBarrier(notice, pidFile string) string {
	allowed := `[ "$test_record" = "$test_anchor_pid none" ] || exit 1`
	if preflightSeedsLaunch(notice) {
		allowed = `case "$test_record" in "$test_anchor_pid append"|"$test_anchor_pid retain") ;; *) exit 1 ;; esac`
	}
	return `	test_barrier_interrupted=
	trap "test_barrier_interrupted=1" HUP INT TERM
	printf "%s\n" "$test_anchor_pid" >` + preflightStartupPath(pidFile+".reached") + ` || exit 1
	test_wait_capture_record ` + preflightStartupPath(pidFile+".request") + ` "$supervisor_pid" || exit 1
	` + allowed + `
	test_seed_action=${test_record#* }
	if [ "$test_seed_action" = append ]; then
		printf "%s\n" "--: child setpgid ($test_anchor_pid to $test_anchor_pid): Operation not permitted" >&2 || exit 1
	fi
	printf "%s %s\n" "$test_anchor_pid" "$test_seed_action" >` + preflightStartupPath(pidFile+".seeded") + ` || exit 1
	test_wait_capture_record ` + preflightStartupPath(pidFile+".complete") + ` "$supervisor_pid" || exit 1
	[ "$test_record" = "$test_anchor_pid" ] || exit 1
	trap - HUP INT TERM
`
}

func preflightCaptureAbort(pidFile string) string {
	return `test_capture_abort() {
	printf "launch prefix capture failed\n" >&2
	if reader_group_is_running_or_interrupted; then
		kill -KILL "$reader_group" || { printf "launch prefix anchor kill failed\n" >&2; exit 1; }
	fi
	while :; do
		test_capture_status=0
		wait "$reader_group" || test_capture_status=$?
		[ "$test_capture_status" -ne 127 ] || { printf "launch prefix anchor join failed\n" >&2; exit 1; }
		kill -0 "$reader_group" 2>/dev/null || break
	done
	printf "%s\n" "$test_capture_status" >` + preflightStartupPath(pidFile+".capture-joined") + ` || { printf "launch prefix join witness failed\n" >&2; exit 1; }
	exit 1
}
`
}

func preflightLaunchObserver(t *testing.T, source, notice, pidFile string) string {
	t.Helper()
	const launch = "\nset -m\n"
	source = replacePreflightStartupBoundary(t, source, launch, "\n"+preflightLaunchRecords()+"set -m\n")
	const boundary = "} 2>\"$state_dir/launch-error\"\nexec 3>&-\n"
	observer := preflightCaptureAbort(pidFile) + `test_wait_capture_record ` + preflightStartupPath(pidFile+".reached") + ` "$reader_group" || test_capture_abort
[ "$test_record" = "$reader_group" ] || test_capture_abort
cat "$state_dir/launch-error" >` + preflightStartupPath(pidFile+".natural") + ` || test_capture_abort
printf "%s\n" "--: child setpgid ($reader_group to $reader_group): Operation not permitted" >` + preflightStartupPath(pidFile+".expected") + ` || test_capture_abort
if ! cmp -s ` + preflightStartupPath(pidFile+".natural") + ` /dev/null; then
	cmp -s ` + preflightStartupPath(pidFile+".natural") + ` ` + preflightStartupPath(pidFile+".expected") + ` || test_capture_abort
fi
` + preflightSeedLaunchNotice(notice, pidFile)
	observer += `printf "%s %s\n" "$reader_group" "$test_seed_action" >` + preflightStartupPath(pidFile+".request") + ` || test_capture_abort
test_wait_capture_record ` + preflightStartupPath(pidFile+".seeded") + ` "$reader_group" || test_capture_abort
[ "$test_record" = "$reader_group $test_seed_action" ] || test_capture_abort
cat "$state_dir/launch-error" >` + preflightStartupPath(pidFile+".prefix") + ` || test_capture_abort
cmp -s ` + preflightStartupPath(pidFile+".prefix") + ` "$test_prefix_expected" || test_capture_abort
[ -z "$interrupted" ] && [ ! -f "$state_dir/expired" ] || test_capture_abort
printf "%s\n" "$reader_group" >` + preflightStartupPath(pidFile+".complete") + ` || test_capture_abort
`
	source = replacePreflightStartupBoundary(t, source, boundary, boundary+observer)
	const admission = "fi\nstartup_failed=\n"
	witness := "fi\nprintf \"%s\\n\" \"$reader_group\" >" + preflightStartupPath(pidFile+".group") + " || exit 1\nstartup_failed=\n"
	return replacePreflightStartupBoundary(t, source, admission, witness)
}

func preflightSeedsLaunch(notice string) bool {
	return notice == "other prefixed" || notice == "expected prefixed" || notice == "duplicate"
}

func preflightSeedLaunchNotice(notice, pidFile string) string {
	if !preflightSeedsLaunch(notice) {
		return "test_seed_action=none\ntest_prefix_expected=" + preflightStartupPath(pidFile+".natural") + "\n"
	}
	return `test_seed_action=retain
if [ ! -s ` + preflightStartupPath(pidFile+".natural") + ` ]; then test_seed_action=append; fi
test_prefix_expected=` + preflightStartupPath(pidFile+".expected") + "\n"
}

func readPreflightLaunchPrefix(path string, pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid launch prefix PID")
	}
	expected := preflightStartupNotice("expected", pid)
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected)+1)))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return "", err
	}
	prefix := string(data)
	if prefix != "" && prefix != expected {
		return "", errors.New("invalid launch prefix bytes")
	}
	return prefix, nil
}

func assertPreflightLaunchCapture(t *testing.T, tc preflightStartupCase, pidFile string, pid int) string {
	t.Helper()
	if !preflightCapturesLaunch(tc.notice, tc.noGroup, tc.interrupt) {
		return ""
	}
	for _, suffix := range []string{"", ".reached", ".complete", ".group"} {
		data, err := os.ReadFile(pidFile + suffix)
		if err != nil || string(data) != strconv.Itoa(pid)+"\n" {
			t.Fatalf("launch witness %s: got=%q pid=%d err=%v", suffix, data, pid, err)
		}
	}
	natural, err := readPreflightLaunchPrefix(pidFile+".natural", pid)
	if err != nil {
		t.Fatalf("natural launch prefix: %v", err)
	}
	prefix, err := readPreflightLaunchPrefix(pidFile+".prefix", pid)
	if err != nil {
		t.Fatalf("captured launch prefix: %v", err)
	}
	assertPreflightSeedWitness(t, tc.notice, pidFile, pid, natural, prefix)
	return prefix
}

func assertPreflightSeedWitness(t *testing.T, notice, pidFile string, pid int, natural, prefix string) {
	t.Helper()
	action := "none"
	wantPrefix := natural
	if preflightSeedsLaunch(notice) {
		action = "retain"
		if natural == "" {
			action = "append"
		}
		wantPrefix = preflightStartupNotice("expected", pid)
	}
	want := strconv.Itoa(pid) + " " + action + "\n"
	for _, suffix := range []string{".request", ".seeded"} {
		data, err := os.ReadFile(pidFile + suffix)
		if err != nil || string(data) != want || prefix != wantPrefix {
			t.Fatalf("seeded launch prefix changed: phase=%s got=%q want=%q prefix=%q err=%v", suffix, data, want, prefix, err)
		}
	}
}

func preflightStartupNotice(notice string, pid int) string {
	number := strconv.Itoa(pid)
	expected := "--: child setpgid (" + number + " to " + number + "): Operation not permitted"
	switch notice {
	case "expected", "exited diagnostic", "expected prefixed", "duplicate":
		return expected + "\n"
	case "multiline":
		return expected + "\nadditional diagnostic\n"
	case "nul":
		return expected + "\n\x00"
	case "unterminated":
		return expected
	case "unknown":
		return "unknown startup diagnostic\n"
	case "other", "other prefixed":
		return "--: child setpgid (" + number + " to 1): Operation not permitted\n"
	default:
		return ""
	}
}

// Only deliberately exited fixtures observe the actual child before cleanup.
// Keeping launch job control off isolates child-exit admission from host launch
// diagnostics; the established-group cases still exercise real process groups.
func preflightExitObserver(t *testing.T, source, pidFile string) string {
	t.Helper()
	const rejection = "\t\tcat \"$state_dir/launch-error\" >&2\n\t\texit 1\n"
	if strings.Count(source, rejection) != 1 {
		t.Fatal("unique early anchor rejection boundary unavailable")
	}
	observer := `		test_exit_status=0
		wait "$reader_group" || test_exit_status=$?
		printf "%s\n" "$test_exit_status" >` + shellQuote(pidFile+".joined") + "\n" +
		`		if [ ! -f "$state_dir/ready" ] && [ ! -f "$state_dir/start" ]; then
			printf x >` + shellQuote(pidFile+".unready") + "\n\t\tfi\n"
	return strings.Replace(source, rejection, observer+rejection, 1)
}

func assertPreflightExitWitness(t *testing.T, notice, pidFile string) {
	t.Helper()
	if notice != "exited" && notice != "exited diagnostic" {
		return
	}
	pid, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	for suffix, want := range map[string]string{
		".reached": string(pid), ".joined": "1\n", ".unready": "x",
	} {
		got, err := os.ReadFile(pidFile + suffix)
		if err != nil || string(got) != want {
			t.Fatalf("anchor exit witness %s: got=%q want=%q err=%v", suffix, got, want, err)
		}
	}
}

func preflightDisableJobControl(t *testing.T, source string) string {
	t.Helper()
	const launch = "\nset -m\n"
	if strings.Count(source, launch) != 1 {
		t.Fatal("unique job-control launch boundary unavailable")
	}
	return strings.Replace(source, launch, "\nset +m\n", 1)
}

func preflightSentinel(t *testing.T, label string) *exec.Cmd {
	t.Helper()
	sentinel := exec.Command("sleep", "60")
	if err := sentinel.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sentinel.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Errorf("clean up %s sentinel: %v", label, err)
		}
		var exitErr *exec.ExitError
		if err := sentinel.Wait(); err != nil && !errors.As(err, &exitErr) {
			t.Errorf("wait for %s sentinel: %v", label, err)
		}
	})
	return sentinel
}
