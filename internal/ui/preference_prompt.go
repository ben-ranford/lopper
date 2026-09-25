package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ben-ranford/lopper/internal/uipreference"
)

// CanManagePreference excludes automation and redirected streams before storage I/O.
func CanManagePreference(input io.Reader, output io.Writer) bool {
	return os.Getenv("CI") == "" && supportsStaveInteractiveTerminal(input, output)
}

// CanOfferPreference uses the same capabilities as the full-screen preview.
func CanOfferPreference(input io.Reader, output io.Writer) bool {
	if !CanManagePreference(input, output) || os.Getenv("NO_COLOR") != "" {
		return false
	}
	return supportsStaveFullScreen(staveSessionOptions(Options{}, supportsStaveInteractiveTerminal(input, output)).RuntimeDetected)
}

// PromptPreference leaves terminal mode alone and reads one byte at a time so
// the subsequent renderer owns every byte after the invitation's newline.
func PromptPreference(ctx context.Context, input io.Reader, output io.Writer) (choice string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	file, ok := input.(*os.File)
	if !ok || file == nil {
		return "", fmt.Errorf("preview invitation requires terminal input")
	}
	reader, err := newPreferenceReader(file)
	if err != nil {
		return "", err
	}
	defer func() { returnErr = errors.Join(returnErr, reader.Close()) }()
	done := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
			reader.Cancel()
		case <-done:
		}
	}()
	defer func() { close(done); <-watchDone }()
	for {
		if _, err := fmt.Fprintln(output, "Try the new UI? Stave is a preview. Try / Keep current remembers your choice for future interactive launches.\n[t] Try  [k] Keep current  [Enter/l] Later:"); err != nil {
			return "", err
		}
		line, err := readPreferenceLine(reader)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "t", "try", "stave":
			return uipreference.Stave, nil
		case "k", "keep", "keep current", "legacy":
			return uipreference.Legacy, nil
		case "", "l", "later":
			return "", nil
		}
	}
}
