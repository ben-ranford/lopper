package shared

import (
	"bytes"
	"path/filepath"
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
	return scanImportSource(content, false, false)
}

// MaskJVMImportSource masks block comments and literal contents while preserving source positions and Kotlin identifiers.
func MaskJVMImportSource(content []byte, filePath string) []byte {
	return scanImportSource(content, strings.EqualFold(filepath.Ext(filePath), ".java"), true)
}

func scanImportSource(content []byte, javaSource, maskLiterals bool) []byte {
	if len(content) == 0 {
		return content
	}

	stripped := make([]byte, len(content))
	copy(stripped, content)

	for index := 0; index < len(stripped); {
		switch {
		case !javaSource && stripped[index] == '`':
			index = afterImportDelimiter(stripped, index+1, "`\r\n")
		case stripped[index] == '\'' || stripped[index] == '"':
			index = scanImportQuotedLiteral(stripped, index, javaSource, maskLiterals)
		case hasBytePrefix(stripped, index, '/', '/'):
			index = afterImportDelimiter(stripped, index+2, "\r\n")
		case hasBytePrefix(stripped, index, '/', '*'):
			index = maskImportBlockComment(stripped, index, !javaSource)
		default:
			index++
		}
	}

	return stripped
}

func afterImportDelimiter(content []byte, start int, delimiters string) int {
	if end := bytes.IndexAny(content[start:], delimiters); end >= 0 {
		return start + end + 1
	}
	return len(content)
}

func maskImportBlockComment(content []byte, start int, allowNesting bool) int {
	content[start], content[start+1] = ' ', ' '
	index := start + 2
	depth := 1
	for index < len(content) && depth > 0 {
		if allowNesting && hasBytePrefix(content, index, '/', '*') {
			content[index], content[index+1] = ' ', ' '
			depth++
			index += 2
			continue
		}
		next, state := scanBlockComment(content, index)
		if state == scannerStateCode {
			depth--
		}
		index = next
	}
	return index
}

func scanImportQuotedLiteral(content []byte, start int, javaSource, maskLiteral bool) int {
	end := skipImportQuotedLiteral(content, start, javaSource)
	if maskLiteral {
		// Keep the boundary bytes so a literal cannot act as leading whitespace.
		for index := start + 1; index < end-1; index++ {
			maskNonNewline(content, index)
		}
	}
	return end
}

func skipImportQuotedLiteral(content []byte, start int, javaSource bool) int {
	switch {
	case !javaSource && content[start] == '"':
		return skipKotlinImportLiteral(content, start)
	case hasImportTextBlockDelimiter(content, start):
		return skipJavaImportTextBlock(content, start+3)
	default:
		return skipPlainImportLiteral(content, start)
	}
}

func skipPlainImportLiteral(content []byte, start int) int {
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

func skipJavaImportTextBlock(content []byte, start int) int {
	for index := start; index < len(content); index++ {
		switch {
		case content[index] == '\\':
			index++
		case hasImportTextBlockDelimiter(content, index):
			return index + 3
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
