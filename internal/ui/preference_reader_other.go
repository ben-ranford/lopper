//go:build !windows

package ui

import (
	"os"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/muesli/cancelreader"
)

func newPreferenceReader(file *os.File) (cancelreader.CancelReader, error) {
	return uv.NewCancelReader(file)
}

func newStaveCancelReader(file *os.File) (cancelreader.CancelReader, error) {
	if err := validateStaveCancelReaderFile(file); err != nil {
		return nil, err
	}
	return uv.NewCancelReader(file)
}
