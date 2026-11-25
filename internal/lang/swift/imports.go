package swift

import (
	"github.com/ben-ranford/lopper/internal/lang/shared"
)

func parseSwiftImports(content []byte, filePath string) []importBinding {
	var imports []importBinding
	for statement := range swiftStatements(content) {
		matches := swiftImportPattern.FindStringSubmatch(statement.text)
		if len(matches) != 2 {
			continue
		}
		module := matches[1]
		imports = append(imports, importBinding{
			Module:   module,
			Name:     module,
			Local:    module,
			Location: shared.Location(filePath, statement.line, statement.offset+shared.FirstContentColumn(statement.text)),
		})
	}
	return imports
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
