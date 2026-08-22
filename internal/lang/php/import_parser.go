package php

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

type importParseResult struct {
	imports                     []importBinding
	groupedByDep                map[string]int
	unresolvedCount             int
	useStatementLimitHit        bool
	useBindingLimitHit          bool
	namespaceReferenceLimitHit  bool
	namespaceResolutionLimitHit bool
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

var useStmtPattern = regexp.MustCompile(`(?ms)(?:^\s*|<\?php\s+)use\s+((?:(?:function|const)\s+)?\\?[A-Za-z_\x{80}-\x{10FFFF}][^;]*);`)
var namespaceRefPattern = regexp.MustCompile(`\\?[A-Za-z_\x{80}-\x{10FFFF}][A-Za-z0-9_\x{80}-\x{10FFFF}]*(?:\\[A-Za-z_\x{80}-\x{10FFFF}][A-Za-z0-9_\x{80}-\x{10FFFF}]*)+`)
var namespaceDeclCandidatePattern = regexp.MustCompile(`\bnamespace\s+[A-Za-z_\x{80}-\x{10FFFF}][A-Za-z0-9_\x{80}-\x{10FFFF}]*(?:\\[A-Za-z_\x{80}-\x{10FFFF}][A-Za-z0-9_\x{80}-\x{10FFFF}]*)*\s*(?:;|\{)`)
var namespaceDeclPrefixPattern = regexp.MustCompile(`^\s*(?:<\?php\b\s*)?(?:declare\s*\([^)]*\)\s*;\s*)*$`)
var dynamicPattern = regexp.MustCompile(`(?m)(new\s+\$[A-Za-z_]|\$[A-Za-z_][A-Za-z0-9_]*\s*::|\b(class_exists|interface_exists|trait_exists|method_exists)\s*\()`) //nolint:lll

func parseImports(content []byte, filePath string, resolver composerResolver) ([]importBinding, map[string]int, int) {
	result := parsePHPImports(content, filePath, resolver)
	return result.imports, result.groupedByDep, result.unresolvedCount
}

func parsePHPImports(content []byte, filePath string, resolver composerResolver) importParseResult {
	sanitized := shared.MaskCommentsAndStringsForFile(content, filePath)
	text := string(sanitized)
	lineIndex := newPHPLineIndex(text)
	matches := useStmtPattern.FindAllStringSubmatchIndex(text, maxPHPUseStatementsPerFile+1)
	result := importParseResult{
		imports:      make([]importBinding, 0),
		groupedByDep: make(map[string]int),
	}
	if len(matches) > maxPHPUseStatementsPerFile {
		result.useStatementLimitHit = true
		matches = matches[:maxPHPUseStatementsPerFile]
	}

	consumedUseParts := 0
	for _, match := range matches {
		remainingUseParts := maxPHPUseStatementsPerFile - consumedUseParts
		if remainingUseParts <= 0 {
			result.useBindingLimitHit = true
			break
		}
		statement := strings.TrimSpace(text[match[2]:match[3]])
		line := lineIndex.lineNumberAt(match[2])
		bindings, groupedDeps, unresolvedCount, consumedParts, bindingLimitHit, resolutionLimitHit := parseUseStatementWithPartLimit(statement, filePath, line, resolver, remainingUseParts)
		if bindingLimitHit {
			result.useBindingLimitHit = true
		}
		if resolutionLimitHit {
			result.namespaceResolutionLimitHit = true
		}
		consumedUseParts += consumedParts
		result.imports = append(result.imports, bindings...)
		for dep := range groupedDeps {
			result.groupedByDep[dep]++
		}
		result.unresolvedCount += unresolvedCount
		if bindingLimitHit || result.namespaceResolutionLimitHit {
			break
		}
	}

	namespaceResult := parseNamespaceReferencesTextWithLineIndex(text, filePath, resolver, lineIndex)
	result.imports = append(result.imports, namespaceResult.imports...)
	result.unresolvedCount += namespaceResult.unresolvedCount
	result.namespaceReferenceLimitHit = namespaceResult.limitHit
	result.namespaceResolutionLimitHit = result.namespaceResolutionLimitHit || namespaceResult.namespaceResolutionLimitHit
	return result
}

func parseNamespaceReferences(content []byte, filePath string, resolver composerResolver) ([]importBinding, int) {
	sanitized := shared.MaskCommentsAndStringsForFile(content, filePath)
	return parseNamespaceReferencesText(string(sanitized), filePath, resolver)
}

func parseNamespaceReferencesText(text string, filePath string, resolver composerResolver) ([]importBinding, int) {
	result := parseNamespaceReferencesTextWithLineIndex(text, filePath, resolver, newPHPLineIndex(text))
	return result.imports, result.unresolvedCount
}

func parseNamespaceReferencesTextWithLineIndex(text string, filePath string, resolver composerResolver, lineIndex phpLineIndex) namespaceReferenceParseResult {
	namespaceText := maskUseStatementRanges(text)
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
	if isUseLineWithLineIndex(lineIndex, line) {
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

func maskUseStatementRanges(text string) string {
	masked := maskMatchedRanges(text, useStmtPattern.FindAllStringIndex(text, -1), findNamespaceDeclarationRanges(text))
	if masked == "" {
		return text
	}
	return masked
}

func findNamespaceDeclarationRanges(text string) [][]int {
	candidates := namespaceDeclCandidatePattern.FindAllStringIndex(text, -1)
	if len(candidates) == 0 {
		return nil
	}
	ranges := make([][]int, 0, len(candidates))
	for _, candidate := range candidates {
		if !isMaskableRange(candidate) || !isNamespaceDeclarationCandidate(text, candidate[0]) {
			continue
		}
		ranges = append(ranges, candidate)
	}
	return ranges
}

func isNamespaceDeclarationCandidate(text string, start int) bool {
	lineStart := strings.LastIndexByte(text[:start], '\n') + 1
	prefix := text[lineStart:start]
	return namespaceDeclPrefixPattern.MatchString(prefix)
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

func isUseLineWithLineIndex(lineIndex phpLineIndex, line int) bool {
	return isImportUseLine(lineIndex.lineTextAt(line))
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

	binding := newImportBinding(filePath, line, dependency, module, local, lastNamespaceSegment(module), false)
	return binding, normalizeDependencyID(dependency), true, false, false
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
	partLower := strings.ToLower(part)
	if strings.HasPrefix(partLower, "function ") {
		return strings.TrimSpace(part[len("function "):])
	}
	if strings.HasPrefix(partLower, "const ") {
		return strings.TrimSpace(part[len("const "):])
	}
	return part
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

func isImportUseLine(lineText string) bool {
	trimmed := strings.TrimSpace(lineText)
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<?php") {
		trimmed = strings.TrimSpace(trimmed[len("<?php"):])
		lower = strings.ToLower(trimmed)
	}
	if !strings.HasPrefix(lower, "use ") {
		return false
	}
	rest := strings.TrimSpace(trimmed[len("use "):])
	restLower := strings.ToLower(rest)
	switch {
	case strings.HasPrefix(restLower, "function "):
		rest = strings.TrimSpace(rest[len("function "):])
	case strings.HasPrefix(restLower, "const "):
		rest = strings.TrimSpace(rest[len("const "):])
	}
	if rest == "" {
		return false
	}
	return rest[0] == '\\' || isPHPIdentifierStart(rest[0])
}

func isPHPIdentifierStart(ch byte) bool {
	return ch == '_' || ch >= 0x80 || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
}
