package swift

import "strings"

func applyUnqualifiedUsageHeuristic(content []byte, imports []importBinding, usage map[string]int) map[string]int {
	return applyUnqualifiedUsageCandidates(imports, usage, collectPotentialUnqualifiedSymbols(content, imports), nil)
}

func applyUnqualifiedUsageCandidates(imports []importBinding, usage map[string]int, candidates []string, declaredSymbols map[string]struct{}) map[string]int {
	if len(imports) == 0 {
		return usage
	}

	byDependency := importsByDependency(imports)
	// Unqualified symbol usage cannot be reliably attributed when a file imports
	// multiple third-party dependencies.
	if len(byDependency) != 1 {
		return usage
	}

	for _, importsForDependency := range byDependency {
		if hasQualifiedImportUsage(importsForDependency, usage) {
			return usage
		}
		for _, candidate := range candidates {
			if _, locallyDeclared := declaredSymbols[candidate]; locallyDeclared {
				continue
			}
			seedUnqualifiedUsage(importsForDependency, usage)
			return usage
		}
	}
	return usage
}

func hasPotentialUnqualifiedSymbolUsage(content []byte, imports []importBinding) bool {
	return len(collectPotentialUnqualifiedSymbols(content, imports)) > 0
}

func collectPotentialUnqualifiedSymbols(content []byte, imports []importBinding) []string {
	return collectPotentialUnqualifiedSymbolsWithDeclarations(content, imports, collectLocalDeclaredSymbols(content))
}

func collectPotentialUnqualifiedSymbolsWithDeclarations(content []byte, imports []importBinding, localDeclaredSymbols map[string]struct{}) []string {
	importModules := importedModuleSet(imports)
	seen := make(map[string]struct{})
	potential := make([]string, 0)
	lines := swiftSymbolScanLines(content)
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || swiftImportPattern.MatchString(line) {
			continue
		}
		genericEnds := swiftGenericSpecializationEnds(line)
		for _, location := range swiftUpperIdentifierPattern.FindAllStringIndex(line, -1) {
			if !hasUnqualifiedUsageEvidenceInLine(line, location[1], genericEnds) {
				continue
			}
			key := lookupKey(line[location[0]:location[1]])
			if isIgnoredUnqualifiedSymbol(key, importModules, localDeclaredSymbols) {
				continue
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			potential = append(potential, key)
		}
	}
	return potential
}

func importsByDependency(imports []importBinding) map[string][]importBinding {
	byDependency := make(map[string][]importBinding)
	for _, imported := range imports {
		dependency := normalizeDependencyID(imported.Dependency)
		if dependency == "" {
			continue
		}
		byDependency[dependency] = append(byDependency[dependency], imported)
	}
	return byDependency
}

func hasQualifiedImportUsage(imports []importBinding, usage map[string]int) bool {
	for _, imported := range imports {
		if imported.Local != "" && usage[imported.Local] > 0 {
			return true
		}
	}
	return false
}

func seedUnqualifiedUsage(imports []importBinding, usage map[string]int) {
	for _, imported := range imports {
		if imported.Local != "" && usage[imported.Local] == 0 {
			usage[imported.Local] = 1
		}
	}
}

func importedModuleSet(imports []importBinding) map[string]struct{} {
	importModules := make(map[string]struct{}, len(imports))
	for _, imported := range imports {
		key := lookupKey(imported.Module)
		if key != "" {
			importModules[key] = struct{}{}
		}
	}
	return importModules
}

func lineHasPotentialUnqualifiedSymbolUsage(line string, importModules map[string]struct{}, localDeclaredSymbols map[string]struct{}) bool {
	locations := swiftUpperIdentifierPattern.FindAllStringIndex(line, -1)
	genericEnds := swiftGenericSpecializationEnds(line)
	for _, location := range locations {
		if !hasUnqualifiedUsageEvidenceInLine(line, location[1], genericEnds) {
			continue
		}
		key := lookupKey(line[location[0]:location[1]])
		if isIgnoredUnqualifiedSymbol(key, importModules, localDeclaredSymbols) {
			continue
		}
		return true
	}
	return false
}

func hasUnqualifiedUsageEvidenceInLine(line string, symbolEnd int, genericEnds map[int]int) bool {
	afterSymbol := line[symbolEnd:]
	trimmed := strings.TrimLeft(afterSymbol, " \t")
	if strings.HasPrefix(trimmed, ".") || strings.HasPrefix(trimmed, "(") {
		return true
	}
	if symbolEnd >= len(line) || line[symbolEnd] != '<' {
		return false
	}
	afterSpecializationStart, ok := genericEnds[symbolEnd]
	if !ok {
		return false
	}
	afterSpecialization := strings.TrimLeft(line[afterSpecializationStart:], " \t")
	return strings.HasPrefix(afterSpecialization, ".") || strings.HasPrefix(afterSpecialization, "(")
}

func swiftGenericSpecializationEnds(line string) map[int]int {
	ends := make(map[int]int)
	stack := make([]int, 0)
	for index, symbol := range line {
		switch symbol {
		case '<':
			stack = append(stack, index)
		case '>':
			if index > 0 && line[index-1] == '-' {
				continue
			}
			if len(stack) == 0 {
				continue
			}
			openIndex := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			ends[openIndex] = index + 1
		}
	}
	return ends
}

func isIgnoredUnqualifiedSymbol(key string, importModules map[string]struct{}, localDeclaredSymbols map[string]struct{}) bool {
	if key == "" {
		return true
	}
	if _, ok := importModules[key]; ok {
		return true
	}
	if _, ok := localDeclaredSymbols[key]; ok {
		return true
	}
	if _, ok := standardSwiftSymbols[key]; ok {
		return true
	}
	return false
}

func collectLocalDeclaredSymbols(content []byte) map[string]struct{} {
	localDeclaredSymbols := make(map[string]struct{})
	lines := swiftSymbolScanLines(content)
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || swiftImportPattern.MatchString(line) {
			continue
		}
		matches := swiftTypeDeclarationPattern.FindAllStringSubmatch(line, -1)
		for _, match := range matches {
			if len(match) != 2 {
				continue
			}
			key := lookupKey(match[1])
			if key == "" {
				continue
			}
			localDeclaredSymbols[key] = struct{}{}
		}
	}
	return localDeclaredSymbols
}

func shouldCollectUnqualifiedUsageCandidates(imports []importBinding, usage map[string]int) bool {
	if len(imports) == 0 {
		return false
	}
	byDependency := importsByDependency(imports)
	if len(byDependency) != 1 {
		return false
	}
	for _, importsForDependency := range byDependency {
		return !hasQualifiedImportUsage(importsForDependency, usage)
	}
	return false
}

func (s *repoScanner) recordUnqualifiedUsageContext(file fileScan, content []byte) {
	if s.declaredSymbolsByScope == nil {
		s.declaredSymbolsByScope = make(map[string]map[string]struct{})
	}
	scope := swiftDeclarationScope(file.Path)
	declared := s.declaredSymbolsByScope[scope]
	if declared == nil {
		declared = make(map[string]struct{})
		s.declaredSymbolsByScope[scope] = declared
	}
	localDeclaredSymbols := collectLocalDeclaredSymbols(content)
	for symbol := range localDeclaredSymbols {
		declared[symbol] = struct{}{}
	}
	candidates := []string(nil)
	if shouldCollectUnqualifiedUsageCandidates(file.Imports, file.Usage) {
		candidates = collectPotentialUnqualifiedSymbolsWithDeclarations(content, file.Imports, localDeclaredSymbols)
	}
	s.unqualifiedUsageContexts = append(s.unqualifiedUsageContexts, unqualifiedUsageContext{
		scope:      scope,
		candidates: candidates,
	})
}

func (s *repoScanner) applyUnqualifiedUsageHeuristics() {
	for i := range s.scan.Files {
		if i >= len(s.unqualifiedUsageContexts) {
			return
		}
		file := &s.scan.Files[i]
		context := s.unqualifiedUsageContexts[i]
		file.Usage = applyUnqualifiedUsageCandidates(file.Imports, file.Usage, context.candidates, s.declaredSymbolsByScope[context.scope])
	}
}

func swiftDeclarationScope(filePath string) string {
	parts := strings.Split(strings.ReplaceAll(filePath, "\\", "/"), "/")
	if len(parts) >= 2 && (parts[0] == "Sources" || parts[0] == "Tests" || parts[0] == "Plugins") {
		return strings.Join(parts[:2], "/")
	}
	if len(parts) <= 1 {
		return "."
	}
	return strings.Join(parts[:len(parts)-1], "/")
}
