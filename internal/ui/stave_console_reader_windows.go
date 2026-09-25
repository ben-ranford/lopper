package ui

import (
	"fmt"
	"os"

	xwindows "github.com/charmbracelet/x/windows"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/windows"
)

// Stave shares the invitation's no-flush console event source. Serializing native
// key records lets Ultraviolet retain its own key/UTF-16 decoding while avoiding
// its constructor's destructive input flush during prompt-to-renderer handoff.
type staveConsoleReader struct {
	console *preferenceConsoleReader
	restore func() error
	pending []byte
}

func newStaveCancelReader(file *os.File) (cancelreader.CancelReader, error) {
	source, err := newPreferenceReader(file)
	if err != nil {
		return nil, err
	}
	handle := windows.Handle(file.Fd())
	var original uint32
	if err := windows.GetConsoleMode(handle, &original); err != nil {
		return nil, err
	}
	return prepareStaveConsole(source.(*preferenceConsoleReader), original, func(mode uint32) error { return windows.SetConsoleMode(handle, mode) })
}

func prepareStaveConsole(source *preferenceConsoleReader, original uint32, setMode func(uint32) error) (*staveConsoleReader, error) {
	// Native key records carry modifiers, Unicode, repeats and key releases.
	// Disable line/processed/VT input; retain resize events. No input is drained.
	if err := setMode(windows.ENABLE_WINDOW_INPUT | windows.ENABLE_EXTENDED_FLAGS); err != nil {
		return nil, err
	}
	return &staveConsoleReader{console: source, restore: func() error { return setMode(original) }}, nil
}

func (r *staveConsoleReader) Cancel() bool { return r.console.Cancel() }
func (r *staveConsoleReader) Close() error { return r.restore() }
func (r *staveConsoleReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 {
		record, err := r.console.next()
		if err != nil {
			return 0, err
		}
		r.pending = encodeStaveConsoleEvent(record)
	}
	n := copy(data, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func encodeStaveConsoleEvent(record xwindows.InputRecord) []byte {
	switch record.EventType {
	case xwindows.KEY_EVENT:
		key := record.KeyEvent()
		down := 0
		if key.KeyDown {
			down = 1
		}
		return fmt.Appendf(nil, "\x1b[%d;%d;%d;%d;%d;%d_", key.VirtualKeyCode, key.VirtualScanCode, key.Char, down, key.ControlKeyState, key.RepeatCount)
	case xwindows.WINDOW_BUFFER_SIZE_EVENT:
		size := record.WindowBufferSizeEvent().Size
		return fmt.Appendf(nil, "\x1b[8;%d;%dt", size.Y, size.X)
	case xwindows.FOCUS_EVENT:
		if record.FocusEvent().SetFocus {
			return []byte("\x1b[I")
		}
		return []byte("\x1b[O")
	default:
		return nil
	}
}
