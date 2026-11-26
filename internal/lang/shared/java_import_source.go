package shared

import (
	"bytes"
	"strconv"
)

type javaImportOffset struct {
	codeIndex    int
	displacement int
}

// Java translates eligible Unicode escapes before recognizing comments or literals.
func javaImportLexicalSource(raw []byte) ([]byte, []javaImportOffset) {
	if !bytes.Contains(raw, []byte(`\u`)) {
		return raw, nil
	}

	var code []byte
	var offsets []javaImportOffset
	start := 0
	previousEscape, oddBackslashes := false, false
	for index := 0; index < len(raw); {
		value, end := javaImportCodeUnit(raw, index, previousEscape || !oddBackslashes)
		previousEscape = end > index+1
		if previousEscape {
			if len(code) == 0 {
				code = make([]byte, 0, len(raw))
			}
			code = append(code, raw[start:index]...)
			code = append(code, value)
			start = end
			offsets = append(offsets, javaImportOffset{codeIndex: len(code), displacement: end - len(code)})
		}
		oddBackslashes = value == '\\' && !oddBackslashes
		index = end
	}
	if len(offsets) == 0 {
		return raw, nil
	}
	return append(code, raw[start:]...), offsets
}

func javaImportCodeUnit(content []byte, index int, eligible bool) (byte, int) {
	if !eligible || !hasBytePrefix(content, index, '\\', 'u') {
		return content[index], index + 1
	}

	digits := index + 2
	for digits < len(content) && content[digits] == 'u' {
		digits++
	}
	if len(content)-digits < 4 {
		return content[index], index + 1
	}
	end := digits + 4
	value, err := strconv.ParseUint(string(content[digits:end]), 16, 16)
	if err != nil {
		return content[index], index + 1
	}
	if value > 127 {
		// Non-ASCII units cannot be delimiters in this lexical view.
		return 0, end
	}
	return byte(value), end
}
