package main

import (
	"fmt"
	"os"
)

func main() {
	status, err := platformMain(os.Args[0], os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, boundedErrorMessage(err))
	}
	os.Exit(status)
}

func boundedErrorMessage(err error) string {
	message := err.Error()
	const suffix = " [diagnostic overflow]"
	if len(message) > maxErrorBytes {
		return message[:maxErrorBytes-len(suffix)] + suffix
	}
	return message
}
