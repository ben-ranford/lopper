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
	for _, line := range strings.Split(string(SanitizeImportContent(content)), "\n") {
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
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(tail), ";"))
}

// SanitizeImportContent masks Kotlin comments while preserving escaped identifiers.
func SanitizeImportContent(content []byte) []byte {
	return sanitizeImportContent(content, true)
}

// SanitizeJVMImportContent masks Java/Kotlin comments with Java block-comment semantics.
func SanitizeJVMImportContent(content []byte) []byte {
	return sanitizeImportContent(content, false)
}

func sanitizeImportContent(content []byte, nestedBlockComments bool) []byte {
	sanitized := append([]byte(nil), content...)
	for index, blockCommentDepth, doubleQuote, rawString, characterLiteral := 0, 0, false, false, false; index < len(sanitized); index++ {
		if blockCommentDepth > 0 {
			if sanitized[index] != '\n' && sanitized[index] != '\r' {
				sanitized[index] = ' '
			}
			if nestedBlockComments && index+1 < len(sanitized) && content[index] == '/' && content[index+1] == '*' {
				sanitized[index+1] = ' '
				blockCommentDepth, index = blockCommentDepth+1, index+1
				continue
			}
			if index+1 < len(sanitized) && content[index] == '*' && content[index+1] == '/' {
				sanitized[index+1] = ' '
				blockCommentDepth, index = blockCommentDepth-1, index+1
			}
			continue
		}
		if rawString {
			if sanitized[index] != '\n' && sanitized[index] != '\r' {
				sanitized[index] = ' '
			}
			if index+2 < len(content) && content[index] == '"' && content[index+1] == '"' && content[index+2] == '"' {
				sanitized[index+1], sanitized[index+2] = ' ', ' '
				rawString, index = false, index+2
			}
			continue
		}
		if doubleQuote || characterLiteral {
			if content[index] == '\\' {
				index++
				continue
			}
			if (doubleQuote && content[index] == '"') || (characterLiteral && content[index] == '\'') {
				doubleQuote, characterLiteral = false, false
			}
			continue
		}
		if index+2 < len(content) && content[index] == '"' && content[index+1] == '"' && content[index+2] == '"' {
			sanitized[index], sanitized[index+1], sanitized[index+2] = ' ', ' ', ' '
			rawString, index = true, index+2
			continue
		}
		if content[index] == '"' {
			doubleQuote = true
			continue
		}
		if content[index] == '\'' {
			characterLiteral = true
			continue
		}
		if index+1 < len(sanitized) && content[index] == '/' && content[index+1] == '*' {
			sanitized[index], sanitized[index+1] = ' ', ' '
			blockCommentDepth, index = 1, index+1
			continue
		}
		if index+1 < len(sanitized) && content[index] == '/' && content[index+1] == '/' {
			for index < len(sanitized) && sanitized[index] != '\n' {
				sanitized[index] = ' '
				index++
			}
			continue
		}
		if content[index] != '`' {
			continue
		}
		index++
		for index < len(sanitized) && content[index] != '`' {
			index++
		}
	}
	return sanitized
}

// NormalizeModuleForLookup removes Kotlin escaping without turning dots inside
// an escaped identifier into package separators.
func NormalizeModuleForLookup(module string) string {
	var normalized strings.Builder
	normalized.Grow(len(module))
	escaped := false
	for _, character := range module {
		if character == '`' {
			escaped = !escaped
			continue
		}
		if escaped && character == '.' {
			normalized.WriteByte(0)
			continue
		}
		normalized.WriteRune(character)
	}
	return normalized.String()
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
	maskCommentCharacter := func(index int) {
		if content[index] != '\n' {
			protected[index] = ' '
		}
	}
	maskRawStringCharacter := func(index int) {
		if content[index] != '\n' && content[index] != '\r' {
			protected[index] = ' '
		}
	}
	var ranges [][2]int
	for index, blockCommentDepth, lineComment, doubleQuote, rawString := 0, 0, false, false, false; index < len(content); index++ {
		if lineComment {
			if content[index] == '\n' {
				lineComment = false
			}
			continue
		}
		if blockCommentDepth > 0 {
			maskCommentCharacter(index)
			if index+1 < len(content) && content[index] == '/' && content[index+1] == '*' {
				maskCommentCharacter(index + 1)
				blockCommentDepth, index = blockCommentDepth+1, index+1
				continue
			}
			if index+1 < len(content) && content[index] == '*' && content[index+1] == '/' {
				maskCommentCharacter(index + 1)
				blockCommentDepth, index = blockCommentDepth-1, index+1
			}
			continue
		}
		if rawString {
			maskRawStringCharacter(index)
			if index+2 < len(content) && content[index] == '"' && content[index+1] == '"' && content[index+2] == '"' {
				maskRawStringCharacter(index + 1)
				maskRawStringCharacter(index + 2)
				rawString, index = false, index+2
			}
			continue
		}
		if doubleQuote {
			if content[index] == '\\' {
				index++
				continue
			}
			if content[index] == '"' {
				doubleQuote = false
			}
			continue
		}
		if index+1 < len(content) && content[index] == '/' && content[index+1] == '/' {
			lineComment, index = true, index+1
			continue
		}
		if index+1 < len(content) && content[index] == '/' && content[index+1] == '*' {
			maskCommentCharacter(index)
			maskCommentCharacter(index + 1)
			blockCommentDepth, index = 1, index+1
			continue
		}
		if index+2 < len(content) && content[index] == '"' && content[index+1] == '"' && content[index+2] == '"' {
			maskRawStringCharacter(index)
			maskRawStringCharacter(index + 1)
			maskRawStringCharacter(index + 2)
			rawString, index = true, index+2
			continue
		}
		if content[index] == '"' {
			doubleQuote = true
			continue
		}
		if content[index] != '`' {
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
