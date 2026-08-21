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

	for _, match := range matches {
		statement := strings.TrimSpace(text[match[2]:match[3]])
		line := lineIndex.lineNumberAt(match[2])
		bindings, groupedDeps, unresolvedCount := parseUseStatement(statement, filePath, line, resolver)
		result.imports = append(result.imports, bindings...)
		for dep := range groupedDeps {
			result.groupedByDep[dep]++
		}
		result.unresolvedCount += unresolvedCount
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
	matches := namespaceRefPattern.FindAllStringIndex(text, maxPHPNamespaceReferencesPerFile+1)
	result := namespaceReferenceParseResult{}
	if len(matches) > maxPHPNamespaceReferencesPerFile {
		result.limitHit = true
		matches = matches[:maxPHPNamespaceReferencesPerFile]
	}
	result.imports = make([]importBinding, 0, len(matches))
	seen := make(map[string]struct{})
	for _, match := range matches {
		binding, unresolvedInc, ok := parseNamespaceReferenceWithLineIndex(text, match, filePath, resolver, seen, lineIndex)
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
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return nil, nil, 0
	}
	if bindings, groupedDeps, unresolved, ok := parseGroupedUseStatement(statement, filePath, line, resolver); ok {
		return bindings, groupedDeps, unresolved
	}
	return parseFlatUseStatement(statement, filePath, line, resolver)
}

func parseGroupedUseStatement(statement, filePath string, line int, resolver composerResolver) ([]importBinding, map[string]struct{}, int, bool) {
	open := strings.Index(statement, "{")
	closeBrace := strings.LastIndex(statement, "}")
	if open < 0 || closeBrace <= open {
		return nil, nil, 0, false
	}
	base := normalizeNamespace(stripUseImportQualifier(statement[:open]))
	inside := statement[open+1 : closeBrace]
	imports, groupedDeps, unresolved := parseUseParts(strings.Split(inside, ","), base, filePath, line, resolver, true)
	return imports, groupedDeps, unresolved, true
}

func parseFlatUseStatement(statement, filePath string, line int, resolver composerResolver) ([]importBinding, map[string]struct{}, int) {
	imports, _, unresolved := parseUseParts(strings.Split(statement, ","), "", filePath, line, resolver, false)
	return imports, map[string]struct{}{}, unresolved
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
