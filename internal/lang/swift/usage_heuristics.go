package swift

import "strings"

func applyUnqualifiedUsageHeuristic(content []byte, imports []importBinding, usage map[string]int) map[string]int {
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
		if !hasPotentialUnqualifiedSymbolUsage(content, importsForDependency) {
			return usage
		}
		seedUnqualifiedUsage(importsForDependency, usage)
	}
	return usage
}

func hasPotentialUnqualifiedSymbolUsage(content []byte, imports []importBinding) bool {
	importModules := importedModuleSet(imports)
	localDeclaredSymbols := collectLocalDeclaredSymbols(content)
	lines := swiftSymbolScanLines(content)
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || swiftImportPattern.MatchString(line) {
			continue
		}
		if lineHasPotentialUnqualifiedSymbolUsage(line, importModules, localDeclaredSymbols) {
			return true
		}
	}
	return false
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
	symbols := swiftUpperIdentifierPattern.FindAllString(line, -1)
	for _, symbol := range symbols {
		key := lookupKey(symbol)
		if isIgnoredUnqualifiedSymbol(key, importModules, localDeclaredSymbols) {
			continue
		}
		return true
	}
	return false
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
