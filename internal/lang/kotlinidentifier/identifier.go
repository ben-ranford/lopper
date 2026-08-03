package kotlinidentifier

import (
	"strings"
	"unicode"
)

var hardKeywords = map[string]struct{}{
	"as": {}, "break": {}, "class": {}, "continue": {}, "do": {}, "else": {}, "false": {}, "for": {}, "fun": {}, "if": {}, "in": {}, "interface": {}, "is": {}, "null": {}, "object": {}, "package": {}, "return": {}, "super": {}, "this": {}, "throw": {}, "true": {}, "try": {}, "typealias": {}, "val": {}, "var": {}, "when": {}, "while": {},
}

func IsBare(local string) bool {
	if local == "" {
		return false
	}
	if _, keyword := hardKeywords[local]; keyword {
		return false
	}
	for index, character := range local {
		if character == '_' || unicode.IsLetter(character) || (index > 0 && unicode.IsDigit(character)) {
			continue
		}
		return false
	}
	return true
}

func LastModuleSegment(module string) string {
	last, escaped := 0, false
	for index, character := range module {
		if character == '`' {
			escaped = !escaped
		}
		if character == '.' && !escaped {
			last = index + 1
		}
	}
	return strings.TrimSpace(module[last:])
}
