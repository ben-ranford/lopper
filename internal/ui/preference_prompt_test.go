package ui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"go.uber.org/goleak"
)

func TestStavePreferencePromptChoicesAndInputHandoff(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"t\nquit\n", "stave"}, {"Keep current\nquit\n", "legacy"}, {"\nquit\n", ""}, {"later\nquit\n", ""}, {"invalid\ntry\nquit\n", "stave"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := master.Close(); err != nil {
					t.Error(err)
				}
			}()
			defer func() {
				if err := slave.Close(); err != nil {
					t.Error(err)
				}
			}()
			done := make(chan struct{})
			var choice string
			var promptErr error
			var output bytes.Buffer
			go func() { choice, promptErr = PromptPreference(context.Background(), slave, &output); close(done) }()
			if _, err := master.Write([]byte(tc.input)); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("prompt blocked")
			}
			if promptErr != nil || choice != tc.want {
				t.Fatalf("choice=%q err=%v", choice, promptErr)
			}
			var remaining [5]byte
			if _, err := io.ReadFull(slave, remaining[:]); err != nil {
				t.Fatal(err)
			}
			if string(remaining[:]) != "quit\n" {
				t.Fatalf("lost UI command %q", remaining)
			}
			if !strings.Contains(output.String(), "preview") {
				t.Fatalf("missing explanation %q", output.String())
			}
		})
	}
}

func TestStavePreferencePromptCancellationAndEOF(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	for _, eof := range []bool{false, true} {
		master, slave, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		ready := &preferencePromptReady{ready: make(chan struct{})}
		go func() { _, err := PromptPreference(ctx, slave, ready); done <- err }()
		<-ready.ready
		if eof {
			if _, err := master.Write([]byte{4}); err != nil {
				t.Fatal(err)
			}
		} else {
			cancel()
		}
		select {
		case err := <-done:
			if eof && !errors.Is(err, io.EOF) {
				t.Fatalf("EOF=%v", err)
			}
			if !eof && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel=%v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("prompt blocked")
		}
		cancel()
		if err := master.Close(); err != nil {
			t.Error(err)
		}
		if err := slave.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestStavePreferenceEligibility(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := master.Close(); err != nil {
			t.Error(err)
		}
	}()
	defer func() {
		if err := slave.Close(); err != nil {
			t.Error(err)
		}
	}()
	t.Setenv("CI", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if !CanOfferPreference(slave, slave) {
		t.Fatal("capable terminal rejected")
	}
	if CanOfferPreference(strings.NewReader(""), slave) || CanOfferPreference(slave, io.Discard) {
		t.Fatal("redirected streams accepted")
	}
	for _, pair := range [][2]string{{"TERM", "dumb"}, {"NO_COLOR", "1"}, {"CI", "true"}} {
		t.Run(pair[0], func(t *testing.T) {
			t.Setenv(pair[0], pair[1])
			if CanOfferPreference(slave, slave) {
				t.Fatal("ineligible terminal accepted")
			}
		})
	}
}

func TestStavePreferencePromptErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PromptPreference(ctx, nil, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := PromptPreference(context.Background(), strings.NewReader("t\n"), io.Discard); err == nil {
		t.Fatal("generic reader accepted")
	}
	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Error(err)
	}
	if _, err := PromptPreference(context.Background(), file, io.Discard); err == nil {
		t.Fatal("closed input accepted")
	}
	if _, err := readPreferenceLine(strings.NewReader("partial")); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if got, err := readPreferenceLine(strings.NewReader(strings.Repeat("x", 100) + "\n")); err != nil || len(got) != 80 {
		t.Fatalf("bounded line=%q %v", got, err)
	}
}

type preferencePromptReady struct {
	ready chan struct{}
	once  sync.Once
}

func (p *preferencePromptReady) Write(data []byte) (int, error) {
	p.once.Do(func() { close(p.ready) })
	return len(data), nil
}

type preferencePromptFailedWriter struct{}

func (*preferencePromptFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestStavePreferencePromptOutputFailure(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := master.Close(); err != nil {
			t.Error(err)
		}
		if err := slave.Close(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := PromptPreference(context.Background(), slave, &preferencePromptFailedWriter{}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("error=%v", err)
	}
}

func TestStavePreferenceLineEditingAndHandoff(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"tx\bry\nq", "try"}, {"\b\bt\x7fk\nq", "k"}, {"tré\by\nq", "try"},
	} {
		input := strings.NewReader(tc.input)
		got, err := readPreferenceLine(input)
		if err != nil || got != tc.want {
			t.Fatalf("answer=%q want=%q err=%v", got, tc.want, err)
		}
		rest, err := io.ReadAll(input)
		if err != nil || string(rest) != "q" {
			t.Fatalf("UI input=%q %v", rest, err)
		}
	}
}
