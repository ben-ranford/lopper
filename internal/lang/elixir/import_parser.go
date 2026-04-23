package elixir

import (
	"bytes"
	"strings"
	"unicode"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report"
)

func parseImports(content []byte, filePath string, declared map[string]struct{}) []shared.ImportRecord {
	return parseImportsFromSanitized(content, sanitizeElixirSource(content), filePath, declared)
}

func parseImportsFromSanitized(content []byte, sanitized []byte, filePath string, declared map[string]struct{}) []shared.ImportRecord {
	if len(sanitized) != len(content) {
		return nil
	}
	matches := importPattern.FindAllSubmatchIndex(sanitized, -1)
	records := make([]shared.ImportRecord, 0, len(matches))
	for _, idx := range matches {
		keywordStart := idx[2]
		keyword := strings.TrimSpace(string(content[idx[2]:idx[3]]))
		module := strings.TrimSpace(string(content[idx[4]:idx[5]]))
		dependency := dependencyFromModule(module, declared)
		if dependency == "" {
			continue
		}
		line := 1 + strings.Count(string(content[:keywordStart]), "\n")
		local := module
		if parts := strings.Split(module, "."); len(parts) > 0 {
			local = parts[len(parts)-1]
		}
		if keyword == "alias" {
			if aliasLocal := parseAliasLocal(lineBytes(content, keywordStart)); aliasLocal != "" {
				local = aliasLocal
			}
		}
		records = append(records, shared.ImportRecord{
			Dependency: dependency,
			Module:     module,
			Name:       module,
			Local:      local,
			Location:   report.Location{File: filePath, Line: line, Column: 1},
		})
	}
	return records
}

func lineBytes(content []byte, start int) []byte {
	if start < 0 || start >= len(content) {
		return nil
	}
	line := content[start:]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		return line[:i]
	}
	return line
}

func parseAliasLocal(line []byte) string {
	matches := aliasAsPattern.FindSubmatch(line)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(string(matches[1]))
}

func dependencyFromModule(module string, declared map[string]struct{}) string {
	moduleParts := strings.Split(module, ".")
	root := strings.TrimSpace(moduleParts[0])
	rootDependency := normalizeDependencyID(camelToSnake(root))
	if rootDependency == "" {
		return ""
	}
	if _, ok := declared[rootDependency]; ok {
		return rootDependency
	}
	if len(moduleParts) > 1 {
		secondSegmentDependency := normalizeDependencyID(camelToSnake(root) + "-" + camelToSnake(moduleParts[1]))
		if _, ok := declared[secondSegmentDependency]; ok {
			return secondSegmentDependency
		}
	}
	return uniqueDeclaredDependencyWithPrefix(rootDependency, declared)
}

func uniqueDeclaredDependencyWithPrefix(rootDependency string, declared map[string]struct{}) string {
	prefix := rootDependency + "-"
	match := ""
	for dependency := range declared {
		if !strings.HasPrefix(dependency, prefix) {
			continue
		}
		if match != "" {
			return ""
		}
		match = dependency
	}
	return match
}

func normalizeDependencyID(value string) string {
	return strings.ReplaceAll(shared.NormalizeDependencyID(value), "_", "-")
}

func camelToSnake(value string) string {
	var b strings.Builder
	runes := []rune(value)
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]))) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
