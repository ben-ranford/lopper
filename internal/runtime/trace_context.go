package runtime

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type traceLoadOptions struct {
	repoRoot         string
	resolvedRepoRoot string
}

var resolveTraceRepoRoot = resolvedRuntimeRepoRoot

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
	if filePath, isFileURL := runtimeContextFileURLPath(value); isFileURL {
		if filePath == "" {
			return "", true
		}
		value = filePath
	} else if rejectsRuntimeContextScheme(value) || strings.Contains(value, "://") {
		return "", true
	}
	if !looksLikeFilesystemPath(value) {
		return "", false
	}
	if strings.TrimSpace(opts.repoRoot) == "" {
		return "", true
	}
	if looksLikeWindowsAbsoluteContextPath(value) && filepath.Separator != '\\' {
		return "", true
	}
	repoRoot := opts.resolvedRepoRoot
	if strings.TrimSpace(repoRoot) == "" {
		repoRoot = resolveTraceRepoRoot(opts.repoRoot)
	}
	if strings.TrimSpace(repoRoot) == "" {
		return "", true
	}
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

func runtimeContextFileURLPath(value string) (string, bool) {
	const fileSchemePrefix = "file:"
	if len(value) < len(fileSchemePrefix) || !strings.EqualFold(value[:len(fileSchemePrefix)], fileSchemePrefix) {
		return "", false
	}

	parsed, err := url.Parse(value)
	if err != nil ||
		!strings.EqualFold(parsed.Scheme, "file") ||
		parsed.Opaque != "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery ||
		parsed.Fragment != "" {
		return "", true
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return "", true
	}

	pathValue := parsed.Path
	if pathValue == "" || strings.IndexByte(pathValue, 0) >= 0 {
		return "", true
	}
	if strings.HasPrefix(pathValue, "/") && looksLikeWindowsAbsoluteContextPath(pathValue[1:]) {
		pathValue = pathValue[1:]
	}
	return pathValue, true
}

func looksLikeWindowsAbsoluteContextPath(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') {
		drive := value[0]
		return (drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')
	}
	return hasLeadingWindowsUNCSeparators(value)
}

func normalizeRuntimeContextLabel(value string) string {
	if rejectsRuntimeContextScheme(value) || strings.Contains(value, "://") {
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

func rejectsRuntimeContextScheme(value string) bool {
	scheme, ok := runtimeContextScheme(value)
	if !ok {
		return false
	}
	return !strings.EqualFold(scheme, "node")
}

func runtimeContextScheme(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return "", false
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 {
		return "", false
	}
	if isWindowsAbsoluteDrivePrefix(value) {
		return "", false
	}
	for i := 0; i < colon; i++ {
		r := value[i]
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9' && i > 0) || (i > 0 && (r == '+' || r == '.' || r == '-')) {
			continue
		}
		return "", false
	}
	return value[:colon], true
}

func isWindowsAbsoluteDrivePrefix(value string) bool {
	if len(value) < 3 || value[1] != ':' {
		return false
	}
	drive := value[0]
	if (drive < 'a' || drive > 'z') && (drive < 'A' || drive > 'Z') {
		return false
	}
	return value[2] == '\\' || value[2] == '/'
}

func hasLeadingWindowsUNCSeparators(value string) bool {
	if len(value) < 2 {
		return false
	}
	return isRuntimeContextSlash(value[0]) && isRuntimeContextSlash(value[1])
}

func isRuntimeContextSlash(b byte) bool {
	return b == '\\' || b == '/'
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
	if pathLikeExtension(base) != "" {
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

func runtimeContextRepoRelative(repoRoot, value string) (string, bool) {
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
