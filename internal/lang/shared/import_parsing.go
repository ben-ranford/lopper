package shared

import (
	"bytes"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

func ParseImportLines(content []byte, filePath string, parseLine func(string, int) []ImportRecord) []ImportRecord {
	lines := strings.Split(string(content), "\n")
	records := make([]ImportRecord, 0)
	for index, line := range lines {
		parsed := parseLine(line, index)
		for _, record := range parsed {
			if record.Location.Line == 0 {
				record.Location = LocationFromLine(filePath, index, line)
			}
			records = append(records, record)
		}
	}
	return records
}

func StripLineComment(line, marker string) string {
	if index := strings.Index(line, marker); index >= 0 {
		return line[:index]
	}
	return line
}

func StripBlockComments(content []byte) []byte {
	return stripBlockComments(content, false)
}

// StripJavaBlockComments preserves escape sequences in Java text blocks.
func StripJavaBlockComments(content []byte) []byte {
	return stripBlockComments(content, true)
}

func stripBlockComments(content []byte, textBlockEscapes bool) []byte {
	if len(content) == 0 {
		return content
	}

	stripped := make([]byte, len(content))
	copy(stripped, content)

	for index := 0; index < len(stripped); {
		switch {
		case stripped[index] == '\'' || stripped[index] == '"':
			index = skipImportQuotedLiteral(stripped, index, textBlockEscapes)
		case hasBytePrefix(stripped, index, '/', '/'):
			index = skipImportLineComment(stripped, index+2)
		case hasBytePrefix(stripped, index, '/', '*'):
			index = maskImportBlockComment(stripped, index)
		default:
			index++
		}
	}

	return stripped
}

func skipImportLineComment(content []byte, start int) int {
	if end := bytes.IndexAny(content[start:], "\r\n"); end >= 0 {
		return start + end
	}
	return len(content)
}

func maskImportBlockComment(content []byte, start int) int {
	content[start], content[start+1] = ' ', ' '
	for index := start + 2; index < len(content); {
		next, state := scanBlockComment(content, index)
		if state == scannerStateCode {
			return next
		}
		index = next
	}
	return len(content)
}

func skipImportQuotedLiteral(content []byte, start int, textBlockEscapes bool) int {
	if hasImportTextBlockDelimiter(content, start) {
		return skipImportTextBlock(content, start+3, textBlockEscapes)
	}
	quote := content[start]
	for index := start + 1; index < len(content); index++ {
		switch content[index] {
		case '\\':
			index++
		case quote:
			return index + 1
		}
	}
	return len(content)
}

func skipImportTextBlock(content []byte, start int, textBlockEscapes bool) int {
	for index := start; index < len(content); index++ {
		switch {
		case textBlockEscapes && content[index] == '\\':
			index++
		case hasImportTextBlockDelimiter(content, index):
			end := index + 3
			// Kotlin raw strings may contain extra quotes before their closing delimiter.
			for !textBlockEscapes && end < len(content) && content[end] == '"' {
				end++
			}
			return end
		}
	}
	return len(content)
}

func hasImportTextBlockDelimiter(content []byte, index int) bool {
	return index+2 < len(content) && content[index] == '"' && content[index+1] == '"' && content[index+2] == '"'
}

func Location(filePath string, line, column int) report.Location {
	return report.Location{
		File:   filePath,
		Line:   line,
		Column: column,
	}
}

func LocationFromLine(filePath string, index int, line string) report.Location {
	return Location(filePath, index+1, FirstContentColumn(line))
}
