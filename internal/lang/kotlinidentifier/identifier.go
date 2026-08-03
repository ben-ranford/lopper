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

func HasEscapedImportLocal(content []byte, local string) bool {
	marker := "`" + local + "`"
	for _, line := range strings.Split(string(content), "\n") {
		if strings.Contains(line, "."+marker) {
			return true
		}
		for index := 0; index < len(line); {
			for index < len(line) && unicode.IsSpace(rune(line[index])) {
				index++
			}
			start := index
			for index < len(line) && !unicode.IsSpace(rune(line[index])) {
				index++
			}
			if line[start:index] == "as" && escapedAliasTail(line[index:]) == marker {
				return true
			}
		}
	}
	return false
}

func escapedAliasTail(tail string) string {
	for _, marker := range []string{"//", "/*"} {
		if index := strings.Index(tail, marker); index >= 0 {
			tail = tail[:index]
		}
	}
	return strings.TrimSuffix(strings.TrimSpace(tail), ";")
}

func CountEscapedLocalUses(masked []byte, local string) int {
	lines := strings.Split(string(masked), "\n")
	firstCodeLine := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") || strings.HasPrefix(trimmed, "import ") {
			firstCodeLine = index + 1
		}
	}
	marker := "`" + local + "`"
	uses := 0
	for _, line := range lines[firstCodeLine:] {
		uses += strings.Count(line, marker)
	}
	return uses
}
