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
