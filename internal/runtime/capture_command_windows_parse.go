package runtime

import (
	"fmt"
	"strings"
	"unicode"
)

func parseWindowsRuntimeCommand(command string) ([]string, error) {
	runes := []rune(command)
	fields := make([]string, 0)

	for index := 0; index < len(runes); {
		for index < len(runes) && unicode.IsSpace(runes[index]) {
			index++
		}
		if index >= len(runes) {
			break
		}

		var current strings.Builder
		inDoubleQuote := false
		tokenStarted := false
		for index < len(runes) {
			ch := runes[index]
			if unicode.IsSpace(ch) && !inDoubleQuote {
				break
			}
			tokenStarted = true

			if ch == '\\' {
				backslashStart := index
				for index < len(runes) && runes[index] == '\\' {
					index++
				}
				backslashCount := index - backslashStart
				if index < len(runes) && runes[index] == '"' {
					writeRepeatedRune(&current, '\\', backslashCount/2)
					if backslashCount%2 == 0 {
						inDoubleQuote = !inDoubleQuote
					} else {
						current.WriteRune('"')
					}
					index++
					continue
				}
				writeRepeatedRune(&current, '\\', backslashCount)
				continue
			}

			if ch == '"' {
				inDoubleQuote = !inDoubleQuote
				index++
				continue
			}

			current.WriteRune(ch)
			index++
		}

		if inDoubleQuote {
			return nil, fmt.Errorf("runtime test command contains an unterminated quote")
		}
		if tokenStarted {
			fields = append(fields, current.String())
		}
	}

	return fields, nil
}

func writeRepeatedRune(builder *strings.Builder, value rune, count int) {
	for range count {
		builder.WriteRune(value)
	}
}
