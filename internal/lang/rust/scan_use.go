package rust

import (
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

func parseUseStatementIndex(content string, idx []int) (string, int, int, bool) {
	if len(idx) < 4 {
		return "", 0, 0, false
	}
	clauseStart, clauseEnd := idx[2], idx[3]
	if clauseStart < 0 || clauseEnd < 0 || clauseEnd > len(content) {
		return "", 0, 0, false
	}
	clause := strings.TrimSpace(content[clauseStart:clauseEnd])
	line, column := lineColumn(content, clauseStart)
	return clause, line, column, true
}

func appendUseClauseImports(imports []importBinding, clause string, ctx useImportContext) []importBinding {
	entries := parseUseClause(clause)
	nextToken := 0
	multiline := strings.ContainsRune(clause, '\n')
	declarationClause := string(shared.MaskCommentsAndStringsForFile([]byte(clause), ctx.FilePath))
	for _, entry := range entries {
		entryContext := ctx
		entryContext.DeclarationTokenHits = countRustUseEntryDeclarationTokens(declarationClause, entry)
		if multiline {
			entryContext, nextToken = locateMultilineUseEntry(declarationClause, entry, entryContext, nextToken)
		}
		binding, ok := makeUseImportBinding(entry, entryContext)
		if !ok {
			continue
		}
		imports = append(imports, binding)
	}
	return imports
}

func locateMultilineUseEntry(clause string, entry usePathEntry, ctx useImportContext, searchStart int) (useImportContext, int) {
	if entry.Wildcard {
		return ctx, advancePastRustUseWildcard(clause, searchStart)
	}
	token := useEntryLocalToken(entry)
	offset := findRustUseEntryToken(clause, entry, token, searchStart)
	if offset < 0 {
		return ctx, searchStart
	}
	line, column := lineColumn(clause, offset)
	ctx.Line += line - 1
	if line == 1 {
		ctx.Column += column - 1
	} else {
		ctx.Column = column
	}
	return ctx, offset + len(token)
}

func countRustUseEntryDeclarationTokens(clause string, entry usePathEntry) int {
	if entry.Wildcard {
		return 0
	}
	return countRustIdentifierTokens(clause, useEntryLocalToken(entry))
}

func advancePastRustUseWildcard(clause string, searchStart int) int {
	if searchStart < 0 || searchStart >= len(clause) {
		return searchStart
	}
	relative := strings.IndexByte(clause[searchStart:], '*')
	if relative < 0 {
		return searchStart
	}
	return searchStart + relative + 1
}

func useEntryLocalToken(entry usePathEntry) string {
	if entry.Local != "" {
		return entry.Local
	}
	return entry.Symbol
}

func findRustUseEntryToken(clause string, entry usePathEntry, token string, searchStart int) int {
	if entry.Local != "" {
		return findRustAliasToken(clause, token, searchStart)
	}
	return findRustIdentifierToken(clause, token, searchStart)
}

func findRustAliasToken(content, alias string, searchStart int) int {
	for searchStart < len(content) {
		asOffset := findRustIdentifierToken(content, "as", searchStart)
		if asOffset < 0 {
			return -1
		}
		aliasOffset := asOffset + len("as")
		for aliasOffset < len(content) && isRustWhitespace(content[aliasOffset]) {
			aliasOffset++
		}
		if rustIdentifierTokenAt(content, alias, aliasOffset) {
			return aliasOffset
		}
		searchStart = asOffset + len("as")
	}
	return -1
}

func countRustIdentifierTokens(content, token string) int {
	count := 0
	for searchStart := 0; searchStart < len(content); {
		offset := findRustIdentifierToken(content, token, searchStart)
		if offset < 0 {
			return count
		}
		count++
		searchStart = offset + len(token)
	}
	return count
}

func findRustIdentifierToken(content, token string, searchStart int) int {
	if token == "" || searchStart < 0 || searchStart >= len(content) {
		return -1
	}
	for searchStart < len(content) {
		relative := strings.Index(content[searchStart:], token)
		if relative < 0 {
			return -1
		}
		offset := searchStart + relative
		if rustIdentifierTokenAt(content, token, offset) {
			return offset
		}
		searchStart = offset + 1
	}
	return -1
}

func rustIdentifierTokenAt(content, token string, offset int) bool {
	end := offset + len(token)
	if token == "" || offset < 0 || end > len(content) || content[offset:end] != token {
		return false
	}
	leftBoundary := offset == 0 || !isRustIdentifierContinue(content[offset-1])
	rightBoundary := end == len(content) || !isRustIdentifierContinue(content[end])
	return leftBoundary && rightBoundary
}

func makeUseImportBinding(entry usePathEntry, ctx useImportContext) (importBinding, bool) {
	if entry.Path == "" {
		return importBinding{}, false
	}
	dependency := resolveDependency(entry.Path, ctx.CrateRoot, ctx.DepLookup, ctx.Scan)
	if dependency == "" {
		return importBinding{}, false
	}
	module := strings.TrimPrefix(entry.Path, "::")
	name, local := normalizeUseSymbolNames(entry, module)
	return importBinding{
		Dependency: dependency,
		Module:     module,
		Name:       name,
		Local:      local,
		Wildcard:   entry.Wildcard,
		Location: report.Location{
			File:   ctx.FilePath,
			Line:   ctx.Line,
			Column: ctx.Column,
		},
		DeclarationTokenHits: ctx.DeclarationTokenHits,
	}, true
}

func normalizeUseSymbolNames(entry usePathEntry, module string) (string, string) {
	name := entry.Symbol
	if name == "" {
		name = lastPathSegment(module)
	}
	local := entry.Local
	if local == "" {
		local = name
	}
	if entry.Wildcard {
		name = "*"
		if local == "" {
			local = lastPathSegment(module)
		}
	}
	return name, local
}

func parseUseClause(clause string) []usePathEntry {
	parts := splitTopLevel(clause, ',')
	entries := make([]usePathEntry, 0)
	for _, part := range parts {
		expandUsePart(strings.TrimSpace(part), "", &entries)
	}
	return entries
}

func expandUsePart(part, prefix string, out *[]usePathEntry) {
	part = strings.TrimSpace(part)
	if part == "" {
		return
	}
	part = strings.TrimPrefix(part, "pub ")
	if expandUseBraceGroup(part, prefix, out) {
		return
	}
	if expandUsePrefixedBraceGroup(part, prefix, out) {
		return
	}
	part, local := parseUseLocalAlias(part)
	part, prefix, wildcard := normalizeUseWildcard(part, prefix)
	*out = append(*out, makeUsePathEntry(prefix, part, local, wildcard))
}

func expandUseBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	if !strings.HasPrefix(part, "{") || !strings.HasSuffix(part, "}") {
		return false
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}"))
	expandUseSegments(inner, prefix, out)
	return true
}

func expandUsePrefixedBraceGroup(part, prefix string, out *[]usePathEntry) bool {
	idx := strings.Index(part, "::{")
	if idx < 0 || !strings.HasSuffix(part, "}") {
		return false
	}
	base := strings.TrimSpace(part[:idx])
	inner := strings.TrimSpace(part[idx+3 : len(part)-1])
	nextPrefix := joinPath(prefix, base)
	expandUseSegments(inner, nextPrefix, out)
	return true
}

func expandUseSegments(inner, prefix string, out *[]usePathEntry) {
	for _, segment := range splitTopLevel(inner, ',') {
		expandUsePart(segment, prefix, out)
	}
}

func parseUseLocalAlias(part string) (string, string) {
	idx := findRustUseAliasIndex(part)
	if idx <= 0 {
		return part, ""
	}
	local := strings.TrimSpace(part[idx+2:])
	base := strings.TrimSpace(part[:idx])
	if base == "" || local == "" {
		return part, ""
	}
	return base, local
}

func findRustUseAliasIndex(part string) int {
	for index := 0; index+1 < len(part); index++ {
		if part[index] != 'a' || part[index+1] != 's' {
			continue
		}
		if index == 0 || !isRustWhitespace(part[index-1]) {
			continue
		}
		next := index + 2
		if next >= len(part) || !isRustWhitespace(part[next]) {
			continue
		}
		return index
	}
	return -1
}

func normalizeUseWildcard(part, prefix string) (string, string, bool) {
	wildcard := part == "*" || strings.HasSuffix(part, "::*")
	if !wildcard {
		return part, prefix, false
	}
	if part == "*" {
		return strings.TrimSpace(prefix), "", true
	}
	return strings.TrimSpace(strings.TrimSuffix(part, "::*")), prefix, true
}

func makeUsePathEntry(prefix, part, local string, wildcard bool) usePathEntry {
	fullPath := joinPath(prefix, part)
	symbol := lastPathSegment(fullPath)
	if strings.EqualFold(symbol, "self") {
		symbol = lastPathSegment(prefix)
	}
	if wildcard {
		symbol = "*"
	}
	if strings.EqualFold(local, "self") {
		local = lastPathSegment(prefix)
	}
	return usePathEntry{
		Path:     fullPath,
		Symbol:   symbol,
		Local:    local,
		Wildcard: wildcard,
	}
}

func joinPath(prefix, value string) string {
	prefix = strings.TrimSpace(prefix)
	value = strings.TrimSpace(value)
	switch {
	case prefix == "":
		return strings.TrimPrefix(value, "::")
	case value == "":
		return strings.TrimPrefix(prefix, "::")
	default:
		return strings.TrimPrefix(prefix+"::"+value, "::")
	}
}

func splitTopLevel(value string, sep rune) []string {
	parts := make([]string, 0)
	depth := 0
	start := 0
	for i, r := range value {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		case sep:
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(value[start:i]))
				start = i + 1
			}
		}
	}
	parts = append(parts, strings.TrimSpace(value[start:]))
	return parts
}

func lineColumn(content string, offset int) (int, int) {
	if offset < 0 {
		return 1, 1
	}
	if offset > len(content) {
		offset = len(content)
	}
	line := 1 + strings.Count(content[:offset], "\n")
	lineStart := strings.LastIndex(content[:offset], "\n")
	if lineStart < 0 {
		return line, offset + 1
	}
	return line, offset - lineStart
}
