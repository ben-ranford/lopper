package ui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
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
