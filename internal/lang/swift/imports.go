package swift

import (
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

func parseSwiftImports(content []byte, filePath string) []importBinding {
	sanitized := blankSwiftStringsAndComments(content)
	return shared.ParseImportLines([]byte(sanitized), filePath, func(line string, index int) []shared.ImportRecord {
		matches := swiftImportPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			return nil
		}
		moduleName := strings.TrimSpace(matches[1])
		if moduleName == "" {
			return nil
		}
		return []shared.ImportRecord{{
			Module:   moduleName,
			Name:     moduleName,
			Local:    moduleName,
			Wildcard: false,
			Location: shared.LocationFromLine(filePath, index, line),
		}}
	})
}

func (s *repoScanner) resolveImports(imports []importBinding) []importBinding {
	mappedImports := make([]importBinding, 0, len(imports))
	for _, imported := range imports {
		dependency := resolveImportDependency(s.catalog, imported.Module)
		if dependency == "" {
			trackUnresolvedImport(s.unresolvedImports, imported.Module, s.catalog)
			continue
		}
		imported.Dependency = dependency
		if imported.Name == "" {
			imported.Name = imported.Module
		}
		if imported.Local == "" {
			imported.Local = imported.Name
		}
		s.scan.ImportedDependencies[dependency] = struct{}{}
		mappedImports = append(mappedImports, imported)
	}
	return mappedImports
}
