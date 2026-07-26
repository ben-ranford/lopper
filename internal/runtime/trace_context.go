package runtime

import (
	"net/url"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type traceLoadOptions struct {
	repoRoot         string
	resolvedRepoRoot string
	resolveRepoRoot  func(string) string
	evalSymlinks     func(string) (string, error)
	contextCache     map[string]string
}

func normalizeRuntimeContextValue(value string, opts traceLoadOptions) string {
	rawValue := value
	trimmedValue := strings.TrimSpace(rawValue)
	if trimmedValue == "" {
		return ""
	}
	if normalized, ok := normalizeRuntimeContextValueWithOuterWhitespace(rawValue, trimmedValue, opts); ok {
		if normalized != "" {
			traceLoadContextCacheStore(opts.contextCache, trimmedValue, normalized)
		}
		return normalized
	}
	if cached, ok := traceLoadContextCacheLookup(opts.contextCache, trimmedValue); ok {
		return cached
	}
	normalized := normalizeRuntimeContextValueUncached(trimmedValue, opts)
	traceLoadContextCacheStore(opts.contextCache, trimmedValue, normalized)
	return normalized
}

func normalizeRuntimeContextValueUncached(value string, opts traceLoadOptions) string {
	if normalized, ok := normalizeRuntimeContextPath(value, opts); ok {
		if normalized != "" || runtimeContextShouldStayRedacted(value, opts) || !looksLikePackageStyleRuntimeContextLabel(value) {
			return normalized
		}
	}
	return normalizeRuntimeContextLabel(value)
}

func normalizeRuntimeContextValueWithOuterWhitespace(rawValue, trimmedValue string, opts traceLoadOptions) (string, bool) {
	if rawValue == trimmedValue || !runtimeContextHasOuterWhitespaceOrControl(rawValue) {
		return "", false
	}
	if normalized, ok := normalizeRuntimeContextPath(trimmedValue, opts); ok && normalized != "" {
		return normalized, true
	}
	if looksLikePackageStyleRuntimeContextLabel(trimmedValue) {
		return "", true
	}
	return "", false
}

func runtimeContextHasOuterWhitespaceOrControl(value string) bool {
	if value == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(value)
	last, _ := utf8.DecodeLastRuneInString(value)
	return runtimeContextIsOuterWhitespaceOrControl(first) || runtimeContextIsOuterWhitespaceOrControl(last)
}

func runtimeContextIsOuterWhitespaceOrControl(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}

func runtimeContextShouldStayRedacted(value string, opts traceLoadOptions) bool {
	value, ok := runtimeContextFilesystemCandidate(value)
	if !ok || value == "" {
		return false
	}
	if runtimeContextHasDefiniteFilesystemSignal(value) {
		return true
	}
	repoRoot, resolvedRepoRoot, ok := runtimeContextTrustedRepoRoots(opts)
	if !ok {
		return false
	}
	pathValue, ok := runtimeContextTrustedCandidatePath(value, repoRoot, resolvedRepoRoot)
	if !ok {
		return true
	}
	return resolveRuntimeContextPath(pathValue, opts.evalSymlinksFunc()) != ""
}

func normalizeRuntimeContextPath(value string, opts traceLoadOptions) (string, bool) {
	value, ok := runtimeContextFilesystemCandidate(value)
	if !ok {
		return "", false
	}
	if value == "" {
		return "", true
	}
	repoRoot, resolvedRepoRoot, ok := runtimeContextTrustedRepoRoots(opts)
	if !ok || shouldRedactRuntimeContextPath(value) {
		return "", true
	}
	pathValue, ok := runtimeContextTrustedCandidatePath(value, repoRoot, resolvedRepoRoot)
	if !ok {
		return "", true
	}
	resolvedPath := resolveRuntimeContextPath(pathValue, opts.evalSymlinksFunc())
	if resolvedPath == "" {
		return "", true
	}
	return runtimeContextTrustedRelativePath(resolvedPath, resolvedRepoRoot, opts.repoRoot), true
}

func runtimeContextFilesystemCandidate(value string) (string, bool) {
	if filePath, isFileURL := runtimeContextFileURLPath(value); isFileURL {
		return filePath, true
	}
	if rejectsRuntimeContextScheme(value) || strings.Contains(value, "://") {
		return "", true
	}
	if !looksLikeFilesystemPath(value) {
		return "", false
	}
	return value, true
}

func runtimeContextTrustedRepoRoots(opts traceLoadOptions) (string, string, bool) {
	lexicalRepoRoot := strings.TrimSpace(opts.repoRoot)
	if lexicalRepoRoot == "" {
		return "", "", false
	}
	resolvedRepoRoot := strings.TrimSpace(opts.resolvedRepoRoot)
	if resolvedRepoRoot == "" {
		resolvedRepoRoot = opts.resolveRepoRootFunc()(lexicalRepoRoot)
	}
	if strings.TrimSpace(resolvedRepoRoot) == "" {
		return "", "", false
	}
	return lexicalRepoRoot, resolvedRepoRoot, true
}

func shouldRedactRuntimeContextPath(value string) bool {
	return looksLikeWindowsAbsoluteContextPath(value) && filepath.Separator != '\\'
}

func runtimeContextTrustedCandidatePath(value, lexicalRepoRoot, resolvedRepoRoot string) (string, bool) {
	pathValue := filepath.Clean(filepath.FromSlash(value))
	if filepath.IsAbs(pathValue) {
		return pathValue, runtimeContextWithinTrustedRoots(pathValue, lexicalRepoRoot, resolvedRepoRoot)
	}
	pathValue = filepath.Join(resolvedRepoRoot, pathValue)
	return pathValue, runtimeContextLexicallyWithinRepo(resolvedRepoRoot, pathValue)
}

func runtimeContextTrustedRelativePath(resolvedPath string, trustedRoots ...string) string {
	for _, trustedRoot := range trustedRoots {
		rel, ok := runtimeContextRepoRelative(trustedRoot, resolvedPath)
		if ok {
			return rel
		}
	}
	return ""
}

func runtimeContextFileURLPath(value string) (string, bool) {
	const fileSchemePrefix = "file:"
	if len(value) < len(fileSchemePrefix) || !strings.EqualFold(value[:len(fileSchemePrefix)], fileSchemePrefix) {
		return "", false
	}

	parsed, err := url.Parse(value)
	if err != nil || !isTrustedRuntimeContextFileURL(parsed) {
		return "", true
	}
	pathValue, ok := normalizedRuntimeContextFileURLPath(parsed.Path)
	if !ok {
		return "", true
	}
	return pathValue, true
}

func isTrustedRuntimeContextFileURL(parsed *url.URL) bool {
	return strings.EqualFold(parsed.Scheme, "file") &&
		parsed.Opaque == "" &&
		parsed.User == nil &&
		parsed.RawQuery == "" &&
		!parsed.ForceQuery &&
		parsed.Fragment == "" &&
		(parsed.Host == "" || strings.EqualFold(parsed.Host, "localhost"))
}

func normalizedRuntimeContextFileURLPath(pathValue string) (string, bool) {
	if pathValue == "" || strings.IndexByte(pathValue, 0) >= 0 {
		return "", false
	}
	if strings.HasPrefix(pathValue, "/") && looksLikeWindowsAbsoluteContextPath(pathValue[1:]) {
		return pathValue[1:], true
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
	if runtimeLabelContainsWhitespaceOrControl(value) {
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
	if strings.Contains(value, "/") && !looksLikePackageStyleRuntimeContextLabel(value) {
		return ""
	}
	return value
}

func looksLikePackageStyleRuntimeContextLabel(value string) bool {
	return looksLikeSafeNodeBuiltinContextLabel(value) || len(safeRuntimePackageSegments(value, false, 2)) != 0
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
	if colon <= 0 || isWindowsAbsoluteDrivePrefix(value) {
		return "", false
	}
	if !isValidRuntimeContextScheme(value[:colon]) {
		return "", false
	}
	return value[:colon], true
}

func isValidRuntimeContextScheme(value string) bool {
	for i := 0; i < len(value); i++ {
		if !isValidRuntimeContextSchemeByte(value[i], i) {
			return false
		}
	}
	return true
}

func isValidRuntimeContextSchemeByte(value byte, index int) bool {
	if value >= 'a' && value <= 'z' {
		return true
	}
	if value >= 'A' && value <= 'Z' {
		return true
	}
	if index > 0 && value >= '0' && value <= '9' {
		return true
	}
	return index > 0 && (value == '+' || value == '.' || value == '-')
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
	if hasFilesystemPathPrefix(value) {
		return true
	}
	value = filepath.ToSlash(strings.TrimSpace(value))
	if !strings.Contains(value, "/") {
		return false
	}
	parts := strings.Split(value, "/")
	if runtimePathPartsContainTraversal(parts) {
		return true
	}
	base := parts[len(parts)-1]
	return looksLikePathStyleBase(base)
}

func runtimeContextHasDefiniteFilesystemSignal(value string) bool {
	if filePath, isFileURL := runtimeContextFileURLPath(value); isFileURL {
		return filePath != ""
	}
	if rejectsRuntimeContextScheme(value) || strings.Contains(value, "://") {
		return true
	}
	if hasFilesystemPathPrefix(value) {
		return true
	}
	value = filepath.ToSlash(strings.TrimSpace(value))
	if !strings.Contains(value, "/") {
		return false
	}
	parts := strings.Split(value, "/")
	if runtimePathPartsContainTraversal(parts) {
		return true
	}
	base := parts[len(parts)-1]
	return base == "" || strings.HasPrefix(base, ".")
}

func hasFilesystemPathPrefix(value string) bool {
	return strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "\\") ||
		strings.HasPrefix(value, "./") ||
		strings.HasPrefix(value, "../") ||
		isWindowsAbsoluteDrivePrefix(value)
}

func runtimePathPartsContainTraversal(parts []string) bool {
	for _, part := range parts {
		if part == "." || part == ".." {
			return true
		}
	}
	return false
}

func looksLikePathStyleBase(base string) bool {
	return base == "" || strings.HasPrefix(base, ".") || pathLikeExtension(base) != ""
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

func (o *traceLoadOptions) resolveRepoRootFunc() func(string) string {
	if o != nil && o.resolveRepoRoot != nil {
		return o.resolveRepoRoot
	}
	return resolvedRuntimeRepoRoot
}

func (o *traceLoadOptions) evalSymlinksFunc() func(string) (string, error) {
	if o != nil && o.evalSymlinks != nil {
		return o.evalSymlinks
	}
	return filepath.EvalSymlinks
}

func traceLoadContextCacheLookup(cache map[string]string, value string) (string, bool) {
	if cache == nil {
		return "", false
	}
	normalized, ok := cache[value]
	return normalized, ok
}

func traceLoadContextCacheStore(cache map[string]string, value string, normalized string) {
	if cache == nil {
		return
	}
	cache[value] = normalized
}

func resolveRuntimeContextPath(value string, evalSymlinks func(string) (string, error)) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if evalSymlinks == nil {
		evalSymlinks = filepath.EvalSymlinks
	}
	resolved, err := evalSymlinks(value)
	if err == nil && strings.TrimSpace(resolved) != "" {
		return filepath.Clean(resolved)
	}
	return ""
}

func runtimeContextWithinTrustedRoots(pathValue string, roots ...string) bool {
	for _, root := range roots {
		if runtimeContextLexicallyWithinRepo(root, pathValue) {
			return true
		}
	}
	return false
}

func runtimeContextLexicallyWithinRepo(repoRoot, value string) bool {
	repoRoot = strings.TrimSpace(repoRoot)
	value = strings.TrimSpace(value)
	if repoRoot == "" || value == "" {
		return false
	}
	rel, err := filepath.Rel(repoRoot, value)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
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
	rel = filepath.ToSlash(filepath.Clean(rel))
	if runtimeContextHasControlWhitespace(rel) {
		return "", false
	}
	return rel, true
}

func runtimeContextHasControlWhitespace(value string) bool {
	return strings.ContainsAny(value, "\n\r\t")
}
