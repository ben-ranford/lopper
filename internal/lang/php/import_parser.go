package php

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

type importParseResult struct {
	imports                      []importBinding
	groupedByDep                 map[string]int
	unresolvedCount              int
	useStatementLimitHit         bool
	useBindingLimitHit           bool
	namespaceDeclarationLimitHit bool
	namespaceReferenceLimitHit   bool
	namespaceResolutionLimitHit  bool
}

type namespaceReferenceParseResult struct {
	imports                     []importBinding
	unresolvedCount             int
	limitHit                    bool
	namespaceResolutionLimitHit bool
}

type phpLineIndex struct {
	text   string
	starts []int
}

type phpUseStatementMatch struct {
	start          int
	end            int
	statementStart int
	statementEnd   int
}

type phpUseStatementScan struct {
	matches []phpUseStatementMatch
	ranges  [][]int
}

type phpUseContext struct {
	classBody     bool
	namespace     string
	namespaceUses map[string]string
}

type phpUseStatementParser struct {
	result         *importParseResult
	contextTracker *phpContextTracker
	text           string
	filePath       string
	resolver       composerResolver
	lineIndex      phpLineIndex
}

type phpCodeState uint8

const (
	phpStateCode phpCodeState = iota
	phpStateLineComment
	phpStateBlockComment
	phpStateSingleQuote
	phpStateDoubleQuote
	phpStateBacktick
)

var namespaceRefPattern = regexp.MustCompile(`\\?[A-Za-z_\x{80}-\x{10FFFF}][A-Za-z0-9_\x{80}-\x{10FFFF}]*(?:\\[A-Za-z_\x{80}-\x{10FFFF}][A-Za-z0-9_\x{80}-\x{10FFFF}]*)+`)
var dynamicPattern = regexp.MustCompile(`(?m)(new\s+\$[A-Za-z_]|\$[A-Za-z_][A-Za-z0-9_]*\s*::|\b(class_exists|interface_exists|trait_exists|method_exists)\s*\()`) //nolint:lll

func parseImports(content []byte, filePath string, resolver composerResolver) ([]importBinding, map[string]int, int) {
	result := parsePHPImports(content, filePath, resolver)
	return result.imports, result.groupedByDep, result.unresolvedCount
}

func parsePHPImports(content []byte, filePath string, resolver composerResolver) importParseResult {
	activePHP := maskInactivePHPRegionsWithShortOpenTags(string(content), resolver.allowPHPShortOpenTags)
	phpMasked := maskPHPHeredocNowdocBodies(activePHP)
	sanitized := shared.MaskCommentsAndStringsForFile([]byte(phpMasked), filePath)
	text := string(sanitized)
	lineIndex := newPHPLineIndex(text)
	namespaceDeclarations, namespaceDeclarationLimitHit := findNamespaceDeclarationsWithShortOpenTags(text, resolver.allowPHPShortOpenTags)
	useScan, namespaceText := scanPHPUseStatementsForImports(text, maxPHPUseStatementsPerFile+1)
	result := parsePHPUseStatementMatches(text, filePath, resolver, lineIndex, useScan.matches, namespaceDeclarations)
	namespaceResult := parseNamespaceReferencesTextWithLineIndexAndUseRangesAndDeclarations(namespaceText, filePath, resolver, lineIndex, nil, namespaceDeclarations)
	result.imports = append(result.imports, namespaceResult.imports...)
	result.unresolvedCount += namespaceResult.unresolvedCount
	result.namespaceDeclarationLimitHit = namespaceDeclarationLimitHit
	result.namespaceReferenceLimitHit = namespaceResult.limitHit
	result.namespaceResolutionLimitHit = result.namespaceResolutionLimitHit || namespaceResult.namespaceResolutionLimitHit
	return result
}

func parsePHPUseStatementMatches(text, filePath string, resolver composerResolver, lineIndex phpLineIndex, matches []phpUseStatementMatch, namespaceDeclarations []phpNamespaceDeclaration) importParseResult {
	contextTracker := newPHPContextTracker(text, namespaceDeclarations)
	result := importParseResult{
		imports:      make([]importBinding, 0),
		groupedByDep: make(map[string]int),
	}
	if len(matches) > maxPHPUseStatementsPerFile {
		result.useStatementLimitHit = true
		matches = matches[:maxPHPUseStatementsPerFile]
	}

	parser := phpUseStatementParser{
		result:         &result,
		contextTracker: &contextTracker,
		text:           text,
		filePath:       filePath,
		resolver:       resolver,
		lineIndex:      lineIndex,
	}
	consumedUseParts := 0
	for _, match := range matches {
		consumedParts, stop := parser.consume(match, maxPHPUseStatementsPerFile-consumedUseParts)
		consumedUseParts += consumedParts
		if stop {
			break
		}
	}
	return result
}

func (p *phpUseStatementParser) consume(match phpUseStatementMatch, remainingUseParts int) (int, bool) {
	if remainingUseParts <= 0 {
		p.result.useBindingLimitHit = true
		return 0, true
	}
	statement := strings.TrimSpace(p.text[match.statementStart:match.statementEnd])
	line := p.lineIndex.lineNumberAt(match.statementStart)
	context := p.contextTracker.advanceTo(match.start)
	bindings, groupedDeps, unresolvedCount, consumedParts, bindingLimitHit, resolutionLimitHit := parseUseStatementByContext(statement, p.filePath, line, p.resolver, remainingUseParts, context)
	if !context.classBody {
		p.contextTracker.addNamespaceUses(statement, remainingUseParts)
	}
	p.result.imports = append(p.result.imports, bindings...)
	for dep := range groupedDeps {
		p.result.groupedByDep[dep]++
	}
	p.result.unresolvedCount += unresolvedCount
	p.result.useBindingLimitHit = p.result.useBindingLimitHit || bindingLimitHit
	p.result.namespaceResolutionLimitHit = p.result.namespaceResolutionLimitHit || resolutionLimitHit
	return consumedParts, bindingLimitHit || resolutionLimitHit
}

func parseUseStatementByContext(statement, filePath string, line int, resolver composerResolver, partLimit int, context phpUseContext) ([]importBinding, map[string]struct{}, int, int, bool, bool) {
	if context.classBody {
		bindings, groupedDeps, unresolved, consumedParts, limitHit, resolutionLimitHit := parseClassBodyUseStatement(statement, filePath, line, resolver, partLimit, context.namespace, context.namespaceUses)
		for i := range bindings {
			bindings[i].Wildcard = true
		}
		return bindings, groupedDeps, unresolved, consumedParts, limitHit, resolutionLimitHit
	}
	return parseUseStatementWithPartLimit(statement, filePath, line, resolver, partLimit)
}

func parseNamespaceReferences(content []byte, filePath string, resolver composerResolver) ([]importBinding, int) {
	activePHP := maskInactivePHPRegionsWithShortOpenTags(string(content), resolver.allowPHPShortOpenTags)
	phpMasked := maskPHPHeredocNowdocBodies(activePHP)
	sanitized := shared.MaskCommentsAndStringsForFile([]byte(phpMasked), filePath)
	return parseNamespaceReferencesText(string(sanitized), filePath, resolver)
}

func parseNamespaceReferencesText(text string, filePath string, resolver composerResolver) ([]importBinding, int) {
	result := parseNamespaceReferencesTextWithLineIndex(text, filePath, resolver, newPHPLineIndex(text))
	return result.imports, result.unresolvedCount
}

func parseNamespaceReferencesTextWithLineIndex(text string, filePath string, resolver composerResolver, lineIndex phpLineIndex) namespaceReferenceParseResult {
	return parseNamespaceReferencesTextWithLineIndexAndUseRanges(text, filePath, resolver, lineIndex, findPHPUseStatementRanges(text))
}

func parseNamespaceReferencesTextWithLineIndexAndUseRanges(text string, filePath string, resolver composerResolver, lineIndex phpLineIndex, useRanges [][]int) namespaceReferenceParseResult {
	namespaceDeclarations, _ := findNamespaceDeclarationsWithShortOpenTags(text, resolver.allowPHPShortOpenTags)
	return parseNamespaceReferencesTextWithLineIndexAndUseRangesAndDeclarations(text, filePath, resolver, lineIndex, useRanges, namespaceDeclarations)
}

func parseNamespaceReferencesTextWithLineIndexAndUseRangesAndDeclarations(text string, filePath string, resolver composerResolver, lineIndex phpLineIndex, useRanges [][]int, namespaceDeclarations []phpNamespaceDeclaration) namespaceReferenceParseResult {
	namespaceText := maskUseStatementRanges(text, useRanges, namespaceDeclarationRanges(namespaceDeclarations))
	matches := namespaceRefPattern.FindAllStringIndex(namespaceText, maxPHPNamespaceReferencesPerFile+1)
	result := namespaceReferenceParseResult{}
	if len(matches) > maxPHPNamespaceReferencesPerFile {
		result.limitHit = true
		matches = matches[:maxPHPNamespaceReferencesPerFile]
	}
	result.imports = make([]importBinding, 0, len(matches))
	seen := make(map[string]struct{})
	for _, match := range matches {
		binding, unresolvedInc, ok, resolutionLimitHit := parseNamespaceReferenceWithLineIndex(namespaceText, match, filePath, resolver, seen, lineIndex)
		if resolutionLimitHit {
			result.namespaceResolutionLimitHit = true
			break
		}
		result.unresolvedCount += unresolvedInc
		if !ok {
			continue
		}
		result.imports = append(result.imports, binding)
	}
	return result
}

func parseNamespaceReference(text string, match []int, filePath string, resolver composerResolver, seen map[string]struct{}) (importBinding, int, bool) {
	binding, unresolved, ok, _ := parseNamespaceReferenceWithLineIndex(text, match, filePath, resolver, seen, newPHPLineIndex(text))
	return binding, unresolved, ok
}

func parseNamespaceReferenceWithLineIndex(text string, match []int, filePath string, resolver composerResolver, seen map[string]struct{}, lineIndex phpLineIndex) (importBinding, int, bool, bool) {
	module, line, local, ok := parseNamespaceReferenceMetadataWithLineIndex(text, match, lineIndex)
	if !ok {
		return importBinding{}, 0, false, false
	}
	resolution := resolver.resolveModule(module)
	dependency, resolved := resolution.dependency, resolution.resolved
	if resolution.limitHit {
		return importBinding{}, 0, false, true
	}
	if dependency == "" {
		if resolved {
			return importBinding{}, 1, false, false
		}
		return importBinding{}, 0, false, false
	}
	if isDuplicateNamespaceReference(seen, module, line) {
		return importBinding{}, 0, false, false
	}
	return namespaceImportBinding(filePath, line, local, module, dependency), 0, true, false
}

func maskUseStatementRanges(text string, useRanges, namespaceDeclarationRanges [][]int) string {
	masked := maskMatchedRanges(text, useRanges, namespaceDeclarationRanges)
	if masked == "" {
		return text
	}
	return masked
}

func findPHPUseStatementRanges(text string) [][]int {
	return scanPHPUseStatements(text, 0).ranges
}

func findPHPUseStatementMatches(text string, limit int) []phpUseStatementMatch {
	return scanPHPUseStatements(text, limit).matches
}

func scanPHPUseStatementsForImports(text string, matchLimit int) (phpUseStatementScan, string) {
	matches := make([]phpUseStatementMatch, 0, matchLimit)
	masked := []byte(text)
	for offset := 0; offset < len(text); {
		if !hasKeywordAt(text, offset, "use") {
			offset++
			continue
		}
		match, nextOffset, ok := scanPHPUseStatementAt(text, offset)
		if nextOffset <= offset {
			nextOffset = offset + 1
		}
		if match.end > match.start {
			maskPHPUseStatementRange(masked, match.start, match.end)
		}
		if ok && (matchLimit <= 0 || len(matches) < matchLimit) {
			matches = append(matches, match)
		}
		offset = nextOffset
	}
	return phpUseStatementScan{matches: matches}, string(masked)
}

func maskPHPUseStatementRange(text []byte, start, end int) {
	if start < 0 {
		start = 0
	}
	if end > len(text) {
		end = len(text)
	}
	for offset := start; offset < end; offset++ {
		if !isLineBreak(text[offset]) {
			text[offset] = ' '
		}
	}
}

func scanPHPUseStatements(text string, limit int) phpUseStatementScan {
	matches := make([]phpUseStatementMatch, 0)
	ranges := make([][]int, 0)
	for offset := 0; offset < len(text); {
		if !hasKeywordAt(text, offset, "use") {
			offset++
			continue
		}
		match, nextOffset, ok := scanPHPUseStatementAt(text, offset)
		if nextOffset <= offset {
			nextOffset = offset + 1
		}
		if !ok {
			if match.end > match.start {
				ranges = append(ranges, []int{match.start, match.end})
			}
			offset = nextOffset
			continue
		}
		ranges = append(ranges, []int{match.start, match.end})
		matches = appendPHPUseStatementMatch(matches, match, limit)
		if limit > 0 && len(matches) >= limit {
			return phpUseStatementScan{matches: matches, ranges: ranges}
		}
		offset = match.end
	}
	return phpUseStatementScan{matches: matches, ranges: ranges}
}

func appendPHPUseStatementMatch(matches []phpUseStatementMatch, match phpUseStatementMatch, limit int) []phpUseStatementMatch {
	if limit > 0 && len(matches) >= limit {
		return matches
	}
	return append(matches, match)
}

func nextSameLineUseStatement(text string, offset int) (phpUseStatementMatch, bool) {
	start := skipHorizontalWhitespace(text, offset)
	if start >= len(text) || isLineBreak(text[start]) {
		return phpUseStatementMatch{}, false
	}
	return phpUseStatementAt(text, start)
}

func phpUseStatementAt(text string, start int) (phpUseStatementMatch, bool) {
	match, _, ok := scanPHPUseStatementAt(text, start)
	if !ok {
		return phpUseStatementMatch{}, false
	}
	return match, ok
}

func scanPHPUseStatementAt(text string, start int) (phpUseStatementMatch, int, bool) {
	if !hasKeywordAt(text, start, "use") {
		return phpUseStatementMatch{}, start + 1, false
	}
	afterUse := start + len("use")
	if afterUse >= len(text) || !isPHPWhitespace(text[afterUse]) {
		return phpUseStatementMatch{}, afterUse, false
	}
	statementStart := skipPHPWhitespace(text, afterUse)
	if statementStart >= len(text) || text[statementStart] == '(' || text[statementStart] == '$' {
		return phpUseStatementMatch{}, statementStart, false
	}
	statementEnd, nextOffset, ok := findPHPUseStatementEnd(text, statementStart)
	match := phpUseStatementMatch{
		start:          start,
		end:            nextOffset,
		statementStart: statementStart,
		statementEnd:   statementEnd,
	}
	if !ok {
		return match, nextOffset, false
	}
	return match, nextOffset, true
}

func findPHPUseStatementEnd(text string, statementStart int) (int, int, bool) {
	traitAdaptationDepth := 0
	for offset := statementStart; offset < len(text); offset++ {
		var endsTraitAdaptation bool
		traitAdaptationDepth, endsTraitAdaptation = advancePHPTraitAdaptationDepth(text, statementStart, offset, traitAdaptationDepth)
		if endsTraitAdaptation {
			return offset, offset + 1, true
		}
		if terminatorLength := phpStatementTerminatorLengthAt(text, offset); traitAdaptationDepth == 0 && terminatorLength > 0 {
			return offset, offset + terminatorLength, true
		}
		if nextOffset, unterminated := unterminatedPHPUseStatementAtLineBreak(text, statementStart, offset, traitAdaptationDepth); unterminated {
			return offset, nextOffset, false
		}
	}
	return len(text), len(text), false
}

func advancePHPTraitAdaptationDepth(text string, statementStart, offset, depth int) (int, bool) {
	if text[offset] == '{' {
		if depth == 0 && isPHPTraitAdaptationBlockStart(text, statementStart, offset) {
			return 1, false
		}
		if depth > 0 {
			return depth + 1, false
		}
		return depth, false
	}
	if text[offset] != '}' || depth == 0 {
		return depth, false
	}
	depth--
	return depth, depth == 0
}

func unterminatedPHPUseStatementAtLineBreak(text string, statementStart, offset, traitAdaptationDepth int) (int, bool) {
	if traitAdaptationDepth > 0 || !isLineBreak(text[offset]) {
		return 0, false
	}
	lineStart := nextPHPLineStart(text, nextPHPLineEnd(text, offset))
	nextToken := skipHorizontalWhitespace(text, lineStart)
	return lineStart, hasKeywordAt(text, nextToken, "use") && !useStatementContinuesAfterNewline(text, statementStart, offset)
}

func phpStatementTerminatorLengthAt(text string, offset int) int {
	if offset >= len(text) {
		return 0
	}
	if text[offset] == ';' {
		return len(";")
	}
	if strings.HasPrefix(text[offset:], "?>") {
		return len("?>")
	}
	return 0
}

func isPHPTraitAdaptationBlockStart(text string, statementStart, braceOffset int) bool {
	for offset := braceOffset - 1; offset >= statementStart; offset-- {
		if isPHPWhitespace(text[offset]) {
			continue
		}
		return text[offset] != '\\'
	}
	return false
}

func useStatementContinuesAfterNewline(text string, statementStart, newlineOffset int) bool {
	for offset := newlineOffset - 1; offset >= statementStart; offset-- {
		if isPHPWhitespace(text[offset]) {
			continue
		}
		return text[offset] == ','
	}
	return false
}

func skipPHPWhitespace(text string, offset int) int {
	for offset < len(text) && isPHPWhitespace(text[offset]) {
		offset++
	}
	return offset
}

func skipHorizontalWhitespace(text string, offset int) int {
	for offset < len(text) && isHorizontalWhitespace(text[offset]) {
		offset++
	}
	return offset
}

func isHorizontalWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\f' || ch == '\v'
}

func isPHPWhitespace(ch byte) bool {
	return isHorizontalWhitespace(ch) || isLineBreak(ch)
}

func hasKeywordAt(text string, offset int, keyword string) bool {
	end := offset + len(keyword)
	if end > len(text) || !strings.EqualFold(text[offset:end], keyword) {
		return false
	}
	return (offset == 0 || !isPHPIdentifierByte(text[offset-1])) && (end == len(text) || !isPHPIdentifierByte(text[end]))
}

func isPHPIdentifierByte(ch byte) bool {
	return ch == '$' || ch == '_' || ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= 0x80
}

func namespaceDeclarationRanges(declarations []phpNamespaceDeclaration) [][]int {
	if len(declarations) == 0 {
		return nil
	}
	ranges := make([][]int, 0, len(declarations))
	for _, declaration := range declarations {
		ranges = append(ranges, []int{declaration.start, declaration.end})
	}
	return ranges
}

type phpBraceFrame struct {
	classLike         bool
	namespaceFrame    bool
	previousNamespace string
	previousUses      map[string]string
}

type phpNamespaceDeclaration struct {
	start       int
	end         int
	braceOffset int
	name        string
	bracketed   bool
}

type phpContextTracker struct {
	text                      string
	offset                    int
	frames                    []phpBraceFrame
	currentNamespace          string
	currentNamespaceUses      map[string]string
	semicolonNamespaceByStart map[int]string
	bracketedNamespaceByBrace map[int]string
	classLikeBraceByOffset    map[int]struct{}
}

func newPHPContextTracker(text string, declarations []phpNamespaceDeclaration) phpContextTracker {
	semicolonNamespaceByStart := make(map[int]string)
	bracketedNamespaceByBrace := make(map[int]string)
	for _, declaration := range declarations {
		if declaration.bracketed {
			bracketedNamespaceByBrace[declaration.braceOffset] = declaration.name
			continue
		}
		semicolonNamespaceByStart[declaration.start] = declaration.name
	}
	return phpContextTracker{
		text:                      text,
		frames:                    make([]phpBraceFrame, 0, 8),
		currentNamespaceUses:      make(map[string]string),
		semicolonNamespaceByStart: semicolonNamespaceByStart,
		bracketedNamespaceByBrace: bracketedNamespaceByBrace,
		classLikeBraceByOffset:    findPHPClassLikeBraceOffsets(text),
	}
}

func (t *phpContextTracker) advanceTo(offset int) phpUseContext {
	if offset > len(t.text) {
		offset = len(t.text)
	}
	if offset < t.offset {
		return t.currentContext()
	}
	for i := t.offset; i < offset; i++ {
		if namespace, ok := t.semicolonNamespaceByStart[i]; ok {
			t.currentNamespace = namespace
			t.currentNamespaceUses = make(map[string]string)
		}
		switch t.text[i] {
		case '{':
			t.pushBraceFrame(i)
		case '}':
			t.popBraceFrame()
		}
	}
	t.offset = offset
	return t.currentContext()
}

func (t *phpContextTracker) currentContext() phpUseContext {
	return phpUseContext{
		classBody:     len(t.frames) > 0 && t.frames[len(t.frames)-1].classLike,
		namespace:     t.currentNamespace,
		namespaceUses: t.currentNamespaceUses,
	}
}

func (t *phpContextTracker) addNamespaceUses(statement string, partLimit int) {
	if hasNonClassUseImportQualifier(statement) {
		return
	}
	base, parts := namespaceUseParts(statement, partLimit)
	for _, part := range parts {
		if hasNonClassUseImportQualifier(part) {
			continue
		}
		module, alias, ok := parseUsePartModuleAndLocal(part, base)
		if !ok {
			continue
		}
		t.currentNamespaceUses[strings.ToLower(alias)] = module
	}
}

func namespaceUseParts(statement string, partLimit int) (string, []string) {
	statement = strings.TrimSpace(statement)
	if open := strings.IndexByte(statement, '{'); open >= 0 {
		closingBrace := strings.LastIndex(statement, "}")
		if closingBrace > open {
			parts, _ := splitUseParts(statement[open+1:closingBrace], partLimit)
			return normalizeNamespace(stripUseImportQualifier(statement[:open])), parts
		}
	}
	parts, _ := splitUseParts(statement, partLimit)
	return "", parts
}

func (t *phpContextTracker) pushBraceFrame(offset int) {
	if namespace, ok := t.bracketedNamespaceByBrace[offset]; ok {
		t.frames = append(t.frames, phpBraceFrame{
			namespaceFrame:    true,
			previousNamespace: t.currentNamespace,
			previousUses:      t.currentNamespaceUses,
		})
		t.currentNamespace = namespace
		t.currentNamespaceUses = make(map[string]string)
		return
	}
	_, classLike := t.classLikeBraceByOffset[offset]
	t.frames = append(t.frames, phpBraceFrame{classLike: classLike})
}

func (t *phpContextTracker) popBraceFrame() {
	if len(t.frames) == 0 {
		return
	}
	frame := t.frames[len(t.frames)-1]
	t.frames = t.frames[:len(t.frames)-1]
	if frame.namespaceFrame {
		t.currentNamespace = frame.previousNamespace
		t.currentNamespaceUses = frame.previousUses
	}
}

func findNamespaceDeclarations(text string) []phpNamespaceDeclaration {
	declarations, _ := findNamespaceDeclarationsWithShortOpenTags(text, false)
	return declarations
}

func findNamespaceDeclarationsWithShortOpenTags(text string, allowShortOpenTags bool) ([]phpNamespaceDeclaration, bool) {
	declarations := make([]phpNamespaceDeclaration, 0)
	for lineStart := 0; lineStart < len(text); {
		lineEnd := nextPHPLineEnd(text, lineStart)
		var limitHit bool
		declarations, limitHit = appendNamespaceDeclarationsInLine(declarations, text, lineStart, lineEnd, allowShortOpenTags, maxPHPNamespaceDeclarationsPerFile)
		if limitHit {
			return declarations, true
		}
		if lineEnd >= len(text) {
			break
		}
		lineStart = nextPHPLineStart(text, lineEnd)
	}
	if len(declarations) == 0 {
		return nil, false
	}
	return declarations, false
}

func appendNamespaceDeclarationsInLine(declarations []phpNamespaceDeclaration, text string, lineStart, lineEnd int, allowShortOpenTags bool, declarationLimit int) ([]phpNamespaceDeclaration, bool) {
	prelude := phpNamespaceLinePrelude{offset: lineStart, valid: true, allowShortOpenTags: allowShortOpenTags}
	completion := phpNamespaceLineCompletion{offset: lineStart, allowShortOpenTags: allowShortOpenTags}
	for offset := lineStart; offset < lineEnd; {
		declaration, ok := parseNamespaceDeclarationAt(text, offset)
		if ok {
			prelude.advanceTo(text, offset, lineEnd)
			if prelude.valid || completion.followsCompletedStatement() {
				if len(declarations) >= declarationLimit {
					return declarations, true
				}
				declarations = append(declarations, declaration)
			}
			completion.advanceTo(text, declaration.end)
			offset = declaration.end
			continue
		}
		completion.advanceTo(text, offset+1)
		offset++
	}
	return declarations, false
}

type phpNamespaceLinePrelude struct {
	offset             int
	valid              bool
	phpTagSeen         bool
	allowShortOpenTags bool
}

func (p *phpNamespaceLinePrelude) advanceTo(text string, target, lineEnd int) {
	if !p.valid {
		p.offset = target
		return
	}
	for p.offset < target {
		next := skipPHPWhitespaceUntil(text, p.offset, target)
		if next > p.offset {
			p.offset = next
			continue
		}
		if p.phpTagSeen && strings.HasPrefix(text[p.offset:], "?>") {
			p.phpTagSeen = false
			p.offset += len("?>")
			continue
		}
		if !p.phpTagSeen {
			if tagLength := phpOpenPreludeLengthAt(text, p.offset, target, p.allowShortOpenTags); tagLength > 0 {
				p.phpTagSeen = true
				p.offset += tagLength
				continue
			}
		}
		if next, ok := parseDeclarePreludeAt(text, p.offset, target, lineEnd); ok {
			p.offset = next
			continue
		}
		p.valid = false
		p.offset = target
		return
	}
}

func skipPHPWhitespaceUntil(text string, offset, limit int) int {
	for offset < limit && isPHPWhitespace(text[offset]) {
		offset++
	}
	return offset
}

func hasPHPOpenPreludeAt(text string, offset, limit int) bool {
	end := offset + len("<?php")
	return end <= limit && strings.EqualFold(text[offset:end], "<?php") && (end == len(text) || isPHPWhitespace(text[end]))
}

func phpOpenPreludeLengthAt(text string, offset, limit int, allowShortOpenTags bool) int {
	if hasPHPOpenPreludeAt(text, offset, limit) {
		return len("<?php")
	}
	if allowShortOpenTags && offset+len("<?") <= limit && strings.HasPrefix(text[offset:], "<?") && !strings.HasPrefix(text[offset:], "<?=") {
		return len("<?")
	}
	return 0
}

func parseDeclarePreludeAt(text string, offset, target, lineEnd int) (int, bool) {
	declareEnd := offset + len("declare")
	if declareEnd > target || !strings.EqualFold(text[offset:declareEnd], "declare") {
		return 0, false
	}
	next := offset + len("declare")
	next = skipPHPWhitespaceUntil(text, next, target)
	if next >= target || text[next] != '(' {
		return 0, false
	}
	closeOffset := strings.IndexByte(text[next+1:lineEnd], ')')
	if closeOffset < 0 {
		return 0, false
	}
	next += closeOffset + 2
	next = skipPHPWhitespaceUntil(text, next, lineEnd)
	terminatorLength := phpStatementTerminatorLengthAt(text, next)
	if terminatorLength == 0 {
		return 0, false
	}
	next += terminatorLength
	next = skipPHPWhitespaceUntil(text, next, target)
	if next > target {
		return 0, false
	}
	return next, true
}

type phpNamespaceLineCompletion struct {
	offset             int
	lastNonWhitespace  byte
	allowShortOpenTags bool
	templateMasked     bool
}

func (c *phpNamespaceLineCompletion) advanceTo(text string, target int) {
	for c.offset < target {
		if strings.HasPrefix(text[c.offset:], "?>") {
			c.lastNonWhitespace = ';'
			c.templateMasked = true
			c.offset += len("?>")
			continue
		}
		if tagLength := phpOpenPreludeLengthAt(text, c.offset, len(text), c.allowShortOpenTags); tagLength > 0 {
			c.templateMasked = false
			c.offset += tagLength
			continue
		}
		ch := text[c.offset]
		if c.templateMasked && ch == '~' {
			c.offset++
			continue
		}
		if !isPHPWhitespace(ch) {
			c.lastNonWhitespace = ch
		}
		c.offset++
	}
}

func (c *phpNamespaceLineCompletion) followsCompletedStatement() bool {
	return c.lastNonWhitespace == '}' || c.lastNonWhitespace == ';'
}

func parseNamespaceDeclarationAt(text string, offset int) (phpNamespaceDeclaration, bool) {
	if !hasKeywordAt(text, offset, "namespace") {
		return phpNamespaceDeclaration{}, false
	}
	nameStart := skipPHPWhitespace(text, offset+len("namespace"))
	if nameStart < len(text) && text[nameStart] == '{' {
		return phpNamespaceDeclaration{
			start:       offset,
			end:         nameStart + len("{"),
			braceOffset: nameStart,
			bracketed:   true,
		}, true
	}
	nameEnd, ok := parseNamespaceDeclarationNameEnd(text, nameStart)
	if !ok {
		return phpNamespaceDeclaration{}, false
	}
	end := skipPHPWhitespace(text, nameEnd)
	if end >= len(text) {
		return phpNamespaceDeclaration{}, false
	}
	bracketed := text[end] == '{'
	braceOffset := -1
	terminatorLength := 0
	if bracketed {
		braceOffset = end
		terminatorLength = len("{")
	} else {
		terminatorLength = phpStatementTerminatorLengthAt(text, end)
		if terminatorLength == 0 {
			return phpNamespaceDeclaration{}, false
		}
	}
	return phpNamespaceDeclaration{
		start:       offset,
		end:         end + terminatorLength,
		braceOffset: braceOffset,
		name:        normalizeNamespace(text[nameStart:nameEnd]),
		bracketed:   bracketed,
	}, true
}

func parseNamespaceDeclarationNameEnd(text string, offset int) (int, bool) {
	end, ok := parsePHPNamespaceIdentifierEnd(text, offset)
	if !ok {
		return 0, false
	}
	for end < len(text) && text[end] == '\\' {
		next, ok := parsePHPNamespaceIdentifierEnd(text, end+1)
		if !ok {
			return 0, false
		}
		end = next
	}
	return end, true
}

func parsePHPNamespaceIdentifierEnd(text string, offset int) (int, bool) {
	if offset >= len(text) || !isPHPNamespaceIdentifierStartByte(text[offset]) {
		return 0, false
	}
	offset++
	for offset < len(text) && isPHPNamespaceIdentifierPartByte(text[offset]) {
		offset++
	}
	return offset, true
}

func isPHPNamespaceIdentifierStartByte(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= 0x80
}

func isPHPNamespaceIdentifierPartByte(ch byte) bool {
	return isPHPNamespaceIdentifierStartByte(ch) || ch >= '0' && ch <= '9'
}

func findPHPClassLikeBraceOffsets(text string) map[int]struct{} {
	var stack []byte
	boundaries := []int{0}
	classLikeKeywordOffsets := []int{-1}
	classLikeOffsets := make(map[int]struct{})
	for offset := 0; offset < len(text); offset++ {
		if phpClassLikeKeywordAt(text, offset) {
			classLikeKeywordOffsets[len(classLikeKeywordOffsets)-1] = offset
		}
		switch text[offset] {
		case '(':
			stack = append(stack, '(')
			boundaries = append(boundaries, offset+1)
			classLikeKeywordOffsets = append(classLikeKeywordOffsets, -1)
		case '[':
			stack = append(stack, '[')
			boundaries = append(boundaries, offset+1)
			classLikeKeywordOffsets = append(classLikeKeywordOffsets, -1)
		case '{':
			start := boundaries[len(boundaries)-1]
			minStart := offset - maxPHPNamespaceAncestorBytes
			if minStart > start {
				start = minStart
			}
			if classLikeKeywordOffsets[len(classLikeKeywordOffsets)-1] >= start {
				classLikeOffsets[offset] = struct{}{}
			}
			stack = append(stack, '{')
			boundaries = append(boundaries, offset+1)
			classLikeKeywordOffsets = append(classLikeKeywordOffsets, -1)
		case ')':
			stack, boundaries, classLikeKeywordOffsets = popPHPClassLikeDelimiter(stack, boundaries, classLikeKeywordOffsets, '(')
		case ']':
			stack, boundaries, classLikeKeywordOffsets = popPHPClassLikeDelimiter(stack, boundaries, classLikeKeywordOffsets, '[')
		case '}':
			stack, boundaries, classLikeKeywordOffsets = popPHPClassLikeDelimiter(stack, boundaries, classLikeKeywordOffsets, '{')
		case ';':
			boundaries[len(boundaries)-1] = offset + 1
			classLikeKeywordOffsets[len(classLikeKeywordOffsets)-1] = -1
		}
	}
	if len(classLikeOffsets) == 0 {
		return nil
	}
	return classLikeOffsets
}

func phpClassLikeKeywordAt(text string, offset int) bool {
	return hasKeywordAt(text, offset, "class") ||
		hasKeywordAt(text, offset, "interface") ||
		hasKeywordAt(text, offset, "trait") ||
		hasKeywordAt(text, offset, "enum")
}

func popPHPClassLikeDelimiter(stack []byte, boundaries []int, classLikeKeywordOffsets []int, opener byte) ([]byte, []int, []int) {
	if len(stack) == 0 || stack[len(stack)-1] != opener {
		return stack, boundaries, classLikeKeywordOffsets
	}
	return stack[:len(stack)-1], boundaries[:len(boundaries)-1], classLikeKeywordOffsets[:len(classLikeKeywordOffsets)-1]
}

func maskInactivePHPRegions(text string) string {
	return maskInactivePHPRegionsWithShortOpenTags(text, false)
}

func maskInactivePHPRegionsWithShortOpenTags(text string, allowShortOpenTag bool) string {
	masked := ensureMaskedText(text, nil)
	maskInactivePHPTemplateRange(masked, text, 0, len(masked))
	for offset := 0; offset < len(text); {
		openStart, codeStart, ok := nextPHPOpenTag(text, offset, allowShortOpenTag)
		if !ok {
			break
		}
		_, nextOffset := findPHPRegionEnd(text, codeStart)
		unmaskByteRange(masked, text, openStart, nextOffset)
		if nextOffset <= offset {
			nextOffset = offset + 1
		}
		offset = nextOffset
	}
	return string(masked)
}

func maskInactivePHPTemplateRange(masked []byte, text string, start, end int) {
	for offset := start; offset < end; offset++ {
		if isPHPWhitespace(text[offset]) {
			masked[offset] = text[offset]
			continue
		}
		masked[offset] = '~'
	}
}

func nextPHPOpenTag(text string, offset int, allowShortOpenTag bool) (int, int, bool) {
	for offset < len(text) {
		relative := strings.Index(text[offset:], "<?")
		if relative < 0 {
			return 0, 0, false
		}
		start := offset + relative
		if strings.HasPrefix(text[start:], "<?=") {
			return start, start + len("<?="), true
		}
		tagEnd := start + len("<?php")
		if tagEnd <= len(text) && strings.EqualFold(text[start:tagEnd], "<?php") && (tagEnd == len(text) || isPHPWhitespace(text[tagEnd])) {
			return start, tagEnd, true
		}
		if allowShortOpenTag && !isXMLDeclarationOpenTag(text, start) {
			return start, start + len("<?"), true
		}
		offset = start + len("<?")
	}
	return 0, 0, false
}

func isXMLDeclarationOpenTag(text string, start int) bool {
	tagEnd := start + len("<?xml")
	if tagEnd > len(text) || !strings.EqualFold(text[start:tagEnd], "<?xml") {
		return false
	}
	if tagEnd == len(text) {
		return false
	}
	if isXMLStylesheetProcessingInstructionOpenTag(text, start) {
		return true
	}
	if !isPHPWhitespace(text[tagEnd]) {
		return false
	}
	attributeStart := skipPHPWhitespace(text, tagEnd)
	attributeEnd := attributeStart + len("version")
	if attributeEnd > len(text) || !strings.EqualFold(text[attributeStart:attributeEnd], "version") {
		return false
	}
	attributeValueStart := skipPHPWhitespace(text, attributeEnd)
	return attributeValueStart < len(text) && text[attributeValueStart] == '='
}

func isXMLStylesheetProcessingInstructionOpenTag(text string, start int) bool {
	targetEnd := start + len("<?xml-stylesheet")
	if targetEnd >= len(text) || !strings.EqualFold(text[start:targetEnd], "<?xml-stylesheet") || !isPHPWhitespace(text[targetEnd]) {
		return false
	}
	attributeStart := skipPHPWhitespace(text, targetEnd)
	attributeEnd := attributeStart
	for attributeEnd < len(text) && isXMLProcessingInstructionAttributeByte(text[attributeEnd]) {
		attributeEnd++
	}
	attributeValueStart := skipPHPWhitespace(text, attributeEnd)
	return attributeEnd > attributeStart && attributeValueStart < len(text) && text[attributeValueStart] == '='
}

func isXMLProcessingInstructionAttributeByte(ch byte) bool {
	return isPHPIdentifierByte(ch) || ch == ':' || ch == '-'
}

func findPHPRegionEnd(text string, offset int) (int, int) {
	state := phpStateCode
	for offset < len(text) {
		if isPHPRegionCloseTagAt(text, offset, state) {
			return offset, offset + len("?>")
		}
		if state == phpStateCode && strings.HasPrefix(text[offset:], "<<<") {
			if nextOffset, ok := skipHeredocNowdocBody(text, offset); ok {
				offset = nextOffset
				continue
			}
		}
		offset = advancePHPCodeState(text, offset, &state)
	}
	return len(text), len(text)
}

func isPHPRegionCloseTagAt(text string, offset int, state phpCodeState) bool {
	if !strings.HasPrefix(text[offset:], "?>") {
		return false
	}
	return state == phpStateCode || state == phpStateLineComment
}

func skipHeredocNowdocBody(text string, markerOffset int) (int, bool) {
	lineEnd := nextPHPLineEnd(text, markerOffset)
	label, ok := parseHeredocNowdocLabelAfterMarker(text[markerOffset+len("<<<") : lineEnd])
	if !ok {
		return 0, false
	}
	bodyStart := nextPHPLineStart(text, lineEnd)
	terminatorStart, _, ok := findHeredocNowdocTerminatorRange(text, bodyStart, label)
	if !ok {
		return len(text), true
	}
	return terminatorStart, true
}

func maskPHPHeredocNowdocBodies(text string) string {
	var masked []byte
	state := phpStateCode
	for lineStart := 0; lineStart < len(text); {
		lineEnd := nextPHPLineEnd(text, lineStart)
		label, ok := heredocNowdocLabelWithState(text[lineStart:lineEnd], &state)
		if !ok {
			lineStart = nextPHPLineStart(text, lineEnd)
			continue
		}
		bodyStart := nextPHPLineStart(text, lineEnd)
		terminatorStart, _, ok := findHeredocNowdocTerminatorRange(text, bodyStart, label)
		if !ok {
			masked = withMaskedPHPHeredocRange(text, masked, bodyStart, len(text))
			return string(masked)
		}
		masked = withMaskedPHPHeredocRange(text, masked, bodyStart, terminatorStart)
		state = phpStateCode
		lineStart = terminatorStart
	}
	if len(masked) == 0 {
		return text
	}
	return string(masked)
}

func withMaskedPHPHeredocRange(text string, masked []byte, start, end int) []byte {
	if start >= end {
		return masked
	}
	masked = ensureMaskedText(text, masked)
	maskByteRange(masked, start, end)
	return masked
}

func heredocNowdocLabel(line string) (string, bool) {
	state := phpStateCode
	return heredocNowdocLabelWithState(line, &state)
}

func heredocNowdocLabelWithState(line string, state *phpCodeState) (string, bool) {
	for offset := 0; offset < len(line); {
		if *state == phpStateCode && strings.HasPrefix(line[offset:], "<<<") {
			return parseHeredocNowdocLabelAfterMarker(line[offset+len("<<<"):])
		}
		offset = advancePHPCodeState(line, offset, state)
	}
	if *state == phpStateLineComment {
		*state = phpStateCode
	}
	return "", false
}

func advancePHPCodeState(text string, offset int, state *phpCodeState) int {
	switch *state {
	case phpStateCode:
		return advancePHPCodeStateFromCode(text, offset, state)
	case phpStateLineComment:
		if isLineBreak(text[offset]) {
			*state = phpStateCode
		}
		return offset + 1
	case phpStateBlockComment:
		if offset+1 < len(text) && text[offset] == '*' && text[offset+1] == '/' {
			*state = phpStateCode
			return offset + 2
		}
		return offset + 1
	case phpStateSingleQuote:
		return advancePHPQuotedState(text, offset, '\'', state)
	case phpStateDoubleQuote:
		return advancePHPQuotedState(text, offset, '"', state)
	case phpStateBacktick:
		return advancePHPQuotedState(text, offset, '`', state)
	default:
		*state = phpStateCode
		return offset + 1
	}
}

func advancePHPCodeStateFromCode(text string, offset int, state *phpCodeState) int {
	if offset+1 < len(text) {
		switch {
		case text[offset] == '/' && text[offset+1] == '/':
			*state = phpStateLineComment
			return offset + 2
		case text[offset] == '/' && text[offset+1] == '*':
			*state = phpStateBlockComment
			return offset + 2
		case text[offset] == '#' && text[offset+1] != '[':
			*state = phpStateLineComment
			return offset + 1
		}
	}
	switch text[offset] {
	case '\'':
		*state = phpStateSingleQuote
	case '"':
		*state = phpStateDoubleQuote
	case '`':
		*state = phpStateBacktick
	}
	return offset + 1
}

func advancePHPQuotedState(text string, offset int, quote byte, state *phpCodeState) int {
	switch text[offset] {
	case '\\':
		if offset+1 < len(text) {
			return offset + 2
		}
	case quote:
		*state = phpStateCode
	}
	return offset + 1
}

func parseHeredocNowdocLabelAfterMarker(rest string) (string, bool) {
	rest = strings.TrimLeft(rest, " \t")
	if rest == "" {
		return "", false
	}
	quote := byte(0)
	if rest[0] == '\'' || rest[0] == '"' {
		quote = rest[0]
		rest = rest[1:]
	}
	end := 0
	for end < len(rest) && isPHPIdentifierByte(rest[end]) && rest[end] != '$' {
		end++
	}
	if end == 0 {
		return "", false
	}
	if quote != 0 && (end >= len(rest) || rest[end] != quote) {
		return "", false
	}
	return rest[:end], true
}

func findHeredocNowdocTerminator(text string, start int, label string) int {
	_, lineEnd, ok := findHeredocNowdocTerminatorRange(text, start, label)
	if !ok {
		return -1
	}
	return lineEnd
}

func findHeredocNowdocTerminatorRange(text string, start int, label string) (int, int, bool) {
	for lineStart := start; lineStart < len(text); {
		lineEnd := nextPHPLineEnd(text, lineStart)
		if isHeredocNowdocTerminatorLine(text[lineStart:lineEnd], label) {
			return lineStart, lineEnd, true
		}
		lineStart = nextPHPLineStart(text, lineEnd)
	}
	return 0, 0, false
}

func isHeredocNowdocTerminatorLine(line, label string) bool {
	line = strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(line, label) {
		return false
	}
	rest := line[len(label):]
	if rest != "" && isPHPIdentifierByte(rest[0]) {
		return false
	}
	return isHeredocNowdocTerminatorTail(rest)
}

func isHeredocNowdocTerminatorTail(rest string) bool {
	for offset := 0; offset < len(rest); {
		offset = skipHorizontalWhitespace(rest, offset)
		if offset >= len(rest) || rest[offset] == '\r' {
			return true
		}
		if strings.HasPrefix(rest[offset:], "//") || rest[offset] == '#' {
			return true
		}
		if strings.HasPrefix(rest[offset:], "/*") {
			commentEnd := strings.Index(rest[offset+len("/*"):], "*/")
			if commentEnd < 0 {
				return false
			}
			offset += len("/*") + commentEnd + len("*/")
			continue
		}
		return isHeredocNowdocTerminatorContinuation(rest[offset])
	}
	return true
}

func isHeredocNowdocTerminatorContinuation(ch byte) bool {
	switch ch {
	case ';', ',', '.', '+', '-', '*', '/', '%', '<', '>', '=', '!', '&', '|', '^', '?', ':', '(', ')', '[', ']', '{', '}':
		return true
	default:
		return false
	}
}

func nextPHPLineEnd(text string, start int) int {
	if next := strings.IndexByte(text[start:], '\n'); next >= 0 {
		return start + next
	}
	return len(text)
}

func nextPHPLineStart(text string, lineEnd int) int {
	if lineEnd < len(text) && text[lineEnd] == '\n' {
		return lineEnd + 1
	}
	return lineEnd
}

func maskMatchedRanges(text string, groups ...[][]int) string {
	var masked []byte
	for _, matches := range groups {
		masked = maskMatchedGroup(text, masked, matches)
	}
	if len(masked) == 0 {
		return text
	}
	return string(masked)
}

func maskMatchedGroup(text string, masked []byte, matches [][]int) []byte {
	for _, match := range matches {
		if !isMaskableRange(match) {
			continue
		}
		masked = ensureMaskedText(text, masked)
		maskByteRange(masked, match[0], match[1])
	}
	return masked
}

func isMaskableRange(match []int) bool {
	return len(match) == 2
}

func ensureMaskedText(text string, masked []byte) []byte {
	if len(masked) != 0 {
		return masked
	}
	return []byte(text)
}

func maskByteRange(masked []byte, start, end int) {
	for i := start; i < end; i++ {
		if isLineBreak(masked[i]) {
			continue
		}
		masked[i] = ' '
	}
}

func unmaskByteRange(masked []byte, text string, start, end int) {
	for i := start; i < end; i++ {
		masked[i] = text[i]
	}
}

func isLineBreak(ch byte) bool {
	return ch == '\n' || ch == '\r'
}

func parseNamespaceReferenceMetadata(text string, match []int) (string, int, string, bool) {
	return parseNamespaceReferenceMetadataWithLineIndex(text, match, newPHPLineIndex(text))
}

func parseNamespaceReferenceMetadataWithLineIndex(text string, match []int, lineIndex phpLineIndex) (string, int, string, bool) {
	if len(match) != 2 {
		return "", 0, "", false
	}
	start := match[0]
	end := match[1]
	rawModule := strings.TrimSpace(text[start:end])
	module := normalizeNamespace(strings.TrimPrefix(rawModule, `\`))
	if module == "" {
		return "", 0, "", false
	}
	line := lineIndex.lineNumberAt(start)
	local := lastNamespaceSegment(module)
	return module, line, local, true
}

func isDuplicateNamespaceReference(seen map[string]struct{}, module string, line int) bool {
	key := module + ":" + fmt.Sprint(line)
	if _, ok := seen[key]; ok {
		return true
	}
	seen[key] = struct{}{}
	return false
}

func namespaceImportBinding(filePath string, line int, local string, module string, dependency string) importBinding {
	return newImportBinding(filePath, line, dependency, module, local, local, true)
}

func newImportBinding(filePath string, line int, dependency, module, local, name string, wildcard bool) importBinding {
	if name == "" {
		name = local
	}
	return importBinding{
		Dependency: dependency,
		Module:     module,
		Name:       name,
		Local:      local,
		Wildcard:   wildcard,
		Location: report.Location{
			File:   filePath,
			Line:   line,
			Column: 1,
		},
	}
}

func lineTextAt(text string, targetLine int) string {
	lineIndex := newPHPLineIndex(text)
	return lineIndex.lineTextAt(targetLine)
}

func lineNumberAt(text string, offset int) int {
	lineIndex := newPHPLineIndex(text)
	return lineIndex.lineNumberAt(offset)
}

func newPHPLineIndex(text string) phpLineIndex {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return phpLineIndex{text: text, starts: starts}
}

func (l *phpLineIndex) lineTextAt(targetLine int) string {
	if targetLine <= 0 || targetLine > len(l.starts) {
		return ""
	}
	start := l.starts[targetLine-1]
	end := len(l.text)
	if targetLine < len(l.starts) {
		end = l.starts[targetLine] - 1
	}
	if end < start {
		end = start
	}
	return l.text[start:end]
}

func (l *phpLineIndex) lineNumberAt(offset int) int {
	if offset <= 0 {
		return 1
	}
	if offset > len(l.text) {
		offset = len(l.text)
	}
	line := sort.Search(len(l.starts), func(i int) bool {
		return l.starts[i] > offset
	})
	return line
}

func parseUseStatement(statement, filePath string, line int, resolver composerResolver) ([]importBinding, map[string]struct{}, int) {
	imports, groupedDeps, unresolved, _, _, _ := parseUseStatementWithPartLimit(statement, filePath, line, resolver, maxPHPUseStatementsPerFile)
	return imports, groupedDeps, unresolved
}

func parseUseStatementWithPartLimit(statement, filePath string, line int, resolver composerResolver, partLimit int) ([]importBinding, map[string]struct{}, int, int, bool, bool) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return nil, nil, 0, 0, false, false
	}
	if strings.ContainsAny(statement, "{}") {
		if bindings, groupedDeps, unresolved, consumedParts, ok, limitHit, resolutionLimitHit := parseGroupedUseStatementWithPartLimit(statement, filePath, line, resolver, partLimit); ok {
			return bindings, groupedDeps, unresolved, consumedParts, limitHit, resolutionLimitHit
		}
		return nil, nil, 0, 0, false, false
	}
	bindings, groupedDeps, unresolved, consumedParts, limitHit, resolutionLimitHit := parseFlatUseStatement(statement, filePath, line, resolver, partLimit)
	return bindings, groupedDeps, unresolved, consumedParts, limitHit, resolutionLimitHit
}

func parseGroupedUseStatement(statement, filePath string, line int, resolver composerResolver) ([]importBinding, map[string]struct{}, int, bool) {
	imports, groupedDeps, unresolved, _, ok, _, _ := parseGroupedUseStatementWithPartLimit(statement, filePath, line, resolver, maxPHPUseStatementsPerFile)
	return imports, groupedDeps, unresolved, ok
}

func parseGroupedUseStatementWithPartLimit(statement, filePath string, line int, resolver composerResolver, partLimit int) ([]importBinding, map[string]struct{}, int, int, bool, bool, bool) {
	open := strings.Index(statement, "{")
	closeBrace := strings.LastIndex(statement, "}")
	if open < 0 || closeBrace <= open {
		return nil, nil, 0, 0, false, false, false
	}
	base := normalizeNamespace(stripUseImportQualifier(statement[:open]))
	inside := statement[open+1 : closeBrace]
	parts, limitHit := splitUseParts(inside, partLimit)
	imports, groupedDeps, unresolved, resolutionLimitHit := parseUseParts(parts, base, filePath, line, resolver, true)
	return imports, groupedDeps, unresolved, len(parts), true, limitHit, resolutionLimitHit
}

func parseFlatUseStatement(statement, filePath string, line int, resolver composerResolver, partLimit int) ([]importBinding, map[string]struct{}, int, int, bool, bool) {
	parts, limitHit := splitUseParts(statement, partLimit)
	imports, _, unresolved, resolutionLimitHit := parseUseParts(parts, "", filePath, line, resolver, false)
	return imports, map[string]struct{}{}, unresolved, len(parts), limitHit, resolutionLimitHit
}

func parseClassBodyUseStatement(statement, filePath string, line int, resolver composerResolver, partLimit int, currentNamespace string, namespaceUses map[string]string) ([]importBinding, map[string]struct{}, int, int, bool, bool) {
	statement = traitUseList(statement)
	parts, limitHit := splitUseParts(statement, partLimit)
	imports, groupedDeps, unresolved, resolutionLimitHit := parseClassBodyUseParts(parts, filePath, line, resolver, currentNamespace, namespaceUses)
	return imports, groupedDeps, unresolved, len(parts), limitHit, resolutionLimitHit
}

func traitUseList(statement string) string {
	statement = strings.TrimSpace(statement)
	if open := strings.IndexByte(statement, '{'); open >= 0 {
		return strings.TrimSpace(statement[:open])
	}
	return statement
}

func splitUseParts(statement string, partLimit int) ([]string, bool) {
	if partLimit <= 0 {
		return nil, true
	}
	parts := strings.SplitN(statement, ",", partLimit+1)
	if len(parts) <= partLimit {
		return parts, false
	}
	return parts[:partLimit], true
}

func parseUseParts(parts []string, base, filePath string, line int, resolver composerResolver, collectGroupedDeps bool) ([]importBinding, map[string]struct{}, int, bool) {
	imports := make([]importBinding, 0)
	groupedDeps := make(map[string]struct{})
	unresolved := 0
	for _, part := range parts {
		binding, dep, ok, unresolvedImport, resolutionLimitHit := parseUsePart(strings.TrimSpace(part), base, filePath, line, resolver)
		if resolutionLimitHit {
			return imports, groupedDeps, unresolved, true
		}
		if unresolvedImport {
			unresolved++
		}
		if !ok {
			continue
		}
		imports = append(imports, binding)
		if collectGroupedDeps && dep != "" {
			groupedDeps[dep] = struct{}{}
		}
	}
	return imports, groupedDeps, unresolved, false
}

func parseClassBodyUseParts(parts []string, filePath string, line int, resolver composerResolver, currentNamespace string, namespaceUses map[string]string) ([]importBinding, map[string]struct{}, int, bool) {
	imports := make([]importBinding, 0)
	groupedDeps := make(map[string]struct{})
	unresolved := 0
	for _, part := range parts {
		binding, dep, ok, unresolvedImport, resolutionLimitHit := parseClassBodyUsePart(strings.TrimSpace(part), filePath, line, resolver, currentNamespace, namespaceUses)
		if resolutionLimitHit {
			return imports, groupedDeps, unresolved, true
		}
		if unresolvedImport {
			unresolved++
		}
		if !ok {
			continue
		}
		imports = append(imports, binding)
		if dep != "" {
			groupedDeps[dep] = struct{}{}
		}
	}
	return imports, groupedDeps, unresolved, false
}

func parseClassBodyUsePart(part, filePath string, line int, resolver composerResolver, currentNamespace string, namespaceUses map[string]string) (importBinding, string, bool, bool, bool) {
	raw := stripUseImportQualifier(part)
	absolute := strings.HasPrefix(strings.TrimSpace(raw), `\`)
	module, local := splitAlias(raw)
	module = normalizeNamespace(module)
	if module == "" {
		return importBinding{}, "", false, false, false
	}
	if !absolute {
		if namespaceRelative, relativeModule := splitNamespaceRelativeModule(module); namespaceRelative {
			module = relativeModule
			if currentNamespace != "" {
				module = normalizeNamespace(currentNamespace + `\` + module)
			}
		} else if expandedModule, ok := expandNamespaceUseAlias(module, namespaceUses); ok {
			module = expandedModule
		} else if currentNamespace != "" {
			module = normalizeNamespace(currentNamespace + `\` + module)
		}
	}
	if local == "" {
		local = lastNamespaceSegment(module)
	}
	resolution := resolver.resolveModule(module)
	dependency, resolved := resolution.dependency, resolution.resolved
	if resolution.limitHit {
		return importBinding{}, "", false, false, true
	}
	if dependency == "" {
		return importBinding{}, "", false, resolved, false
	}

	binding := newImportBinding(filePath, usePartLocalLine(part, line, local), dependency, module, local, lastNamespaceSegment(module), false)
	return binding, normalizeDependencyID(dependency), true, false, false
}

func expandNamespaceUseAlias(module string, namespaceUses map[string]string) (string, bool) {
	first, rest, hasSeparator := strings.Cut(module, `\`)
	target, ok := namespaceUses[strings.ToLower(first)]
	if !ok {
		return module, false
	}
	if !hasSeparator {
		return target, true
	}
	return normalizeNamespace(target + `\` + rest), true
}

func splitNamespaceRelativeModule(module string) (bool, string) {
	const namespacePrefix = `namespace\`
	if len(module) <= len(namespacePrefix) || !strings.EqualFold(module[:len(namespacePrefix)], namespacePrefix) {
		return false, module
	}
	return true, module[len(namespacePrefix):]
}

func parseUsePart(part, base, filePath string, line int, resolver composerResolver) (importBinding, string, bool, bool, bool) {
	module, local, ok := parseUsePartModuleAndLocal(part, base)
	if !ok {
		return importBinding{}, "", false, false, false
	}
	resolution := resolver.resolveModule(module)
	dependency, resolved := resolution.dependency, resolution.resolved
	if resolution.limitHit {
		return importBinding{}, "", false, false, true
	}
	if dependency == "" {
		return importBinding{}, "", false, resolved, false
	}

	binding := newImportBinding(filePath, usePartLocalLine(part, line, local), dependency, module, local, lastNamespaceSegment(module), false)
	return binding, normalizeDependencyID(dependency), true, false, false
}

func usePartLocalLine(part string, baseLine int, local string) int {
	if baseLine <= 0 || local == "" {
		return baseLine
	}
	if localOffset := strings.LastIndex(part, local); localOffset >= 0 {
		return baseLine + strings.Count(part[:localOffset], "\n")
	}
	return baseLine
}

func parseUsePartModuleAndLocal(part, base string) (string, string, bool) {
	module, local := splitAlias(stripUseImportQualifier(part))
	if base != "" {
		module = normalizeNamespace(base + `\` + module)
	}
	module = normalizeNamespace(module)
	if module == "" {
		return "", "", false
	}
	if local == "" {
		local = lastNamespaceSegment(module)
	}
	return module, local, true
}

func stripUseImportQualifier(part string) string {
	part = strings.TrimSpace(part)
	if hasUseImportQualifier(part, "function") {
		return strings.TrimSpace(part[len("function"):])
	}
	if hasUseImportQualifier(part, "const") {
		return strings.TrimSpace(part[len("const"):])
	}
	return part
}

func hasNonClassUseImportQualifier(part string) bool {
	return hasUseImportQualifier(part, "function") || hasUseImportQualifier(part, "const")
}

func hasUseImportQualifier(part, qualifier string) bool {
	part = strings.TrimSpace(part)
	return len(part) > len(qualifier) && strings.EqualFold(part[:len(qualifier)], qualifier) && unicode.IsSpace(rune(part[len(qualifier)]))
}

func splitAlias(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	parts := regexp.MustCompile(`(?i)\s+as\s+`).Split(value, 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return value, ""
}

func lastNamespaceSegment(module string) string {
	module = normalizeNamespace(module)
	if module == "" {
		return ""
	}
	parts := strings.Split(module, `\`)
	return strings.TrimSpace(parts[len(parts)-1])
}

func hasDynamicPatterns(content []byte) bool {
	return dynamicPattern.Match(content)
}
