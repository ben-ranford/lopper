package rust

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func collectManifestData(repoPath string) ([]string, map[string]dependencyInfo, map[string][]string, []string, error) {
	manifestPaths, warnings, err := discoverManifestPaths(repoPath)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	lookup := make(map[string]dependencyInfo)
	renamed := make(map[string]map[string]struct{})
	for _, manifestPath := range manifestPaths {
		_, deps, parseErr := parseCargoManifest(manifestPath, repoPath)
		if parseErr != nil {
			return nil, nil, nil, nil, parseErr
		}
		warnings = mergeDependencyLookup(lookup, renamed, deps, warnings)
	}

	renamedByDep := make(map[string][]string, len(renamed))
	for dependency, aliases := range renamed {
		renamedByDep[dependency] = shared.SortedKeys(aliases)
	}
	return manifestPaths, lookup, renamedByDep, dedupeWarnings(warnings), nil
}

func mergeDependencyLookup(lookup map[string]dependencyInfo, renamed map[string]map[string]struct{}, deps map[string]dependencyInfo, warnings []string) []string {
	for alias, info := range deps {
		if existing, ok := lookup[alias]; ok {
			warnings = handleExistingDependencyAlias(lookup, alias, existing, info, warnings)
			continue
		}
		lookup[alias] = info
		trackRenamedDependencyAlias(renamed, alias, info)
	}
	return warnings
}

func handleExistingDependencyAlias(lookup map[string]dependencyInfo, alias string, existing, incoming dependencyInfo, warnings []string) []string {
	if existing.Canonical != incoming.Canonical {
		warnings = append(warnings, fmt.Sprintf("ambiguous dependency alias %q maps to multiple crates; using %q", alias, existing.Canonical))
	}
	if existing.LocalPath && !incoming.LocalPath {
		lookup[alias] = incoming
	}
	return warnings
}

func trackRenamedDependencyAlias(renamed map[string]map[string]struct{}, alias string, info dependencyInfo) {
	if !info.Renamed {
		return
	}
	if _, ok := renamed[info.Canonical]; !ok {
		renamed[info.Canonical] = make(map[string]struct{})
	}
	renamed[info.Canonical][alias] = struct{}{}
}

func discoverManifestPaths(repoPath string) ([]string, []string, error) {
	rootManifest := filepath.Join(repoPath, cargoTomlName)
	if _, err := os.Stat(rootManifest); err == nil {
		return discoverFromRootManifest(repoPath, rootManifest)
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	return discoverManifestsByWalk(repoPath)
}

func discoverFromRootManifest(repoPath, rootManifest string) ([]string, []string, error) {
	meta, _, parseErr := parseCargoManifest(rootManifest, repoPath)
	if parseErr != nil {
		return nil, nil, parseErr
	}
	paths := make([]string, 0, 1+len(meta.WorkspaceMembers))
	warnings := make([]string, 0)
	if meta.HasPackage || len(meta.WorkspaceMembers) == 0 {
		paths = append(paths, rootManifest)
	}
	for _, member := range meta.WorkspaceMembers {
		memberManifests, warning := resolveWorkspaceMemberManifestPaths(repoPath, member)
		if warning != "" {
			warnings = append(warnings, warning)
		}
		paths = append(paths, memberManifests...)
	}
	return uniquePaths(paths), dedupeWarnings(warnings), nil
}

func resolveWorkspaceMemberManifestPaths(repoPath, member string) ([]string, string) {
	memberRoots := resolveWorkspaceMembers(repoPath, member)
	if len(memberRoots) == 0 {
		return nil, fmt.Sprintf("workspace member pattern %q did not resolve to a Cargo.toml", member)
	}
	paths := make([]string, 0, len(memberRoots))
	for _, root := range memberRoots {
		paths = append(paths, filepath.Join(root, cargoTomlName))
	}
	return paths, ""
}

func discoverManifestsByWalk(repoPath string) ([]string, []string, error) {
	paths := make([]string, 0)
	warnings := make([]string, 0)
	count := 0
	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(entry.Name(), cargoTomlName) {
			return nil
		}
		count++
		if count > maxManifestCount {
			return fs.SkipAll
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return nil, nil, err
	}
	if count > maxManifestCount {
		warnings = append(warnings, fmt.Sprintf("cargo manifest discovery capped at %d manifests", maxManifestCount))
	}
	if len(paths) == 0 {
		warnings = append(warnings, "no Cargo.toml files found for analysis")
	}
	return uniquePaths(paths), dedupeWarnings(warnings), nil
}

func resolveWorkspaceMembers(repoPath, pattern string) []string {
	glob := filepath.Join(repoPath, pattern)
	matches, err := filepath.Glob(glob)
	if err != nil {
		return nil
	}
	roots := make(map[string]struct{})
	for _, match := range matches {
		match = filepath.Clean(match)
		info, statErr := os.Stat(match)
		if statErr != nil || !info.IsDir() {
			continue
		}
		if !isSubPath(repoPath, match) {
			continue
		}
		manifest := filepath.Join(match, cargoTomlName)
		if _, manifestErr := os.Stat(manifest); manifestErr != nil {
			continue
		}
		roots[match] = struct{}{}
	}
	return shared.SortedKeys(roots)
}

func parseCargoManifest(manifestPath, repoPath string) (manifestMeta, map[string]dependencyInfo, error) {
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		return manifestMeta{}, nil, err
	}
	return parseCargoManifestContent(string(content)), parseCargoDependencies(string(content)), nil
}

func parseCargoManifestContent(content string) manifestMeta {
	meta := manifestMeta{}
	inWorkspaceMembers := false
	onSection := func(section string) {
		inWorkspaceMembers = false
		markManifestPackageSection(section, &meta)
	}
	onLine := func(section, clean string) {
		inWorkspaceMembers = parseWorkspaceMembersLine(clean, section, inWorkspaceMembers, &meta)
	}
	consumeTomlContent(content, onSection, onLine)
	meta.WorkspaceMembers = dedupeStrings(meta.WorkspaceMembers)
	return meta
}

func parseTomlSectionName(clean string) (string, bool) {
	match := tablePattern.FindStringSubmatch(clean)
	if len(match) != 2 {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(match[1])), true
}

func markManifestPackageSection(section string, meta *manifestMeta) {
	if section == "package" {
		meta.HasPackage = true
	}
}

func parseWorkspaceMembersLine(clean, section string, inWorkspaceMembers bool, meta *manifestMeta) bool {
	if section != "workspace" {
		return false
	}
	if inWorkspaceMembers {
		meta.WorkspaceMembers = append(meta.WorkspaceMembers, extractQuotedStrings(clean)...)
		return !strings.Contains(clean, "]")
	}
	right, ok := workspaceMembersAssignmentValue(clean)
	if !ok {
		return false
	}
	meta.WorkspaceMembers = append(meta.WorkspaceMembers, extractQuotedStrings(right)...)
	return strings.Contains(right, "[") && !strings.Contains(right, "]")
}

func workspaceMembersAssignmentValue(clean string) (string, bool) {
	eq := strings.Index(clean, "=")
	if eq < 0 {
		return "", false
	}
	key := strings.TrimSpace(clean[:eq])
	if key != workspaceFieldStart {
		return "", false
	}
	return strings.TrimSpace(clean[eq+1:]), true
}

func parseCargoDependencies(content string) map[string]dependencyInfo {
	deps := make(map[string]dependencyInfo)
	consumeTomlContent(content, nil, func(section, clean string) {
		addDependencyFromLine(deps, section, clean)
	})
	return deps
}

func consumeTomlContent(content string, onSection func(string), onLine func(section, clean string)) {
	section := ""
	for _, line := range strings.Split(content, "\n") {
		clean := strings.TrimSpace(stripTomlComment(line))
		if clean == "" {
			continue
		}
		if nextSection, isSection := parseTomlSectionName(clean); isSection {
			section = nextSection
			if onSection != nil {
				onSection(section)
			}
			continue
		}
		onLine(section, clean)
	}
}

func addDependencyFromLine(deps map[string]dependencyInfo, section, clean string) {
	if !isDependencySection(section) {
		return
	}
	alias, info, ok := parseDependencyInfo(clean)
	if !ok {
		return
	}
	deps[alias] = info
	ensureCanonicalDependencyAlias(deps, info)
}

func parseDependencyInfo(clean string) (string, dependencyInfo, bool) {
	key, value, ok := parseTomlAssignment(clean)
	if !ok {
		return "", dependencyInfo{}, false
	}
	alias := normalizeDependencyID(key)
	if alias == "" {
		return "", dependencyInfo{}, false
	}
	info := dependencyInfo{Canonical: alias}
	if strings.HasPrefix(strings.TrimSpace(value), "{") {
		applyInlineDependencyFields(value, alias, &info)
	}
	return alias, info, true
}

func applyInlineDependencyFields(value, alias string, info *dependencyInfo) {
	fields := parseInlineFields(value)
	if pkg, ok := fields["package"]; ok {
		info.Canonical = normalizeDependencyID(pkg)
		info.Renamed = info.Canonical != alias
	}
	if pathValue, ok := fields["path"]; ok && strings.TrimSpace(pathValue) != "" {
		info.LocalPath = true
	}
}

func ensureCanonicalDependencyAlias(deps map[string]dependencyInfo, info dependencyInfo) {
	if _, ok := deps[info.Canonical]; ok {
		return
	}
	deps[info.Canonical] = dependencyInfo{
		Canonical: info.Canonical,
		LocalPath: info.LocalPath,
	}
}

func isDependencySection(section string) bool {
	section = strings.ToLower(strings.TrimSpace(section))
	if section == "dependencies" || section == "dev-dependencies" || section == "build-dependencies" {
		return true
	}
	if strings.HasPrefix(section, "target.") {
		return strings.HasSuffix(section, ".dependencies") || strings.HasSuffix(section, ".dev-dependencies") || strings.HasSuffix(section, ".build-dependencies")
	}
	return false
}

func parseTomlAssignment(line string) (string, string, bool) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key := strings.TrimSpace(parts[0])
	value := strings.TrimSpace(parts[1])
	key = strings.Trim(key, `"'`)
	if key == "" || value == "" {
		return "", "", false
	}
	return key, value, true
}

func parseInlineFields(value string) map[string]string {
	fields := make(map[string]string)
	for _, match := range stringFieldPattern.FindAllStringSubmatch(value, -1) {
		if len(match) != 3 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(match[1]))
		fields[key] = strings.Trim(strings.TrimSpace(match[2]), `"'`)
	}
	return fields
}

func stripTomlComment(line string) string {
	inDouble := false
	inSingle := false
	for index, r := range line {
		switch r {
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '#':
			if !inDouble && !inSingle {
				return line[:index]
			}
		}
	}
	return line
}

func extractQuotedStrings(value string) []string {
	results := make([]string, 0)
	current := strings.Builder{}
	inString := false
	quote := byte(0)
	for i := 0; i < len(value); i++ {
		ch := value[i]
		if !inString {
			if ch == '"' || ch == '\'' {
				inString = true
				quote = ch
				current.Reset()
			}
			continue
		}
		if ch == quote {
			inString = false
			results = append(results, current.String())
			continue
		}
		current.WriteByte(ch)
	}
	return dedupeStrings(results)
}
