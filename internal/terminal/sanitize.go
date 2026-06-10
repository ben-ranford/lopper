package terminal

import (
	"strings"
	"unicode/utf8"
)

func SanitizeString(value string) string {
	firstControl := firstControlIndex(value)
	if firstControl == -1 {
		return value
	}

	const hex = "0123456789abcdef"
	var output strings.Builder
	output.Grow(len(value))
	output.WriteString(value[:firstControl])
	for i := firstControl; i < len(value); {
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 1 {
			b := value[i]
			if !isControlRune(rune(b)) {
				output.WriteByte(b)
				i++
				continue
			}
			writeEscapedByte(&output, b, hex)
			i++
			continue
		}
		if !isControlRune(r) {
			output.WriteRune(r)
			i += size
			continue
		}
		writeEscapedByte(&output, byte(r), hex)
		i += size
	}
	return output.String()
}

func firstControlIndex(value string) int {
	for i := 0; i < len(value); {
		r, size := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && size == 1 {
			if isControlRune(rune(value[i])) {
				return i
			}
			i++
			continue
		}
		if isControlRune(r) {
			return i
		}
		i += size
	}
	return -1
}

func SanitizeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sanitized := make([]string, len(values))
	for i, value := range values {
		sanitized[i] = SanitizeString(value)
	}
	return sanitized
}

func isControlRune(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

func writeEscapedByte(output *strings.Builder, b byte, hex string) {
	output.WriteByte('\\')
	output.WriteByte('x')
	output.WriteByte(hex[b>>4])
	output.WriteByte(hex[b&0x0f])
}
