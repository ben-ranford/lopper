//go:build !windows

package ui

import (
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

// Hold the independent read across Bubble Tea shutdown so terminal restoration
// cannot pass merely because the reader happened to finish first.
type rawModeLeaseReader struct {
	entered   chan struct{}
	cancelled chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (r *rawModeLeaseReader) Read([]byte) (int, error) {
	close(r.entered)
	<-r.release
	return 0, io.EOF
}

func (r *rawModeLeaseReader) Cancel() bool {
	close(r.cancelled)
	return true
}

func (*rawModeLeaseReader) Close() error { return nil }

func (r *rawModeLeaseReader) finish() { r.once.Do(func() { close(r.release) }) }

type rawModeLeaseModel struct {
	input   *staveTerminalInput
	program *tea.Program
	reader  *rawModeLeaseReader
}

func (m *rawModeLeaseModel) Init() tea.Cmd {
	m.input.start(m.program)
	return func() tea.Msg {
		<-m.reader.entered
		return tea.Quit()
	}
}

func (m *rawModeLeaseModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (*rawModeLeaseModel) View() tea.View                        { return tea.NewView("") }

func TestStaveTerminalRawLeaseOutlivesProgramAndInputRead(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "raw lease master", master)
	cleanupStaveTestCloser(t, "raw lease terminal", terminal)
	original := rawModeLeaseState(t, terminal)
	input, err := newStaveTerminalInput(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if err := input.reader.Close(); err != nil {
		t.Fatal(err)
	}
	reader := &rawModeLeaseReader{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	input.reader = reader
	restore, err := enterStaveTerminalRaw(input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		reader.finish()
		if input.done != nil {
			select {
			case <-input.done:
			case <-time.After(staveSignalSubprocessBound):
				t.Error("raw lease input did not finish after release")
			}
		}
		if err := restore(); err != nil {
			t.Error(err)
		}
	})
	raw := rawModeLeaseState(t, terminal)
	if *raw == *original {
		t.Fatal("terminal did not enter raw mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	model := &rawModeLeaseModel{input: input, reader: reader}
	model.program = tea.NewProgram(model, tea.WithInput(input.terminal()), tea.WithOutput(io.Discard), tea.WithContext(ctx), tea.WithoutSignalHandler())
	if _, err := model.program.Run(); err != nil {
		t.Fatal(err)
	}
	if *rawModeLeaseState(t, terminal) != *raw {
		t.Fatal("Bubble Tea restored terminal mode while the independent reader was active")
	}
	closeRawModeLeaseInput(ctx, t, input, reader, raw)
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if *rawModeLeaseState(t, terminal) != *original {
		t.Fatal("raw lease did not restore the exact original terminal state")
	}
}

func TestStaveTerminalRawLeaseAcceptsNonTerminalInput(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "raw lease pipe reader", reader)
	cleanupStaveTestCloser(t, "raw lease pipe writer", writer)
	restore, err := enterStaveTerminalRaw(&staveTerminalInput{source: reader})
	if err != nil {
		t.Fatalf("non-terminal input required raw mode: %v", err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Stat(); err != nil {
		t.Fatalf("raw lease closed borrowed pipe: %v", err)
	}
}

func closeRawModeLeaseInput(ctx context.Context, t *testing.T, input *staveTerminalInput, reader *rawModeLeaseReader, raw *term.State) {
	t.Helper()
	closed := make(chan error, 1)
	go func() { closed <- input.close() }()
	select {
	case <-reader.cancelled:
	case <-ctx.Done():
		t.Fatal("input shutdown did not cancel the reader")
	}
	select {
	case err := <-closed:
		t.Fatalf("input shutdown returned before its read finished: %v", err)
	default:
	}
	if *rawModeLeaseState(t, input.source) != *raw {
		t.Fatal("terminal mode was restored before the independent read finished")
	}
	reader.finish()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("input shutdown did not finish after releasing its read")
	}
}

func rawModeLeaseState(t *testing.T, terminal *os.File) *term.State {
	t.Helper()
	state, err := term.GetState(terminal.Fd())
	if err != nil {
		t.Fatalf("read borrowed terminal state: %v", err)
	}
	return state
}
