package js

import (
	"path/filepath"
	"strings"
)

func (r *reExportResolver) resolveLocalModule(importerPath, module string) (string, bool) {
	key := normalizeModulePath(importerPath) + "\x00" + module
	if value, ok := r.resolveCache[key]; ok {
		if value == "" {
			return "", false
		}
		return value, true
	}

	base := filepath.Dir(normalizeModulePath(importerPath))
	target := normalizeModulePath(filepath.Join(base, module))
	for _, candidate := range localModuleCandidates(target) {
		if _, ok := r.filesByPath[candidate]; ok {
			r.resolveCache[key] = candidate
			return candidate, true
		}
	}

	r.resolveCache[key] = ""
	return "", false
}

func localModuleCandidates(path string) []string {
	normalized := normalizeModulePath(path)
	candidates := make([]string, 0, 24)
	candidates = append(candidates, normalized)
	base := strings.TrimSuffix(normalized, filepath.Ext(normalized))
	if base != normalized {
		candidates = append(candidates, base)
	}

	extensions := []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}
	for _, ext := range extensions {
		candidates = append(candidates, base+ext)
		candidates = append(candidates, filepath.Join(base, "index"+ext))
	}

	unique := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		normalizedCandidate := normalizeModulePath(candidate)
		if _, ok := seen[normalizedCandidate]; ok {
			continue
		}
		seen[normalizedCandidate] = struct{}{}
		unique = append(unique, normalizedCandidate)
	}
	return unique
}

func normalizeModulePath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}
