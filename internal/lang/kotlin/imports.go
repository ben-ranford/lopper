// Package kotlin contains Kotlin-specific import parsing and usage helpers.
package kotlin

import (
	"regexp"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

const (
	importMatchGroups = 4
	identifierPattern = "(?:[A-Za-z_][A-Za-z0-9_]*|`[A-Za-z_][A-Za-z0-9_]*`)"
)

var importPattern = regexp.MustCompile(`^\s*import\s+(?:static\s+)?(` + identifierPattern + `(?:\.` + identifierPattern + `)*)(\.\*)?(?:\s+as\s+(` + identifierPattern + `))?\s*;?\s*$`)

var hardKeywords = map[string]struct{}{
	"as": {}, "break": {}, "class": {}, "continue": {}, "do": {}, "else": {}, "false": {},
	"for": {}, "fun": {}, "if": {}, "in": {}, "interface": {}, "is": {}, "null": {}, "object": {},
	"package": {}, "return": {}, "super": {}, "this": {}, "throw": {}, "true": {}, "try": {},
	"typealias": {}, "typeof": {}, "val": {}, "var": {}, "when": {}, "while": {},
}

// MatchImport recognizes Java imports plus Kotlin escaped identifiers and aliases.
func MatchImport(line string) []string {
	return importPattern.FindStringSubmatch(line)
}

// IsImportMatch reports whether matches came from MatchImport.
func IsImportMatch(matches []string) bool {
	return len(matches) == importMatchGroups
}

// LocalName returns the local binding represented by a matched import.
func LocalName(matches []string, module string) string {
	if alias := strings.TrimSpace(matches[3]); alias != "" && strings.TrimSpace(matches[2]) != ".*" {
		return trimEscapes(alias)
	}
	if strings.TrimSpace(matches[2]) == ".*" {
		return "*"
	}
	parts := strings.Split(module, ".")
	return trimEscapes(strings.TrimSpace(parts[len(parts)-1]))
}

// CountUsage counts Kotlin import bindings without treating directives or bare hard keywords as uses.
func CountUsage(content []byte, imports []shared.ImportRecord) map[string]int {
	scannable := maskDirectiveLines(shared.MaskCommentsAndStringsForFile(content, "source.kt"))
	escapedLocals := escapedImportLocals(content, imports)
	if len(escapedLocals) == 0 {
		return shared.CountUsage(scannable, imports)
	}

	bareImports := make([]shared.ImportRecord, 0, len(imports))
	for _, imported := range imports {
		if _, escaped := escapedLocals[imported.Local]; !escaped {
			bareImports = append(bareImports, imported)
		}
	}
	usage := shared.CountUsage(scannable, bareImports)
	for index := 0; index < len(scannable); index++ {
		local, end, ok := escapedIdentifierAt(scannable, index)
		if !ok {
			continue
		}
		if _, escaped := escapedLocals[local]; escaped {
			usage[local]++
		}
		index = end
	}
	return usage
}

func escapedImportLocals(content []byte, imports []shared.ImportRecord) map[string]struct{} {
	imported := make(map[string]struct{}, len(imports))
	for _, record := range imports {
		imported[record.Local] = struct{}{}
	}
	escaped := make(map[string]struct{})
	bare := make(map[string]struct{})
	for _, line := range strings.Split(string(shared.StripBlockComments(content)), "\n") {
		matches := MatchImport(shared.StripLineComment(line, "//"))
		if !IsImportMatch(matches) || strings.TrimSpace(matches[2]) == ".*" {
			continue
		}
		local := importLocal(matches)
		if isEscapedKeyword(local) {
			local = trimEscapes(local)
			if _, ok := imported[local]; ok {
				escaped[local] = struct{}{}
			}
			continue
		}
		local = trimEscapes(local)
		if _, ok := imported[local]; ok {
			bare[local] = struct{}{}
		}
	}
	for local := range bare {
		delete(escaped, local)
	}
	return escaped
}

func importLocal(matches []string) string {
	if alias := strings.TrimSpace(matches[3]); alias != "" {
		return alias
	}
	parts := strings.Split(strings.TrimSpace(matches[1]), ".")
	return strings.TrimSpace(parts[len(parts)-1])
}

func isEscapedKeyword(local string) bool {
	if len(local) < 3 || local[0] != '`' || local[len(local)-1] != '`' {
		return false
	}
	_, ok := hardKeywords[local[1:len(local)-1]]
	return ok
}

func trimEscapes(local string) string {
	return strings.TrimPrefix(strings.TrimSuffix(local, "`"), "`")
}

func maskDirectiveLines(content []byte) []byte {
	masked := append([]byte(nil), content...)
	for start := 0; start < len(masked); {
		end := start
		for end < len(masked) && masked[end] != '\n' {
			end++
		}
		fields := strings.Fields(string(masked[start:end]))
		if len(fields) > 0 && (fields[0] == "import" || fields[0] == "package") {
			for index := start; index < end; index++ {
				masked[index] = ' '
			}
		}
		start = end + 1
	}
	return masked
}

func escapedIdentifierAt(content []byte, start int) (string, int, bool) {
	if content[start] != '`' {
		return "", start, false
	}
	end := start + 1
	for end < len(content) && (content[end] == '_' || content[end] >= 'a' && content[end] <= 'z' || content[end] >= 'A' && content[end] <= 'Z' || content[end] >= '0' && content[end] <= '9') {
		end++
	}
	if end == start+1 || end >= len(content) || content[end] != '`' {
		return "", start, false
	}
	return string(content[start+1 : end]), end, true
}
