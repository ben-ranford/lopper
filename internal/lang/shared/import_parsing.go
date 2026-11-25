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

	stripped := bytes.Clone(content)
	source := importSourceMask{raw: stripped, code: stripped}
	if javaSource {
		source.code, source.offsets = javaImportLexicalSource(stripped)
	}
	code := source.code

	for index := 0; index < len(code); {
		switch {
		case !javaSource && code[index] == '`':
			index = afterImportDelimiter(code, index+1, "`\r\n")
		case code[index] == '\'' || code[index] == '"':
			index = source.scanQuotedLiteral(index, javaSource, maskLiterals)
		case hasBytePrefix(code, index, '/', '/'):
			index = afterImportDelimiter(code, index+2, "\r\n")
		case hasBytePrefix(code, index, '/', '*'):
			index = source.scanBlockComment(index, !javaSource)
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

type importSourceMask struct {
	raw          []byte
	code         []byte
	offsets      []javaImportOffset
	nextOffset   int
	displacement int
}

func (s *importSourceMask) scanQuotedLiteral(start int, javaSource, maskLiteral bool) int {
	end := skipImportQuotedLiteral(s.code, start, javaSource)
	if maskLiteral && end > start+2 {
		// Keep the boundary bytes so a literal cannot act as leading whitespace.
		s.mask(start+1, end-1)
	}
	return end
}

func (s *importSourceMask) scanBlockComment(start int, allowNesting bool) int {
	end := maskImportBlockComment(s.code, start, allowNesting)
	if len(s.offsets) != 0 {
		s.mask(start, end)
	}
	return end
}

func (s *importSourceMask) mask(start, end int) {
	rawStart := s.rawIndex(start)
	rawEnd := s.rawIndex(end)
	for index := rawStart; index < rawEnd; index++ {
		maskNonNewline(s.raw, index)
	}
}

func (s *importSourceMask) rawIndex(codeIndex int) int {
	// Mask ranges are visited in source order.
	for s.nextOffset < len(s.offsets) && s.offsets[s.nextOffset].codeIndex <= codeIndex {
		s.displacement = s.offsets[s.nextOffset].displacement
		s.nextOffset++
	}
	return codeIndex + s.displacement
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
