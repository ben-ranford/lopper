package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	xwindows "github.com/charmbracelet/x/windows"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/windows"
)

// The invitation borrows the console in its existing mode. The terminal runtime's
// cancel readers switch to raw input and flush pending events on Windows, which
// would discard typeahead and disable the console's usual Ctrl-C processing.
// Read one event at a time, leaving all events after Enter for the chosen UI.
type preferenceConsoleReader struct {
	wait      func() (uint32, error)
	read      func() (xwindows.InputRecord, error)
	canceled  atomic.Bool
	repeat    uint16
	character byte
}

func newPreferenceReader(file *os.File) (cancelreader.CancelReader, error) {
	handle := windows.Handle(file.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return nil, err
	}
	return &preferenceConsoleReader{
		wait: func() (uint32, error) { return windows.WaitForSingleObject(handle, 25) },
		read: func() (xwindows.InputRecord, error) {
			var record xwindows.InputRecord
			var count uint32
			err := xwindows.ReadConsoleInput(handle, &record, 1, &count)
			return record, err
		},
	}, nil
}

func (r *preferenceConsoleReader) Cancel() bool { r.canceled.Store(true); return true }
func (*preferenceConsoleReader) Close() error   { return nil }

func (r *preferenceConsoleReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	for !r.canceled.Load() {
		if r.repeat > 0 {
			r.repeat--
			data[0] = r.character
			return 1, nil
		}
		record, err := r.next()
		if err != nil {
			return 0, err
		}
		if err := r.accept(record); err != nil {
			return 0, err
		}
	}
	return 0, cancelreader.ErrCanceled
}

func (r *preferenceConsoleReader) next() (xwindows.InputRecord, error) {
	for !r.canceled.Load() {
		result, err := r.wait()
		if err != nil {
			return xwindows.InputRecord{}, err
		}
		if result == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
		if result != windows.WAIT_OBJECT_0 {
			return xwindows.InputRecord{}, fmt.Errorf("unexpected console wait result: %d", result)
		}
		if r.canceled.Load() {
			break
		}
		return r.read()
	}
	return xwindows.InputRecord{}, cancelreader.ErrCanceled
}

func (r *preferenceConsoleReader) accept(record xwindows.InputRecord) error {
	if record.EventType != xwindows.KEY_EVENT {
		return nil
	}
	key := record.KeyEvent()
	if !key.KeyDown {
		return nil
	}
	if key.VirtualKeyCode == xwindows.VK_BACK {
		key.Char = '\b'
	}
	// Modifiers, navigation keys and other non-character records do not edit
	// the invitation's answer. A shifted letter arrives as its own key record.
	if key.Char == 0 {
		return nil
	}
	switch key.Char {
	case 3:
		return context.Canceled
	case 4, 26:
		return io.EOF
	case '\r':
		r.character = '\n'
	default:
		// Choices are ASCII words; unrecognized characters still make the answer
		// invalid instead of silently converting, for example, “tréy” to “try”.
		r.character = '?'
		if key.Char > 0 && key.Char < 128 {
			r.character = byte(key.Char)
		}
	}
	r.repeat = key.RepeatCount
	if r.repeat == 0 {
		r.repeat = 1
	}
	return nil
}
