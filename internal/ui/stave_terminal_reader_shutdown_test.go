package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

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

func TestStaveTerminalInputPreservesReadErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	prepared := parentCancellationPreparedSession(ctx, t)
	defer prepared.Session.Close()
	want := errors.New("input read failed")
	preview := &StavePreview{}
	if err := preview.runStaveTerminal(ctx, Options{Width: 80}, prepared, &errReader{err: want}, io.Discard, false); !errors.Is(err, want) {
		t.Fatalf("input error = %v, want %v", err, want)
	}
}
