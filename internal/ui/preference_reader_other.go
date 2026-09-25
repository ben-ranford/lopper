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
	return uv.NewCancelReader(file)
}
