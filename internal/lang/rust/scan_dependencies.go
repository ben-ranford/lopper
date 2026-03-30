package rust

import (
	"os"
	"path/filepath"
	"strings"
)

func resolveDependency(path string, crateRoot string, depLookup map[string]dependencyInfo, scan *scanResult) string {
	path = strings.TrimSpace(strings.TrimPrefix(path, "::"))
	if path == "" {
		return ""
	}
	root := strings.Split(path, "::")[0]
	normalizedRoot := normalizeDependencyID(root)
	if normalizedRoot == "" {
		return ""
	}
	if rustStdRoots[normalizedRoot] {
		return ""
	}
	if normalizedRoot == "crate" || normalizedRoot == "self" || normalizedRoot == "super" {
		return ""
	}
	if isLocalRustModuleWithCache(scan, crateRoot, root) {
		return ""
	}

	if info, ok := depLookup[normalizedRoot]; ok {
		if info.LocalPath {
			return ""
		}
		return info.Canonical
	}
	if scan != nil {
		scan.UnresolvedImports[normalizedRoot]++
	}
	return normalizedRoot
}

func isLocalRustModuleWithCache(scan *scanResult, crateRoot, root string) bool {
	if scan == nil {
		return isLocalRustModule(crateRoot, root)
	}
	if scan.LocalModuleCache == nil {
		scan.LocalModuleCache = make(map[string]bool)
	}
	key := crateRoot + localModuleCacheSep + root
	if cached, ok := scan.LocalModuleCache[key]; ok {
		return cached
	}
	isLocal := isLocalRustModule(crateRoot, root)
	scan.LocalModuleCache[key] = isLocal
	return isLocal
}

func isLocalRustModule(crateRoot, root string) bool {
	if crateRoot == "" || root == "" {
		return false
	}
	candidates := []string{
		filepath.Join(crateRoot, "src", root+".rs"),
		filepath.Join(crateRoot, "src", root, "mod.rs"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return true
		}
	}
	return false
}
