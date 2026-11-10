//go:build !unix

package main

import "errors"

func mkfifo(string) error {
	return errors.New("fifo is unavailable")
}
