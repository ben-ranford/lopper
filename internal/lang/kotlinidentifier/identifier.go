package kotlinidentifier

import (
	"strings"
	"unicode"

	"github.com/ben-ranford/lopper/internal/lang/shared"
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
		if isHeaderDeclaration(line) {
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

func isHeaderDeclaration(line string) bool {
	fields := strings.Fields(line)
	return len(fields) > 1 && (fields[0] == "package" || fields[0] == "import")
}

func MaskForFile(content []byte, filePath string) []byte {
	protected := append([]byte(nil), content...)
	var ranges [][2]int
	for index := 0; index < len(content); index++ {
		if content[index] != '`' {
			continue
		}
		lineStart := strings.LastIndex(string(content[:index]), "\n") + 1
		if strings.Contains(string(content[lineStart:index]), "//") || strings.Contains(string(content[lineStart:index]), "/*") {
			continue
		}
		end := index + 1
		for end < len(content) && content[end] != '`' {
			end++
		}
		if end == len(content) {
			break
		}
		ranges = append(ranges, [2]int{index, end + 1})
		for position := index + 1; position < end; position++ {
			if protected[position] == '\'' || protected[position] == '"' {
				protected[position] = '_'
			}
		}
		index = end
	}
	masked := shared.MaskCommentsAndStringsForFile(protected, filePath)
	for _, span := range ranges {
		copy(masked[span[0]:span[1]], content[span[0]:span[1]])
	}
	return masked
}
