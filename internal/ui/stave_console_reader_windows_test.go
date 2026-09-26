package ui

import (
	"encoding/binary"
	"errors"
	"io"
	"testing"

	xwindows "github.com/charmbracelet/x/windows"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/windows"
)

func TestStaveConsolePromptHandoffAndExactModeRestoration(t *testing.T) {
	for _, original := range []uint32{0, windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT} {
		events := []xwindows.InputRecord{preferenceKey('t', true, 1), preferenceKey('\r', true, 1), preferenceKey('q', true, 1)}
		index := 0
		source := &preferenceConsoleReader{wait: func() (uint32, error) { return windows.WAIT_OBJECT_0, nil }, read: func() (xwindows.InputRecord, error) {
			if index == len(events) {
				return xwindows.InputRecord{}, io.EOF
			}
			event := events[index]
			index++
			return event, nil
		}}
		var consent [2]byte
		if _, err := io.ReadFull(source, consent[:]); err != nil || string(consent[:]) != "t\n" {
			t.Fatalf("consent=%q %v", consent, err)
		}
		mode := original
		updates := 0
		reader, err := prepareStaveConsole(source, original, func(next uint32) error { mode = next; updates++; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if index != 2 {
			t.Fatal("startup consumed pending command")
		}
		command, err := io.ReadAll(reader)
		if err != nil || string(command) != "\x1b[0;0;113;1;0;1_" {
			t.Fatalf("command lost/duplicated: %q %v", command, err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
		if mode != original || updates != 2 {
			t.Fatalf("mode=%d original=%d updates=%d", mode, original, updates)
		}
	}
}

func TestStaveConsoleCancelAndRestoreFailures(t *testing.T) {
	failure := errors.New("console mode failed")
	source := &preferenceConsoleReader{}
	if _, err := prepareStaveConsole(source, 1, func(uint32) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	sets := 0
	reader, err := prepareStaveConsole(source, 1, func(uint32) error {
		sets++
		if sets == 2 {
			return failure
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reader.Cancel()
	var data [1]byte
	if _, err := reader.Read(data[:]); !errors.Is(err, cancelreader.ErrCanceled) {
		t.Fatal(err)
	}
	if err := reader.Close(); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if n, err := reader.Read(nil); n != 0 || err != nil {
		t.Fatalf("empty=%d %v", n, err)
	}
}

func TestStaveConsoleResizeFocusAndKeyRecords(t *testing.T) {
	resize := xwindows.InputRecord{EventType: xwindows.WINDOW_BUFFER_SIZE_EVENT}
	binary.LittleEndian.PutUint16(resize.Event[:2], 100)
	binary.LittleEndian.PutUint16(resize.Event[2:4], 30)
	if got := string(encodeStaveConsoleEvent(resize)); got != "\x1b[8;30;100t" {
		t.Fatalf("resize=%q", got)
	}
	focus := xwindows.InputRecord{EventType: xwindows.FOCUS_EVENT}
	if got := string(encodeStaveConsoleEvent(focus)); got != "\x1b[O" {
		t.Fatalf("blur=%q", got)
	}
	focus.Event[0] = 1
	if got := string(encodeStaveConsoleEvent(focus)); got != "\x1b[I" {
		t.Fatalf("focus=%q", got)
	}
	if got := encodeStaveConsoleEvent(xwindows.InputRecord{}); len(got) != 0 {
		t.Fatalf("unexpected event=%q", got)
	}
	if got := string(encodeStaveConsoleEvent(preferenceKey('q', false, 1))); got != "\x1b[0;0;113;0;0;1_" {
		t.Fatalf("release=%q", got)
	}
}
