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

// MatchImportModule recognizes an import line and returns its imported module path.
func MatchImportModule(line string) ([]string, string, bool) {
	matches := MatchImport(line)
	if !IsImportMatch(matches) {
		return nil, "", false
	}
	module := strings.TrimSpace(matches[1])
	if module == "" {
		return nil, "", false
	}
	return matches, module, true
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
	scannable := scannableKotlinContent(content)
	escapedLocals := escapedImportLocals(content, imports)
	if len(escapedLocals) == 0 {
		return shared.CountUsage(scannable, imports)
	}

	usage := shared.CountUsage(scannable, importsWithoutEscapedLocals(imports, escapedLocals))
	countEscapedIdentifierUsage(scannable, escapedLocals, usage)
	return usage
}

func scannableKotlinContent(content []byte) []byte {
	return maskDirectiveLines(shared.MaskCommentsAndStringsForFile(content, "source.kt"))
}

func importsWithoutEscapedLocals(imports []shared.ImportRecord, escapedLocals map[string]struct{}) []shared.ImportRecord {
	unescaped := make([]shared.ImportRecord, 0, len(imports))
	for _, imported := range imports {
		if _, escaped := escapedLocals[imported.Local]; escaped {
			continue
		}
		unescaped = append(unescaped, imported)
	}
	return unescaped
}

func countEscapedIdentifierUsage(scannable []byte, escapedLocals map[string]struct{}, usage map[string]int) {
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
}

func escapedImportLocals(content []byte, imports []shared.ImportRecord) map[string]struct{} {
	tracker := newEscapedImportTracker(imports)
	for _, line := range strings.Split(string(shared.StripBlockComments(content)), "\n") {
		tracker.recordLine(line)
	}
	return tracker.escapedOnly()
}

type escapedImportTracker struct {
	imported map[string]struct{}
	escaped  map[string]struct{}
	bare     map[string]struct{}
}

func newEscapedImportTracker(imports []shared.ImportRecord) escapedImportTracker {
	tracker := escapedImportTracker{
		imported: make(map[string]struct{}, len(imports)),
		escaped:  make(map[string]struct{}),
		bare:     make(map[string]struct{}),
	}
	for _, record := range imports {
		tracker.imported[record.Local] = struct{}{}
	}
	return tracker
}

func (t *escapedImportTracker) recordLine(line string) {
	matches, _, ok := MatchImportModule(shared.StripLineComment(line, "//"))
	if !ok || isWildcardImport(matches) {
		return
	}
	t.recordLocal(importLocal(matches))
}

func (t *escapedImportTracker) recordLocal(local string) {
	if isEscapedKeyword(local) {
		t.recordEscapedKeyword(local)
		return
	}
	t.recordBareLocal(trimEscapes(local))
}

func (t *escapedImportTracker) recordEscapedKeyword(local string) {
	local = trimEscapes(local)
	if _, ok := t.imported[local]; ok {
		t.escaped[local] = struct{}{}
	}
}

func (t *escapedImportTracker) recordBareLocal(local string) {
	if _, ok := t.imported[local]; ok {
		t.bare[local] = struct{}{}
	}
}

func (t *escapedImportTracker) escapedOnly() map[string]struct{} {
	for local := range t.bare {
		delete(t.escaped, local)
	}
	return t.escaped
}

func isWildcardImport(matches []string) bool {
	return strings.TrimSpace(matches[2]) == ".*"
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
	forEachLine(masked, func(start, end int) {
		if isDirectiveLine(masked[start:end]) {
			maskByteRange(masked, start, end)
		}
	})
	return masked
}

func forEachLine(content []byte, visit func(start, end int)) {
	for start := 0; start < len(content); {
		end := lineEnd(content, start)
		visit(start, end)
		start = end + 1
	}
}

func lineEnd(content []byte, start int) int {
	end := start
	for end < len(content) && content[end] != '\n' {
		end++
	}
	return end
}

func isDirectiveLine(line []byte) bool {
	fields := strings.Fields(string(line))
	return len(fields) > 0 && isKotlinDirective(fields[0])
}

func isKotlinDirective(token string) bool {
	return token == "import" || token == "package"
}

func maskByteRange(content []byte, start, end int) {
	for index := start; index < end; index++ {
		content[index] = ' '
	}
}

func escapedIdentifierAt(content []byte, start int) (string, int, bool) {
	if !hasOpeningEscape(content, start) {
		return "", start, false
	}
	end := scanEscapedIdentifierEnd(content, start+1)
	if !hasClosingEscape(content, start, end) {
		return "", start, false
	}
	return string(content[start+1 : end]), end, true
}

func hasOpeningEscape(content []byte, start int) bool {
	return start < len(content) && content[start] == '`'
}

func scanEscapedIdentifierEnd(content []byte, start int) int {
	end := start
	for end < len(content) && isIdentifierByte(content[end]) {
		end++
	}
	return end
}

func hasClosingEscape(content []byte, start, end int) bool {
	return end > start+1 && end < len(content) && content[end] == '`'
}

func isIdentifierByte(char byte) bool {
	return char == '_' || isASCIILetter(char) || isASCIIDigit(char)
}

func isASCIILetter(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func isASCIIDigit(char byte) bool {
	return char >= '0' && char <= '9'
}
