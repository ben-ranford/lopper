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
	imports                    []importBinding
	groupedByDep               map[string]int
	unresolvedCount            int
	useStatementLimitHit       bool
	useBindingLimitHit         bool
	namespaceReferenceLimitHit bool
}

type namespaceReferenceParseResult struct {
	imports         []importBinding
	unresolvedCount int
	limitHit        bool
}

type phpLineIndex struct {
	text   string
	starts []int
}

var useStmtPattern = regexp.MustCompile(`(?ms)(?:^\s*|<\?php\s+)use\s+([^;]+);`)
var namespaceRefPattern = regexp.MustCompile(`\\?[A-Za-z_][A-Za-z0-9_]*(?:\\[A-Za-z_][A-Za-z0-9_]*)+`)
var namespaceDeclPattern = regexp.MustCompile(`(?m)(?:^\s*|<\?php\s+)namespace\s+[A-Za-z_][A-Za-z0-9_]*(?:\\[A-Za-z_][A-Za-z0-9_]*)*\s*(?:;|\{)`)
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
		bindings, groupedDeps, unresolvedCount, consumedParts, bindingLimitHit := parseUseStatementWithPartLimit(statement, filePath, line, resolver, remainingUseParts)
		consumedUseParts += consumedParts
		result.imports = append(result.imports, bindings...)
		for dep := range groupedDeps {
			result.groupedByDep[dep]++
		}
		result.unresolvedCount += unresolvedCount
		if bindingLimitHit {
			result.useBindingLimitHit = true
			break
		}
	}

	namespaceResult := parseNamespaceReferencesTextWithLineIndex(text, filePath, resolver, lineIndex)
	result.imports = append(result.imports, namespaceResult.imports...)
	result.unresolvedCount += namespaceResult.unresolvedCount
	result.namespaceReferenceLimitHit = namespaceResult.limitHit
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
		binding, unresolvedInc, ok := parseNamespaceReferenceWithLineIndex(namespaceText, match, filePath, resolver, seen, lineIndex)
		result.unresolvedCount += unresolvedInc
		if !ok {
			continue
		}
		result.imports = append(result.imports, binding)
	}
	return result
}

func parseNamespaceReference(text string, match []int, filePath string, resolver composerResolver, seen map[string]struct{}) (importBinding, int, bool) {
	return parseNamespaceReferenceWithLineIndex(text, match, filePath, resolver, seen, newPHPLineIndex(text))
}

func parseNamespaceReferenceWithLineIndex(text string, match []int, filePath string, resolver composerResolver, seen map[string]struct{}, lineIndex phpLineIndex) (importBinding, int, bool) {
	module, line, local, ok := parseNamespaceReferenceMetadataWithLineIndex(text, match, lineIndex)
	if !ok {
		return importBinding{}, 0, false
	}
	if isUseLineWithLineIndex(lineIndex, line) {
		return importBinding{}, 0, false
	}
	dependency, resolved := resolver.dependencyFromModule(module)
	if dependency == "" {
		if resolved {
			return importBinding{}, 1, false
		}
		return importBinding{}, 0, false
	}
	if isDuplicateNamespaceReference(seen, module, line) {
		return importBinding{}, 0, false
	}
	return namespaceImportBinding(filePath, line, local, module, dependency), 0, true
}

func maskUseStatementRanges(text string) string {
	masked := maskMatchedRanges(text, useStmtPattern.FindAllStringIndex(text, -1), namespaceDeclPattern.FindAllStringIndex(text, -1))
	if masked == "" {
		return text
	}
	return masked
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
	lineText := lineIndex.lineTextAt(line)
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(lineText)), "use ")
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
	imports, groupedDeps, unresolved, _, _ := parseUseStatementWithPartLimit(statement, filePath, line, resolver, maxPHPUseStatementsPerFile)
	return imports, groupedDeps, unresolved
}

func parseUseStatementWithPartLimit(statement, filePath string, line int, resolver composerResolver, partLimit int) ([]importBinding, map[string]struct{}, int, int, bool) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return nil, nil, 0, 0, false
	}
	if strings.ContainsAny(statement, "{}") {
		if bindings, groupedDeps, unresolved, consumedParts, ok, limitHit := parseGroupedUseStatementWithPartLimit(statement, filePath, line, resolver, partLimit); ok {
			return bindings, groupedDeps, unresolved, consumedParts, limitHit
		}
		return nil, nil, 0, 0, false
	}
	bindings, groupedDeps, unresolved, consumedParts, limitHit := parseFlatUseStatement(statement, filePath, line, resolver, partLimit)
	return bindings, groupedDeps, unresolved, consumedParts, limitHit
}

func parseGroupedUseStatement(statement, filePath string, line int, resolver composerResolver) ([]importBinding, map[string]struct{}, int, bool) {
	imports, groupedDeps, unresolved, _, ok, _ := parseGroupedUseStatementWithPartLimit(statement, filePath, line, resolver, maxPHPUseStatementsPerFile)
	return imports, groupedDeps, unresolved, ok
}

func parseGroupedUseStatementWithPartLimit(statement, filePath string, line int, resolver composerResolver, partLimit int) ([]importBinding, map[string]struct{}, int, int, bool, bool) {
	open := strings.Index(statement, "{")
	closeBrace := strings.LastIndex(statement, "}")
	if open < 0 || closeBrace <= open {
		return nil, nil, 0, 0, false, false
	}
	base := normalizeNamespace(stripUseImportQualifier(statement[:open]))
	inside := statement[open+1 : closeBrace]
	parts, limitHit := splitUseParts(inside, partLimit)
	imports, groupedDeps, unresolved := parseUseParts(parts, base, filePath, line, resolver, true)
	return imports, groupedDeps, unresolved, len(parts), true, limitHit
}

func parseFlatUseStatement(statement, filePath string, line int, resolver composerResolver, partLimit int) ([]importBinding, map[string]struct{}, int, int, bool) {
	parts, limitHit := splitUseParts(statement, partLimit)
	imports, _, unresolved := parseUseParts(parts, "", filePath, line, resolver, false)
	return imports, map[string]struct{}{}, unresolved, len(parts), limitHit
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

func parseUseParts(parts []string, base, filePath string, line int, resolver composerResolver, collectGroupedDeps bool) ([]importBinding, map[string]struct{}, int) {
	imports := make([]importBinding, 0)
	groupedDeps := make(map[string]struct{})
	unresolved := 0
	for _, part := range parts {
		binding, dep, ok, unresolvedImport := parseUsePart(strings.TrimSpace(part), base, filePath, line, resolver)
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
	return imports, groupedDeps, unresolved
}

func parseUsePart(part, base, filePath string, line int, resolver composerResolver) (importBinding, string, bool, bool) {
	module, local, ok := parseUsePartModuleAndLocal(part, base)
	if !ok {
		return importBinding{}, "", false, false
	}
	dependency, resolved := resolver.dependencyFromModule(module)
	if dependency == "" {
		return importBinding{}, "", false, resolved
	}

	binding := newImportBinding(filePath, line, dependency, module, local, lastNamespaceSegment(module), false)
	return binding, normalizeDependencyID(dependency), true, false
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
