package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

const stavePTYTimeout = 20 * time.Second

type ptyReadResult struct {
	n   int
	buf []byte
	err error
}

type ptyOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *ptyOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func (o *ptyOutput) Write(p []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf.Write(p)
}

func TestStaveTUIFeatureFlagRendersAndQuitsInPTY(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	fixture := filepath.Join(root, "testdata", "js", "esm")

	cmd := exec.Command(bin, "tui", "--repo", fixture, "--language", "js-ts", "--top", "5", "--enable-feature", "stave-tui-preview")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "CI=")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("start lopper in pty: %v", err)
	}
	defer func() {
		if err := ptmx.Close(); err != nil {
			t.Logf("close pty: %v", err)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Logf("kill process: %v", err)
		}
	}()

	output := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool {
		return strings.Contains(s, "Stave preview") || strings.Contains(s, "Lopper")
	})
	assertNoUnsafeTerminalSequences(t, output)
	if !strings.Contains(output, "Stave preview") {
		t.Fatalf("feature-flagged TUI did not render Stave status frame; output=%q", output)
	}

	if _, err := ptmx.Write([]byte("q")); err != nil {
		t.Fatalf("send quit key: %v", err)
	}
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		t.Fatalf("feature-flagged TUI did not exit after q: %v", err)
	}
}

func TestStaveTUIRedirectedInputUsesLineModeWithPTYOutput(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	fixture := filepath.Join(root, "testdata", "js", "esm")

	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create redirected stdin pipe: %v", err)
	}
	inputWriterOpen := true
	t.Cleanup(func() {
		if inputWriterOpen {
			if err := inputWriter.Close(); err != nil {
				t.Errorf("close redirected stdin writer: %v", err)
			}
		}
	})
	// Start with a controlling PTY, then have the shell replace stdin with the
	// pipe passed as child fd 3. This keeps stdout terminal-capable while the
	// executed lopper process receives redirected stdin.
	cmd := exec.Command("/bin/sh", "-c", "exec 0<&3; exec \"$@\"", "sh", bin, "tui", "--repo", fixture, "--language", "js-ts", "--top", "5", "--enable-feature", "stave-tui-preview")
	cmd.Dir = root
	cmd.Env = terminalCapableTestEnvironment()
	cmd.ExtraFiles = []*os.File{inputReader}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("start lopper with redirected stdin: %v", err)
	}
	if err := inputReader.Close(); err != nil {
		t.Fatalf("close parent stdin reader: %v", err)
	}
	finished := false
	defer func() {
		if !finished {
			closePTYProcess(t, ptmx, cmd)
		}
	}()

	output, readDone := collectPTYOutput(ptmx)
	if _, err := inputWriter.Write([]byte("filter esm\nq\n")); err != nil {
		t.Fatalf("write redirected commands: %v", err)
	}
	if err := inputWriter.Close(); err != nil {
		t.Fatalf("close redirected stdin: %v", err)
	}
	inputWriterOpen = false
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		t.Fatalf("redirected-input TUI did not exit: %v; output=%q", err, output.String())
	}
	if err := ptmx.Close(); err != nil {
		t.Fatalf("close stdout PTY after exit: %v", err)
	}
	finished = true
	<-readDone

	text := output.String()
	if !strings.Contains(text, "filter esm") || !strings.Contains(text, "esm") {
		t.Fatalf("redirected filter command was not processed: %q", text)
	}
	if strings.Contains(text, "\x1b[?1049") || strings.Contains(text, "\x1b[?25") {
		t.Fatalf("redirected stdin entered full-screen terminal mode: %q", text)
	}
}

func terminalCapableTestEnvironment() []string {
	keys := map[string]bool{"TERM": true, "COLORTERM": true, "NO_COLOR": true, "CI": true}
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !keys[key] {
			env = append(env, entry)
		}
	}
	return append(env, "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "CI=")
}

func TestStaveTUIResizeAndInterruptExitWithinBound(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	fixture := filepath.Join(root, "testdata", "js", "esm")
	cmd := exec.Command(bin, "tui", "--repo", fixture, "--language", "js-ts", "--enable-feature", "stave-tui-preview")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "CI=")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start lopper in pty: %v", err)
	}
	defer func() {
		if err := ptmx.Close(); err != nil {
			t.Logf("close pty: %v", err)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Logf("kill process: %v", err)
		}
	}()
	output := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Stave preview") })
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: 42, Rows: 12}); err != nil {
		t.Fatalf("resize pty: %v", err)
	}
	if _, err := ptmx.Write([]byte{3}); err != nil {
		t.Fatalf("send interrupt: %v", err)
	}
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		t.Fatalf("interrupt did not terminate TUI: %v; output=%q", err, output)
	}
}

// TestStaveTUIProcessSignalsRestoreTerminal verifies the signal path that a
// real terminal uses (rather than a literal ETX byte written to the PTY).
// Bubble Tea must leave the alternate screen and restore the cursor exactly
// once before the child exits, and must not repaint after leaving the screen.
func TestStaveTUIProcessSignalsRestoreTerminal(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	fixture := filepath.Join(root, "testdata", "js", "esm")

	for _, tc := range []struct {
		name string
		sig  os.Signal
	}{
		{name: "interrupt", sig: os.Interrupt},
		{name: "terminate", sig: syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testStaveSignalRestore(t, bin, root, fixture, tc.name, tc.sig)
		})
	}
}

func testStaveSignalRestore(t *testing.T, bin, root, fixture, name string, sig os.Signal) {
	t.Helper()
	cmd := exec.Command(bin, "tui", "--repo", fixture, "--language", "js-ts", "--enable-feature", "stave-tui-preview")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "CI=")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("start lopper in pty: %v", err)
	}
	defer closePTYProcess(t, ptmx, cmd)
	output, readDone := collectPTYOutput(ptmx)
	waitForStaveFrame(t, output)
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("send %s: %v", name, err)
	}
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		t.Fatalf("%s did not terminate within bound: %v", name, err)
	}
	if err := ptmx.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Logf("set signal PTY read deadline: %v", err)
	}
	<-readDone
	assertStaveSignalRestore(t, name, output.String())
}

func collectPTYOutput(ptmx *os.File) (*ptyOutput, <-chan struct{}) {
	output := &ptyOutput{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return output, done
}

func waitForStaveFrame(t *testing.T, output *ptyOutput) {
	t.Helper()
	deadline := time.Now().Add(stavePTYTimeout)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), "Status: Stave preview") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("signal test did not render initial frame: %q", output.String())
}

func assertStaveSignalRestore(t *testing.T, name, output string) {
	t.Helper()
	if got := strings.Count(output, "\x1b[?1049l"); got != 1 {
		t.Fatalf("%s alternate-screen leave count = %d, want 1; output=%q", name, got, output)
	}
	if got := strings.Count(output, "\x1b[?25h"); got != 1 {
		t.Fatalf("%s cursor restore count = %d, want 1; output=%q", name, got, output)
	}
	if idx := strings.LastIndex(output, "\x1b[?1049l"); idx >= 0 && strings.Contains(output[idx+len("\x1b[?1049l"):], "Status: Stave preview") {
		t.Fatalf("%s repainted Stave frame after alternate-screen leave: %q", name, output[idx:])
	}
}

func closePTYProcess(t *testing.T, ptmx *os.File, cmd *exec.Cmd) {
	t.Helper()
	if err := ptmx.Close(); err != nil {
		t.Logf("close pty: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Logf("kill process: %v", err)
	}
}

func TestStaveTUIInteractiveNavigationFilterDetailAndHelp(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	cmd := exec.Command(bin, "tui", "--repo", root, "--language", "go", "--top", "5", "--enable-feature", "stave-tui-preview")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "CI=")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("start lopper in pty: %v", err)
	}
	defer func() {
		if err := ptmx.Close(); err != nil {
			t.Logf("close pty: %v", err)
		}
		if err := cmd.Process.Kill(); err != nil {
			t.Logf("kill process: %v", err)
		}
	}()

	assertStaveInteractiveNavigation(t, ptmx)
	assertStaveInteractiveFilterAndDetail(t, ptmx)
	assertStaveInteractiveRefreshHelpAndSort(t, ptmx)
	assertStaveInteractiveCompactQuit(t, ptmx, cmd)
}

func assertStaveInteractiveNavigation(t *testing.T, ptmx *os.File) {
	t.Helper()
	initial := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool {
		return strings.Contains(s, "Status: Stave preview") && strings.Contains(s, "go-toml")
	})
	assertNoUnsafeTerminalSequences(t, initial)
	if !strings.Contains(initial, "go-toml") {
		t.Fatalf("initial frame did not contain first dependency: %q", initial)
	}
	initialSelected := selectedLine(initial)
	if _, err := ptmx.Write([]byte("\x1b[B")); err != nil {
		t.Fatalf("send down key: %v", err)
	}
	selected := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return selectedLine(s) != "" && selectedLine(s) != initialSelected })
	if selectedLine(selected) == "" || selectedLine(selected) == initialSelected {
		t.Fatalf("down key did not move selection: %q", selected)
	}
}

func assertStaveInteractiveFilterAndDetail(t *testing.T, ptmx *os.File) {
	t.Helper()
	if _, err := ptmx.Write([]byte("/charm\r")); err != nil {
		t.Fatalf("send filter command: %v", err)
	}
	filtered := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "filter charm") && strings.Contains(s, "bubbletea") })
	if !strings.Contains(filtered, "filter charm") || !strings.Contains(filtered, "bubbletea") {
		t.Fatalf("filter did not select expected dependency: %q", filtered)
	}
	if _, err := ptmx.Write([]byte("\r")); err != nil {
		t.Fatalf("open selected dependency: %v", err)
	}
	opened := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Opened") })
	if !strings.Contains(opened, "Opened") {
		t.Fatalf("enter did not open selected detail: %q", opened)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: 101, Rows: 30}); err != nil {
		t.Fatalf("redraw detail: %v", err)
	}
	detail := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Detail:") && strings.Contains(s, "Waste") })
	if !strings.Contains(detail, "Detail:") || !strings.Contains(detail, "Waste") {
		t.Fatalf("enter did not open selected detail: %q", detail)
	}
}

func assertStaveInteractiveRefreshHelpAndSort(t *testing.T, ptmx *os.File) {
	t.Helper()
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: 160, Rows: 30}); err != nil {
		t.Fatalf("widen interactive pty: %v", err)
	}
	if _, err := ptmx.Write([]byte("r")); err != nil {
		t.Fatalf("send refresh key: %v", err)
	}
	if output := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Refreshed") }); !strings.Contains(output, "Refreshed") {
		t.Fatalf("refresh status was not visible: %q", output)
	}
	if _, err := ptmx.Write([]byte("?")); err != nil {
		t.Fatalf("send help key: %v", err)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: 102, Rows: 30}); err != nil {
		t.Fatalf("redraw help: %v", err)
	}
	help := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Navigate:") && strings.Contains(s, "Codemod:") })
	if !strings.Contains(help, "Navigate:") || !strings.Contains(help, "Codemod:") {
		t.Fatalf("help content was not visible: %q", help)
	}
	if _, err := ptmx.Write([]byte("?:sort name\r")); err != nil {
		t.Fatalf("send sort command: %v", err)
	}
	if output := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Sorted by name") }); !strings.Contains(output, "Sorted by name") {
		t.Fatalf("sort command status was not visible: %q", output)
	}
}

func assertStaveInteractiveCompactQuit(t *testing.T, ptmx *os.File, cmd *exec.Cmd) {
	t.Helper()
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: 40, Rows: 12}); err != nil {
		t.Fatalf("resize interactive pty: %v", err)
	}
	assertNoUnsafeTerminalSequences(t, readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Status:") || strings.Contains(s, "Context:") }))
	if _, err := ptmx.Write([]byte("q")); err != nil {
		t.Fatalf("quit interactive TUI: %v", err)
	}
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		t.Fatalf("interactive TUI did not exit: %v", err)
	}
	if err := ptmx.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Logf("set read deadline: %v", err)
	}
	var tail bytes.Buffer
	if _, err := io.Copy(&tail, ptmx); err != nil {
		t.Logf("read PTY tail: %v", err)
	}
	if idx := strings.LastIndex(tail.String(), "\x1b[?1049l"); idx >= 0 && strings.Contains(tail.String()[idx:], "Status: Stave preview") {
		t.Fatalf("terminal repainted Stave frame after alt-screen leave: %q", tail.String()[idx:])
	}
}

func TestTUIWithoutStaveFlagUsesLegacyLinePath(t *testing.T) {
	root := mustModuleRoot(t)
	fixture := filepath.Join(root, "testdata", "js", "esm")
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	ctx, cancel := context.WithTimeout(context.Background(), stavePTYTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "tui", "--repo", fixture, "--language", "js-ts")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1", "CI=1")
	cmd.Stdin = strings.NewReader("q\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("legacy non-TTY path failed: %v stderr=%q", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "Stave preview") {
		t.Fatalf("default-off legacy path unexpectedly rendered Stave UI: %q", stdout.String())
	}
}

func TestStaveFeatureFlagNonTTYFinalEOFCommandRendersSeparatedFrame(t *testing.T) {
	root := mustModuleRoot(t)
	fixture := filepath.Join(root, "testdata", "js", "esm")
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	cmd := exec.Command(bin, "tui", "--repo", fixture, "--language", "js-ts", "--enable-feature", "stave-tui-preview")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1", "CI=1")
	cmd.Stdin = strings.NewReader("filter esm")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("feature-flagged non-TTY EOF command failed: %v stderr=%q", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "filter esm") || strings.Contains(out, "constraintsLopper:") {
		t.Fatalf("final EOF frame missing or concatenated: %q", out)
	}
}

func TestStaveFeatureFlagNonTTYSequentialRunsAreFresh(t *testing.T) {
	root := mustModuleRoot(t)
	fixture := filepath.Join(root, "testdata", "js", "esm")
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	for i := 0; i < 2; i++ {
		cmd := exec.Command(bin, "tui", "--repo", fixture, "--language", "js-ts", "--enable-feature", "stave-tui-preview")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1", "CI=1")
		cmd.Stdin = strings.NewReader("q\n")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sequential run %d failed: %v output=%q", i+1, err, output)
		}
	}
}

func TestStavePTYResizeBoundsAndRepeatedRestore(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	fixture := filepath.Join(root, "testdata", "js", "esm")
	for run := 0; run < 5; run++ {
		testStavePTYResizeRun(t, bin, root, fixture, run+1)
	}
}

func testStavePTYResizeRun(t *testing.T, bin, root, fixture string, run int) {
	t.Helper()
	cmd := exec.Command(bin, "tui", "--repo", fixture, "--language", "js-ts", "--enable-feature", "stave-tui-preview")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "CI=")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("run %d start: %v", run, err)
	}
	defer closePTYProcess(t, ptmx, cmd)
	assertStavePTYResizeFrames(t, ptmx, run)
	assertStavePTYExitRestoresTerminal(t, ptmx, cmd, run)
}

func assertStavePTYResizeFrames(t *testing.T, ptmx *os.File, run int) {
	t.Helper()
	initial := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Status: Stave preview") })
	assertNoUnsafeTerminalSequences(t, initial)
	for _, size := range []struct{ cols, rows uint16 }{{40, 12}, {20, 8}} {
		if err := pty.Setsize(ptmx, &pty.Winsize{Cols: size.cols, Rows: size.rows}); err != nil {
			t.Fatalf("run %d resize: %v", run, err)
		}
		frame := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Status:") })
		assertNoUnsafeTerminalSequences(t, frame)
		if len(frame) == 0 {
			t.Fatalf("run %d resize produced empty frame", run)
		}
	}
}

func assertStavePTYExitRestoresTerminal(t *testing.T, ptmx *os.File, cmd *exec.Cmd, run int) {
	t.Helper()
	if _, err := ptmx.Write([]byte("q")); err != nil {
		t.Fatalf("run %d quit: %v", run, err)
	}
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		t.Fatalf("run %d exit: %v", run, err)
	}
	if err := ptmx.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Logf("set read deadline: %v", err)
	}
	var tail bytes.Buffer
	if _, err := io.Copy(&tail, ptmx); err != nil {
		t.Logf("read PTY tail: %v", err)
	}
	tailText := tail.String()
	if strings.Count(tailText, "\x1b[?1049l") > 1 || (strings.Contains(tailText, "\x1b[?1049l") && strings.Contains(tailText, "Status: Stave preview")) {
		t.Fatalf("run %d invalid restore tail: %q", run, tailText)
	}
}

func TestStaveTUIDumbTerminalErrorsAndHelpStayVisible(t *testing.T) {
	root := mustModuleRoot(t)
	bin := filepath.Join(t.TempDir(), "lopper")
	buildBinary(t, root, bin)
	fixture := filepath.Join(root, "testdata", "js", "esm")
	cmd := exec.Command(bin, "tui", "--repo", fixture, "--language", "js-ts", "--enable-feature", "stave-tui-preview")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1", "COLORTERM=")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 40, Rows: 12})
	if err != nil {
		t.Fatal(err)
	}
	initial := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(s, "Status: Stave preview") })
	if containsAnyByte(initial, 0x1b, '[', 0x9b) {
		t.Fatalf("dumb terminal emitted control sequence: %q", initial)
	}
	assertPlainFrameBounds(t, initial, "Status:", 40, 12)
	if _, err := ptmx.Write([]byte(":bogus\r")); err != nil {
		t.Fatal(err)
	}
	errFrame := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool { return strings.Contains(strings.ToLower(s), "unknown command") })
	if !strings.Contains(strings.ToLower(errFrame), "unknown command") {
		t.Fatalf("unknown command not visible: %q", errFrame)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: 20, Rows: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := ptmx.Write([]byte("?\r")); err != nil {
		t.Fatal(err)
	}
	resized := readPTYUntil(t, ptmx, stavePTYTimeout, func(s string) bool {
		return strings.Contains(s, "Nav") && strings.Contains(s, "Exit")
	})
	if containsAnyByte(resized, 0x1b, '[', 0x9b) {
		t.Fatalf("dumb resize emitted control sequence: %q", resized)
	}
	assertPlainFrameBounds(t, resized, "Update:", 20, 8)
	if !strings.Contains(resized, "Nav") || !strings.Contains(resized, "Exit") {
		t.Fatalf("help not visible after resize: %q", resized)
	}
	if _, err := ptmx.Write([]byte("q\r")); err != nil {
		t.Fatal(err)
	}
	if err := waitPTYExit(cmd, stavePTYTimeout); err != nil {
		t.Fatal(err)
	}
	if err := ptmx.Close(); err != nil {
		t.Logf("close pty: %v", err)
	}
}

func readPTYUntil(t *testing.T, r io.Reader, timeout time.Duration, done func(string) bool) string {
	t.Helper()
	var out bytes.Buffer
	readDone := make(chan ptyReadResult, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := r.Read(buf)
		readDone <- ptyReadResult{n: n, buf: buf, err: err}
	}()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case result := <-readDone:
			if result.n > 0 {
				out.Write(result.buf[:result.n])
				if done(out.String()) {
					return out.String()
				}
			}
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return out.String()
				}
				t.Fatalf("read PTY output: %v", result.err)
			}
			go func() {
				buf := make([]byte, 4096)
				n, err := r.Read(buf)
				readDone <- struct {
					n   int
					buf []byte
					err error
				}{n, buf, err}
			}()
		case <-deadline.C:
			t.Fatalf("timed out waiting for PTY output after %s: %q", timeout, out.String())
		}
	}
}

func selectedLine(output string) string {
	var selected string
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "> ") {
			selected = strings.TrimSpace(line)
		}
	}
	return selected
}

func TestSelectedLineUsesMostRecentTerminalDelta(t *testing.T) {
	output := "\x1b[H\x1b[2J\r\n> github.com/pelletier/go-toml/v2: go\r\n" +
		"\x1b[3d\x1b[38;2;242;246;248m> charm.land/bubbletea/v2: go\r\n"

	selected := selectedLine(output)
	if !strings.Contains(selected, "charm.land/bubbletea/v2") {
		t.Fatalf("selectedLine returned stale selection %q", selected)
	}
	if strings.Contains(selected, "go-toml") {
		t.Fatalf("selectedLine retained an earlier selection %q", selected)
	}
}

func assertPlainFrameBounds(t *testing.T, output, anchor string, width, height int) {
	t.Helper()
	start := strings.LastIndex(output, anchor)
	if start < 0 {
		t.Fatalf("plain frame missing %q row: %q", anchor, output)
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(output[start:], "\r", ""), "\n"), "\n")
	if len(lines) > height {
		t.Fatalf("plain frame used %d rows at %dx%d: %q", len(lines), width, height, output[start:])
	}
	for i, line := range lines {
		if got := len([]rune(line)); got > width {
			t.Fatalf("plain frame row %d used %d columns at %dx%d: %q", i+1, got, width, height, line)
		}
	}
}

func containsAnyByte(value string, forbidden ...byte) bool {
	for i := 0; i < len(value); i++ {
		for _, candidate := range forbidden {
			if value[i] == candidate {
				return true
			}
		}
	}
	return false
}

func waitPTYExit(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 130 {
				return nil
			}
			return err
		}
		return nil
	case <-time.After(timeout):
		return context.DeadlineExceeded
	}
}

func assertNoUnsafeTerminalSequences(t *testing.T, output string) {
	t.Helper()
	for _, marker := range []string{"\x1b]", "\x9d", "\x90", "\x98", "\x9b"} {
		if strings.Contains(output, marker) {
			t.Fatalf("PTY output contains unsafe terminal control %q", marker)
		}
	}
}
