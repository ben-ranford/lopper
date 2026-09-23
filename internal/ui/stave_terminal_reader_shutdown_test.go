package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"go.uber.org/goleak"
)

func TestStaveTerminalQuitDrainsQueuedInput(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CI", "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("COLORTERM", "truecolor")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	capture := newSignalPTYCapture(master)
	t.Cleanup(func() { waitSignalCapture(t, capture) })
	cleanupStaveTestCloser(t, "queued-input master", master)
	cleanupStaveTestCloser(t, "queued-input terminal", terminal)
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	before, err := term.GetState(terminal.Fd())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan error, 1)
	preview := NewStavePreview(NewSummary(terminal, terminal, &stubAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "alpha"}}}}, report.NewFormatter()))
	opts := Options{RepoPath: ".", UseStavePreview: true, Features: previewFeatures(t), Width: 80}
	go func() {
		returned <- preview.Start(ctx, opts)
	}()
	waitSignalOutput(t, capture, returned, func(s string) bool { return strings.Contains(s, "Status: Stave preview") })
	// One read contains quit followed by events that must be drained during exit.
	if _, err := master.Write([]byte("q" + strings.Repeat("x", 1024))); err != nil {
		t.Fatal(err)
	}
	if err := waitSignalProcess(returned); err != nil {
		t.Fatal(err)
	}
	after, err := term.GetState(terminal.Fd())
	if err != nil {
		t.Fatalf("caller-owned input was closed: %v", err)
	}
	if *before != *after {
		t.Fatal("terminal state was not restored")
	}
	if err := terminal.Close(); err != nil {
		t.Fatal(err)
	}
	waitSignalCapture(t, capture)
	goleak.VerifyNone(t)
}

// The terminal runner requires a cancellable file; copy finite fixture input
// into a real pipe instead of exercising the non-interruptible reader fallback.
func staveTerminalTestInput(t *testing.T, source io.Reader) *os.File {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(writer, source); done <- errors.Join(err, writer.Close()) }()
	t.Cleanup(func() {
		if err := reader.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Error(err)
		}
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, syscall.EPIPE) {
				t.Error(err)
			}
		case <-time.After(staveSignalSubprocessBound):
			t.Error("terminal fixture writer did not stop")
		}
	})
	return reader
}

func TestStaveTerminalRejectsUninterruptibleInput(t *testing.T) {
	reader, writer := io.Pipe()
	cleanupStaveTestCloser(t, "non-cancellable reader", reader)
	cleanupStaveTestCloser(t, "non-cancellable writer", writer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prepared := parentCancellationPreparedSession(context.Background(), t)
	defer prepared.Session.Close()
	returned := make(chan error, 1)
	go func() {
		returned <- (&StavePreview{}).runStaveTerminal(ctx, Options{Width: 80}, prepared, reader, io.Discard, false)
	}()
	cancel()
	select {
	case err := <-returned:
		if err == nil || !strings.Contains(err.Error(), "requires a file") {
			t.Fatalf("non-cancellable input error = %v", err)
		}
	case <-time.After(time.Second):
		// Unblock the old implementation before failing, so the regression itself
		// does not leave an input reader behind in the package test process.
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
		if err := waitSignalProcess(returned); err != nil {
			t.Log(err)
		}
		t.Fatal("terminal shutdown waited for non-cancellable input")
	}
	written := make(chan error, 1)
	go func() { _, err := writer.Write([]byte("x")); written <- err }()
	var data [1]byte
	if _, err := io.ReadFull(reader, data[:]); err != nil {
		t.Fatalf("borrowed input was closed: %v", err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	prepared.Session.Close()
	goleak.VerifyNone(t)
}
