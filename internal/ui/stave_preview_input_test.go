package ui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/stave/layout"
	"github.com/creack/pty"
)

type blockedStaveLineReader struct {
	started chan struct{}
	unblock chan struct{}
}

type readyStaveWriter struct{ ready chan struct{} }

func (w *readyStaveWriter) Write(p []byte) (int, error) {
	select {
	case <-w.ready:
	default:
		close(w.ready)
	}
	return len(p), nil
}

func cleanupStaveTestCloser(t *testing.T, name string, closer io.Closer) {
	t.Helper()
	t.Cleanup(func() {
		if err := closer.Close(); err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("close %s: %v", name, err)
		}
	})
}

func (r *blockedStaveLineReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.unblock
	return 0, io.EOF
}

func TestStaveInteractiveTerminalRequiresInputAndOutputTTY(t *testing.T) {
	terminalInput, terminalOutput, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "terminal input", terminalInput)
	cleanupStaveTestCloser(t, "terminal output", terminalOutput)
	if !staveTerminalFile(terminalInput) || !staveTerminalFile(terminalOutput) {
		t.Skip("pseudo-terminal status unavailable")
	}
	if !supportsStaveInteractiveTerminal(terminalInput, terminalOutput) {
		t.Fatal("paired pseudo-terminals were not accepted")
	}

	pipedInput, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "piped input", pipedInput)
	cleanupStaveTestCloser(t, "pipe writer", pipeWriter)
	if supportsStaveInteractiveTerminal(pipedInput, terminalOutput) {
		t.Fatal("piped input incorrectly enabled full-screen Stave mode")
	}
}

func TestStavePreviewStartUsesLineModeForPipedInput(t *testing.T) {
	terminalInput, terminalOutput, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "terminal input", terminalInput)
	cleanupStaveTestCloser(t, "terminal output", terminalOutput)
	pipedInput, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "piped input", pipedInput)
	cleanupStaveTestCloser(t, "pipe writer", pipeWriter)
	if _, err := pipeWriter.WriteString("q\n"); err != nil {
		t.Fatal(err)
	}
	if err := pipeWriter.Close(); err != nil {
		t.Fatal(err)
	}
	summary := NewSummary(terminalOutput, pipedInput, &stubAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}}, report.NewFormatter())
	if err := NewStavePreview(summary).Start(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}); err != nil {
		t.Fatalf("piped-input preview start: %v", err)
	}
}

func TestStavePreviewPipedInputTracksPTYOutputSizeInLineMode(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")
	terminalInput, terminalOutput, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "terminal input", terminalInput)
	cleanupStaveTestCloser(t, "terminal output", terminalOutput)
	if err := pty.Setsize(terminalOutput, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	pipedInput, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "piped input", pipedInput)
	cleanupStaveTestCloser(t, "pipe writer", pipeWriter)

	summary := NewSummary(terminalOutput, pipedInput, &stubAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}}, report.NewFormatter())
	done := make(chan error, 1)
	go func() {
		done <- NewStavePreview(summary).Start(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80})
	}()
	capture := newSignalPTYCapture(terminalInput)
	waitSignalOutput(t, capture, done, func(output string) bool { return strings.Contains(output, "Stave preview") })
	initial := capture.String()
	if strings.Contains(initial, "\x1b[") {
		t.Fatalf("piped input emitted terminal control output: %q", initial)
	}
	if width := maxStaveRenderedLineWidth(initial); width != 100 {
		t.Fatalf("initial frame width = %d, want 100", width)
	}

	if err := pty.Setsize(terminalOutput, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	if _, err := pipeWriter.WriteString("refresh\nq\n"); err != nil {
		t.Fatal(err)
	}
	if err := pipeWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := waitSignalProcess(done); err != nil {
		t.Fatalf("piped-input preview start: %v", err)
	}
	output := capture.String()
	if width := maxStaveRenderedLineWidth(output); width != 120 {
		t.Fatalf("resized frame width = %d, want 120", width)
	}
	if err := terminalOutput.Close(); err != nil {
		t.Fatal(err)
	}
	if err := terminalInput.Close(); err != nil {
		t.Fatal(err)
	}
	waitSignalCapture(t, capture)
}

func maxStaveRenderedLineWidth(output string) int {
	maxWidth := 0
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r", ""), "\n") {
		maxWidth = max(maxWidth, utf8.RuneCountInString(line))
	}
	return maxWidth
}

func TestStavePreviewStartUsesLineModeForDumbTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")
	terminalInput, terminalOutput, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "terminal input", terminalInput)
	cleanupStaveTestCloser(t, "terminal output", terminalOutput)
	if err := pty.Setsize(terminalOutput, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := terminalInput.WriteString("q\n"); err != nil {
		t.Fatal(err)
	}
	summary := NewSummary(terminalOutput, terminalOutput, &stubAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}}, report.NewFormatter())
	if err := NewStavePreview(summary).Start(context.Background(), Options{UseStavePreview: true, Features: previewFeatures(t), Width: 80}); err != nil {
		t.Fatalf("dumb-terminal preview start: %v", err)
	}
}

func TestStaveLineSessionResizesOnDumbTerminal(t *testing.T) {
	t.Setenv("TERM", "dumb")
	terminalInput, terminalOutput, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "terminal input", terminalInput)
	cleanupStaveTestCloser(t, "terminal output", terminalOutput)
	if err := pty.Setsize(terminalOutput, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}

	summary := NewSummary(terminalOutput, terminalOutput, &stubAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}}, report.NewFormatter())
	opts := summary.applyDefaults(Options{Width: 80})
	view := summaryReportView{}
	state := buildSummaryState(opts)
	program, err := newLopperStaveProgram(summary, &opts, &view, &state)
	if err != nil {
		t.Fatal(err)
	}
	sessionOpts := staveSessionOptions(opts, true)
	prepared, err := program.NewSession(context.Background(), sessionOpts)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Session.Close()

	line := staveLineSession{prepared: prepared, opts: sessionOpts, writer: terminalOutput, tty: true}
	if err := line.refreshFrame(context.Background()); err != nil {
		t.Fatalf("refresh resized dumb-terminal frame: %v", err)
	}
	if line.opts.Viewport != (layout.Size{Width: 100, Height: 30}) {
		t.Fatalf("line viewport = %+v, want 100x30", line.opts.Viewport)
	}
	snapshot, err := prepared.Session.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Model.interaction.viewport != (layout.Size{Width: 100, Height: 30}) {
		t.Fatalf("model viewport = %+v, want 100x30", snapshot.Model.interaction.viewport)
	}
}

func TestReadStaveLineInputContextCancelsIdlePipe(t *testing.T) {
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "pipe reader", pipeReader)
	cleanupStaveTestCloser(t, "pipe writer", pipeWriter)
	input := newStaveLineInput(pipeReader)
	defer func() {
		if err := input.cleanup(); err != nil {
			t.Errorf("clean up cancellable input: %v", err)
		}
	}()
	if input.cancel == nil {
		t.Fatal("file-backed pipe did not receive a cancellable reader")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := readStaveLineInputContext(ctx, input.reader, input.cancel)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("idle pipe cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle pipe read did not stop after cancellation")
	}
}

func TestStaveLineSessionIdlePipeHonorsCancellation(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close idle-pipe reader: %v", err)
		}
	})
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Errorf("close idle-pipe writer: %v", err)
		}
	})
	summary := NewSummary(io.Discard, reader, &stubAnalyzer{report: report.Report{}}, report.NewFormatter())
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
	defer prepared.Session.Close()
	output := &readyStaveWriter{ready: make(chan struct{})}
	input := newStaveLineInput(reader)
	defer func() {
		if err := input.cleanup(); err != nil {
			t.Errorf("clean up idle-pipe input: %v", err)
		}
	}()
	line := staveLineSession{prepared: prepared, opts: staveSessionOptions(opts, false), reader: input.reader, cancelRead: input.cancel, writer: output}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- line.run(ctx) }()
	<-output.ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("idle pipe cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle pipe read remained blocked after cancellation")
	}
}

func TestNewStaveLineInputDoesNotCloseBorrowedPipeReader(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	cleanupStaveTestCloser(t, "borrowed pipe reader", pipeReader)
	cleanupStaveTestCloser(t, "borrowed pipe writer", pipeWriter)
	input := newStaveLineInput(pipeReader)
	if input.cancel != nil {
		t.Fatal("borrowed pipe reader unexpectedly received a close-on-cancel hook")
	}
	if err := input.cleanup(); err != nil {
		t.Fatalf("clean up borrowed pipe reader input: %v", err)
	}
	if err := pipeWriter.Close(); err != nil {
		t.Fatal(err)
	}
	_, eof, err := readStaveLineInput(input.reader)
	if err != nil || !eof {
		t.Fatalf("borrowed pipe reader was closed: eof=%t err=%v", eof, err)
	}
}

func TestNewStaveLineInputLeavesRegularFilesUnwrapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte("refresh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "regular input", file)
	input := newStaveLineInput(file)
	if input.cancel != nil {
		t.Fatal("regular file unexpectedly received a cancellable wrapper")
	}
	line, eof, err := readStaveLineInputContext(context.Background(), input.reader, input.cancel)
	if err != nil || eof || line != "refresh" {
		t.Fatalf("regular input = (%q, %t, %v)", line, eof, err)
	}
	if err := input.cleanup(); err != nil {
		t.Fatalf("clean up regular input: %v", err)
	}
}

func TestReadStaveLineInputContextStopsWatcherAfterNormalRead(t *testing.T) {
	canceled := false
	line, eof, err := readStaveLineInputContext(context.Background(), bufio.NewReader(strings.NewReader("refresh\n")), func() bool { canceled = true; return true })
	if err != nil || eof || line != "refresh" {
		t.Fatalf("normal cancellable read = (%q, %t, %v)", line, eof, err)
	}
	if canceled {
		t.Fatal("normal read unexpectedly triggered cancellation")
	}
}

func TestReadStaveLineInputContextReturnsCancellationAfterGenericRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelOnFirstRead{reader: strings.NewReader("refresh\n"), cancel: cancel}
	_, _, err := readStaveLineInputContext(ctx, bufio.NewReader(reader), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("generic read cancellation = %v", err)
	}
}

func TestReadStaveLineInputContextCancelsBlockedReader(t *testing.T) {
	reader := &blockedStaveLineReader{started: make(chan struct{}), unblock: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := readStaveLineInputContext(ctx, bufio.NewReader(reader), func() bool { close(reader.unblock); return true })
		done <- err
	}()
	<-reader.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked read cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked read watcher did not stop")
	}
}

func TestStaveLineInputCleanupReportsDeadlineResetFailure(t *testing.T) {
	pipeReader, pipeWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "pipe writer", pipeWriter)
	input := newStaveLineInput(pipeReader)
	if err := pipeReader.Close(); err != nil {
		t.Fatal(err)
	}
	if input.cancel() {
		t.Fatal("closed pipe cancellation unexpectedly succeeded")
	}
	if err := input.cleanup(); err == nil {
		t.Fatal("closed pipe cleanup unexpectedly succeeded")
	}
}
