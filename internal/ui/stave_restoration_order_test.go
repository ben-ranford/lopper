//go:build !windows

package ui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStaveRestorationWaitsForReaderAcrossShutdown(t *testing.T) {
	for _, exit := range []string{"quit", "parent-cancel", "program-error", "close-error"} {
		t.Run(exit, func(t *testing.T) { checkRestorationOrder(t, exit) })
	}
}

func TestStaveRestorationSignalProcess(t *testing.T) {
	if name := os.Getenv("LOPPER_RESTORATION_SIGNAL_HELPER"); name != "" {
		checkRestorationOrder(t, name)
		return
	}
	for _, name := range []string{"SIGINT", "SIGTERM"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStaveRestorationSignalProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "LOPPER_RESTORATION_SIGNAL_HELPER="+name)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("signal helper failed: %v\n%s", err, output)
			}
			for _, marker := range []string{"READER_PENDING_RAW", "READER_STOPPED", "ORIGINAL_RESTORED"} {
				if !strings.Contains(string(output), marker) {
					t.Fatalf("missing helper sentinel %s: %s", marker, output)
				}
			}
		})
	}
}

func checkRestorationOrder(t *testing.T, exit string) {
	t.Helper()
	t.Cleanup(func() { verifyRestorationNoLeaks(t) })
	t.Setenv("TERM", "xterm-256color")
	_, terminal := openRestorationOrderTerminal(t)
	original := rawModeLeaseState(t, terminal)
	input, err := newStaveTerminalInput(terminal)
	if err != nil {
		t.Fatal(err)
	}
	if err := input.reader.Close(); err != nil {
		t.Fatal(err)
	}
	reader := &rawModeLeaseReader{entered: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), stopped: make(chan struct{})}
	reader.onClose = func() {
		select {
		case <-reader.stopped:
		default:
			t.Error("reader closed before pending read stopped")
		}
	}
	input.reader = reader
	closeErr := errors.New("reader close sentinel")
	if exit == "close-error" {
		reader.closeErr = closeErr
	}
	restore, err := enterStaveTerminalRaw(input)
	if err != nil {
		t.Fatal(err)
	}
	registerRestorationCleanup(t, input, reader, restore)
	raw := rawModeLeaseState(t, terminal)
	if *raw == *original {
		t.Fatal("terminal did not enter raw mode")
	}
	runRestorationShutdown(t, input, reader, exit)
	if *rawModeLeaseState(t, terminal) != *raw {
		t.Fatal("terminal restored before reader shutdown")
	}
	t.Log("READER_PENDING_RAW")
	wantErr := error(nil)
	if exit == "close-error" {
		wantErr = closeErr
	}
	finishRestorationRead(t, input, reader, restore, wantErr, func() {
		if *rawModeLeaseState(t, terminal) != *raw {
			t.Fatal("terminal restored while cancelled read remained pending")
		}
	})
	if *rawModeLeaseState(t, terminal) != *original {
		t.Fatal("exact original terminal state not restored")
	}
	if _, err := terminal.Stat(); err != nil {
		t.Fatalf("borrowed terminal closed: %v", err)
	}
	t.Log("ORIGINAL_RESTORED")
}

func runRestorationShutdown(t *testing.T, input *staveTerminalInput, reader *rawModeLeaseReader, exit string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), staveSignalSubprocessBound)
	defer cancel()
	if exit == "SIGINT" || exit == "SIGTERM" {
		signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx = signalCtx
	}
	model := newRestorationOrderModel(ctx, input, reader)
	program := model.program
	stop := restorationShutdownTrigger(t, exit, cancel, program.Kill)
	// The model calls stop only after the independent reader announces entry.
	model.stop = stop
	_, err := program.Run()
	if exit == "quit" || exit == "close-error" {
		if err != nil {
			t.Fatal(err)
		}
	} else if err == nil {
		t.Fatal("cancelled or killed program returned success")
	}

}

func waitRestorationGate(t *testing.T, gate <-chan struct{}, phase string) {
	t.Helper()
	select {
	case <-gate:
	case <-time.After(staveSignalSubprocessBound):
		t.Fatalf("timed out at %s", phase)
	}
}

func TestStaveRestorationBeforeInputStart(t *testing.T) {
	t.Cleanup(func() { verifyRestorationNoLeaks(t) })
	_, terminal := openRestorationOrderTerminal(t)
	original := rawModeLeaseState(t, terminal)
	input, err := newStaveTerminalInput(terminal)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := enterStaveTerminalRaw(input)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := restore(); err != nil {
			t.Error(err)
		}
	})
	raw := rawModeLeaseState(t, terminal)
	if err := input.close(); err != nil {
		t.Fatal(err)
	}
	if *rawModeLeaseState(t, terminal) != *raw {
		t.Fatal("closing unstarted reader changed terminal mode")
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if *rawModeLeaseState(t, terminal) != *original {
		t.Fatal("early cleanup did not restore original state")
	}
	if _, err := terminal.Stat(); err != nil {
		t.Fatalf("early cleanup closed caller input: %v", err)
	}
}

func finishRestorationRead(t *testing.T, input *staveTerminalInput, reader *rawModeLeaseReader, restore func() error, wantErr error, assertRaw func()) {
	t.Helper()
	closed := make(chan error, 1)
	// Run the production shutdown sequence so reversing it restores the
	// terminal while the cancelled read is still pending.
	go func() { closed <- shutdownStaveTerminalInput(input, restore) }()
	waitRestorationGate(t, reader.cancelled, "cancel request")
	select {
	case err := <-closed:
		t.Fatalf("close returned before pending read stopped: %v", err)
	default:
	}
	assertRaw()
	reader.finish()
	waitRestorationGate(t, input.done, "reader stopped")
	t.Log("READER_STOPPED")
	select {
	case err := <-closed:
		if !errors.Is(err, wantErr) {
			t.Fatalf("input close sentinel changed: got %v want %v", err, wantErr)
		}
	case <-time.After(staveSignalSubprocessBound):
		t.Fatal("input close did not finish")
	}
}

func restorationShutdownTrigger(t *testing.T, exit string, cancel, kill func()) func() {
	t.Helper()
	switch exit {
	case "quit", "close-error":
		return nil
	case "program-error":
		return kill
	case "SIGINT", "SIGTERM":
		return func() { signalRestorationProcess(t, exit, cancel) }
	default:
		return cancel
	}
}

func signalRestorationProcess(t *testing.T, exit string, cancel func()) {
	t.Helper()
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Error(err)
		cancel()
		return
	}
	sig := os.Interrupt
	if exit == "SIGTERM" {
		sig = syscall.SIGTERM
	}
	if err := process.Signal(sig); err != nil {
		t.Error(err)
		cancel()
	}
}

func registerRestorationCleanup(t *testing.T, input *staveTerminalInput, reader *rawModeLeaseReader, restore func() error) {
	t.Helper()
	t.Cleanup(func() {
		reader.finish()
		if input.done != nil {
			waitRestorationGate(t, input.done, "cleanup reader")
		}
		if err := restore(); err != nil {
			t.Error(err)
		}
	})
}
