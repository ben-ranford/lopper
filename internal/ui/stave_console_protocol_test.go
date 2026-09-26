package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// Exercise the actual Ultraviolet decoder on every platform: the Windows
// no-flush reader must emit these native-key records, not lossy ASCII keys.
func TestStaveWindowsConsoleProtocolDecodesQuitExactlyOnce(t *testing.T) {
	for _, record := range []string{"\x1b[81;16;113;1;0;1_", "\x1b[0;0;113;1;0;1_"} {
		events := make(chan uv.Event, 4)
		err := uv.NewTerminalReader(strings.NewReader(record), "xterm-256color").StreamEvents(context.Background(), events)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		close(events)
		count := 0
		for event := range events {
			key, ok := event.(uv.KeyPressEvent)
			if !ok || key.Code != 'q' {
				t.Fatalf("decoded event=%#v", event)
			}
			count++
		}
		if count != 1 {
			t.Fatalf("quit delivered %d times", count)
		}
	}
}
