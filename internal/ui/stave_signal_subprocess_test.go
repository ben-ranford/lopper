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
	"github.com/ben-ranford/stave/action"
	"github.com/ben-ranford/stave/event"
	"github.com/creack/pty"
)

const (
	staveSignalHelperEnv       = "LOPPER_STAVE_SIGNAL_HELPER"
	staveSignalMarkerFD        = 3
	staveSignalActionStarted   = "ACTION_STARTED"
	staveSignalCancelObserved  = "CANCEL_OBSERVED"
	staveSignalSubprocessBound = 15 * time.Second
)

// blockingRefreshAnalyzer makes the second analysis call—the interactive
// refresh action—observable before blocking on the action context. The marker
// pipe is separate from the PTY so test synchronization cannot be confused by
// terminal rendering or escape sequences.
type blockingRefreshAnalyzer struct {
	marker       io.Writer
	calls        atomic.Int32
	report       report.Report
	mu           sync.Mutex
	sealed       bool
	observerDone chan struct{}
	observerErr  error
	markerErr    error
}

func (a *blockingRefreshAnalyzer) Analyse(ctx context.Context, _ analysis.Request) (report.Report, error) {
	switch a.calls.Add(1) {
	case 1:
		return a.report, nil
	case 2:
		return a.observeSignalCancellation(ctx)
	default:
		return report.Report{}, fmt.Errorf("unexpected analysis call")
	}
}

func (a *blockingRefreshAnalyzer) observeSignalCancellation(ctx context.Context) (_ report.Report, resultErr error) {
	a.mu.Lock()
	if a.sealed {
		a.mu.Unlock()
		return report.Report{}, errors.New("signal marker owner is closed")
	}
	a.observerDone = make(chan struct{})
	a.mu.Unlock()
	var markerErr error
	defer func() {
		a.mu.Lock()
		a.observerErr, a.markerErr = resultErr, markerErr
		close(a.observerDone)
		a.mu.Unlock()
	}()
	if _, err := fmt.Fprintln(a.marker, staveSignalActionStarted); err != nil {
		markerErr = fmt.Errorf("announce refresh start: %w", err)
		return report.Report{}, markerErr
	}
	<-ctx.Done()
	if _, err := fmt.Fprintln(a.marker, staveSignalCancelObserved); err != nil {
		markerErr = fmt.Errorf("announce refresh cancellation: %w", err)
		return report.Report{}, markerErr
	}
	return report.Report{}, ctx.Err()
}

func awaitSignalObserver(ctx context.Context, done <-chan struct{}, entered func()) error {
	if entered != nil {
		entered()
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		select {
		case <-done:
			return nil
		default:
			return ctx.Err()
		}
	}
}

func closeSignalMarker(ctx context.Context, a *blockingRefreshAnalyzer, closeMarker func() error, entered func()) error {
	a.mu.Lock()
	a.sealed = true
	done := a.observerDone
	a.mu.Unlock()
	if done != nil {
		if err := awaitSignalObserver(ctx, done, entered); err != nil {
			return err
		}
	}
	a.mu.Lock()
	markerErr := a.markerErr
	a.mu.Unlock()
	closeErr := closeMarker()
	if errors.Is(closeErr, os.ErrClosed) {
		closeErr = nil
	}
	return errors.Join(markerErr, closeErr)
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
	analyzer := &blockingRefreshAnalyzer{marker: marker}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
		defer cancel()
		if err := closeSignalMarker(ctx, analyzer, marker.Close, nil); err != nil {
			t.Errorf("close signal marker: %v", err)
		}
	})

	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
		Enable:  []string{staveTUIFeature},
	})
	if err != nil {
		t.Fatalf("resolve Stave preview feature: %v", err)
	}
	analyzer.report = report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies: []report.DependencyReport{{
			Language: "go",
			Name:     "signal-fixture",
		}},
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
	cmd, ptmx, markerReader, process := startStaveSignalHelper(t)

	capture := newSignalPTYCapture(ptmx)
	registerSignalCaptureCleanup(t, ptmx, capture)
	markers := scanSignalMarkers(markerReader)
	registerSignalMarkerCleanup(t, markerReader, markers)
	processDone := process.notify

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
	waitSignalMarker(t, markers, process, staveSignalActionStarted, capture)
	select {
	case err := <-processDone:
		t.Fatalf("helper exited while refresh action should be blocked: %v", err)
	default:
	}

	if _, err := ptmx.Write([]byte(strings.Repeat("x", 1024))); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("send %s: %v", sig, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	if err := receiveSignalMarker(ctx, markers, process, staveSignalCancelObserved); err != nil {
		failSignalMarker(ctx, t, markers, process, capture, staveSignalCancelObserved, err)
	}
	if err := process.await(ctx); err != nil {
		t.Fatalf("helper did not exit cleanly after %s: %v; %s", sig, err, signalDiagnostics(markers, process, capture))
	}
	if err := awaitSignalObserver(ctx, capture.done, nil); err != nil {
		t.Fatalf("PTY reader did not finish: %v; %s", err, signalDiagnostics(markers, process, capture))
	}
	if err := markers.finish(ctx); err != nil {
		t.Fatalf("marker scanner failed: %v; %s", err, signalDiagnostics(markers, process, capture))
	}

	assertStaveSignalRestored(t, sig, capture.String())
}

func assertStaveSignalRestored(t *testing.T, sig os.Signal, output string) {
	t.Helper()
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

func startStaveSignalHelper(t *testing.T) (*exec.Cmd, *os.File, *os.File, *signalProcessReceipt) {
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
	process := newSignalProcessReceipt(cmd)
	t.Cleanup(func() {
		stopStaveSignalHelper(t, cmd, process)
		if closeErr := ptmx.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Errorf("close signal PTY: %v", closeErr)
		}
	})
	if err := markerWriter.Close(); err != nil {
		t.Fatalf("close parent marker writer: %v", err)
	}

	return cmd, ptmx, markerReader, process
}

func stopStaveSignalHelper(t *testing.T, cmd *exec.Cmd, process *signalProcessReceipt) {
	t.Helper()
	if cmd.Process == nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Logf("kill signal helper: %v", err)
	}
	if err := process.wait(); errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("signal child not joined: %v", err)
	}

}

type signalProcessReceipt struct {
	done   chan struct{}
	notify chan error
	err    error
	state  *os.ProcessState
}

func newSignalProcessReceipt(cmd *exec.Cmd) *signalProcessReceipt {
	p := &signalProcessReceipt{done: make(chan struct{}), notify: make(chan error, 1)}
	go func() {
		p.err = cmd.Wait()
		p.state = cmd.ProcessState
		close(p.done)
		p.notify <- p.err
	}()
	return p
}

func (p *signalProcessReceipt) await(ctx context.Context) error {
	if err := awaitSignalObserver(ctx, p.done, nil); err != nil {
		return err
	}
	return p.err
}

func (p *signalProcessReceipt) wait() error {
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	return p.await(ctx)
}

func (p *signalProcessReceipt) diagnostic() string {
	select {
	case <-p.done:
		return fmt.Sprintf("wait=%v state=%v", p.err, p.state)
	default:
		return "wait=pending"
	}
}

type signalPTYCapture struct {
	mu        sync.Mutex
	output    bytes.Buffer
	updated   chan struct{}
	done      chan struct{}
	readErr   error
	appendErr error
}

func newSignalPTYCapture(reader io.Reader) *signalPTYCapture {
	capture := &signalPTYCapture{updated: make(chan struct{}, 1), done: make(chan struct{})}
	go func() {
		defer close(capture.done)
		buffer := make([]byte, 4096)
		for {
			n, err := reader.Read(buffer)
			if n > 0 {
				if !capture.append(buffer[:n]) {
					return
				}
			}
			if err != nil {
				capture.mu.Lock()
				capture.readErr = err
				capture.mu.Unlock()
				return
			}
		}
	}()
	return capture
}

func (c *signalPTYCapture) append(chunk []byte) bool {
	c.mu.Lock()
	if _, writeErr := c.output.Write(chunk); writeErr != nil {
		c.appendErr = writeErr
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()
	select {
	case c.updated <- struct{}{}:
	default:
	}
	return true
}

func (c *signalPTYCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.output.String()
}

type signalMarkerStream struct {
	markers     chan string
	done        chan struct{}
	cancel      context.CancelFunc
	err         error
	interrupted bool
}

func scanSignalMarkers(reader io.Reader) *signalMarkerStream {
	ctx, cancel := context.WithCancel(context.Background())
	stream := &signalMarkerStream{markers: make(chan string, 1), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(stream.markers)
		defer close(stream.done)
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			select {
			case stream.markers <- scanner.Text():
			case <-ctx.Done():
				stream.interrupted = true
				stream.err = scanner.Err()
				return
			}
		}
		stream.err = scanner.Err()
		stream.interrupted = ctx.Err() != nil
	}()
	return stream
}

func (s *signalMarkerStream) finish(ctx context.Context) error {
	if err := awaitSignalObserver(ctx, s.done, nil); err != nil {
		return err
	}
	if s.interrupted {
		return errors.Join(s.err, errors.New("marker delivery interrupted"))
	}
	return s.err
}

func (s *signalMarkerStream) diagnostic() string {
	select {
	case <-s.done:
		return fmt.Sprintf("scanner EOF=%t error=%v interrupted=%t", s.err == nil && !s.interrupted, s.err, s.interrupted)
	default:
		return "scanner=pending"
	}
}

func signalDiagnostics(markers *signalMarkerStream, process *signalProcessReceipt, capture *signalPTYCapture) string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	state := "pending"
	select {
	case <-capture.done:
		state = fmt.Sprintf("complete read=%v append=%v", capture.readErr, capture.appendErr)
	default:
	}
	return fmt.Sprintf("%s; %s; PTY=%s output=%q", markers.diagnostic(), process.diagnostic(), state, capture.output.String())
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

func waitSignalMarker(t *testing.T, markers *signalMarkerStream, process *signalProcessReceipt, want string, capture *signalPTYCapture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	if err := receiveSignalMarker(ctx, markers, process, want); err != nil {
		failSignalMarker(ctx, t, markers, process, capture, want, err)
	}
}

func receiveSignalMarker(ctx context.Context, stream *signalMarkerStream, process *signalProcessReceipt, want string) error {
	processDone := process.done
	var seen []string
	for {
		select {
		case marker, ok := <-stream.markers:
			if !ok {
				return fmt.Errorf("marker pipe closed before %q; seen=%v; %s", want, seen, stream.diagnostic())
			}
			seen = append(seen, marker)
			if marker == want {
				return nil
			}
		case <-processDone:
			processDone = nil // Drain the real stream within the same deadline after exit.
		case <-ctx.Done():
			return fmt.Errorf("waiting for %q seen=%v: %w", want, seen, ctx.Err())
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

type signalGatedWriter struct {
	file    *os.File
	entered chan struct{}
	release chan struct{}
}

func (w *signalGatedWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), staveSignalCancelObserved) {
		close(w.entered)
		<-w.release
	}
	return w.file.Write(p)
}

func TestStaveSignalFixtureBaseline(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	gate := &signalGatedWriter{file: writer, entered: make(chan struct{}), release: make(chan struct{})}
	analyzer := &blockingRefreshAnalyzer{marker: gate}
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	done := make(chan error, 1)
	var release sync.Once
	t.Cleanup(func() {
		cancel()
		release.Do(func() { close(gate.release) })
		closeSignalTestFile(t, reader)
		select {
		case <-done:
		case <-time.After(staveSignalSubprocessBound):
			t.Error("baseline observer not joined")
		}
		closeSignalTestFile(t, writer)
	})
	if err := reader.SetReadDeadline(time.Now().Add(staveSignalSubprocessBound)); err != nil {
		t.Fatal(err)
	}
	if _, err := analyzer.Analyse(ctx, analysis.Request{}); err != nil {
		t.Fatal(err)
	}
	go func() { _, err := analyzer.Analyse(ctx, analysis.Request{}); done <- err; close(done) }()
	scanner := bufio.NewScanner(reader)
	if !scanner.Scan() || scanner.Text() != staveSignalActionStarted {
		t.Fatalf("start marker: %q %v", scanner.Text(), scanner.Err())
	}
	cancel()
	select {
	case <-gate.entered:
	case <-time.After(staveSignalSubprocessBound):
		t.Fatal("final write not entered")
	}
	release.Do(func() { close(gate.release) })
	if !scanner.Scan() || scanner.Text() != staveSignalCancelObserved {
		t.Fatalf("cancel marker: %q %v", scanner.Text(), scanner.Err())
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("observer: %v", err)
		}
	case <-time.After(staveSignalSubprocessBound):
		t.Fatal("observer did not return")
	}
}

type signalObserverFixture struct {
	analyzer *blockingRefreshAnalyzer
	reader   *os.File
	writer   *os.File
	gate     *signalGatedWriter
	scanner  *bufio.Scanner
	cancel   context.CancelFunc
	release  sync.Once
	result   chan error
}

func newSignalObserverFixture(t *testing.T) *signalObserverFixture {
	t.Helper()
	r, w := newSignalMarkerPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	f := &signalObserverFixture{reader: r, writer: w, gate: &signalGatedWriter{file: w, entered: make(chan struct{}), release: make(chan struct{})}, scanner: bufio.NewScanner(r), cancel: cancel, result: make(chan error, 1)}
	f.analyzer = &blockingRefreshAnalyzer{marker: f.gate}
	t.Cleanup(func() {
		cancel()
		f.release.Do(func() { close(f.gate.release) })
		if err := r.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
		select {
		case <-f.result:
		case <-time.After(staveSignalSubprocessBound):
			t.Error("observer fixture not joined")
		}
		if err := w.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
	})
	if _, err := f.analyzer.Analyse(ctx, analysis.Request{}); err != nil {
		t.Fatal(err)
	}
	go func() { _, err := f.analyzer.Analyse(ctx, analysis.Request{}); f.result <- err; close(f.result) }()
	if !f.scanner.Scan() || f.scanner.Text() != staveSignalActionStarted {
		t.Fatalf("start marker: %v", f.scanner.Err())
	}
	cancel()
	select {
	case <-f.gate.entered:
	case <-time.After(staveSignalSubprocessBound):
		t.Fatal("observer did not reach final write")
	}
	return f
}

func (f *signalObserverFixture) finish(t *testing.T) {
	t.Helper()
	f.release.Do(func() { close(f.gate.release) })
	if !f.scanner.Scan() || f.scanner.Text() != staveSignalCancelObserved {
		t.Fatalf("final real marker: %q %v", f.scanner.Text(), f.scanner.Err())
	}
	select {
	case err := <-f.result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("observer result: %v", err)
		}
	case <-time.After(staveSignalSubprocessBound):
		t.Fatal("observer result pending")
	}
}

func TestStaveSignalObserverCleanupOrdering(t *testing.T) {
	t.Run("completion-before-close", func(t *testing.T) {
		assertSignalObserverCompletion(t)
	})
	t.Run("expired-pending-writer", func(t *testing.T) {
		f := newSignalObserverFixture(t)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		closed := false
		err := closeSignalMarker(ctx, f.analyzer, func() error { closed = true; return f.writer.Close() }, nil)
		if !errors.Is(err, context.DeadlineExceeded) || closed {
			t.Fatalf("pending writer closed or reported complete: close=%v err=%v", closed, err)
		}
		if _, err := f.writer.Stat(); err != nil {
			t.Fatalf("pending descriptor not open: %v", err)
		}
		f.finish(t)
		if err := closeSignalMarker(ctx, f.analyzer, f.writer.Close, nil); err != nil {
			t.Fatalf("published completion lost to expired context: %v", err)
		}
	})
}

func TestStaveSignalObserverCleanupNotStarted(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprintf("prepared-%t", prepared), func(t *testing.T) {
			assertSignalObserverNotStarted(t, prepared)
		})
	}
}

func prepareUnrunSignalAction(t *testing.T, a *blockingRefreshAnalyzer) {
	t.Helper()
	summary := NewSummary(io.Discard, strings.NewReader(""), a, report.NewFormatter())
	opts := summary.applyDefaults(Options{Width: 80})
	view := summaryReportView{}
	state := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := program.NewSession(context.Background(), staveSessionOptions(opts, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(prepared.Session.Close)
	bridge := &staveTerminal{ctx: context.Background(), prepared: prepared, sendEvent: func(ctx context.Context, _ any, ev event.Event) error { return sendLopperEvent(ctx, prepared, ev) }, snapshot: func(context.Context, any) (staveTerminalSnapshot, error) {
		current, err := prepared.Session.Snapshot()
		if err != nil {
			return staveTerminalSnapshot{}, err
		}
		return staveTerminalSnapshot{model: current.Model, tree: current.Tree, caps: current.Capabilities, theme: prepared.Theme}, nil
	}}
	cmd := bridge.beginAction(action.ID(staveActionRefresh), map[string]any{}, false)
	if cmd == nil || !bridge.inflight {
		t.Fatal("real action not accepted")
	}
	t.Cleanup(func() {
		if bridge.actionCancel != nil {
			bridge.actionCancel()
		}
	})
	// Keep the accepted command unexecuted; cleanup must not manufacture startup.
}

func TestStaveSignalObserverWriteErrors(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(fmt.Sprintf("final-%t", final), func(t *testing.T) {
			assertSignalObserverWriteError(t, final)
		})
	}
}

const signalReceiptChildEnv = "LOPPER_SIGNAL_RECEIPT_CHILD"

func TestStaveSignalProcessReceipt(t *testing.T) {
	if value := os.Getenv(signalReceiptChildEnv); value != "" {
		if value == "zero" {
			os.Exit(0)
		}
		os.Exit(3)
	}
	for _, value := range []string{"zero", "nonzero"} {
		t.Run(value, func(t *testing.T) {
			assertSignalProcessReceipt(t, value)
		})
	}
}

func startSignalReceiptChild(t *testing.T, value string) *signalProcessReceipt {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStaveSignalProcessReceipt$")
	cmd.Env = append(os.Environ(), signalReceiptChildEnv+"="+value)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := newSignalProcessReceipt(cmd)
	t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Error(err)
			}
		}
		if err := p.wait(); errors.Is(err, context.DeadlineExceeded) {
			t.Error("receipt child not joined")
		}
	})
	return p
}

type signalTerminalErrorReader struct {
	data []byte
	err  error
}

func (r *signalTerminalErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}

func TestStaveSignalMarkerDiagnostics(t *testing.T) {
	process := startSignalReceiptChild(t, "zero")
	if err := process.wait(); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("marker read failed")
	for _, tc := range []struct {
		name   string
		reader io.Reader
		seen   bool
		err    error
	}{
		{"EOF", strings.NewReader(""), false, nil},
		{"read-error", &signalTerminalErrorReader{err: failure}, false, failure},
		{"queued-after-exit", strings.NewReader(staveSignalCancelObserved + "\n"), true, nil},
		{"missing-after-exit", strings.NewReader("OTHER\n"), false, nil},
		{"final-marker-and-error", &signalTerminalErrorReader{data: []byte(staveSignalCancelObserved + "\n"), err: failure}, true, failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := scanSignalMarkers(tc.reader)
			defer stream.cancel()
			ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
			defer cancel()
			err := receiveSignalMarker(ctx, stream, process, staveSignalCancelObserved)
			if (err == nil) != tc.seen {
				t.Fatalf("marker observation=%v want seen=%v", err, tc.seen)
			}
			if err := stream.finish(ctx); !errors.Is(err, tc.err) {
				t.Fatalf("scanner terminal error=%v want=%v", err, tc.err)
			}
			if !strings.Contains(stream.diagnostic(), "EOF=") {
				t.Fatalf("terminal diagnostic missing: %s", stream.diagnostic())
			}
		})
	}
	t.Run("interrupted-delivery", func(t *testing.T) {
		stream := scanSignalMarkers(strings.NewReader("one\ntwo\nthree\n"))
		stream.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
		defer cancel()
		if err := stream.finish(ctx); err == nil || !stream.interrupted {
			t.Fatalf("interrupted scanner reported EOF: %v", err)
		}
	})
	t.Run("pending", func(t *testing.T) {
		assertSignalMarkerPending(t)
	})
}

func assertSignalObserverCompletion(t *testing.T) {
	t.Helper()
	f := newSignalObserverFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	entered := make(chan struct{})
	cleaned := make(chan error, 1)
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		cleaned <- closeSignalMarker(ctx, f.analyzer, func() error {
			select {
			case <-f.analyzer.observerDone:
			default:
				return errors.New("marker closed before observer completion")
			}
			return f.writer.Close()
		}, func() { close(entered) })
	}()
	t.Cleanup(func() {
		f.release.Do(func() { close(f.gate.release) })
		joinCtx, joinCancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
		defer joinCancel()
		if err := awaitSignalObserver(joinCtx, cleanupDone, nil); err != nil {
			t.Errorf("cleanup not joined: %v", err)
		}
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("cleanup did not enter observer wait")
	}
	f.finish(t)
	select {
	case err := <-cleaned:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("cleanup incomplete")
	}
	if _, err := f.writer.Write([]byte("closed")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("real marker not closed: %v", err)
	}
}

func assertSignalObserverNotStarted(t *testing.T, prepared bool) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSignalTestFile(t, r); closeSignalTestFile(t, w) })
	a := &blockingRefreshAnalyzer{marker: w}
	if _, err := a.Analyse(context.Background(), analysis.Request{}); err != nil {
		t.Fatal(err)
	}
	if prepared {
		prepareUnrunSignalAction(t, a)
	}
	waited := false
	if err := closeSignalMarker(context.Background(), a, w.Close, func() { waited = true }); err != nil {
		t.Fatal(err)
	}
	if waited || a.calls.Load() != 1 {
		t.Fatalf("never-started observer waited or invoked: wait=%v calls=%d", waited, a.calls.Load())
	}
	if _, err := a.Analyse(context.Background(), analysis.Request{}); err == nil || !strings.Contains(err.Error(), "owner is closed") {
		t.Fatalf("post-seal claim: %v", err)
	}
	if _, err := w.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("marker still open: %v", err)
	}
}

func assertSignalObserverWriteError(t *testing.T, final bool) {
	t.Helper()
	r, w := newSignalMarkerPipe(t)
	a := &blockingRefreshAnalyzer{marker: w}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := a.Analyse(ctx, analysis.Request{}); err != nil {
		t.Fatal(err)
	}
	if !final {
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	result := make(chan error, 1)
	done := make(chan struct{})
	go func() { defer close(done); _, err := a.Analyse(ctx, analysis.Request{}); result <- err }()
	registerSignalWriteErrorCleanup(t, cancel, r, done)
	if final {
		scanner := bufio.NewScanner(r)
		if !scanner.Scan() || scanner.Text() != staveSignalActionStarted {
			t.Fatal("missing real start")
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, os.ErrClosed) {
			t.Fatalf("write error lost: %v", err)
		}
	case <-time.After(staveSignalSubprocessBound):
		t.Fatal("write observer not joined")
	}
	if err := closeSignalMarker(context.Background(), a, w.Close, nil); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("retained write error lost: %v", err)
	}
}

func assertSignalProcessReceipt(t *testing.T, value string) {
	t.Helper()
	p := startSignalReceiptChild(t, value)
	err := p.wait()
	if (err == nil) != (value == "zero") {
		t.Fatalf("actual child status: %v", err)
	}
	if p.state == nil || (p.state.ExitCode() == 0) != (value == "zero") {
		t.Fatalf("actual ProcessState: %v", p.state)
	}
	if again := p.wait(); !errors.Is(again, err) {
		t.Fatalf("retained wait result changed: %v / %v", err, again)
	}
	if notification := <-p.notify; !errors.Is(notification, err) {
		t.Fatalf("compatibility notification: %v", notification)
	}
}

func assertSignalMarkerPending(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stream := scanSignalMarkers(r)
	t.Cleanup(func() {
		stream.cancel()
		closeSignalTestFile(t, r)
		closeSignalTestFile(t, w)
		ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
		defer cancel()
		if err := awaitSignalObserver(ctx, stream.done, nil); err != nil {
			t.Error(err)
		}
	})
	if stream.diagnostic() != "scanner=pending" {
		t.Fatalf("pending scanner fabricated completion: %s", stream.diagnostic())
	}
}

func registerSignalCaptureCleanup(t *testing.T, ptmx *os.File, capture *signalPTYCapture) {
	t.Helper()
	t.Cleanup(func() {
		if err := ptmx.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
		defer cancel()
		if err := awaitSignalObserver(ctx, capture.done, nil); err != nil {
			t.Errorf("join PTY capture: %v", err)
		}
	})
}

func registerSignalMarkerCleanup(t *testing.T, markerReader *os.File, markers *signalMarkerStream) {
	t.Helper()
	t.Cleanup(func() {
		markers.cancel()
		if err := markerReader.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
		defer cancel()
		if err := awaitSignalObserver(ctx, markers.done, nil); err != nil {
			t.Errorf("join marker scanner: %v", err)
		}
	})
}

func closeSignalTestFile(t *testing.T, file *os.File) {
	t.Helper()
	if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Error(err)
	}
}

func failSignalMarker(ctx context.Context, t *testing.T, markers *signalMarkerStream, process *signalProcessReceipt, capture *signalPTYCapture, want string, cause error) {
	t.Helper()
	// Retain pending diagnostics when the original marker deadline expires.
	for _, done := range []<-chan struct{}{process.done, markers.done, capture.done} {
		if err := awaitSignalObserver(ctx, done, nil); err != nil {
			break
		}
	}
	t.Fatalf("marker %q: %v; %s", want, cause, signalDiagnostics(markers, process, capture))
}

func registerSignalWriteErrorCleanup(t *testing.T, cancel context.CancelFunc, r *os.File, done <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		closeSignalTestFile(t, r)
		joinCtx, joinCancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
		defer joinCancel()
		if err := awaitSignalObserver(joinCtx, done, nil); err != nil {
			t.Errorf("write-error observer not joined: %v", err)
		}
	})
}

func TestStaveSignalPTYDiagnostics(t *testing.T) {
	failure := errors.New("pty read failed")
	capture := newSignalPTYCapture(&signalTerminalErrorReader{data: []byte("last output"), err: failure})
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	if err := awaitSignalObserver(ctx, capture.done, nil); err != nil {
		t.Fatal(err)
	}
	capture.mu.Lock()
	readErr, appendErr := capture.readErr, capture.appendErr
	capture.mu.Unlock()
	if !errors.Is(readErr, failure) || appendErr != nil || capture.String() != "last output" {
		t.Fatalf("PTY terminal evidence lost: read=%v append=%v output=%q", readErr, appendErr, capture.String())
	}
}

func newSignalMarkerPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSignalTestFile(t, r); closeSignalTestFile(t, w) })
	if err := r.SetReadDeadline(time.Now().Add(staveSignalSubprocessBound)); err != nil {
		t.Fatal(err)
	}
	return r, w
}
