package shared

import (
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
	if len(content) == 0 {
		return content
	}

	stripped := make([]byte, len(content))
	copy(stripped, content)

	inBlockComment := false
	for i := 0; i < len(stripped); i++ {
		if inBlockComment {
			if i+1 < len(stripped) && stripped[i] == '*' && stripped[i+1] == '/' {
				stripped[i] = ' '
				stripped[i+1] = ' '
				inBlockComment = false
				i++
				continue
			}
			if stripped[i] != '\n' && stripped[i] != '\r' {
				stripped[i] = ' '
			}
			continue
		}
		if i+1 < len(stripped) && stripped[i] == '/' && stripped[i+1] == '*' {
			stripped[i] = ' '
			stripped[i+1] = ' '
			inBlockComment = true
			i++
		}
	}

	return stripped
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
