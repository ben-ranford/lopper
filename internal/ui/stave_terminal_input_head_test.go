package ui

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestStaveTerminalEOFAdapterPreservesBorrowedFile(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "adapter file", file)
	adapter := staveTerminalEOF{file: file}
	if adapter.Fd() != file.Fd() {
		t.Fatal("adapter lost terminal descriptor")
	}
	if _, err := adapter.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if n, err := adapter.Read(make([]byte, 8)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("adapter read = %d, %v", n, err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("adapter closed borrowed file: %v", err)
	}
}

func TestStaveTerminalInputCloseBeforeStart(t *testing.T) {
	input, err := newStaveTerminalInput(strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if err := input.close(); err != nil {
		t.Fatal(err)
	}
}
