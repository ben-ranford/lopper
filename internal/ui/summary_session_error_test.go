//go:build !regressionproof && (darwin || linux)

package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/creack/pty"
)

type summarySessionFailedOutput struct {
	mu     sync.Mutex
	ready  chan struct{}
	once   sync.Once
	phase  string
	err    error
	failed bool
}

func (s *summarySessionFailedOutput) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	text := string(data)
	if strings.Contains(text, "> ") {
		s.once.Do(func() { close(s.ready) })
	}
	fail := s.phase == "shutdown" && text == "\r\n" || s.phase == "command" && strings.Contains(text, "> q")
	if fail && !s.failed {
		s.failed = true
		return 0, s.err
	}
	return len(data), nil
}

func TestSummaryTerminalOutputFailuresRestoreCallerTerminal(t *testing.T) {
	for _, phase := range []string{"command", "shutdown"} {
		t.Run(phase, func(t *testing.T) { checkSummarySessionOutputFailure(t, phase) })
	}
}

func checkSummarySessionOutputFailure(t *testing.T, phase string) {
	t.Helper()
	t.Setenv("TERM", "xterm-256color")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "summary error master", master)
	cleanupStaveTestCloser(t, "summary error terminal", terminal)
	original := rawModeLeaseState(t, terminal)
	want := errors.New("summary output disconnected")
	output := &summarySessionFailedOutput{ready: make(chan struct{}), phase: phase, err: want}
	summary := NewSummary(output, terminal, &stubAnalyzer{}, report.NewFormatter())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan error, 1)
	exited := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Error("summary error session did not release its reader")
		}
	})
	go func() {
		defer close(exited)
		done <- summary.runTerminal(ctx, Options{}, summaryReportView{})
	}()
	select {
	case <-output.ready:
	case err := <-done:
		t.Fatalf("session exited before accepting input: %v", err)
	case <-ctx.Done():
		t.Fatal("summary prompt did not become ready")
	}
	if *rawModeLeaseState(t, terminal) == *original {
		t.Fatal("session never entered raw terminal mode")
	}
	if _, err := master.Write([]byte("q\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("session error=%v, want original output failure", err)
		}
	case <-ctx.Done():
		t.Fatal("output failure did not stop the summary session")
	}
	if *rawModeLeaseState(t, terminal) != *original {
		t.Fatal("output failure did not restore the caller's terminal state")
	}
}

func TestSummaryLineModePropagatesCommandOutputFailure(t *testing.T) {
	want := io.ErrClosedPipe
	output := &failOnMarkerWriter{marker: "Unknown command", failOn: 1, err: want}
	summary := NewSummary(output, strings.NewReader("unknown-command\nq\n"), &stubAnalyzer{}, report.NewFormatter())
	if err := summary.Start(context.Background(), Options{}); !errors.Is(err, want) {
		t.Fatalf("line command error=%v, want disconnected output", err)
	}
	if output.seen != 1 {
		t.Fatalf("command output failure was not exercised: %d writes", output.seen)
	}
}
