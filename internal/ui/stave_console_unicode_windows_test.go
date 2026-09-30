package ui

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"testing"
	"testing/iotest"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	xwindows "github.com/charmbracelet/x/windows"
	"github.com/muesli/cancelreader"
	"golang.org/x/sys/windows"
)

const (
	staveUnicodePacket = 0xe7 // VK_PACKET carries Unicode input without a physical key.
	staveUnicodeHigh   = 0xd83d
	staveUnicodeLow    = 0xde00
)

type staveUnicodeEvent struct {
	code    rune
	text    string
	mod     uv.KeyMod
	release bool
}

func staveUnicodeRecord(vk, char uint16, down bool, modifiers uint32, repeat uint16) xwindows.InputRecord {
	record := preferenceNativeKey(vk, char, down, modifiers)
	binary.LittleEndian.PutUint16(record.Event[4:6], repeat)
	binary.LittleEndian.PutUint16(record.Event[8:10], 7)
	return record
}

func TestStaveConsoleUnicodePairsThroughDecoder(t *testing.T) {
	for _, vk := range []uint16{0, staveUnicodePacket} {
		for _, singleByte := range []bool{false, true} {
			records := []xwindows.InputRecord{
				staveUnicodeRecord(vk, staveUnicodeHigh, true, 0, 1),
				staveUnicodeRecord(vk, staveUnicodeHigh, false, 0, 1),
				staveUnicodeRecord(vk, staveUnicodeLow, true, 0, 1),
				staveUnicodeRecord(vk, staveUnicodeLow, false, 0, 1),
				staveUnicodeRecord('Q', 'q', true, 0, 1),
			}
			want := []staveUnicodeEvent{{code: '😀', text: "😀"}, {code: '😀', text: "😀", release: true}, {code: 'q', text: "q"}}
			if got := decodeStaveUnicodeRecords(t, records, singleByte); !reflect.DeepEqual(got, want) {
				t.Fatalf("vk=%d singleByte=%v: events=%+v want=%+v", vk, singleByte, got, want)
			}
		}
	}
}

func TestStaveConsoleUnicodeModifiersAndRepeats(t *testing.T) {
	for _, tc := range []struct {
		name    string
		control uint32
		mod     uv.KeyMod
		text    string
		down    bool
	}{
		{name: "control press", control: xwindows.LEFT_CTRL_PRESSED, mod: uv.ModCtrl, down: true},
		{name: "shift release", control: xwindows.SHIFT_PRESSED, mod: uv.ModShift, text: "😀"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records := []xwindows.InputRecord{
				staveUnicodeRecord(staveUnicodePacket, staveUnicodeHigh, tc.down, tc.control, 3),
				staveUnicodeRecord(staveUnicodePacket, staveUnicodeLow, tc.down, tc.control, 3),
			}
			key := staveUnicodeEvent{code: '😀', text: tc.text, mod: tc.mod, release: !tc.down}
			want := []staveUnicodeEvent{key, key, key}
			if got := decodeStaveUnicodeRecords(t, records, true); !reflect.DeepEqual(got, want) {
				t.Fatalf("events=%+v want=%+v", got, want)
			}
		})
	}
}

func TestStaveConsoleUnicodeMalformedInputPreservesFollowingKeys(t *testing.T) {
	high := staveUnicodeRecord(staveUnicodePacket, staveUnicodeHigh, true, 0, 1)
	low := staveUnicodeRecord(staveUnicodePacket, staveUnicodeLow, true, 0, 1)
	quit := staveUnicodeRecord('Q', 'q', true, 0, 1)
	replacement := staveUnicodeEvent{code: '\ufffd', text: "\ufffd"}
	for _, tc := range []struct {
		name    string
		records []xwindows.InputRecord
		want    []staveUnicodeEvent
	}{
		{"lone low", []xwindows.InputRecord{low, quit}, []staveUnicodeEvent{replacement, {code: 'q', text: "q"}}},
		{"high then key", []xwindows.InputRecord{high, quit}, []staveUnicodeEvent{replacement, {code: 'q', text: "q"}}},
		{"high then pair", []xwindows.InputRecord{high, high, low}, []staveUnicodeEvent{replacement, {code: '😀', text: "😀"}}},
		{"high at EOF", []xwindows.InputRecord{high}, []staveUnicodeEvent{replacement}},
		{"reversed pair", []xwindows.InputRecord{low, high}, []staveUnicodeEvent{replacement, replacement}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeStaveUnicodeRecords(t, tc.records, true); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("events=%+v want=%+v", got, tc.want)
			}
		})
	}
}

func TestStaveConsoleUnicodeDoesNotPairDifferentMetadata(t *testing.T) {
	high := staveUnicodeRecord(staveUnicodePacket, staveUnicodeHigh, true, 0, 1)
	low := staveUnicodeRecord(staveUnicodePacket, staveUnicodeLow, true, 0, 1)
	otherScan := low
	binary.LittleEndian.PutUint16(otherScan.Event[8:10], 8)
	replacement := staveUnicodeEvent{code: '\ufffd', text: "\ufffd"}
	for _, tc := range []struct {
		name string
		low  xwindows.InputRecord
		want []staveUnicodeEvent
	}{
		{"virtual key", staveUnicodeRecord('Q', staveUnicodeLow, true, 0, 1), []staveUnicodeEvent{replacement, replacement}},
		{"scan code", otherScan, []staveUnicodeEvent{replacement, replacement}},
		{"control state", staveUnicodeRecord(staveUnicodePacket, staveUnicodeLow, true, xwindows.LEFT_CTRL_PRESSED, 1), []staveUnicodeEvent{replacement, {code: '\ufffd', mod: uv.ModCtrl}}},
		{"repeat count", staveUnicodeRecord(staveUnicodePacket, staveUnicodeLow, true, 0, 2), []staveUnicodeEvent{replacement, replacement, replacement}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeStaveUnicodeRecords(t, []xwindows.InputRecord{high, tc.low}, false); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("events=%+v want=%+v", got, tc.want)
			}
		})
	}
}

func TestStaveConsoleUnicodeCancellationDiscardsIncompletePair(t *testing.T) {
	reads := 0
	source := &preferenceConsoleReader{}
	source.wait = func() (uint32, error) {
		if reads > 0 {
			source.Cancel()
		}
		return windows.WAIT_OBJECT_0, nil
	}
	source.read = func() (xwindows.InputRecord, error) {
		reads++
		return staveUnicodeRecord(staveUnicodePacket, staveUnicodeHigh, true, 0, 1), nil
	}
	reader := &staveConsoleReader{console: source}
	var output [64]byte
	if n, err := reader.Read(output[:]); n != 0 || !errors.Is(err, cancelreader.ErrCanceled) {
		t.Fatalf("canceled read=%d %v, bytes=%q", n, err, output[:n])
	}
	if reads != 1 {
		t.Fatalf("read %d records after cancellation", reads)
	}
}

func decodeStaveUnicodeRecords(t *testing.T, records []xwindows.InputRecord, singleByte bool) []staveUnicodeEvent {
	t.Helper()
	index := 0
	source := &preferenceConsoleReader{
		wait: func() (uint32, error) { return windows.WAIT_OBJECT_0, nil },
		read: func() (xwindows.InputRecord, error) {
			if index == len(records) {
				return xwindows.InputRecord{}, io.EOF
			}
			record := records[index]
			index++
			return record, nil
		},
	}
	var reader io.Reader = &staveConsoleReader{console: source}
	if singleByte {
		reader = iotest.OneByteReader(reader)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events := make(chan uv.Event, 32)
	if err := uv.NewTerminalReader(reader, "xterm-256color").StreamEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("console decoder did not finish: %v", err)
	}
	close(events)
	var decoded []staveUnicodeEvent
	for event := range events {
		var key uv.Key
		var release bool
		switch event := event.(type) {
		case uv.KeyPressEvent:
			key = uv.Key(event)
		case uv.KeyReleaseEvent:
			key = uv.Key(event)
			release = true
		default:
			t.Fatalf("unexpected console event: %#v", event)
		}
		decoded = append(decoded, staveUnicodeEvent{code: key.Code, text: key.Text, mod: key.Mod, release: release})
	}
	return decoded
}
