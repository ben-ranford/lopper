package runtime

import (
	"fmt"
	"strings"
	"unicode"
)

func parseWindowsRuntimeCommand(command string) ([]string, error) {
	runes := []rune(command)
	fields := make([]string, 0)
	index := skipWindowsRuntimeSpaces(runes, 0)
	for index < len(runes) {
		field, next, err := parseWindowsRuntimeField(runes, index)
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
		index = skipWindowsRuntimeSpaces(runes, next)
	}

	return fields, nil
}

func skipWindowsRuntimeSpaces(runes []rune, index int) int {
	for index < len(runes) && unicode.IsSpace(runes[index]) {
		index++
	}
	return index
}

func parseWindowsRuntimeField(runes []rune, index int) (string, int, error) {
	var current strings.Builder
	inDoubleQuote := false
	for index < len(runes) && !windowsRuntimeFieldBoundary(runes[index], inDoubleQuote) {
		index, inDoubleQuote = appendWindowsRuntimeRune(&current, runes, index, inDoubleQuote)
	}
	if inDoubleQuote {
		return "", index, fmt.Errorf("runtime test command contains an unterminated quote")
	}
	return current.String(), index, nil
}

func windowsRuntimeFieldBoundary(value rune, inDoubleQuote bool) bool {
	return unicode.IsSpace(value) && !inDoubleQuote
}

func appendWindowsRuntimeRune(builder *strings.Builder, runes []rune, index int, inDoubleQuote bool) (int, bool) {
	switch runes[index] {
	case '\\':
		return appendWindowsRuntimeBackslashes(builder, runes, index, inDoubleQuote)
	case '"':
		return index + 1, !inDoubleQuote
	default:
		builder.WriteRune(runes[index])
		return index + 1, inDoubleQuote
	}
}

func appendWindowsRuntimeBackslashes(builder *strings.Builder, runes []rune, index int, inDoubleQuote bool) (int, bool) {
	next := index
	for next < len(runes) && runes[next] == '\\' {
		next++
	}
	count := next - index
	if next >= len(runes) || runes[next] != '"' {
		writeRepeatedRune(builder, '\\', count)
		return next, inDoubleQuote
	}

	writeRepeatedRune(builder, '\\', count/2)
	if count%2 == 0 {
		return next + 1, !inDoubleQuote
	}
	builder.WriteRune('"')
	return next + 1, inDoubleQuote
}

func writeRepeatedRune(builder *strings.Builder, value rune, count int) {
	for range count {
		builder.WriteRune(value)
	}
}
