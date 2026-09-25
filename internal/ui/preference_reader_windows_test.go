package ui

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	xwindows "github.com/charmbracelet/x/windows"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/windows"
)

func preferenceKey(char uint16, down bool, repeat uint16) xwindows.InputRecord {
	record := xwindows.InputRecord{EventType: xwindows.KEY_EVENT}
	if down {
		binary.LittleEndian.PutUint32(record.Event[:4], 1)
	}
	binary.LittleEndian.PutUint16(record.Event[4:6], repeat)
	binary.LittleEndian.PutUint16(record.Event[10:12], char)
	return record
}

func TestPreferenceConsoleEnterPreservesQueuedUIInput(t *testing.T) {
	events := []xwindows.InputRecord{preferenceKey('t', true, 1), preferenceKey('\r', true, 1), preferenceKey('q', true, 1)}
	reads := 0
	reader := &preferenceConsoleReader{wait: func() (uint32, error) { return windows.WAIT_OBJECT_0, nil }, read: func() (xwindows.InputRecord, error) { event := events[reads]; reads++; return event, nil }}
	var input [2]byte
	if _, err := io.ReadFull(reader, input[:]); err != nil {
		t.Fatal(err)
	}
	if string(input[:]) != "t\n" {
		t.Fatalf("Enter=%q", input)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if reads != 2 || events[reads].KeyEvent().Char != 'q' {
		t.Fatal("read ahead or discarded UI command")
	}
}

func TestPreferenceConsoleCancellationDoesNotReadOrFlush(t *testing.T) {
	reader := &preferenceConsoleReader{read: func() (xwindows.InputRecord, error) {
		t.Fatal("consumed input after cancellation")
		return xwindows.InputRecord{}, nil
	}}
	reader.wait = func() (uint32, error) { reader.Cancel(); return uint32(windows.WAIT_TIMEOUT), nil }
	var input [1]byte
	if _, err := reader.Read(input[:]); !errors.Is(err, cancelreader.ErrCanceled) {
		t.Fatalf("cancel=%v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPreferenceConsoleKeysAndErrors(t *testing.T) {
	for _, tc := range []struct {
		char uint16
		want error
	}{{3, context.Canceled}, {4, io.EOF}, {26, io.EOF}} {
		reader := &preferenceConsoleReader{}
		if err := reader.accept(preferenceKey(tc.char, true, 1)); !errors.Is(err, tc.want) {
			t.Fatalf("key %d error=%v", tc.char, err)
		}
	}
	reader := &preferenceConsoleReader{}
	for _, event := range []xwindows.InputRecord{{}, preferenceKey('a', false, 1)} {
		if err := reader.accept(event); err != nil || reader.repeat != 0 {
			t.Fatal("accepted non-keydown")
		}
	}
	if err := reader.accept(preferenceKey('a', true, 2)); err != nil {
		t.Fatal(err)
	}
	var input [2]byte
	if _, err := io.ReadFull(reader, input[:]); err != nil || string(input[:]) != "aa" {
		t.Fatalf("repeat=%q %v", input, err)
	}
	failure := errors.New("console failed")
	reader.wait = func() (uint32, error) { return 0, failure }
	if _, err := reader.Read(input[:]); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	reader.wait = func() (uint32, error) { return windows.WAIT_FAILED, nil }
	if _, err := reader.Read(input[:]); err == nil {
		t.Fatal("accepted unexpected wait")
	}
	reader.wait = func() (uint32, error) { return windows.WAIT_OBJECT_0, nil }
	reader.read = func() (xwindows.InputRecord, error) { return xwindows.InputRecord{}, failure }
	if _, err := reader.Read(input[:]); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func preferenceNativeKey(virtualKey uint16, char uint16, down bool, modifiers uint32) xwindows.InputRecord {
	record := preferenceKey(char, down, 1)
	binary.LittleEndian.PutUint16(record.Event[6:8], virtualKey)
	binary.LittleEndian.PutUint32(record.Event[12:16], modifiers)
	return record
}

func TestPreferenceConsoleShiftAndBackspaceEditing(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		events     []xwindows.InputRecord
	}{
		{name: "shifted try", want: "T", events: []xwindows.InputRecord{
			preferenceNativeKey(xwindows.VK_SHIFT, 0, true, xwindows.SHIFT_PRESSED),
			preferenceNativeKey('T', 'T', true, xwindows.SHIFT_PRESSED),
			preferenceNativeKey('T', 'T', false, xwindows.SHIFT_PRESSED),
			preferenceNativeKey(xwindows.VK_SHIFT, 0, false, 0),
		}},
		{name: "correct typo", want: "try", events: []xwindows.InputRecord{
			preferenceNativeKey('T', 't', true, 0), preferenceNativeKey('X', 'x', true, 0),
			preferenceNativeKey(xwindows.VK_BACK, '\b', true, 0),
			preferenceNativeKey('R', 'r', true, 0), preferenceNativeKey('Y', 'y', true, 0),
		}},
		{name: "empty and characterless backspace", want: "k", events: []xwindows.InputRecord{
			preferenceNativeKey(xwindows.VK_BACK, 0, true, 0),
			preferenceNativeKey('T', 't', true, 0), preferenceNativeKey(xwindows.VK_BACK, 0, true, 0),
			preferenceNativeKey(xwindows.VK_CONTROL, 0, true, xwindows.LEFT_CTRL_PRESSED),
			preferenceNativeKey(xwindows.VK_CONTROL, 0, false, 0),
			preferenceNativeKey('K', 'k', true, 0),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := append(tc.events, preferenceNativeKey(xwindows.VK_RETURN, '\r', true, 0), preferenceNativeKey('Q', 'q', true, 0))
			reads := 0
			reader := &preferenceConsoleReader{wait: func() (uint32, error) { return windows.WAIT_OBJECT_0, nil }, read: func() (xwindows.InputRecord, error) {
				if reads >= len(events) {
					return xwindows.InputRecord{}, io.EOF
				}
				event := events[reads]
				reads++
				return event, nil
			}}
			got, err := readPreferenceLine(reader)
			if err != nil || got != tc.want {
				t.Fatalf("answer=%q want=%q err=%v", got, tc.want, err)
			}
			if reads != len(events)-1 || events[reads].KeyEvent().Char != 'q' {
				t.Fatal("consumed UI input after Enter")
			}
		})
	}
}
