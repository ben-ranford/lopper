package rust

func parseRustImportStatement(content []byte, lineStart, lineEnd, line int, includeUse bool) (rustImportKind, rustImportStatement, bool) {
	scan := rustUseTerminator{remaining: len(content)}
	return scan.parseRustImportStatement(content, lineStart, lineEnd, line, includeUse)
}

func buildRustUseStatement(content []byte, lineStart, line, statementStart int, statement []byte) (rustImportStatement, bool) {
	scan := rustUseTerminator{remaining: len(content)}
	return scan.buildRustUseStatement(content, lineStart, line, statementStart, statement)
}
