package dotnet

import "strings"

func normalizeNamespace(module string) string {
	module = strings.TrimSpace(module)
	module = strings.TrimPrefix(module, "global::")
	return module
}

func resolveImportDependency(module string, mapper dependencyMapper, meta *mappingMetadata) (string, bool) {
	dependency, ambiguous, undeclared := mapper.resolve(module)
	if dependency == "" {
		return "", false
	}
	if ambiguous {
		meta.ambiguousByDependency[dependency]++
	}
	if undeclared {
		meta.undeclaredByDependency[dependency]++
	}
	return dependency, true
}

func lastSegment(value string) string {
	if value == "" {
		return ""
	}
	if index := strings.LastIndexByte(value, '.'); index >= 0 {
		return strings.TrimSpace(value[index+1:])
	}
	return strings.TrimSpace(value)
}

func normalizeDependencyID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

type dependencyMapper struct {
	declared []declaredDependency
}

func newDependencyMapper(declared []string) dependencyMapper {
	items := make([]declaredDependency, 0, len(declared))
	for _, dependency := range declared {
		id := normalizeDependencyID(dependency)
		if id == "" {
			continue
		}
		items = append(items, declaredDependency{
			id:       id,
			segments: splitNamespace(id),
		})
	}
	return dependencyMapper{declared: items}
}

type declaredDependency struct {
	id       string
	segments namespaceSegments
}

type namespaceSegments struct {
	first string
	last  string
}

func (m *dependencyMapper) resolve(module string) (dependency string, ambiguous bool, undeclared bool) {
	module = normalizeNamespace(module)
	if module == "" {
		return "", false, false
	}
	moduleID := normalizeDependencyID(module)
	if moduleID == "system" || strings.HasPrefix(moduleID, "system.") {
		return "", false, false
	}
	moduleSegments := splitNamespace(moduleID)
	bestID := ""
	bestScore := 0
	bestMatches := 0
	for _, dep := range m.declared {
		score := matchScoreWithSegments(moduleID, dep.id, moduleSegments, dep.segments)
		if score == 0 {
			continue
		}
		switch {
		case score > bestScore:
			bestScore = score
			bestID = dep.id
			bestMatches = 1
		case score == bestScore:
			bestMatches++
			if bestID == "" || dep.id < bestID {
				bestID = dep.id
			}
		}
	}
	if bestScore == 0 {
		return fallbackDependencyID(moduleID), false, true
	}
	return bestID, bestMatches > 1, false
}

func matchScore(module, dependency string) int {
	return matchScoreWithSegments(module, dependency, splitNamespace(module), splitNamespace(dependency))
}

func matchScoreWithSegments(module, dependency string, moduleSegments, dependencySegments namespaceSegments) int {
	if module == dependency {
		return 100
	}
	if hasDottedPrefix(module, dependency) {
		return 90
	}
	if hasDottedPrefix(dependency, module) {
		return 75
	}

	if moduleSegments.first != "" && moduleSegments.first == dependencySegments.first {
		return 60
	}
	if moduleSegments.last != "" && moduleSegments.last == dependencySegments.last {
		return 50
	}
	if strings.Contains(module, dependency) || strings.Contains(dependency, module) {
		return 40
	}
	return 0
}

func hasDottedPrefix(value, prefix string) bool {
	return prefix != "" && len(value) > len(prefix) && strings.HasPrefix(value, prefix) && value[len(prefix)] == '.'
}

func fallbackDependency(module string) string {
	return fallbackDependencyID(normalizeDependencyID(module))
}

func fallbackDependencyID(moduleID string) string {
	if moduleID == "" {
		return ""
	}
	firstDot := strings.IndexByte(moduleID, '.')
	if firstDot < 0 {
		return moduleID
	}
	secondDot := strings.IndexByte(moduleID[firstDot+1:], '.')
	if secondDot < 0 {
		return moduleID
	}
	return moduleID[:firstDot+1+secondDot]
}

func firstSegment(value string) string {
	if value == "" {
		return ""
	}
	if index := strings.IndexByte(value, '.'); index >= 0 {
		return value[:index]
	}
	return value
}

func splitNamespace(value string) namespaceSegments {
	return namespaceSegments{
		first: firstSegment(value),
		last:  lastSegment(value),
	}
}
