package rust

import (
	"iter"
	"strings"
	"unicode/utf8"

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

func appendUseClauseImportsWithLimits(imports []importBinding, clause string, ctx useImportContext, limits useClauseLimits) ([]importBinding, bool) {
	entries, capped := parseUseClauseWithLimits(clause, limits)
	nextToken := 0
	multiline := strings.ContainsRune(clause, '\n')
	declarationClause := string(shared.MaskCommentsAndStringsForFile([]byte(clause), ctx.FilePath))
	declarationTokenHits := countRustDeclarationTokens(declarationClause, collectUseEntryLocalTokens(entries))
	for _, entry := range entries {
		entryContext := ctx
		entryContext.DeclarationTokenHits = declarationTokenHits[useEntryLocalToken(entry)]
		if multiline {
			entryContext, nextToken = locateMultilineUseEntry(declarationClause, entry, entryContext, nextToken)
		}
		binding, ok := makeUseImportBinding(entry, entryContext)
		if !ok {
			continue
		}
		imports = append(imports, binding)
	}
	return imports, capped
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

func collectUseEntryLocalTokens(entries []usePathEntry) map[string]struct{} {
	wanted := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		token := useEntryLocalToken(entry)
		if token == "" {
			continue
		}
		wanted[token] = struct{}{}
	}
	return wanted
}

func countRustDeclarationTokens(clause string, wanted map[string]struct{}) map[string]int {
	hits := make(map[string]int, len(wanted))
	if len(wanted) == 0 {
		return hits
	}
	for index := 0; index < len(clause); {
		token, next, ok := nextRustIdentifierToken(clause, index)
		if !ok {
			break
		}
		if _, ok := wanted[token]; ok {
			hits[token]++
		}
		index = next
	}
	mergeASCIIWordTokenHits(clause, wanted, hits)
	return hits
}

func nextRustIdentifierToken(content string, searchStart int) (string, int, bool) {
	for searchStart < len(content) {
		r, width := utf8.DecodeRuneInString(content[searchStart:])
		if !isRustIdentifierStartRune(r) {
			searchStart += width
			continue
		}

		start := searchStart
		searchStart += width
		for searchStart < len(content) {
			next, nextWidth := utf8.DecodeRuneInString(content[searchStart:])
			if !isRustIdentifierContinueRune(next) {
				break
			}
			searchStart += nextWidth
		}
		return content[start:searchStart], searchStart, true
	}
	return "", searchStart, false
}

func mergeASCIIWordTokenHits(content string, wanted map[string]struct{}, hits map[string]int) {
	asciiHits := countASCIIWordTokenHits(content, wanted)
	for token, count := range asciiHits {
		if count > hits[token] {
			hits[token] = count
		}
	}
}

func countASCIIWordTokenHits(content string, wanted map[string]struct{}) map[string]int {
	hits := make(map[string]int, len(wanted))
	if len(wanted) == 0 {
		return hits
	}
	for offset := 0; offset < len(content); {
		if !isASCIIUsageWordByte(content[offset]) {
			offset++
			continue
		}
		start := offset
		for offset < len(content) && isASCIIUsageWordByte(content[offset]) {
			offset++
		}
		token := content[start:offset]
		if _, ok := wanted[token]; ok && rustIdentifierTokenAt(content, token, start) {
			hits[token]++
		}
	}
	return hits
}

func isASCIIUsageWordByte(b byte) bool {
	return b == '$' || b == '_' || (b >= '0' && b <= '9') || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
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
	leftBoundary := offset == 0
	if !leftBoundary {
		left, _ := utf8.DecodeLastRuneInString(content[:offset])
		leftBoundary = !isRustIdentifierContinueRune(left)
	}
	rightBoundary := end == len(content)
	if !rightBoundary {
		right, _ := utf8.DecodeRuneInString(content[end:])
		rightBoundary = !isRustIdentifierContinueRune(right)
	}
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

func parseUseClauseWithLimits(clause string, limits useClauseLimits) ([]usePathEntry, bool) {
	parser := useClauseParser{limits: limits}
	parser.expandSegments(clause, "", 0)
	return parser.entries, parser.capped
}

type useClauseLimits struct {
	maxDepth   int
	maxEntries int
}

type useClauseParser struct {
	entries []usePathEntry
	limits  useClauseLimits
	capped  bool
}

func (p *useClauseParser) expand(part, prefix string, depth int) {
	if depth >= p.limits.maxDepth || len(p.entries) >= p.limits.maxEntries {
		p.capped = true
		return
	}
	part = strings.TrimSpace(part)
	if part == "" {
		return
	}
	part = strings.TrimPrefix(part, "pub ")
	if p.expandBraceGroup(part, prefix, depth) || p.expandPrefixedBraceGroup(part, prefix, depth) {
		return
	}
	part, local := parseUseLocalAlias(part)
	part, prefix, wildcard := normalizeUseWildcard(part, prefix)
	p.entries = append(p.entries, makeUsePathEntry(prefix, part, local, wildcard))
}

func (p *useClauseParser) expandBraceGroup(part, prefix string, depth int) bool {
	if !strings.HasPrefix(part, "{") || !strings.HasSuffix(part, "}") {
		return false
	}
	p.expandSegments(part[1:len(part)-1], prefix, depth+1)
	return true
}

func (p *useClauseParser) expandPrefixedBraceGroup(part, prefix string, depth int) bool {
	index := strings.Index(part, "::{")
	if index < 0 || !strings.HasSuffix(part, "}") {
		return false
	}
	p.expandSegments(part[index+3:len(part)-1], joinPath(prefix, part[:index]), depth+1)
	return true
}

func (p *useClauseParser) expandSegments(inner, prefix string, depth int) {
	if depth >= p.limits.maxDepth {
		p.capped = true
		return
	}
	for segment := range topLevelUseSegments(inner, ',') {
		if segment == "" {
			continue
		}
		if len(p.entries) >= p.limits.maxEntries {
			p.capped = true
			return
		}
		p.expand(segment, prefix, depth)
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

func topLevelUseSegments(value string, sep rune) iter.Seq[string] {
	return func(yield func(string) bool) {
		depth, start := 0, 0
		for index, char := range value {
			switch char {
			case '{':
				depth++
			case '}':
				depth--
			case sep:
				if depth == 0 {
					if !yield(strings.TrimSpace(value[start:index])) {
						return
					}
					start = index + 1
				}
			}
		}
		yield(strings.TrimSpace(value[start:]))
	}
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
