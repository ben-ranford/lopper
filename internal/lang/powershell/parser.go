package powershell

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

type assignmentExpression struct {
	Line int
	Expr string
}

type powerShellExpressionScanner struct {
	depthRound  int
	depthSquare int
	depthBrace  int
	inSingle    bool
	inDouble    bool
	escaped     bool
}

func (s *powerShellExpressionScanner) advance(ch byte) bool {
	if s.escaped {
		s.escaped = false
		return true
	}
	if ch == '`' {
		s.escaped = true
		return true
	}
	if s.inSingle {
		if ch == '\'' {
			s.inSingle = false
		}
		return true
	}
	if s.inDouble {
		if ch == '"' {
			s.inDouble = false
		}
		return true
	}
	switch ch {
	case '\'':
		s.inSingle = true
		return true
	case '"':
		s.inDouble = true
		return true
	case '(':
		s.depthRound++
		return true
	case ')':
		if s.depthRound > 0 {
			s.depthRound--
		}
		return true
	case '[':
		s.depthSquare++
		return true
	case ']':
		if s.depthSquare > 0 {
			s.depthSquare--
		}
		return true
	case '{':
		s.depthBrace++
		return true
	case '}':
		if s.depthBrace > 0 {
			s.depthBrace--
		}
		return true
	default:
		return false
	}
}

func (s *powerShellExpressionScanner) complete() bool {
	return !s.inSingle &&
		!s.inDouble &&
		s.depthRound == 0 &&
		s.depthSquare == 0 &&
		s.depthBrace == 0
}

func parseRequiredModules(content []byte, manifestPath string) ([]string, []string) {
	text := string(content)
	assignments, warnings := extractRequiredModulesAssignments(text, manifestPath)
	if len(assignments) == 0 {
		return nil, warnings
	}

	moduleSet := make(map[string]struct{})
	for _, assignment := range assignments {
		modules, parseWarnings := parseModuleExpression(assignment.Expr)
		for _, parseWarning := range parseWarnings {
			warnings = append(warnings, fmt.Sprintf("%s:%d %s", manifestPath, assignment.Line, parseWarning))
		}
		for _, module := range modules {
			normalized := normalizeDependencyID(module)
			if normalized == "" {
				continue
			}
			moduleSet[normalized] = struct{}{}
		}
	}

	declared := make([]string, 0, len(moduleSet))
	for dependency := range moduleSet {
		declared = append(declared, dependency)
	}
	sort.Strings(declared)
	return declared, warnings
}

func extractRequiredModulesAssignments(content string, manifestPath string) ([]assignmentExpression, []string) {
	lines := strings.Split(content, "\n")
	assignments := make([]assignmentExpression, 0)
	warnings := make([]string, 0)
	for i := 0; i < len(lines); i++ {
		line := stripPowerShellInlineComment(lines[i])
		idx := requiredModulesAssignmentPattern.FindStringIndex(line)
		if len(idx) == 0 {
			continue
		}

		exprStart := strings.TrimSpace(line[idx[1]:])
		expr, consumed, complete := collectAssignmentExpression(lines, i, exprStart)
		i += consumed
		if !complete {
			warnings = append(warnings, fmt.Sprintf("%s:%d could not fully parse RequiredModules expression", manifestPath, i+1-consumed))
		}
		if strings.TrimSpace(expr) == "" {
			warnings = append(warnings, fmt.Sprintf("%s:%d RequiredModules expression was empty", manifestPath, i+1-consumed))
			continue
		}
		assignments = append(assignments, assignmentExpression{Line: i + 1 - consumed, Expr: expr})
	}
	return assignments, warnings
}

func collectAssignmentExpression(lines []string, startLine int, initialExpr string) (string, int, bool) {
	lineIndex := startLine
	expr := strings.TrimSpace(initialExpr)
	if expr == "" {
		for lineIndex+1 < len(lines) {
			lineIndex++
			next := strings.TrimSpace(stripPowerShellInlineComment(lines[lineIndex]))
			if next == "" {
				continue
			}
			expr = next
			break
		}
	}
	if expr == "" {
		return "", lineIndex - startLine, false
	}
	if expressionComplete(expr) {
		return expr, lineIndex - startLine, true
	}

	builder := strings.Builder{}
	builder.WriteString(expr)
	for lineIndex+1 < len(lines) {
		lineIndex++
		segment := strings.TrimSpace(stripPowerShellInlineComment(lines[lineIndex]))
		if segment == "" {
			continue
		}
		builder.WriteByte('\n')
		builder.WriteString(segment)
		candidate := builder.String()
		if expressionComplete(candidate) {
			return candidate, lineIndex - startLine, true
		}
	}
	return builder.String(), lineIndex - startLine, false
}

func expressionComplete(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false
	}
	if strings.HasSuffix(expr, ",") {
		return false
	}

	scanner := powerShellExpressionScanner{}
	for i := 0; i < len(expr); i++ {
		scanner.advance(expr[i])
	}
	return scanner.complete()
}

func parseModuleExpression(expr string) ([]string, []string) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}
	if inner, ok := unwrapArrayExpression(expr); ok {
		expr = inner
	}

	items := splitTopLevel(expr, ',')
	modules := make([]string, 0)
	warnings := make([]string, 0)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parsed, dynamic, warning := parseModuleExpressionItem(item)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		if dynamic {
			warnings = append(warnings, fmt.Sprintf("dynamic module reference %q was ignored", item))
			continue
		}
		if parsed == "" {
			continue
		}
		modules = append(modules, parsed)
	}
	return modules, warnings
}

func parseModuleExpressionItem(item string) (string, bool, string) {
	item = strings.TrimSpace(item)
	if item == "" {
		return "", false, ""
	}
	if inner, ok := unwrapArrayExpression(item); ok {
		modules, warnings := parseModuleExpression(inner)
		if len(warnings) > 0 {
			return "", false, strings.Join(warnings, "; ")
		}
		if len(modules) == 1 {
			return modules[0], false, ""
		}
		if len(modules) > 1 {
			return "", false, "nested module list produced multiple values"
		}
		return "", false, ""
	}
	if isHashtableExpression(item) {
		module, dynamic, warning := parseModuleNameFromHashtable(item)
		return module, dynamic, warning
	}
	module, dynamic := parseStaticModuleToken(item)
	return module, dynamic, ""
}

func parseModuleNameFromHashtable(value string) (string, bool, string) {
	match := moduleNamePattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return "", false, "hashtable module reference did not include ModuleName"
	}
	module, dynamic := parseStaticModuleToken(match[1])
	if dynamic {
		return "", true, "hashtable ModuleName used a dynamic expression"
	}
	return module, false, ""
}

func parsePowerShellImports(content []byte, filePath string, declared map[string]struct{}) ([]importBinding, []string) {
	lines := strings.Split(string(content), "\n")
	imports := make([]importBinding, 0)
	warnings := make([]string, 0)

	for index, line := range lines {
		lineNo := index + 1
		parsedImports, lineWarnings := parsePowerShellLine(line, filePath, lineNo, declared)
		imports = append(imports, parsedImports...)
		warnings = append(warnings, lineWarnings...)
	}
	return imports, warnings
}

func parsePowerShellLine(line string, filePath string, lineNo int, declared map[string]struct{}) ([]importBinding, []string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil, nil
	}

	if match := requiresDirectivePattern.FindStringSubmatch(trimmed); len(match) == 2 {
		return parseRequiresLine(match[1], line, filePath, lineNo, declared)
	}

	lineWithoutComment := strings.TrimSpace(stripPowerShellInlineComment(line))
	if lineWithoutComment == "" {
		return nil, nil
	}

	if match := usingModulePattern.FindStringSubmatch(lineWithoutComment); len(match) == 2 {
		dependency, module, dynamic := parseUsingModuleDependency(match[1], declared)
		if dynamic {
			warning := fmt.Sprintf("dynamic using module expression in %s:%d could not be attributed", filePath, lineNo)
			return nil, []string{warning}
		}
		if dependency == "" {
			return nil, nil
		}
		return []importBinding{newImportBinding(dependency, module, filePath, lineNo, line, usageSourceUsingModule)}, nil
	}

	if match := importModulePattern.FindStringSubmatch(lineWithoutComment); len(match) == 2 {
		dependency, module, dynamic := parseImportModuleDependency(match[1], declared)
		if dynamic {
			warning := fmt.Sprintf("dynamic Import-Module expression in %s:%d could not be attributed", filePath, lineNo)
			return nil, []string{warning}
		}
		if dependency == "" {
			return nil, nil
		}
		return []importBinding{newImportBinding(dependency, module, filePath, lineNo, line, usageSourceImportModule)}, nil
	}

	return nil, nil
}

func parseRequiresLine(requiresBody string, line string, filePath string, lineNo int, declared map[string]struct{}) ([]importBinding, []string) {
	requiresBody = stripPowerShellInlineComment(requiresBody)
	match := requiresModulesOptionPattern.FindStringSubmatch(requiresBody)
	if len(match) != 2 {
		return nil, nil
	}
	expr := strings.TrimSpace(match[1])
	if expr == "" {
		warning := fmt.Sprintf("#Requires -Modules in %s:%d had no module list", filePath, lineNo)
		return nil, []string{warning}
	}
	dependencies, warnings := parseRequiresModulesDependencies(expr, declared)
	imports := make([]importBinding, 0, len(dependencies))
	for _, dependency := range dependencies {
		imports = append(imports, newImportBinding(dependency, dependency, filePath, lineNo, line, usageSourceRequiresModule))
	}
	for i, warning := range warnings {
		warnings[i] = fmt.Sprintf("%s:%d %s", filePath, lineNo, warning)
	}
	return imports, warnings
}

func parseImportModuleDependency(value string, declared map[string]struct{}) (string, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}

	candidate := ""
	if token, ok := flagValue(value, "name"); ok {
		candidate = token
	} else {
		tokens := splitArguments(value)
		for _, token := range tokens {
			if strings.HasPrefix(token, "-") {
				continue
			}
			candidate = token
			break
		}
	}
	if candidate == "" {
		return "", "", true
	}

	module, dynamic := parseStaticModuleToken(candidate)
	if dynamic {
		return "", "", true
	}
	if module == "" {
		return "", "", false
	}
	dependency := resolveDependency(module, declared)
	return dependency, module, false
}

func parseUsingModuleDependency(value string, declared map[string]struct{}) (string, string, bool) {
	candidate := strings.TrimSpace(value)
	module, dynamic := parseStaticModuleToken(candidate)
	if dynamic {
		return "", "", true
	}
	if module == "" {
		return "", "", false
	}
	dependency := resolveDependency(module, declared)
	return dependency, module, false
}

func parseRequiresModulesDependencies(expr string, declared map[string]struct{}) ([]string, []string) {
	modules, warnings := parseModuleExpression(expr)
	set := make(map[string]struct{})
	for _, module := range modules {
		set[resolveDependency(module, declared)] = struct{}{}
	}
	dependencies := make([]string, 0, len(set))
	for dependency := range set {
		dependencies = append(dependencies, dependency)
	}
	sort.Strings(dependencies)
	return dependencies, warnings
}

func newImportBinding(dependency string, module string, filePath string, lineNo int, line string, source string) importBinding {
	dependency = normalizeDependencyID(dependency)
	if module == "" {
		module = dependency
	}
	module = strings.TrimSpace(module)
	location := shared.LocationFromLine(filePath, lineNo-1, line)
	return importBinding{
		Record: shared.ImportRecord{
			Dependency: dependency,
			Module:     module,
			Name:       dependency,
			Local:      dependency,
			Location:   location,
			Wildcard:   true,
		},
		Source: source,
	}
}

func normalizeDependencyID(value string) string {
	value = strings.TrimSpace(value)
	return shared.NormalizeDependencyID(value)
}

func resolveDependency(module string, declared map[string]struct{}) string {
	module = normalizeDependencyID(module)
	if module == "" {
		return ""
	}
	if _, ok := declared[module]; ok {
		return module
	}
	if slash := strings.IndexAny(module, "\\/"); slash > 0 {
		root := module[:slash]
		if _, ok := declared[root]; ok {
			return root
		}
	}
	return module
}

func parseStaticModuleToken(token string) (string, bool) {
	token = strings.TrimSpace(strings.TrimRight(token, ",;"))
	token = strings.TrimSpace(trimOuterParentheses(token))
	if token == "" {
		return "", false
	}
	if isHashtableExpression(token) {
		module, dynamic, _ := parseModuleNameFromHashtable(token)
		return module, dynamic
	}
	if isDynamicToken(token) {
		return "", true
	}
	if isQuoted(token, '\'') {
		value := strings.Trim(token, "'")
		if isLocalModulePath(value) {
			return "", false
		}
		return value, false
	}
	if isQuoted(token, '"') {
		value := strings.Trim(token, "\"")
		if strings.Contains(value, "$") {
			return "", true
		}
		if isLocalModulePath(value) {
			return "", false
		}
		return value, false
	}
	if isLocalModulePath(token) {
		return "", false
	}
	return token, false
}

func isDynamicToken(token string) bool {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "$") {
		return true
	}
	if strings.Contains(trimmed, "$(") {
		return true
	}
	if strings.HasPrefix(trimmed, "@(") || strings.HasPrefix(trimmed, "[") {
		return true
	}
	if strings.HasPrefix(trimmed, "(") {
		return true
	}
	return false
}

func isLocalModulePath(value string) bool {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "./") || strings.HasPrefix(lower, ".\\") {
		return true
	}
	if strings.HasPrefix(lower, "../") || strings.HasPrefix(lower, "..\\") {
		return true
	}
	if strings.HasPrefix(lower, "/") || strings.HasPrefix(lower, "\\") {
		return true
	}
	if strings.Contains(lower, ":\\") {
		return true
	}
	switch strings.ToLower(filepath.Ext(lower)) {
	case moduleManifestExt, moduleScriptExt, scriptExt:
		return true
	default:
		return false
	}
}

func isQuoted(value string, quote byte) bool {
	value = strings.TrimSpace(value)
	return len(value) >= 2 && value[0] == quote && value[len(value)-1] == quote
}

func isHashtableExpression(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "@{") && strings.HasSuffix(value, "}")
}

func unwrapArrayExpression(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "@(") && strings.HasSuffix(value, ")") {
		inner := strings.TrimSpace(value[2 : len(value)-1])
		return inner, true
	}
	return "", false
}

func trimOuterParentheses(value string) string {
	value = strings.TrimSpace(value)
	for strings.HasPrefix(value, "(") && strings.HasSuffix(value, ")") {
		inner := strings.TrimSpace(value[1 : len(value)-1])
		if inner == "" {
			break
		}
		if !expressionComplete(inner) {
			break
		}
		value = inner
	}
	return value
}

func splitArguments(value string) []string {
	return splitTopLevel(strings.TrimSpace(value), ' ')
}

func splitTopLevel(value string, separator byte) []string {
	items := make([]string, 0)
	if strings.TrimSpace(value) == "" {
		return items
	}

	start := 0
	scanner := powerShellExpressionScanner{}

	appendSegment := func(end int) {
		segment := strings.TrimSpace(value[start:end])
		if segment != "" {
			items = append(items, segment)
		}
	}

	for i := 0; i < len(value); i++ {
		ch := value[i]
		if scanner.advance(ch) {
			continue
		}
		if ch == separator && scanner.complete() {
			appendSegment(i)
			if separator == ' ' {
				for i+1 < len(value) && value[i+1] == ' ' {
					i++
				}
			}
			start = i + 1
		}
	}
	appendSegment(len(value))
	return items
}

func flagValue(value string, flag string) (string, bool) {
	tokens := splitArguments(value)
	for i := 0; i < len(tokens); i++ {
		token := strings.TrimSpace(tokens[i])
		if strings.EqualFold(token, "-"+flag) {
			if i+1 >= len(tokens) {
				return "", false
			}
			return tokens[i+1], true
		}
		prefix := "-" + flag + ":"
		if strings.HasPrefix(strings.ToLower(token), strings.ToLower(prefix)) {
			return strings.TrimSpace(token[len(prefix):]), true
		}
	}
	return "", false
}

func stripPowerShellInlineComment(line string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '`' {
			escaped = true
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			if ch == '"' {
				inDouble = false
			}
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '#':
			return line[:i]
		}
	}
	return line
}
