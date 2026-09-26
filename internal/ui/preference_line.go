package ui

import (
	"io"
	"unicode/utf8"
)

// Consume exactly one line. Native Windows console records bypass cooked line
// editing, so Backspace must edit the answer here without reading past Enter.
func readPreferenceLine(reader io.Reader) (string, error) {
	line := make([]byte, 0, 80)
	var one [1]byte
	for {
		n, err := reader.Read(one[:])
		if n > 0 {
			switch one[0] {
			case '\n':
				return string(line), nil
			case '\b', 127:
				_, size := utf8.DecodeLastRune(line)
				line = line[:len(line)-size]
			default:
				if len(line) < 80 {
					line = append(line, one[0])
				}
			}
		}
		if err != nil {
			return "", err
		}
	}
}
