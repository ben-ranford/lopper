package js

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func resolveDependencyExports(repoPath string, dependency string) (ExportSurface, error) {
	surface := ExportSurface{Names: make(map[string]struct{})}
	depPath, err := dependencyRoot(repoPath, dependency)
	if err != nil {
		return surface, err
	}

	pkg, warnings, err := loadPackageJSONForSurface(depPath)
	if err != nil {
		surface.Warnings = append(surface.Warnings, warnings...)
		return surface, nil
	}
	surface.Warnings = append(surface.Warnings, warnings...)

	entrypoints := collectCandidateEntrypoints(pkg, &surface)
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

func collectCandidateEntrypoints(pkg packageJSON, surface *ExportSurface) map[string]struct{} {
	entrypoints := make(map[string]struct{})
	if pkg.Exports != nil {
		collectExportPaths(pkg.Exports, entrypoints, surface)
	}
	addEntrypoint(entrypoints, pkg.Main)
	addEntrypoint(entrypoints, pkg.Module)
	addEntrypoint(entrypoints, pkg.Types)
	addEntrypoint(entrypoints, pkg.Typings)
	if len(entrypoints) == 0 {
		addEntrypoint(entrypoints, "index.js")
	}
	return entrypoints
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
