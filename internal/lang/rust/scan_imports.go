package rust

import (
	"bytes"

	"github.com/ben-ranford/lopper/internal/report"
)

func parseRustImports(content, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	return parseRustImportsBytes([]byte(content), filePath, crateRoot, depLookup, scan)
}

func parseRustImportsBytes(content []byte, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	externImports, useImports := collectRustImports(content, filePath, crateRoot, depLookup, scan, true)
	return append(externImports, useImports...)
}

func parseExternCrateImports(content, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	return parseExternCrateImportsBytes([]byte(content), filePath, crateRoot, depLookup, scan)
}

func parseExternCrateImportsBytes(content []byte, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) []importBinding {
	imports, _ := collectRustImports(content, filePath, crateRoot, depLookup, scan, false)
	return imports
}

type rustImportKind uint8

const (
	rustImportExternCrate rustImportKind = iota
	rustImportUse
)

type rustImportStatement struct {
	Clause []byte
	Line   int
	Column int
}

func collectRustImports(content []byte, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult, includeUse bool) ([]importBinding, []importBinding) {
	externImports := make([]importBinding, 0)
	useImports := make([]importBinding, 0)
	scanRustImportStatements(content, includeUse, func(kind rustImportKind, stmt rustImportStatement) {
		switch kind {
		case rustImportExternCrate:
			binding, ok := parseExternCrateClause(stmt.Clause, filePath, crateRoot, depLookup, scan, stmt.Line, stmt.Column)
			if ok {
				externImports = append(externImports, binding)
			}
		case rustImportUse:
			ctx := useImportContext{
				FilePath:  filePath,
				Line:      stmt.Line,
				Column:    stmt.Column,
				CrateRoot: crateRoot,
				DepLookup: depLookup,
				Scan:      scan,
			}
			useImports = appendUseClauseImports(useImports, string(stmt.Clause), ctx)
		}
	})
	return externImports, useImports
}

func scanRustImportStatements(content []byte, includeUse bool, visit func(rustImportKind, rustImportStatement)) {
	state := rustImportLexState{rawHashCount: -1}
	for lineStart, line := 0, 1; lineStart < len(content); line++ {
		lineEnd := lineStart
		for lineEnd < len(content) && content[lineEnd] != '\n' {
			lineEnd++
		}
		if !state.insideRawString() {
			if kind, stmt, ok := parseRustImportStatement(content, lineStart, lineEnd, line, includeUse); ok {
				visit(kind, stmt)
			}
		}
		state.consumeLine(content[lineStart:lineEnd])
		if lineEnd == len(content) {
			break
		}
		lineStart = lineEnd + 1
	}
}

type rustImportLexState struct {
	rawHashCount      int
	inString          bool
	stringEscaped     bool
	blockCommentDepth int
}

func (s *rustImportLexState) insideRawString() bool {
	return s.rawHashCount >= 0
}

func (s *rustImportLexState) consumeLine(line []byte) {
	for index := 0; index < len(line); index++ {
		if nextIndex, handled := s.consumeLineRawString(line, index); handled {
			index = nextIndex
			continue
		}

		if nextIndex, handled := s.consumeLineBlockComment(line, index); handled {
			index = nextIndex
			continue
		}

		if s.consumeLineQuotedString(line[index]) {
			continue
		}

		if hasRustBytePair(line, index, '/', '/') {
			break
		}
		if hasRustBytePair(line, index, '/', '*') {
			s.blockCommentDepth++
			index++
			continue
		}
		if nextIndex, handled := s.consumeLineRawStringStart(line, index); handled {
			index = nextIndex
			continue
		}
		if line[index] == '"' {
			s.inString = true
			s.stringEscaped = false
		}
	}
}

func (s *rustImportLexState) consumeLineRawString(line []byte, index int) (int, bool) {
	if !s.insideRawString() {
		return index, false
	}
	hashCount := s.rawHashCount
	if hasRustRawStringTerminator(line, index, hashCount) {
		s.rawHashCount = -1
		return index + hashCount, true
	}
	return index, true
}

func (s *rustImportLexState) consumeLineBlockComment(line []byte, index int) (int, bool) {
	if s.blockCommentDepth == 0 {
		return index, false
	}
	if hasRustBytePair(line, index, '/', '*') {
		s.blockCommentDepth++
		return index + 1, true
	}
	if hasRustBytePair(line, index, '*', '/') {
		s.blockCommentDepth--
		return index + 1, true
	}
	return index, true
}

func (s *rustImportLexState) consumeLineQuotedString(ch byte) bool {
	if !s.inString {
		return false
	}
	if s.stringEscaped {
		s.stringEscaped = false
		return true
	}
	if ch == '\\' {
		s.stringEscaped = true
		return true
	}
	if ch == '"' {
		s.inString = false
	}
	return true
}

func (s *rustImportLexState) consumeLineRawStringStart(line []byte, index int) (int, bool) {
	hashCount, consumed, ok := parseRustRawStringStart(line, index)
	if !ok {
		return index, false
	}
	s.rawHashCount = hashCount
	return index + consumed - 1, true
}

func hasRustBytePair(line []byte, index int, first, second byte) bool {
	return index+1 < len(line) && line[index] == first && line[index+1] == second
}

func parseRustRawStringStart(line []byte, index int) (int, int, bool) {
	if index >= len(line) {
		return 0, 0, false
	}

	start := index
	if line[index] == 'b' {
		if index+1 >= len(line) || line[index+1] != 'r' {
			return 0, 0, false
		}
		index++
	} else if line[index] != 'r' {
		return 0, 0, false
	}

	if start > 0 && isRustIdentifierContinue(line[start-1]) {
		return 0, 0, false
	}

	hashCount := 0
	index++
	for index < len(line) && line[index] == '#' {
		hashCount++
		index++
	}
	if index >= len(line) || line[index] != '"' {
		return 0, 0, false
	}

	return hashCount, index - start + 1, true
}

func hasRustRawStringTerminator(line []byte, index, hashCount int) bool {
	if index >= len(line) || line[index] != '"' || index+hashCount >= len(line) {
		return false
	}
	for offset := 1; offset <= hashCount; offset++ {
		if line[index+offset] != '#' {
			return false
		}
	}
	return true
}

func parseRustImportStatement(content []byte, lineStart, lineEnd, line int, includeUse bool) (rustImportKind, rustImportStatement, bool) {
	currentLine := content[lineStart:lineEnd]
	firstContent := firstContentByteIndex(currentLine)
	if firstContent >= len(currentLine) {
		return 0, rustImportStatement{}, false
	}

	statementStart := lineStart + firstContent
	statement := currentLine[firstContent:]
	if visibilityOffset := skipRustVisibilityPrefix(statement); visibilityOffset > 0 {
		statementStart += visibilityOffset
		statement = statement[visibilityOffset:]
	}
	if len(statement) == 0 {
		return 0, rustImportStatement{}, false
	}

	if includeUse {
		if stmt, ok := buildRustUseStatement(content, lineStart, line, statementStart, statement); ok {
			return rustImportUse, stmt, true
		}
	}
	if stmt, ok := buildRustExternCrateStatement(content, lineEnd, line, firstContent, statementStart, statement); ok {
		return rustImportExternCrate, stmt, true
	}
	return 0, rustImportStatement{}, false
}

func buildRustUseStatement(content []byte, lineStart, line, statementStart int, statement []byte) (rustImportStatement, bool) {
	clauseStart, ok := matchRustUseStatement(statement)
	if !ok {
		return rustImportStatement{}, false
	}

	clauseOffset := skipRustWhitespace(content, statementStart+clauseStart)
	if clauseOffset >= len(content) {
		return rustImportStatement{}, false
	}

	end := bytes.IndexByte(content[clauseOffset:], ';')
	if end < 0 {
		return rustImportStatement{}, false
	}

	stmtLine, column := lineColumnBytesFrom(content, line, lineStart, clauseOffset)
	return rustImportStatement{
		Clause: bytes.TrimSpace(content[clauseOffset : clauseOffset+end]),
		Line:   stmtLine,
		Column: column,
	}, true
}

func buildRustExternCrateStatement(content []byte, lineEnd, line, firstContent, statementStart int, statement []byte) (rustImportStatement, bool) {
	clauseStart, ok := matchExternCrateStatement(statement)
	if !ok {
		return rustImportStatement{}, false
	}

	end := bytes.IndexByte(content[statementStart+clauseStart:lineEnd], ';')
	if end < 0 {
		return rustImportStatement{}, false
	}

	return rustImportStatement{
		Clause: bytes.TrimSpace(content[statementStart+clauseStart : statementStart+clauseStart+end]),
		Line:   line,
		Column: firstContent + 1,
	}, true
}

func matchRustUseStatement(line []byte) (int, bool) {
	if !bytes.HasPrefix(line, []byte("use")) {
		return 0, false
	}
	if len(line) == len("use") {
		return len("use"), true
	}
	if !isRustWhitespace(line[len("use")]) {
		return 0, false
	}
	return len("use"), true
}

func matchExternCrateStatement(line []byte) (int, bool) {
	if !bytes.HasPrefix(line, []byte("extern")) {
		return 0, false
	}
	if len(line) <= len("extern") || !isRustWhitespace(line[len("extern")]) {
		return 0, false
	}
	index := skipRustWhitespace(line, len("extern"))
	if !bytes.HasPrefix(line[index:], []byte("crate")) {
		return 0, false
	}
	index += len("crate")
	if index >= len(line) || !isRustWhitespace(line[index]) {
		return 0, false
	}
	return index, true
}

func parseExternCrateClause(clause []byte, filePath, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult, line, column int) (importBinding, bool) {
	clause = bytes.TrimSpace(clause)
	root, offset, ok := consumeRustIdentifier(clause)
	if !ok {
		return importBinding{}, false
	}

	local := root
	rest := bytes.TrimSpace(clause[offset:])
	if len(rest) > 0 {
		if len(rest) <= len("as") || !bytes.HasPrefix(rest, []byte("as")) || !isRustWhitespace(rest[len("as")]) {
			return importBinding{}, false
		}
		aliasClause := bytes.TrimSpace(rest[len("as"):])
		alias, next, ok := consumeRustIdentifier(aliasClause)
		if !ok {
			return importBinding{}, false
		}
		if len(bytes.TrimSpace(aliasClause[next:])) > 0 {
			return importBinding{}, false
		}
		local = alias
	}

	dependency := resolveDependency(root, crateRoot, depLookup, scan)
	if dependency == "" {
		return importBinding{}, false
	}

	return importBinding{
		Dependency: dependency,
		Module:     root,
		Name:       root,
		Local:      local,
		Location: report.Location{
			File:   filePath,
			Line:   line,
			Column: column,
		},
	}, true
}

func consumeRustIdentifier(value []byte) (string, int, bool) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 || !isRustIdentifierStart(value[0]) {
		return "", 0, false
	}
	index := 1
	for index < len(value) && isRustIdentifierContinue(value[index]) {
		index++
	}
	return string(value[:index]), index, true
}

func isRustIdentifierStart(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isRustIdentifierContinue(b byte) bool {
	return isRustIdentifierStart(b) || (b >= '0' && b <= '9')
}

func firstContentByteIndex(line []byte) int {
	for index := 0; index < len(line); index++ {
		if line[index] != ' ' && line[index] != '\t' {
			return index
		}
	}
	return len(line)
}

func skipRustVisibilityPrefix(line []byte) int {
	if !bytes.HasPrefix(line, []byte("pub")) || len(line) <= len("pub") {
		return 0
	}
	index := len("pub")
	if line[index] == '(' {
		index++
		for index < len(line) && line[index] != ')' {
			index++
		}
		if index >= len(line) {
			return 0
		}
		index++
	}
	if index >= len(line) || !isRustWhitespace(line[index]) {
		return 0
	}
	return skipRustWhitespace(line, index)
}

func skipRustWhitespace(value []byte, index int) int {
	for index < len(value) && isRustWhitespace(value[index]) {
		index++
	}
	return index
}

func isRustWhitespace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r':
		return true
	default:
		return false
	}
}

func lineColumnBytesFrom(content []byte, baseLine, baseOffset, offset int) (int, int) {
	if offset < 0 {
		return baseLine, 1
	}
	if offset > len(content) {
		offset = len(content)
	}
	if offset <= baseOffset {
		return baseLine, 1
	}
	segment := content[baseOffset:offset]
	lineDelta := bytes.Count(segment, []byte{'\n'})
	if lineDelta == 0 {
		return baseLine, offset - baseOffset + 1
	}
	lastNewline := bytes.LastIndexByte(segment, '\n')
	lineStart := baseOffset + lastNewline + 1
	return baseLine + lineDelta, offset - lineStart + 1
}
