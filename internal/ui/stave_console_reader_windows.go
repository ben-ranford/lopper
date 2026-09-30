package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf16"
	"unicode/utf8"

	xwindows "github.com/charmbracelet/x/windows"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/windows"
)

// Stave shares the invitation's no-flush console event source. Serializing native
// key records preserves Ultraviolet's key decoding while avoiding
// its constructor's destructive input flush during prompt-to-renderer handoff.
type staveConsoleReader struct {
	console    *preferenceConsoleReader
	restore    func() error
	pending    []byte
	surrogates [2]xwindows.KeyEventRecord
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
			if !errors.Is(err, io.EOF) {
				return 0, err
			}
			r.flushSurrogates()
			if len(r.pending) == 0 {
				return 0, err
			}
			break
		}
		r.pending = r.encodeEvent(record)
	}
	n := copy(data, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *staveConsoleReader) encodeEvent(record xwindows.InputRecord) []byte {
	switch record.EventType {
	case xwindows.KEY_EVENT:
		return r.encodeKey(record.KeyEvent())
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

func (r *staveConsoleReader) encodeKey(key xwindows.KeyEventRecord) []byte {
	down := staveKeyDirection(key)
	previous := r.surrogates[down]
	r.surrogates[down] = xwindows.KeyEventRecord{}
	var data []byte
	if previous.Char != 0 {
		combined := utf16.DecodeRune(previous.Char, key.Char)
		previous.Char = key.Char
		if combined != utf8.RuneError && previous == key {
			key.Char = combined
			return appendStaveKey(data, key)
		}
		previous.Char = utf8.RuneError
		data = appendStaveKey(data, previous)
	}
	if key.Char >= 0xD800 && key.Char <= 0xDBFF {
		// Ultraviolet only buffers serialized surrogate pairs when VK is zero.
		// Combine native pairs here too, retaining VK_PACKET and its metadata.
		r.surrogates[down] = key
		return data
	}
	if utf16.IsSurrogate(key.Char) {
		key.Char = utf8.RuneError
	}
	return appendStaveKey(data, key)
}

func (r *staveConsoleReader) flushSurrogates() {
	for direction, key := range r.surrogates {
		if key.Char != 0 {
			key.Char = utf8.RuneError
			r.pending = appendStaveKey(r.pending, key)
			r.surrogates[direction] = xwindows.KeyEventRecord{}
		}
	}
}

func appendStaveKey(data []byte, key xwindows.KeyEventRecord) []byte {
	return fmt.Appendf(data, "\x1b[%d;%d;%d;%d;%d;%d_", key.VirtualKeyCode, key.VirtualScanCode, key.Char, staveKeyDirection(key), key.ControlKeyState, key.RepeatCount)
}

func staveKeyDirection(key xwindows.KeyEventRecord) int {
	if key.KeyDown {
		return 1
	}
	return 0
}
