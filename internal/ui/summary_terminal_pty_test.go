//go:build darwin || linux

package ui

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/creack/pty"
)

func TestSummaryTerminalArrowsWithoutEnter(t *testing.T) {
	for _, exit := range []string{"q\r", "\x03", "\x04", "cancel", "action", "mutation", "queued"} {
		t.Run(exit, func(t *testing.T) { runSummaryArrowPTY(t, exit) })
	}
}

func TestSummaryTerminalAltGrScopedCommands(t *testing.T) {
	runSummaryArrowPTY(t, "altgr")
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
	registerSummaryPTYCleanup(t, master, cancel, exited)
	s := NewSummary(terminal, terminal, &stubAnalyzer{report: report.Report{Dependencies: []report.DependencyReport{{Name: "alpha"}, {Name: "beta"}}}}, report.NewFormatter())
	runner, mutationRunner := summaryPTYActionRunner(exit)
	s.Actions = runner
	if mutationRunner != nil {
		defer mutationRunner.releaseOnce.Do(func() { close(mutationRunner.release) })
		s.Actions = mutationRunner
	}
	go func() { defer close(exited); done <- s.Start(ctx, Options{PageSize: 1}) }()
	waitSignalOutput(t, capture, done, func(s string) bool { return strings.Contains(s, "Page: 1/2") })
	exit = sendSummaryPTYCommand(t, exit, master, capture, done)
	if exit == "mutation" {
		finishMutationPTY(t, master, ctx, done, mutationRunner)
		return
	}
	finishSummaryPTY(t, exit, master, cancel, ctx, done, runner)
}

func registerSummaryPTYCleanup(t *testing.T, master *os.File, cancel context.CancelFunc, exited <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
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
}

func summaryPTYActionRunner(exit string) (*summaryBlockingRunner, *summaryUncancellableMutationRunner) {
	if exit == "mutation" {
		return nil, &summaryUncancellableMutationRunner{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	}
	return &summaryBlockingRunner{started: make(chan struct{})}, nil
}

func sendSummaryPTYCommand(t *testing.T, exit string, master *os.File, capture *signalPTYCapture, done <-chan error) string {
	t.Helper()
	input, ready, want := "\x1b[C", "Page: 2/2", "Page: 2/2"
	if exit == "altgr" {
		input, ready, want, exit = "open \x1b[113;7;64uscope/pkg\r", "scope/pkg\"\r\n", `No data for dependency "@scope/pkg"`, "q\r"
	}
	if _, err := master.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	waitSignalOutput(t, capture, done, func(s string) bool { return strings.Contains(s, ready) })
	if !strings.Contains(capture.String(), want) {
		t.Fatalf("summary command did not preserve its input: want %q in %q", want, capture.String())
	}
	return exit
}

func finishMutationPTY(t *testing.T, master *os.File, ctx context.Context, done <-chan error, runner *summaryUncancellableMutationRunner) {
	t.Helper()
	if _, err := master.Write([]byte("save-baseline nightly\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-ctx.Done():
		t.Fatal("mutating action never started")
	}
	if _, err := master.Write([]byte("\x03")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("terminal returned before the mutation completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	runner.releaseOnce.Do(func() { close(runner.release) })
	waitSummaryPTYResult(t, done, "\x03")
	select {
	case <-runner.finished:
	default:
		t.Fatal("terminal returned before the mutating action finished")
	}
}

func finishSummaryPTY(t *testing.T, exit string, master *os.File, cancel context.CancelFunc, ctx context.Context, done <-chan error, runner *summaryBlockingRunner) {
	t.Helper()
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
	waitSummaryPTYResult(t, done, exit)
}

func waitSummaryPTYResult(t *testing.T, done <-chan error, exit string) {
	t.Helper()
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

type summaryUncancellableMutationRunner struct {
	stubSummaryActionRunner
	started     chan struct{}
	release     chan struct{}
	finished    chan struct{}
	releaseOnce sync.Once
}

func (s *summaryUncancellableMutationRunner) SaveBaseline(context.Context, BaselineSaveRequest) (report.Report, string, error) {
	close(s.started)
	<-s.release
	close(s.finished)
	return report.Report{}, "saved.json", nil
}

func (s *summaryBlockingRunner) SaveBaseline(ctx context.Context, _ BaselineSaveRequest) (report.Report, string, error) {
	close(s.started)
	<-ctx.Done()
	return report.Report{}, "", ctx.Err()
}
