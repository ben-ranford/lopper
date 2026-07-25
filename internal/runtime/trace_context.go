package runtime

import (
	"os"
	"path/filepath"
	"strings"
)

type traceLoadOptions struct {
	repoRoot string
}

func normalizeRuntimeContextValue(value string, opts traceLoadOptions) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if normalized, ok := normalizeRuntimeContextPath(value, opts); ok {
		return normalized
	}
	return normalizeRuntimeContextLabel(value)
}

func normalizeRuntimeContextPath(value string, opts traceLoadOptions) (string, bool) {
	value = strings.TrimPrefix(value, fileURLPrefix)
	if !looksLikeFilesystemPath(value) {
		return "", false
	}
	if strings.TrimSpace(opts.repoRoot) == "" {
		return "", true
	}
	repoRoot := resolvedRuntimeRepoRoot(opts.repoRoot)
	pathValue := filepath.FromSlash(value)
	if !filepath.IsAbs(pathValue) {
		pathValue = filepath.Join(repoRoot, pathValue)
	}
	resolvedPath := resolveRuntimeContextPath(pathValue)
	if resolvedPath == "" {
		return "", true
	}
	rel, ok := runtimeContextRepoRelative(repoRoot, resolvedPath)
	if !ok {
		return "", true
	}
	return rel, true
}

func normalizeRuntimeContextLabel(value string) string {
	if strings.Contains(value, "://") {
		return ""
	}
	if strings.Contains(value, "\\") {
		return ""
	}
	value = filepath.ToSlash(value)
	if strings.HasPrefix(value, "/") {
		return ""
	}
	if strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") {
		return ""
	}
	return value
}

func looksLikeFilesystemPath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") {
		return true
	}
	if strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") {
		return true
	}
	if len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		return true
	}
	value = filepath.ToSlash(strings.TrimSpace(value))
	if !strings.Contains(value, "/") {
		return false
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "." || part == ".." {
			return true
		}
	}
	base := parts[len(parts)-1]
	if base == "" || strings.HasPrefix(base, ".") {
		return true
	}
	if ext := pathLikeExtension(base); ext != "" {
		return true
	}
	return false
}

func pathLikeExtension(base string) string {
	dot := strings.LastIndex(base, ".")
	if dot <= 0 || dot == len(base)-1 {
		return ""
	}
	ext := base[dot+1:]
	for _, r := range ext {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}

func resolveRuntimeContextPath(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(value)
	if err == nil && strings.TrimSpace(resolved) != "" {
		return filepath.Clean(resolved)
	}
	if info, statErr := os.Stat(value); statErr == nil && !info.IsDir() {
		return filepath.Clean(value)
	}
	return filepath.Clean(value)
}

func runtimeContextRepoRelative(repoRoot string, value string) (string, bool) {
	rel, err := filepath.Rel(repoRoot, value)
	if err != nil {
		return "", false
	}
	if rel == "." {
		return ".", true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(filepath.Clean(rel)), true
}
