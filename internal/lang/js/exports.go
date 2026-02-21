package js

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

type ExportSurface struct {
	Names            map[string]struct{}
	IncludesWildcard bool
	EntryPoints      []string
	Warnings         []string
}

type packageJSON struct {
	Main                 string            `json:"main"`
	Module               string            `json:"module"`
	Types                string            `json:"types"`
	Typings              string            `json:"typings"`
	Exports              interface{}       `json:"exports"`
	Gypfile              bool              `json:"gypfile"`
	Scripts              map[string]string `json:"scripts"`
	Dependencies         map[string]string `json:"dependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

const defaultRuntimeProfile = "node-import"

type runtimeProfile struct {
	name       string
	conditions []string
}

func supportedRuntimeProfiles() []string {
	return []string{"node-import", "node-require", "browser-import", "browser-require"}
}

func resolveRuntimeProfile(name string) (runtimeProfile, string) {
	trimmed := strings.TrimSpace(strings.ToLower(name))
	if trimmed == "" {
		trimmed = defaultRuntimeProfile
	}
	switch trimmed {
	case "node-import":
		return runtimeProfile{name: trimmed, conditions: []string{"node", "import", "default"}}, ""
	case "node-require":
		return runtimeProfile{name: trimmed, conditions: []string{"node", "require", "default"}}, ""
	case "browser-import":
		return runtimeProfile{name: trimmed, conditions: []string{"browser", "import", "default"}}, ""
	case "browser-require":
		return runtimeProfile{name: trimmed, conditions: []string{"browser", "require", "default"}}, ""
	default:
		return runtimeProfile{name: defaultRuntimeProfile, conditions: []string{"node", "import", "default"}},
			fmt.Sprintf(
				"unknown runtime profile %q; using %q (supported: %s)",
				name,
				defaultRuntimeProfile,
				strings.Join(supportedRuntimeProfiles(), ", "),
			)
	}
}

func resolveDependencyExports(repoPath string, dependency string, dependencyRootPath string, runtimeProfileName string) (ExportSurface, error) {
	surface := ExportSurface{Names: make(map[string]struct{})}
	profile, profileWarning := resolveRuntimeProfile(runtimeProfileName)
	if profileWarning != "" {
		surface.Warnings = append(surface.Warnings, profileWarning)
	}
	depPath := dependencyRootPath
	if depPath == "" {
		root, err := dependencyRoot(repoPath, dependency)
		if err != nil {
			return surface, err
		}
		depPath = root
	}

	pkg, warnings, err := loadPackageJSONForSurface(depPath)
	if err != nil {
		surface.Warnings = append(surface.Warnings, warnings...)
		return surface, nil
	}
	surface.Warnings = append(surface.Warnings, warnings...)

	entrypoints := collectCandidateEntrypoints(pkg, profile, &surface)
	resolved := resolveEntrypoints(depPath, entrypoints, &surface)

	if len(resolved) == 0 {
		surface.Warnings = append(surface.Warnings, "no entrypoints resolved for dependency")
		return surface, nil
	}

	parseEntrypointsIntoSurface(resolved, &surface)

	return surface, nil
}

func loadPackageJSONForSurface(depPath string) (packageJSON, []string, error) {
	pkgPath := filepath.Join(depPath, "package.json")
	// #nosec G304 -- depPath is resolved under repoPath/node_modules for the selected dependency.
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return packageJSON{}, []string{fmt.Sprintf("unable to read %s", pkgPath)}, err
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, []string{"failed to parse dependency package.json"}, err
	}
	return pkg, nil, nil
}

func collectCandidateEntrypoints(pkg packageJSON, profile runtimeProfile, surface *ExportSurface) map[string]struct{} {
	entrypoints := make(map[string]struct{})
	if pkg.Exports != nil {
		resolved := resolveExportsEntryPaths(pkg.Exports, profile, "exports", surface)
		for _, entry := range resolved {
			addEntrypoint(entrypoints, entry)
		}
		if len(resolved) > 0 {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("resolved exports using runtime profile %q", profile.name))
		} else {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("no exports resolved for runtime profile %q; falling back to legacy entrypoints", profile.name))
		}
	}
	if len(entrypoints) == 0 {
		addEntrypoint(entrypoints, pkg.Main)
		addEntrypoint(entrypoints, pkg.Module)
		addEntrypoint(entrypoints, pkg.Types)
		addEntrypoint(entrypoints, pkg.Typings)
	}
	if len(entrypoints) == 0 {
		addEntrypoint(entrypoints, "index.js")
	}
	return entrypoints
}

func resolveExportsEntryPaths(value interface{}, profile runtimeProfile, scope string, surface *ExportSurface) []string {
	paths, _ := resolveExportNode(value, profile, scope, surface)
	return paths
}

func resolveExportNode(value interface{}, profile runtimeProfile, scope string, surface *ExportSurface) ([]string, bool) {
	switch typed := value.(type) {
	case string:
		if !isLikelyCodeAsset(typed) {
			if surface != nil {
				surface.Warnings = append(surface.Warnings, fmt.Sprintf("skipping non-js export target at %s: %s", scope, typed))
			}
			return nil, false
		}
		return []string{typed}, true
	case []interface{}:
		for idx, item := range typed {
			paths, ok := resolveExportNode(item, profile, fmt.Sprintf("%s[%d]", scope, idx), surface)
			if ok && len(paths) > 0 {
				return paths, true
			}
		}
		return nil, false
	case map[string]interface{}:
		if len(typed) == 0 {
			return nil, false
		}

		if hasSubpathExportKeys(typed) {
			collected := make(map[string]struct{})
			keys := sortedObjectKeys(typed)
			for _, key := range keys {
				if !isSubpathExportKey(key) {
					continue
				}
				paths, ok := resolveExportNode(typed[key], profile, fmt.Sprintf("%s.%s", scope, key), surface)
				if !ok {
					continue
				}
				for _, path := range paths {
					collected[path] = struct{}{}
				}
			}
			return sortedMapKeys(collected), len(collected) > 0
		}

		if hasConditionKeys(typed) {
			return resolveConditionalExportMap(typed, profile, scope, surface)
		}

		collected := make(map[string]struct{})
		for _, key := range sortedObjectKeys(typed) {
			paths, ok := resolveExportNode(typed[key], profile, fmt.Sprintf("%s.%s", scope, key), surface)
			if !ok {
				continue
			}
			for _, path := range paths {
				collected[path] = struct{}{}
			}
		}
		return sortedMapKeys(collected), len(collected) > 0
	default:
		return nil, false
	}
}

func resolveConditionalExportMap(node map[string]interface{}, profile runtimeProfile, scope string, surface *ExportSurface) ([]string, bool) {
	matches := matchingConditionKeys(node, profile)
	if len(matches) == 0 {
		return nil, false
	}
	if len(matches) > 1 && surface != nil {
		surface.Warnings = append(surface.Warnings, fmt.Sprintf("ambiguous export conditions at %s for profile %q: matched %s; selected %q", scope, profile.name, strings.Join(matches, ", "), matches[0]))
	}
	for _, key := range matches {
		paths, ok := resolveExportNode(node[key], profile, fmt.Sprintf("%s.%s", scope, key), surface)
		if ok && len(paths) > 0 {
			return paths, true
		}
	}
	return nil, false
}

func matchingConditionKeys(node map[string]interface{}, profile runtimeProfile) []string {
	items := make([]string, 0, len(profile.conditions))
	for _, key := range profile.conditions {
		if _, ok := node[key]; ok {
			items = append(items, key)
		}
	}
	return items
}

func hasConditionKeys(node map[string]interface{}) bool {
	for key := range node {
		if looksLikeConditionKey(key) {
			return true
		}
	}
	return false
}

func hasSubpathExportKeys(node map[string]interface{}) bool {
	for key := range node {
		if isSubpathExportKey(key) {
			return true
		}
	}
	return false
}

func isSubpathExportKey(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), ".")
}

func sortedObjectKeys(node map[string]interface{}) []string {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapKeys(node map[string]struct{}) []string {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func resolveEntrypoints(depPath string, entrypoints map[string]struct{}, surface *ExportSurface) []string {
	resolved := make([]string, 0, len(entrypoints))
	for entry := range entrypoints {
		path, ok := resolveEntrypoint(depPath, entry)
		if !ok {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("entrypoint not found: %s", entry))
			continue
		}
		resolved = append(resolved, path)
	}
	return resolved
}

func parseEntrypointsIntoSurface(resolved []string, surface *ExportSurface) {
	parser := newSourceParser()
	seenEntries := make(map[string]struct{})
	for _, entry := range resolved {
		if _, ok := seenEntries[entry]; ok {
			continue
		}
		seenEntries[entry] = struct{}{}

		// #nosec G304 -- entrypoints are resolved from dependency exports under depPath.
		content, err := os.ReadFile(entry)
		if err != nil {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("failed to read entrypoint: %s", entry))
			continue
		}
		tree, err := parser.Parse(context.Background(), entry, content)
		if err != nil {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("failed to parse entrypoint: %s", entry))
			continue
		}
		if tree != nil {
			addCollectedExports(surface, collectExportNames(tree, content))
		}
	}
	for entry := range seenEntries {
		surface.EntryPoints = append(surface.EntryPoints, entry)
	}
}

func addCollectedExports(surface *ExportSurface, names []string) {
	for _, name := range names {
		if name == "*" {
			surface.IncludesWildcard = true
			continue
		}
		surface.Names[name] = struct{}{}
	}
}

func dependencyRoot(repoPath string, dependency string) (string, error) {
	if repoPath == "" {
		return "", errors.New("repo path is empty")
	}
	if dependency == "" {
		return "", errors.New("dependency is empty")
	}

	if strings.HasPrefix(dependency, "@") {
		parts := strings.SplitN(dependency, "/", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid scoped dependency: %s", dependency)
		}
		return filepath.Join(repoPath, "node_modules", parts[0], parts[1]), nil
	}
	return filepath.Join(repoPath, "node_modules", dependency), nil
}

func collectExportPaths(value interface{}, dest map[string]struct{}, surface *ExportSurface) {
	switch typed := value.(type) {
	case string:
		addEntrypoint(dest, typed)
	case []interface{}:
		for _, item := range typed {
			collectExportPaths(item, dest, surface)
		}
	case map[string]interface{}:
		for key, item := range typed {
			if surface != nil && looksLikeConditionKey(key) {
				if path, ok := item.(string); ok && !isLikelyCodeAsset(path) {
					surface.Warnings = append(surface.Warnings, fmt.Sprintf("skipping non-js export condition %q: %s", key, path))
					continue
				}
			}
			collectExportPaths(item, dest, surface)
		}
	}
}

func addEntrypoint(dest map[string]struct{}, entry string) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return
	}
	dest[entry] = struct{}{}
}

func looksLikeConditionKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "browser", "node", "default", "import", "require", "development", "production", "types":
		return true
	default:
		return false
	}
}

func isLikelyCodeAsset(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".js", ".mjs", ".cjs", ".ts", ".tsx", ".cts", ".mts", ".d.ts":
		return true
	default:
		return false
	}
}

func resolveEntrypoint(root string, entry string) (string, bool) {
	path := entry
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, entry)
	}

	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return resolveEntrypoint(root, filepath.Join(entry, "index"))
		}
		return path, true
	}

	if filepath.Ext(path) == "" {
		candidates := []string{".js", ".mjs", ".cjs", ".ts", ".tsx", ".d.ts"}
		for _, ext := range candidates {
			candidate := path + ext
			if _, err := os.Stat(candidate); err == nil {
				return candidate, true
			}
		}
	}

	return "", false
}

func collectExportNames(tree *sitter.Tree, content []byte) []string {
	root := tree.RootNode()
	items := make([]string, 0)
	walkNode(root, func(node *sitter.Node) {
		if node.Type() != "export_statement" {
			return
		}
		items = append(items, parseExportStatement(node, content)...)
	})

	return items
}

func parseExportStatement(node *sitter.Node, content []byte) []string {
	if ns := firstNamedChildOfType(node, "namespace_export"); ns != nil {
		nameNode := firstNamedChildOfType(ns, "identifier")
		name := nodeText(nameNode, content)
		if name != "" {
			return []string{name}
		}
	}

	clause := node.ChildByFieldName("export_clause")
	if clause == nil {
		clause = firstNamedChildOfType(node, "export_clause")
	}
	if clause != nil {
		return parseExportClause(clause, content)
	}

	decl := node.ChildByFieldName("declaration")
	if decl != nil {
		return parseExportDeclaration(decl, content)
	}

	if node.ChildByFieldName("value") != nil {
		return []string{"default"}
	}

	if node.ChildByFieldName("source") != nil {
		return []string{"*"}
	}

	return nil
}

func parseExportClause(node *sitter.Node, content []byte) []string {
	names := make([]string, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		if child.Type() != "export_specifier" {
			continue
		}
		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			nameNode = firstNamedChildOfType(child, "identifier", "property_identifier")
		}
		aliasNode := child.ChildByFieldName("alias")
		if aliasNode == nil {
			aliasNode = nameNode
		}

		exportName := nodeText(aliasNode, content)
		if exportName == "" {
			exportName = nodeText(nameNode, content)
		}
		if exportName != "" {
			names = append(names, exportName)
		}
	}

	return names
}

func parseExportDeclaration(node *sitter.Node, content []byte) []string {
	switch node.Type() {
	case "function_declaration", "class_declaration":
		nameNode := node.ChildByFieldName("name")
		name := nodeText(nameNode, content)
		if name != "" {
			return []string{name}
		}
	case "lexical_declaration", "variable_declaration":
		return extractVariableDeclarationNames(node, content)
	}

	return nil
}

func extractVariableDeclarationNames(node *sitter.Node, content []byte) []string {
	names := make([]string, 0)
	walkNode(node, func(child *sitter.Node) {
		if child.Type() != "variable_declarator" {
			return
		}
		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		names = append(names, extractBindingNames(nameNode, content)...)
	})

	return names
}

func extractBindingNames(node *sitter.Node, content []byte) []string {
	switch node.Type() {
	case "identifier":
		name := nodeText(node, content)
		if name == "" {
			return nil
		}
		return []string{name}
	case "object_pattern":
		return extractObjectPatternNames(node, content)
	case "array_pattern":
		return extractArrayPatternNames(node, content)
	case "assignment_pattern":
		left := node.ChildByFieldName("left")
		if left != nil {
			return extractBindingNames(left, content)
		}
	case "rest_pattern":
		arg := node.ChildByFieldName("argument")
		if arg != nil {
			return extractBindingNames(arg, content)
		}
	}

	return nil
}

func extractObjectPatternNames(node *sitter.Node, content []byte) []string {
	items := make([]string, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		switch child.Type() {
		case "shorthand_property_identifier_pattern", "property_identifier":
			name := nodeText(child, content)
			if name != "" {
				items = append(items, name)
			}
		case "pair_pattern":
			value := child.ChildByFieldName("value")
			if value != nil {
				items = append(items, extractBindingNames(value, content)...)
			}
		}
	}

	return items
}

func extractArrayPatternNames(node *sitter.Node, content []byte) []string {
	items := make([]string, 0)
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		items = append(items, extractBindingNames(child, content)...)
	}

	return items
}
