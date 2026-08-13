package ui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/creack/pty"
)

const (
	staveSignalHelperEnv       = "LOPPER_STAVE_SIGNAL_HELPER"
	staveSignalMarkerFD        = 3
	staveSignalActionStarted   = "ACTION_STARTED"
	staveSignalCancelObserved  = "CANCEL_OBSERVED"
	staveSignalSubprocessBound = 10 * time.Second
)

// blockingRefreshAnalyzer makes the second analysis call—the interactive
// refresh action—observable before blocking on the action context. The marker
// pipe is separate from the PTY so test synchronization cannot be confused by
// terminal rendering or escape sequences.
type blockingRefreshAnalyzer struct {
	marker io.Writer
	calls  atomic.Int32
	report report.Report
}

func (a *blockingRefreshAnalyzer) Analyse(ctx context.Context, _ analysis.Request) (report.Report, error) {
	switch a.calls.Add(1) {
	case 1:
		return a.report, nil
	case 2:
		if _, err := fmt.Fprintln(a.marker, staveSignalActionStarted); err != nil {
			return report.Report{}, fmt.Errorf("announce refresh start: %w", err)
		}
		<-ctx.Done()
		if _, err := fmt.Fprintln(a.marker, staveSignalCancelObserved); err != nil {
			return report.Report{}, fmt.Errorf("announce refresh cancellation: %w", err)
		}
		return report.Report{}, ctx.Err()
	default:
		return report.Report{}, fmt.Errorf("unexpected analysis call")
	}
}

// TestStaveInFlightActionProcessSignalsRestoreTerminal is both the parent
// assertion and the deliberately narrow helper-process entry point. The child
// runs the real StavePreview full-screen path on a PTY; the parent sends an OS
// signal only after the refresh handler proves that it is blocked in Analyse.
func TestStaveInFlightActionProcessSignalsRestoreTerminal(t *testing.T) {
	if os.Getenv(staveSignalHelperEnv) == "1" {
		runStaveSignalHelper(t)
		return
	}

	for _, tc := range []struct {
		name string
		sig  os.Signal
	}{
		{name: "SIGINT", sig: os.Interrupt},
		{name: "SIGTERM", sig: syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runStaveSignalParent(t, tc.sig)
		})
	}
}

func runStaveSignalHelper(t *testing.T) {
	marker := os.NewFile(staveSignalMarkerFD, "stave-signal-marker")
	if marker == nil {
		t.Fatal("signal marker file descriptor is unavailable")
	}
	t.Cleanup(func() {
		if closeErr := marker.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close signal marker: %v", closeErr)
		}
	})

	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{staveTUIFeature},
	})
	if err != nil {
		t.Fatalf("resolve Stave preview feature: %v", err)
	}
	analyzer := &blockingRefreshAnalyzer{
		marker: marker,
		report: report.Report{
			SchemaVersion: report.SchemaVersion,
			Dependencies: []report.DependencyReport{{
				Language: "go",
				Name:     "signal-fixture",
			}},
		},
	}
	summary := NewSummary(os.Stdout, os.Stdin, analyzer, report.NewFormatter())
	err = NewStavePreview(summary).Start(context.Background(), Options{
		RepoPath:        ".",
		UseStavePreview: true,
		Features:        features,
		Width:           100,
	})
	if err != nil {
		t.Fatalf("run signal helper: %v", err)
	}
}

func runStaveSignalParent(t *testing.T, sig os.Signal) {
	t.Helper()
	markerReader, markerWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create marker pipe: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := markerReader.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close marker reader: %v", closeErr)
		}
	})

	cmd := exec.Command(os.Args[0], "-test.run=^TestStaveInFlightActionProcessSignalsRestoreTerminal$")
	cmd.Env = append(os.Environ(), staveSignalHelperEnv+"=1", "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "CI=")
	cmd.ExtraFiles = []*os.File{markerWriter}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		if closeErr := markerWriter.Close(); closeErr != nil {
			t.Logf("close failed marker writer: %v", closeErr)
		}
		t.Fatalf("start signal helper in PTY: %v", err)
	}
	if err := markerWriter.Close(); err != nil {
		t.Fatalf("close parent marker writer: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := ptmx.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close signal PTY: %v", closeErr)
		}
	})
	defer func() {
		if cmd.Process != nil {
			if killErr := cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				t.Logf("kill signal helper: %v", killErr)
			}
		}
	}()

	capture := newSignalPTYCapture(ptmx)
	markers := scanSignalMarkers(markerReader)
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()

	waitSignalOutput(t, capture, processDone, func(output string) bool {
		return strings.Contains(output, "Status: Stave preview") &&
			strings.Contains(output, "\x1b[?1049h")
	})
	// The canonical command path supplies the refresh action's empty schema;
	// the single-key shortcut currently includes row context and is covered by
	// separate input-validation tests.
	if _, err := ptmx.Write([]byte(":refresh\r")); err != nil {
		t.Fatalf("start refresh action: %v", err)
	}
	waitSignalMarker(t, markers, processDone, staveSignalActionStarted, capture)
	select {
	case err := <-processDone:
		t.Fatalf("helper exited while refresh action should be blocked: %v", err)
	default:
	}

	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("send %s: %v", sig, err)
	}
	waitSignalMarker(t, markers, processDone, staveSignalCancelObserved, capture)
	if err := waitSignalProcess(processDone); err != nil {
		t.Fatalf("helper did not exit cleanly after %s: %v; output=%q", sig, err, capture.String())
	}
	waitSignalCapture(t, capture)

	output := capture.String()
	if got := strings.Count(output, "\x1b[?1049h"); got != 1 {
		t.Fatalf("%s alternate-screen enter count = %d, want 1; output=%q", sig, got, output)
	}
	if got := strings.Count(output, "\x1b[?1049l"); got != 1 {
		t.Fatalf("%s alternate-screen leave count = %d, want 1; output=%q", sig, got, output)
	}
	if got := strings.Count(output, "\x1b[?25h"); got != 1 {
		t.Fatalf("%s cursor-show count = %d, want 1; output=%q", sig, got, output)
	}
	restore := strings.LastIndex(output, "\x1b[?1049l")
	if restore < 0 {
		t.Fatalf("%s output did not leave alternate screen: %q", sig, output)
	}
	tail := output[restore+len("\x1b[?1049l"):]
	if strings.Contains(tail, "Status: Stave preview") ||
		strings.Contains(tail, "signal-fixture") ||
		strings.Contains(tail, "\x1b[H") ||
		strings.Contains(tail, "\x1b[2J") {
		t.Fatalf("%s repainted after terminal restoration: %q", sig, tail)
	}
}

type signalPTYCapture struct {
	mu      sync.Mutex
	output  bytes.Buffer
	updated chan struct{}
	done    chan struct{}
}

func newSignalPTYCapture(reader io.Reader) *signalPTYCapture {
	capture := &signalPTYCapture{updated: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(capture.done)
		buffer := make([]byte, 4096)
		for {
			n, err := reader.Read(buffer)
			if n > 0 {
				capture.mu.Lock()
				if _, writeErr := capture.output.Write(buffer[:n]); writeErr != nil {
					capture.mu.Unlock()
					return
				}
				capture.mu.Unlock()
				select {
				case capture.updated <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return capture
}

func (c *signalPTYCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output.String()
}

func scanSignalMarkers(reader io.Reader) <-chan string {
	markers := make(chan string)
	go func() {
		defer close(markers)
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			markers <- scanner.Text()
		}
	}()
	return markers
}

func waitSignalOutput(t *testing.T, capture *signalPTYCapture, processDone <-chan error, ready func(string) bool) {
	t.Helper()
	timer := time.NewTimer(staveSignalSubprocessBound)
	defer timer.Stop()
	for {
		if ready(capture.String()) {
			return
		}
		select {
		case <-capture.updated:
		case err := <-processDone:
			t.Fatalf("helper exited before initial full-screen render: %v; output=%q", err, capture.String())
		case <-timer.C:
			t.Fatalf("timed out waiting for initial full-screen render; output=%q", capture.String())
		}
	}
}

func waitSignalMarker(t *testing.T, markers <-chan string, processDone <-chan error, want string, capture *signalPTYCapture) {
	t.Helper()
	timer := time.NewTimer(staveSignalSubprocessBound)
	defer timer.Stop()
	for {
		select {
		case marker, ok := <-markers:
			if !ok {
				t.Fatalf("marker pipe closed before %q", want)
			}
			if marker == want {
				return
			}
		case err := <-processDone:
			t.Fatalf("helper exited before marker %q: %v", want, err)
		case <-timer.C:
			t.Fatalf("timed out waiting for marker %q; output=%q", want, capture.String())
		}
	}
}

func waitSignalProcess(processDone <-chan error) error {
	timer := time.NewTimer(staveSignalSubprocessBound)
	defer timer.Stop()
	select {
	case err := <-processDone:
		return err
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

func waitSignalCapture(t *testing.T, capture *signalPTYCapture) {
	t.Helper()
	timer := time.NewTimer(staveSignalSubprocessBound)
	defer timer.Stop()
	select {
	case <-capture.done:
	case <-timer.C:
		t.Fatalf("PTY reader did not finish; output=%q", capture.String())
	}
}
