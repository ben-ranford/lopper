package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/term"
	"github.com/muesli/cancelreader"
)

// staveTerminalInput owns the cancel reader, not the caller's input. Its relay
// continues receiving ultraviolet's buffered events after Program.Run exits:
// Program.Send then discards them, allowing StreamEvents to finish flushing.
type staveTerminalInput struct {
	reader cancelreader.CancelReader
	source io.Reader
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func newStaveTerminalInput(source io.Reader) (*staveTerminalInput, error) {
	reader, err := uv.NewCancelReader(source)
	if err != nil {
		return nil, err
	}
	return &staveTerminalInput{reader: reader, source: source}, nil
}

func (in *staveTerminalInput) start(program *tea.Program) {
	ctx, cancel := context.WithCancel(context.Background())
	in.cancel = cancel
	in.done = make(chan struct{})
	events := make(chan uv.Event)
	go func() {
		in.err = uv.NewTerminalReader(in.reader, os.Getenv("TERM")).StreamEvents(ctx, events)
		close(events)
	}()
	go func() {
		defer close(in.done)
		for event := range events {
			program.Send(event)
		}
		if in.err != nil {
			program.Send(staveTerminalInputError{in.err})
		}
	}()
}

func (in *staveTerminalInput) close() error {
	if in.cancel != nil {
		in.cancel()
		in.reader.Cancel()
		<-in.done
	}
	return errors.Join(in.err, in.reader.Close())
}

// Bubble Tea still owns raw mode and renderer restoration. This EOF adapter
// preserves its terminal detection while keeping its internal parser idle.
func (in *staveTerminalInput) terminal() io.Reader {
	if file, ok := in.source.(term.File); ok {
		return &staveTerminalEOF{file: file}
	}
	return strings.NewReader("")
}

type staveTerminalEOF struct{ file term.File }

func (*staveTerminalEOF) Read([]byte) (int, error)      { return 0, io.EOF }
func (r *staveTerminalEOF) Write(p []byte) (int, error) { return r.file.Write(p) }
func (*staveTerminalEOF) Close() error                  { return nil }
func (r *staveTerminalEOF) Fd() uintptr {
	return r.file.Fd()
}

type staveTerminalInputError struct{ err error }
