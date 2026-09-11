package shared

import "strings"

func FallbackDependency(module string, normalize func(string) string) string {
	parts := strings.Split(module, ".")
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return normalize(parts[0])
	default:
		return normalize(parts[0] + "." + parts[1])
	}
}

func LastModuleSegment(module string) string {
	if module == "" {
		return ""
	}
	parts := strings.Split(module, ".")
	return strings.TrimSpace(parts[len(parts)-1])
}

func NormalizeEscapedModuleSegments(module string) string {
	parts := strings.Split(strings.TrimSpace(module), ".")
	for index, part := range parts {
		parts[index] = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(part), "`"), "`")
	}
	return strings.Join(parts, ".")
}
