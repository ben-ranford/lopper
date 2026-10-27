//go:build linux

package ui

import "os"

func validateStaveCancelReaderFile(*os.File) error { return nil }
