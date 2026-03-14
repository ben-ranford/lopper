package dart

import (
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

func isPubspecFile(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == pubspecYAMLName || name == pubspecYMLName
}

func normalizeDependencyID(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	return value
}

func shouldSkipDir(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", ".idea", ".vscode", ".dart_tool", ".artifacts", "build", "dist", "vendor", "node_modules", "pods", ".gradle", "android", "ios", "macos", "linux", "windows":
		return true
	default:
		return false
	}
}

func uniquePaths(values []string) []string {
	return shared.UniqueCleanPaths(values)
}

func dedupeWarnings(warnings []string) []string {
	return shared.UniqueTrimmedStrings(warnings)
}

func dedupeStrings(values []string) []string {
	return shared.UniqueTrimmedStrings(values)
}
