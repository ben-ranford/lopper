package shared

import (
	"path/filepath"
	"strings"
)

func CountUsage(content []byte, imports []ImportRecord) map[string]int {
	importCount := make(map[string]int)
	for _, imported := range imports {
		if imported.Wildcard || imported.Local == "" {
			continue
		}
		importCount[imported.Local]++
	}

	usage := make(map[string]int, len(importCount))
	scannable := MaskCommentsAndStringsWithProfile(content, inferMaskProfile(imports))
	scanTokenUsage(scannable, importCount, usage)
	subtractDeclarationTokenHits(scannable, imports, usage)
	clampUsageCounts(importCount, usage)
	return usage
}

func scanTokenUsage(content []byte, importCount map[string]int, usage map[string]int) {
	for i := 0; i < len(content); {
		if !isWordByte(content[i]) {
			i++
			continue
		}
		start := i
		for i < len(content) && isWordByte(content[i]) {
			i++
		}
		token := string(content[start:i])
		if _, ok := importCount[token]; ok {
			usage[token]++
		}
	}
}

func subtractDeclarationTokenHits(content []byte, imports []ImportRecord, usage map[string]int) {
	var lineStarts []int
	haveLineStarts := false
	for _, imported := range imports {
		if imported.Wildcard || imported.Local == "" {
			continue
		}
		if imported.Location.Line <= 0 {
			usage[imported.Local]--
			continue
		}
		if !haveLineStarts {
			lineStarts = lineStartOffsets(content)
			haveLineStarts = true
		}
		if declarationLineContainsToken(content, lineStarts, imported.Location.Line, imported.Local) {
			usage[imported.Local]--
		}
	}
}

func clampUsageCounts(importCount, usage map[string]int) {
	for local := range importCount {
		if usage[local] < 0 {
			usage[local] = 0
		}
	}
}

func declarationLineContainsToken(content []byte, lineStarts []int, line int, token string) bool {
	if line <= 0 || line > len(lineStarts) {
		return false
	}
	lineStart := lineStarts[line-1]
	lineEnd := len(content)
	if line < len(lineStarts) {
		lineEnd = lineStarts[line] - 1
	}
	if lineStart < 0 || lineStart >= lineEnd || lineEnd > len(content) {
		return false
	}
	return containsWordToken(content[lineStart:lineEnd], token)
}

func lineStartOffsets(content []byte) []int {
	starts := make([]int, 0, 64)
	starts = append(starts, 0)
	for i := 0; i < len(content); i++ {
		if content[i] == '\n' && i+1 < len(content) {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func containsWordToken(content []byte, token string) bool {
	for i := 0; i < len(content); {
		if !isWordByte(content[i]) {
			i++
			continue
		}
		start := i
		for i < len(content) && isWordByte(content[i]) {
			i++
		}
		if string(content[start:i]) == token {
			return true
		}
	}
	return false
}

type maskProfile struct {
	lineSlashSlash bool
	lineHash       bool
	blockSlashStar bool
	singleQuote    bool
	doubleQuote    bool
	backtickQuote  bool
}

var defaultMaskProfile = maskProfile{
	lineSlashSlash: true,
	lineHash:       true,
	blockSlashStar: true,
	singleQuote:    true,
	doubleQuote:    true,
	backtickQuote:  true,
}

func inferMaskProfile(imports []ImportRecord) maskProfile {
	for _, imported := range imports {
		if imported.Location.File == "" {
			continue
		}
		return maskProfileForFile(imported.Location.File)
	}
	return defaultMaskProfile
}

func maskProfileForFile(filePath string) maskProfile {
	switch strings.ToLower(filepath.Ext(filePath)) {
	case ".py", ".pyi":
		return maskProfile{
			lineHash:    true,
			singleQuote: true,
			doubleQuote: true,
		}
	case ".rs":
		return slashCommentStringProfile()
	case ".swift", ".kt", ".kts", ".fs", ".fsx":
		return slashCommentStringProfile()
	case ".rb":
		return maskProfile{
			lineHash:      true,
			singleQuote:   true,
			doubleQuote:   true,
			backtickQuote: true,
		}
	default:
		return defaultMaskProfile
	}
}

func slashCommentStringProfile() maskProfile {
	return maskProfile{
		lineSlashSlash: true,
		blockSlashStar: true,
		singleQuote:    true,
		doubleQuote:    true,
	}
}

// MaskCommentsAndStrings blanks comment and string literal content while
// preserving newlines and byte offsets for line/column calculations.
func MaskCommentsAndStrings(content []byte) []byte {
	return MaskCommentsAndStringsWithProfile(content, defaultMaskProfile)
}

func MaskCommentsAndStringsForFile(content []byte, filePath string) []byte {
	return MaskCommentsAndStringsWithProfile(content, maskProfileForFile(filePath))
}

func MaskCommentsAndStringsWithProfile(content []byte, profile maskProfile) []byte {
	if !containsMaskableSyntax(content, profile) {
		return content
	}

	masked := make([]byte, len(content))
	copy(masked, content)

	state := scannerStateCode
	for i := 0; i < len(masked); {
		i, state = advanceMasking(masked, i, state, profile)
	}
	return masked
}

func containsMaskableSyntax(content []byte, profile maskProfile) bool {
	for i := 0; i < len(content); i++ {
		switch {
		case profile.singleQuote && content[i] == '\'':
			return true
		case profile.doubleQuote && content[i] == '"':
			return true
		case profile.backtickQuote && content[i] == '`':
			return true
		case profile.lineHash && content[i] == '#':
			return true
		case profile.lineSlashSlash && hasBytePrefix(content, i, '/', '/'):
			return true
		case profile.blockSlashStar && hasBytePrefix(content, i, '/', '*'):
			return true
		}
	}
	return false
}

func advanceMasking(content []byte, index int, state scannerState, profile maskProfile) (int, scannerState) {
	switch state {
	case scannerStateCode:
		return scanCode(content, index, profile)
	case scannerStateLineComment:
		return scanLineComment(content, index)
	case scannerStateBlockComment:
		return scanBlockComment(content, index)
	case scannerStateSingleQuote:
		return scanQuoted(content, index, '\'', scannerStateSingleQuote)
	case scannerStateDoubleQuote:
		return scanQuoted(content, index, '"', scannerStateDoubleQuote)
	case scannerStateBacktick:
		return scanQuoted(content, index, '`', scannerStateBacktick)
	default:
		return index + 1, scannerStateCode
	}
}

func scanCode(content []byte, index int, profile maskProfile) (int, scannerState) {
	if profile.lineSlashSlash && hasBytePrefix(content, index, '/', '/') {
		maskNonNewline(content, index)
		maskNonNewline(content, index+1)
		return index + 2, scannerStateLineComment
	}
	if profile.blockSlashStar && hasBytePrefix(content, index, '/', '*') {
		maskNonNewline(content, index)
		maskNonNewline(content, index+1)
		return index + 2, scannerStateBlockComment
	}
	ch := content[index]
	if profile.lineHash && ch == '#' {
		maskNonNewline(content, index)
		return index + 1, scannerStateLineComment
	}
	if profile.singleQuote && ch == '\'' {
		maskNonNewline(content, index)
		return index + 1, scannerStateSingleQuote
	}
	if profile.doubleQuote && ch == '"' {
		maskNonNewline(content, index)
		return index + 1, scannerStateDoubleQuote
	}
	if profile.backtickQuote && ch == '`' {
		maskNonNewline(content, index)
		return index + 1, scannerStateBacktick
	}
	return index + 1, scannerStateCode
}

func scanLineComment(content []byte, index int) (int, scannerState) {
	if content[index] == '\n' || content[index] == '\r' {
		return index + 1, scannerStateCode
	}
	maskNonNewline(content, index)
	return index + 1, scannerStateLineComment
}

func scanBlockComment(content []byte, index int) (int, scannerState) {
	if hasBytePrefix(content, index, '*', '/') {
		maskNonNewline(content, index)
		maskNonNewline(content, index+1)
		return index + 2, scannerStateCode
	}
	maskNonNewline(content, index)
	return index + 1, scannerStateBlockComment
}

func scanQuoted(content []byte, index int, delimiter byte, state scannerState) (int, scannerState) {
	ch := content[index]
	maskNonNewline(content, index)
	if ch == '\\' && index+1 < len(content) {
		maskNonNewline(content, index+1)
		return index + 2, state
	}
	if ch == delimiter {
		return index + 1, scannerStateCode
	}
	return index + 1, state
}

func hasBytePrefix(content []byte, index int, first, second byte) bool {
	return index+1 < len(content) && content[index] == first && content[index+1] == second
}

type scannerState uint8

const (
	scannerStateCode scannerState = iota
	scannerStateLineComment
	scannerStateBlockComment
	scannerStateSingleQuote
	scannerStateDoubleQuote
	scannerStateBacktick
)

func maskNonNewline(content []byte, index int) {
	if content[index] == '\n' || content[index] == '\r' {
		return
	}
	content[index] = ' '
}

// isWordByte implements an ASCII-only token scanner for import local names.
// It intentionally treats '$' as part of identifiers (common in JS/PHP), so
// tokens such as "$foo" and "foo$bar" can be matched. Non-ASCII bytes are not
// considered word characters and therefore split/ignore Unicode identifiers.
func isWordByte(ch byte) bool {
	return ch == '$' || ch == '_' || (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
}
