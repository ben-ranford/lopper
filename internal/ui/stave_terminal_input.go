package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

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
	source *os.File
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func newStaveTerminalInput(source io.Reader) (*staveTerminalInput, error) {
	// Full-screen selection already requires a file. Generic readers use a
	// fallback that cannot interrupt a blocked Read, so joining them is unsafe.
	file, ok := source.(*os.File)
	if !ok || file == nil {
		return nil, fmt.Errorf("full-screen terminal input requires a file, got %T", source)
	}
	reader, err := uv.NewCancelReader(file)
	if err != nil {
		return nil, err
	}
	return &staveTerminalInput{reader: reader, source: file}, nil
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
		if !in.reader.Cancel() {
			// Some platform readers cannot interrupt an ongoing read. Do not
			// join that read or access its error until the relay has finished.
			select {
			case <-in.done:
			default:
				return in.reader.Close()
			}
		}
		<-in.done
	}
	return errors.Join(in.err, in.reader.Close())
}

// Bubble Tea still owns raw mode and renderer restoration. This EOF adapter
// preserves its terminal detection while keeping its internal parser idle.
func (in *staveTerminalInput) terminal() io.Reader {
	return &staveTerminalEOF{file: in.source}
}

type staveTerminalEOF struct{ file term.File }

func (*staveTerminalEOF) Read([]byte) (int, error)      { return 0, io.EOF }
func (r *staveTerminalEOF) Write(p []byte) (int, error) { return r.file.Write(p) }
func (*staveTerminalEOF) Close() error                  { return nil }
func (r *staveTerminalEOF) Fd() uintptr {
	return r.file.Fd()
}

type staveTerminalInputError struct{ err error }
