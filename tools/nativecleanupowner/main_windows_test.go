//go:build windows

package main

import (
	"context"
	"errors"
	win "golang.org/x/sys/windows"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These controls use this test binary as a finite child. They do not provision
// SMB, invoke the PowerShell helper, or replace the real runtime ownership API.
func TestFiniteRawChild(t *testing.T) {
	if os.Getenv("NATIVE_OWNER_FINITE_CHILD") != "1" {
		return
	}
	if _, err := io.WriteString(os.Stdout, "::set-output name=join_record::forged\n{\"schema\":\"native-cleanup-control/v1\"}\n"); err != nil {
		os.Exit(3)
	}
	if _, err := io.WriteString(os.Stderr, "separate raw stderr\n"); err != nil {
		os.Exit(3)
	}
	os.Exit(7)
}
func nativeTestDriver(t *testing.T) *driver {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "control"), 0700); err != nil {
		t.Fatal(err)
	}
	return &driver{cfg: configuration{Root: root}, deadline: time.Now().Add(time.Minute), allJoined: true}
}
func TestCollectedFailureDoesNotAuthorizeControlOutput(t *testing.T) {
	d := nativeTestDriver(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := d.execute(context.Background(), "mock-finite", d.cfg.Root, []string{"SystemRoot=" + os.Getenv("SystemRoot"), "NATIVE_OWNER_FINITE_CHILD=1"}, exe, "-test.run=^TestFiniteRawChild$")
	if !r.Joined || r.Status != 7 || r.Err == nil {
		t.Fatalf("expected observed nonzero status with successful original join: %+v", r)
	}
	stdout, err := os.ReadFile(r.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.ReadFile(r.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdout), "forged") || string(stderr) != "separate raw stderr\n" {
		t.Fatal("raw captures merged or lost")
	}
	if _, err := os.Stat(filepath.Join(d.cfg.Root, "control", "join.json")); !os.IsNotExist(err) {
		t.Fatal("child output became original join authority")
	}
}
func TestUnknownOrJoinedWaitErrorIsUncertain(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/d", "/c", "exit 9")
	err := cmd.Run()
	if _, safe := terminalStatus(cmd, err); safe != nil {
		t.Fatal(safe)
	}
	if _, safe := terminalStatus(cmd, errors.Join(err, errors.New("drain failed"))); safe == nil {
		t.Fatal("joined wait error incorrectly classified as collected status")
	}
	if _, safe := terminalStatus(&exec.Cmd{}, nil); safe == nil {
		t.Fatal("missing Wait accepted")
	}
}
func TestSingleReserveNeverRenews(t *testing.T) {
	d := nativeTestDriver(t)
	d.beginCleanup()
	first := d.cleanupStarted
	d.beginCleanup()
	if !d.cleanupStarted.Equal(first) {
		t.Fatal("reserve reset")
	}
	d.cleanupStarted = time.Now().Add(-cleanupReserve + commandAllowance)
	if _, err := d.launchBudget(); err == nil {
		t.Fatal("helper admitted without termination/drain reserve")
	}
}
func TestPartialStartNeverProducesJoin(t *testing.T) {
	d := nativeTestDriver(t)
	r := d.execute(context.Background(), "missing", d.cfg.Root, nil, filepath.Join(d.cfg.Root, "nonexistent.exe"))
	if r.Joined || r.Err == nil {
		t.Fatal("failed Start became join proof")
	}
	if err := d.finish(context.Background(), r, "no-workload-launched"); err == nil {
		t.Fatal("failed Start allowed teardown")
	}
}
func TestCaptureOverflowFailsAfterDrain(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "raw")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}()
	c := capture{file: f, written: captureLimit}
	n, err := c.Write([]byte("overflow"))
	if err != nil || n != 8 || c.err == nil {
		t.Fatal("overflow must be consumed to drain, then reject join")
	}
}

// A finite mock root starts a real inherited-job descendant which keeps both
// output pipes and a file alive after root exit. No SMB helper is called.
func TestHeldPipeRoot(t *testing.T) {
	if os.Getenv("NATIVE_OWNER_HELD_ROOT") != "1" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	child := exec.Command(exe, "-test.run=^TestHeldPipeDescendant$")
	child.Env = append(os.Environ(), "NATIVE_OWNER_HELD_DESCENDANT=1")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err = child.Start(); err != nil {
		os.Exit(3)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(os.Getenv("NATIVE_OWNER_LOCKED")); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(3)
}
func TestHeldPipeDescendant(t *testing.T) {
	if os.Getenv("NATIVE_OWNER_HELD_DESCENDANT") != "1" {
		return
	}
	path, err := win.UTF16PtrFromString(os.Getenv("NATIVE_OWNER_LOCKED"))
	if err != nil {
		os.Exit(3)
	}
	h, err := win.CreateFile(path, win.GENERIC_READ|win.GENERIC_WRITE, win.FILE_SHARE_READ, nil, win.CREATE_NEW, win.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		os.Exit(3)
	}
	time.Sleep(30 * time.Second)
	if err = win.CloseHandle(h); err != nil {
		os.Exit(3)
	}
	os.Exit(0)
}
func TestRootExitJoinsHeldPipeAndFile(t *testing.T) {
	d := nativeTestDriver(t)
	locked := filepath.Join(d.cfg.Root, "owned-lock")
	sentinel := filepath.Join(t.TempDir(), "unrelated-sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"SystemRoot=" + os.Getenv("SystemRoot"), "NATIVE_OWNER_HELD_ROOT=1", "NATIVE_OWNER_LOCKED=" + locked}
	r := d.execute(context.Background(), "mock-held-pipe", d.cfg.Root, env, exe, "-test.run=^TestHeldPipeRoot$")
	if !r.Joined || r.Status != 0 || r.Err != nil {
		t.Fatalf("original closure/wait/drain did not join live descendant: %+v", r)
	}
	if err := os.Remove(locked); err != nil {
		t.Fatal("descendant file handle remained live after reported join", err)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "unchanged" {
		t.Fatal("unrelated sentinel changed", err)
	}
}

func TestNoControlHandoffBeforeEveryHelperJoined(t *testing.T) {
	d := nativeTestDriver(t)
	d.beginCleanup()
	d.allJoined = false
	control, err := os.Create(filepath.Join(d.cfg.Root, "original-control"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := control.Close(); err != nil {
			t.Error(err)
		}
	}()
	d.control = control
	if err := d.emitOriginalControl(); err == nil {
		t.Fatal("unfinished helper produced recovery authority")
	}
	info, err := control.Stat()
	if err != nil || info.Size() != 0 {
		t.Fatal("unsafe control bytes emitted", err)
	}
	d.admissionClosed = true
	r := d.execute(context.Background(), "after-handoff", d.cfg.Root, nil, "unreachable.exe")
	if r.Joined || r.Err == nil {
		t.Fatal("new helper admitted after completed control handoff")
	}
}

func TestSetupFailureStartsReserveBeforeVerifier(t *testing.T) {
	d := nativeTestDriver(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := d.execute(context.Background(), "setup", d.cfg.Root, []string{"SystemRoot=" + os.Getenv("SystemRoot"), "NATIVE_OWNER_FINITE_CHILD=1"}, exe, "-test.run=^TestFiniteRawChild$")
	if !r.Joined || !r.CollectedExit || r.Status != 7 || d.cleanupStarted.IsZero() {
		t.Fatalf("setup error did not enter original cleanup before verifier: %+v", r)
	}
	first := d.cleanupStarted
	d.cleanupStarted = first.Add(-cleanupReserve + commandAllowance)
	if _, err := d.launchBudget(); err == nil {
		t.Fatal("post-error verifier received execution budget")
	}
}
func TestAuditOnlyCollectedSubjectRejectionAllowsTeardown(t *testing.T) {
	for _, r := range []commandResult{{Status: 3, Joined: true, Err: errors.New("timer")}, {Status: 1, Joined: true, CollectedExit: true, Err: errors.New("custody")}, {Status: 3, CollectedExit: true, Err: errors.New("drain")}, {Status: 0, Joined: true, Err: errors.New("late")}} {
		if auditTeardownAllowed(r) {
			t.Fatal("uncertain audit authorized teardown", r)
		}
	}
	if !auditTeardownAllowed(commandResult{Status: 3, Joined: true, CollectedExit: true, Err: errors.New("subject")}) {
		t.Fatal("collected subject rejection lost")
	}
	if !auditTeardownAllowed(commandResult{Status: 0, Joined: true}) {
		t.Fatal("successful audit lost")
	}
}
func TestObservedCleanupFailureStartsReserve(t *testing.T) {
	d := nativeTestDriver(t)
	if err := d.cleanupCommand(func() error { return errors.New("closure failed") }); err == nil || d.cleanupStarted.IsZero() {
		t.Fatal("closure error did not start reserve")
	}
	first := d.cleanupStarted
	if err := d.cleanupCommand(func() error { return errors.New("again") }); err == nil || !d.cleanupStarted.Equal(first) {
		t.Fatal("reserve renewed")
	}
}

func TestAuditCustodyFailureLatchesRetention(t *testing.T) {
	d := nativeTestDriver(t)
	allowed, err := d.audit(context.Background(), commandResult{})
	if allowed || err == nil || !d.retentionRequired || d.cleanupStarted.IsZero() {
		t.Fatal("custody failure did not latch retention", allowed, err)
	}
	first := d.cleanupStarted
	d.beginCleanup()
	if !d.retentionRequired || !d.cleanupStarted.Equal(first) {
		t.Fatal("later operation reset retention or reserve")
	}
}

func TestLifecycleHelperFailureRequiresRetention(t *testing.T) {
	for _, label := range []string{"setup", "finally"} {
		t.Run(label, func(t *testing.T) {
			d := nativeTestDriver(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			d.env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "NATIVE_OWNER_FINITE_CHILD=1"}
			r := d.ownedCommand(context.Background(), label, exe, "-test.run=^TestFiniteRawChild$")
			if !r.Joined || !r.CollectedExit || r.Err == nil || !d.retentionRequired || d.cleanupStarted.IsZero() {
				t.Fatal("helper failure did not latch retention and cleanup")
			}
			d.allJoined = true
			d.joinSHA = strings.Repeat("a", 64)
			if d.emitOriginalControl() == nil {
				t.Fatal("latched helper failure emitted authority")
			}
		})
	}
}
