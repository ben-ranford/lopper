//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !solaris

package ui

import (
	"errors"
	"os"
)

func validateStaveCancelReaderFile(*os.File) error {
	return errors.New("unsupported Stave terminal input: this platform has no cancellable terminal reader")
}
