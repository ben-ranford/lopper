//go:build darwin || linux

package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/creack/pty"
)

func TestSummaryTerminalArrowsWithoutEnter(t *testing.T) {
	for _, exit := range []string{"q\r", "\x03", "\x04", "cancel", "action", "queued"} {
		t.Run(exit, func(t *testing.T) { runSummaryArrowPTY(t, exit) })
	}
}

func runSummaryArrowPTY(t *testing.T, exit string) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	capture := newSignalPTYCapture(master)
	t.Cleanup(func() { waitSignalCapture(t, capture) })
	cleanupStaveTestCloser(t, "summary master", master)
	cleanupStaveTestCloser(t, "summary terminal", terminal)
	if err := pty.Setsize(terminal, &pty.Winsize{Rows: 24, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done := make(chan error, 1)
	exited := make(chan struct{})
	t.Cleanup(func() {
		// Also release the baseline line reader when a regression assertion fails.
		defer cancel()
		select {
		case <-exited:
			return
		default:
		}
		if _, err := master.Write([]byte("\rq\r")); err != nil {
			t.Errorf("release summary input: %v", err)
		}
		cancel()
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Error("summary cleanup timed out")
		}
	})
	s := NewSummary(terminal, terminal, &stubAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{Name: "alpha"}, {Name: "beta"}}}}, report.NewFormatter())
	runner := &summaryBlockingRunner{started: make(chan struct{})}
	s.Actions = runner
	go func() { defer close(exited); done <- s.Start(ctx, Options{PageSize: 1}) }()
	waitSignalOutput(t, capture, done, func(s string) bool { return strings.Contains(s, "Page: 1/2") })
	if _, err := master.Write([]byte("\x1b[C")); err != nil {
		t.Fatal(err)
	}
	waitSignalOutput(t, capture, done, func(s string) bool { return strings.Contains(s, "Page: 2/2") })
	if exit == "queued" {
		exit = "q\r" + strings.Repeat("x", 1024)
	}
	if exit == "action" {
		if _, err := master.Write([]byte("save-baseline nightly\r")); err != nil {
			t.Fatal(err)
		}
		select {
		case <-runner.started:
		case <-ctx.Done():
			t.Fatal("action never started")
		}
		exit = "\x03"
	}
	if exit == "cancel" {
		cancel()
	} else if _, err := master.Write([]byte(exit)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if exit == "cancel" {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("summary did not stop")
	}
}

// Deliberately block until cancellation so the PTY proves Ctrl-C is processed
// while the action is running, rather than only after a fast action returns.
type summaryBlockingRunner struct {
	stubSummaryActionRunner
	started chan struct{}
}

func (s *summaryBlockingRunner) SaveBaseline(ctx context.Context, _ BaselineSaveRequest) (report.Report, string, error) {
	close(s.started)
	<-ctx.Done()
	return report.Report{}, "", ctx.Err()
}
