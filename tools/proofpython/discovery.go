package main

import (
	"bytes"
	"errors"
	"strings"
)

func validateDiscovery(output []byte, expected []string) error {
	if len(expected) != 2 || len(output) > 2*maxStringBytes+4 {
		return errors.New("native alias discovery limit differs")
	}
	output = bytes.ReplaceAll(output, []byte("\r\n"), []byte("\n"))
	lines := strings.Split(string(output), "\n")
	if len(lines) != 3 || lines[2] != "" {
		return errors.New("native alias discovery framing differs")
	}
	for i, path := range expected {
		if !strings.EqualFold(lines[i], path) {
			return errors.New("native alias discovery did not select captured PE transport")
		}
	}
	return nil
}
