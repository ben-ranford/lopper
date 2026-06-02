package report

import (
	"strings"
	"unicode/utf8"
)

func sanitizeTerminalString(value string) string {
	if value == "" {
		return value
	}

	const hex = "0123456789abcdef"
	var output strings.Builder
	output.Grow(len(value))
	for i := 0; i < len(value); {
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 1 {
			b := value[i]
			if !isTerminalControlRune(rune(b)) {
				output.WriteByte(b)
				i++
				continue
			}
			writeEscapedByte(&output, b, hex)
			i++
			continue
		}
		if !isTerminalControlRune(r) {
			output.WriteRune(r)
			i += size
			continue
		}
		writeEscapedByte(&output, byte(r), hex)
		i += size
	}
	return output.String()
}

func sanitizeTerminalStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sanitized := make([]string, len(values))
	for i, value := range values {
		sanitized[i] = sanitizeTerminalString(value)
	}
	return sanitized
}

func isTerminalControlRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

func writeEscapedByte(output *strings.Builder, b byte, hex string) {
	output.WriteByte('\\')
	output.WriteByte('x')
	output.WriteByte(hex[b>>4])
	output.WriteByte(hex[b&0x0f])
}
