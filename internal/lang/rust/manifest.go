package rust

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
	toml "github.com/pelletier/go-toml/v2"
)

type manifestDiscoveryResult struct {
	ManifestPaths      []string
	Warnings           []string
	ParsedDependencies map[string]map[string]dependencyInfo
}

func collectManifestData(repoPath string) ([]string, map[string]dependencyInfo, map[string][]string, []string, error) {
	discovery, err := discoverManifestData(repoPath)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	lookup, renamedByDep, warnings, err := extractManifestDependencies(repoPath, discovery)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return discovery.ManifestPaths, lookup, renamedByDep, warnings, nil
}

func extractManifestDependencies(repoPath string, discovery manifestDiscoveryResult) (map[string]dependencyInfo, map[string][]string, []string, error) {
	lookup := make(map[string]dependencyInfo)
	renamed := make(map[string]map[string]struct{})

	warnings := append([]string(nil), discovery.Warnings...)
	for _, manifestPath := range discovery.ManifestPaths {
		deps, ok := discovery.ParsedDependencies[manifestPath]
		if !ok {
			_, parsedDeps, parseErr := parseCargoManifest(manifestPath, repoPath)
			if parseErr != nil {
				return nil, nil, nil, parseErr
			}
			deps = parsedDeps
		}
		warnings = mergeDependencyLookup(lookup, renamed, deps, warnings)
	}

	renamedByDep := make(map[string][]string, len(renamed))
	for dependency, aliases := range renamed {
		renamedByDep[dependency] = shared.SortedKeys(aliases)
	}
	return lookup, renamedByDep, dedupeWarnings(warnings), nil
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
	discovery, err := discoverManifestData(repoPath)
	if err != nil {
		return nil, nil, err
	}
	return discovery.ManifestPaths, discovery.Warnings, nil
}

func discoverManifestData(repoPath string) (manifestDiscoveryResult, error) {
	rootManifest := filepath.Join(repoPath, cargoTomlName)
	if _, err := os.Stat(rootManifest); err == nil {
		return discoverFromRootManifestData(repoPath, rootManifest)
	} else if !os.IsNotExist(err) {
		return manifestDiscoveryResult{}, err
	}

	paths, warnings, err := discoverManifestsByWalk(repoPath)
	if err != nil {
		return manifestDiscoveryResult{}, err
	}
	return manifestDiscoveryResult{
		ManifestPaths:      paths,
		Warnings:           warnings,
		ParsedDependencies: make(map[string]map[string]dependencyInfo),
	}, nil
}

func discoverFromRootManifestData(repoPath, rootManifest string) (manifestDiscoveryResult, error) {
	meta, deps, parseErr := parseCargoManifest(rootManifest, repoPath)
	if parseErr != nil {
		return manifestDiscoveryResult{}, parseErr
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

	discovery := manifestDiscoveryResult{
		ManifestPaths:      uniquePaths(paths),
		Warnings:           dedupeWarnings(warnings),
		ParsedDependencies: make(map[string]map[string]dependencyInfo),
	}
	for _, manifestPath := range discovery.ManifestPaths {
		if manifestPath == rootManifest {
			discovery.ParsedDependencies[manifestPath] = deps
			break
		}
	}
	return discovery, nil
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
	if err != nil && !errors.Is(err, fs.SkipAll) {
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
	roots := make(map[string]struct{})
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	collector := workspaceMemberCollector{
		repoPath: repoPath,
		pattern:  pattern,
		roots:    roots,
	}
	err := filepath.WalkDir(repoPath, collector.walk)
	if err != nil {
		return nil
	}
	return shared.SortedKeys(roots)
}

type workspaceMemberCollector struct {
	repoPath string
	pattern  string
	roots    map[string]struct{}
}

func (c *workspaceMemberCollector) walk(path string, entry fs.DirEntry, walkErr error) error {
	matched, err := c.matchDirectory(path, entry, walkErr)
	if err != nil || !matched {
		return err
	}
	c.roots[path] = struct{}{}
	return nil
}

func (c *workspaceMemberCollector) matchDirectory(path string, entry fs.DirEntry, walkErr error) (bool, error) {
	if walkErr != nil {
		return false, walkErr
	}
	if !entry.IsDir() {
		return false, nil
	}
	if shouldSkipDir(entry.Name()) {
		return false, filepath.SkipDir
	}

	rel, err := filepath.Rel(c.repoPath, path)
	if err != nil {
		return false, err
	}
	matched, err := workspaceMemberPatternMatches(c.pattern, rel)
	if err != nil || !matched {
		return false, err
	}

	manifest := filepath.Join(path, cargoTomlName)
	if _, err := os.Stat(manifest); err != nil {
		return false, nil
	}
	return true, nil
}

func workspaceMemberPatternMatches(pattern, candidate string) (bool, error) {
	pattern = filepath.ToSlash(filepath.Clean(strings.TrimSpace(pattern)))
	candidate = filepath.ToSlash(filepath.Clean(strings.TrimSpace(candidate)))
	if pattern == "" || candidate == "" {
		return false, nil
	}
	patternParts := strings.Split(pattern, "/")
	candidateParts := strings.Split(candidate, "/")
	return matchWorkspaceMemberPatternParts(patternParts, candidateParts)
}

func matchWorkspaceMemberPatternParts(patternParts, candidateParts []string) (bool, error) {
	if len(patternParts) == 0 {
		return len(candidateParts) == 0, nil
	}
	if patternParts[0] == "**" {
		if len(patternParts) == 1 {
			return true, nil
		}
		for index := 0; index <= len(candidateParts); index++ {
			matched, err := matchWorkspaceMemberPatternParts(patternParts[1:], candidateParts[index:])
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
		return false, nil
	}
	if len(candidateParts) == 0 {
		return false, nil
	}
	matched, err := filepath.Match(patternParts[0], candidateParts[0])
	if err != nil || !matched {
		return false, err
	}
	return matchWorkspaceMemberPatternParts(patternParts[1:], candidateParts[1:])
}

func parseCargoManifest(manifestPath, repoPath string) (manifestMeta, map[string]dependencyInfo, error) {
	content, err := safeio.ReadFileUnder(repoPath, manifestPath)
	if err != nil {
		return manifestMeta{}, nil, err
	}
	document, err := parseCargoManifestDocument(content)
	if err != nil {
		return manifestMeta{}, nil, fmt.Errorf("parse Cargo manifest %s: %w", relativeManifestPath(repoPath, manifestPath), err)
	}
	return cargoManifestMeta(document), cargoManifestDependencies(document), nil
}

func parseCargoManifestContent(content string) manifestMeta {
	document, err := parseCargoManifestDocument([]byte(content))
	if err != nil {
		return manifestMeta{}
	}
	return cargoManifestMeta(document)
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
	document, err := parseCargoManifestDocument([]byte(content))
	if err != nil {
		return map[string]dependencyInfo{}
	}
	return cargoManifestDependencies(document)
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
	if section == "dependencies" || section == "dev-dependencies" || section == "build-dependencies" || section == "workspace.dependencies" {
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
	document, err := parseInlineTomlFields(value)
	if err == nil {
		return flattenTomlStringFields(document)
	}

	fields := make(map[string]string)
	for _, item := range strings.Split(strings.Trim(value, "{}"), ",") {
		key, raw, ok := parseTomlAssignment(item)
		if !ok {
			continue
		}
		unquoted, ok := parseTomlStringLiteral(raw)
		if !ok {
			continue
		}
		fields[strings.ToLower(key)] = unquoted
	}
	return fields
}

func parseCargoManifestDocument(content []byte) (map[string]any, error) {
	document := make(map[string]any)
	if err := toml.Unmarshal(content, &document); err != nil {
		return nil, err
	}
	return document, nil
}

func cargoManifestMeta(document map[string]any) manifestMeta {
	meta := manifestMeta{}
	if _, ok := document["package"].(map[string]any); ok {
		meta.HasPackage = true
	}
	workspace, _ := document["workspace"].(map[string]any)
	meta.WorkspaceMembers = dedupeStrings(tomlStringSlice(workspace["members"]))
	return meta
}

func cargoManifestDependencies(document map[string]any) map[string]dependencyInfo {
	deps := make(map[string]dependencyInfo)
	addTomlDependencyTable(deps, document["dependencies"])
	addTomlDependencyTable(deps, document["dev-dependencies"])
	addTomlDependencyTable(deps, document["build-dependencies"])
	if workspace, ok := document["workspace"].(map[string]any); ok {
		addTomlDependencyTable(deps, workspace["dependencies"])
	}
	if target, ok := document["target"].(map[string]any); ok {
		addTargetTomlDependencyTables(deps, target)
	}
	return deps
}

func addTargetTomlDependencyTables(deps map[string]dependencyInfo, target map[string]any) {
	for _, value := range target {
		targetTable, ok := value.(map[string]any)
		if !ok {
			continue
		}
		addTomlDependencyTable(deps, targetTable["dependencies"])
		addTomlDependencyTable(deps, targetTable["dev-dependencies"])
		addTomlDependencyTable(deps, targetTable["build-dependencies"])
	}
}

func addTomlDependencyTable(deps map[string]dependencyInfo, value any) {
	table, ok := value.(map[string]any)
	if !ok {
		return
	}
	for alias, raw := range table {
		addTomlDependency(deps, alias, raw)
	}
}

func addTomlDependency(deps map[string]dependencyInfo, alias string, raw any) {
	alias = normalizeDependencyID(alias)
	if alias == "" {
		return
	}
	info := dependencyInfo{Canonical: alias}
	if fields, ok := raw.(map[string]any); ok {
		if pkg := tomlString(fields["package"]); pkg != "" {
			info.Canonical = normalizeDependencyID(pkg)
			info.Renamed = info.Canonical != alias
		}
		if tomlString(fields["path"]) != "" {
			info.LocalPath = true
		}
	}
	deps[alias] = info
	ensureCanonicalDependencyAlias(deps, info)
}

func tomlString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func tomlStringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		text := tomlString(item)
		if text != "" {
			values = append(values, text)
		}
	}
	return values
}

func parseInlineTomlFields(value string) (map[string]any, error) {
	document := make(map[string]any)
	content := []byte("value = " + strings.TrimSpace(value))
	if err := toml.Unmarshal(content, &document); err != nil {
		return nil, err
	}
	fields, ok := document["value"].(map[string]any)
	if !ok {
		return map[string]any{}, nil
	}
	return fields, nil
}

func flattenTomlStringFields(document map[string]any) map[string]string {
	fields := make(map[string]string)
	flattenTomlStringFieldsInto(fields, "", document)
	return fields
}

func flattenTomlStringFieldsInto(fields map[string]string, prefix string, document map[string]any) {
	for key, value := range document {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if prefix != "" {
			key = prefix + "." + key
		}
		switch typed := value.(type) {
		case string:
			fields[key] = strings.TrimSpace(typed)
		case map[string]any:
			flattenTomlStringFieldsInto(fields, key, typed)
		}
	}
}

func parseTomlStringLiteral(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return "", false
	}
	if (value[0] != '"' || value[len(value)-1] != '"') && (value[0] != '\'' || value[len(value)-1] != '\'') {
		return "", false
	}
	return strings.Trim(value, `"'`), true
}

func relativeManifestPath(repoPath, manifestPath string) string {
	relative, err := filepath.Rel(repoPath, manifestPath)
	if err != nil {
		return manifestPath
	}
	return relative
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
