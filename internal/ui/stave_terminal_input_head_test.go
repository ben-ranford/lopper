package ui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStaveTerminalEOFAdapterPreservesBorrowedFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "adapter file", file)
	adapter := staveTerminalEOF{file: file}
	if adapter.Fd() != file.Fd() {
		t.Fatal("adapter lost terminal descriptor")
	}
	if _, err := adapter.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if n, err := adapter.Read(make([]byte, 8)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("adapter read = %d, %v", n, err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("adapter closed borrowed file: %v", err)
	}
}

func TestStaveTerminalInputCloseBeforeStart(t *testing.T) {
	input, err := newStaveTerminalInput(staveTerminalTestInput(t, strings.NewReader("")))
	if err != nil {
		t.Fatal(err)
	}
	if err := input.close(); err != nil {
		t.Fatal(err)
	}
}

type failingTerminalReader struct{ err error }

func (r *failingTerminalReader) Read([]byte) (int, error) { return 0, r.err }
func (*failingTerminalReader) Close() error               { return nil }
func (*failingTerminalReader) Cancel() bool               { return true }

func TestStaveTerminalInputPreservesReadErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	want := errors.New("input read failed")
	input := &staveTerminalInput{reader: &failingTerminalReader{err: want}}
	model := &staveTerminalModel{bridge: &staveTerminal{ctx: ctx}}
	program := tea.NewProgram(model, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignalHandler())
	model.startInput = func() { input.start(program) }
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	if err := input.close(); !errors.Is(err, want) {
		t.Fatalf("input error = %v, want %v", err, want)
	}
	if !errors.Is(model.bridge.err, want) {
		t.Fatalf("model input error = %v", model.bridge.err)
	}
}

// Simulate an OS reader whose in-flight read cannot be cancelled.
type nonCancelableTerminalReader struct {
	entered chan struct{}
	release chan struct{}
	closed  chan struct{}
	err     error
}

func (r *nonCancelableTerminalReader) Read([]byte) (int, error) {
	close(r.entered)
	<-r.release
	return 0, io.EOF
}
func (*nonCancelableTerminalReader) Cancel() bool { return false }
func (r *nonCancelableTerminalReader) Close() error {
	close(r.closed)
	return r.err
}

func TestStaveTerminalInputFailedCancellationDoesNotBlock(t *testing.T) {
	want := errors.New("reader close failed")
	reader := &nonCancelableTerminalReader{
		entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), err: want,
	}
	input := &staveTerminalInput{reader: reader}
	program := tea.NewProgram(&staveTerminalModel{}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignalHandler())
	program.Kill()
	input.start(program)
	t.Cleanup(func() {
		close(reader.release)
		select {
		case <-input.done:
		case <-time.After(staveSignalSubprocessBound):
			t.Error("input relay failed to finish after read was released")
		}
	})
	<-reader.entered
	result := make(chan error, 1)
	go func() { result <- input.close() }()
	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("close error = %v, want %v", err, want)
		}
	case <-time.After(staveSignalSubprocessBound):
		t.Fatal("close blocked after failed reader cancellation")
	}
	select {
	case <-reader.closed:
	default:
		t.Fatal("reader resources were not closed")
	}
}

func TestStaveTerminalInputFailedCancellationPreservesFinishedError(t *testing.T) {
	want := errors.New("finished read failed")
	reader := &nonCancelableTerminalReader{closed: make(chan struct{})}
	input := &staveTerminalInput{reader: reader, cancel: func() {}, done: make(chan struct{}), err: want}
	close(input.done)
	if err := input.close(); !errors.Is(err, want) {
		t.Fatalf("close error = %v, want %v", err, want)
	}
}
