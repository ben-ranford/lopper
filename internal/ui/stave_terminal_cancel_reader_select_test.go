//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func TestStaveTerminalRejectsHighFDDevTTYBeforeRawMode(t *testing.T) {
	ensureStaveCancelReaderFDLimit(t)
	master, terminal, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cleanupStaveTestCloser(t, "cancel-reader PTY master", master)
	cleanupStaveTestCloser(t, "cancel-reader PTY terminal", terminal)
	original := *rawModeLeaseState(t, terminal)

	t.Run("ordinary-dev-tty", func(t *testing.T) {
		file := duplicateStaveCancelReaderTestFile(t, terminal, 3, "/dev/tty")
		if file.Fd() >= uintptr(unix.FD_SETSIZE) {
			t.Fatalf("ordinary control fd=%d, want < FD_SETSIZE %d", file.Fd(), unix.FD_SETSIZE)
		}
		assertStaveCancelReaderAccepted(t, file, terminal, original, "ordinary /dev/tty")
	})

	t.Run("high-fd-kqueue-name", func(t *testing.T) {
		file := duplicateStaveCancelReaderTestFile(t, terminal, unix.FD_SETSIZE, "pty-kqueue-high-fd")
		if file.Fd() < uintptr(unix.FD_SETSIZE) {
			t.Fatalf("kqueue control fd=%d, want >= FD_SETSIZE %d", file.Fd(), unix.FD_SETSIZE)
		}
		assertStaveCancelReaderAccepted(t, file, terminal, original, "high-FD kqueue-named input")
	})

	t.Run("high-fd-dev-tty", func(t *testing.T) {
		file := duplicateStaveCancelReaderTestFile(t, terminal, unix.FD_SETSIZE, "/dev/tty")
		if file.Fd() < uintptr(unix.FD_SETSIZE) {
			t.Fatalf("/dev/tty repro fd=%d, want >= FD_SETSIZE %d", file.Fd(), unix.FD_SETSIZE)
		}
		assertHighFDDevTTYRejected(t, file, terminal, original)
	})
}

func assertStaveCancelReaderAccepted(t *testing.T, file, terminal *os.File, original term.State, description string) {
	t.Helper()
	input, err := newStaveTerminalInput(file)
	if err != nil {
		t.Fatalf("%s constructor: %v", description, err)
	}
	readerClosed := false
	t.Cleanup(func() {
		if !readerClosed {
			if err := input.reader.Close(); err != nil {
				t.Errorf("close %s reader during cleanup: %v", description, err)
			}
		}
	})
	if !input.reader.Cancel() {
		t.Fatalf("%s reader did not support cancellation", description)
	}
	if err := input.reader.Close(); err != nil {
		t.Fatalf("close %s reader wrapper: %v", description, err)
	}
	readerClosed = true
	assertStaveCancelReaderFilesRemainOpen(t, file, terminal)
	assertStaveCancelReaderModeUnchanged(t, terminal, original)
}

func assertHighFDDevTTYRejected(t *testing.T, file, terminal *os.File, original term.State) {
	t.Helper()
	input, err := newStaveTerminalInput(file)
	if err == nil {
		if input != nil && input.reader != nil {
			if closeErr := input.reader.Close(); closeErr != nil {
				t.Errorf("close unexpectedly accepted high-FD /dev/tty reader: %v", closeErr)
			}
		}
		t.Fatal("high-FD /dev/tty was accepted with an uncancellable select fallback")
	}
	if !strings.Contains(err.Error(), "FD_SETSIZE") {
		t.Fatalf("high-FD /dev/tty error = %v, want explicit FD_SETSIZE limitation", err)
	}
	assertStaveCancelReaderFilesRemainOpen(t, file, terminal)
	assertStaveCancelReaderModeUnchanged(t, terminal, original)
}

func ensureStaveCancelReaderFDLimit(t *testing.T) {
	t.Helper()
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	const need = unix.FD_SETSIZE + 8
	if original.Cur >= need {
		return
	}
	if original.Max < need {
		t.Fatalf("process descriptor hard limit %d cannot create deterministic fd >= FD_SETSIZE %d", original.Max, unix.FD_SETSIZE)
	}
	updated := original
	updated.Cur = need
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &updated); err != nil {
		t.Fatalf("raise soft descriptor limit for deterministic PTY repro: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
			t.Errorf("restore process descriptor limit: %v", err)
		}
	})
}

func duplicateStaveCancelReaderTestFile(t *testing.T, terminal *os.File, minimum int, name string) *os.File {
	t.Helper()
	fd, err := unix.FcntlInt(terminal.Fd(), unix.F_DUPFD, minimum)
	if err != nil {
		t.Fatalf("duplicate PTY descriptor at minimum %d: %v", minimum, err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		if closeErr := unix.Close(fd); closeErr != nil {
			t.Errorf("close descriptor that could not be wrapped: %v", closeErr)
		}
		t.Fatal("wrap duplicate PTY descriptor")
	}
	cleanupStaveTestCloser(t, "cancel-reader duplicated PTY", file)
	return file
}

func assertStaveCancelReaderFilesRemainOpen(t *testing.T, caller, original *os.File) {
	t.Helper()
	if _, err := caller.Stat(); err != nil {
		t.Fatalf("caller-owned duplicated input was closed: %v", err)
	}
	if _, err := original.Stat(); err != nil {
		t.Fatalf("caller-owned original PTY input was closed: %v", err)
	}
}

func assertStaveCancelReaderModeUnchanged(t *testing.T, terminal *os.File, original term.State) {
	t.Helper()
	if got := rawModeLeaseState(t, terminal); *got != original {
		t.Fatalf("terminal mode changed during cancel-reader construction: got %+v want %+v", got, original)
	}
}
