package js

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/ben-ranford/lopper/internal/report"
)

func collectIdentifierUsage(tree *sitter.Tree, content []byte) map[string]int {
	counts, _ := collectFileIdentifierUsage(tree, content, nil)
	return counts
}

func collectFileIdentifierUsage(tree *sitter.Tree, content []byte, imports []ImportBinding) (map[string]int, map[report.Location]int) {
	counts := make(map[string]int)
	destructures := make(map[report.Location]int)
	moduleNames := uniqueModuleNamedImports(imports)
	root := tree.RootNode()
	walkNode(root, func(node *sitter.Node) {
		if node.Type() != "identifier" {
			return
		}
		name := nodeText(node, content)
		if name == "" {
			return
		}
		if isIdentifierUsage(node) {
			counts[name]++
			return
		}
		location := moduleNames[name]
		if location.Line > 0 && ((isStaticObjectDestructureValueUsage(node) && isDirectModuleScope(node)) || isNamedObjectUsage(node)) {
			destructures[location]++
		}
	})

	return counts, destructures
}

func uniqueModuleNamedImports(imports []ImportBinding) map[string]report.Location {
	names := make(map[string]report.Location)
	for _, imported := range imports {
		if !imported.moduleScope {
			continue
		}
		location := imported.Location
		if _, seen := names[imported.LocalName]; seen {
			location = report.Location{}
		}
		names[imported.LocalName] = location
	}
	return names
}

// Only claim a binding when both its declaration and use are in direct module scope.
func isDirectModuleScope(node *sitter.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		switch parent.Type() {
		case "program":
			return true
		case "import_specifier", "named_imports", "import_clause", "import_statement",
			"pair_pattern", "object_pattern", "variable_declarator", "lexical_declaration",
			"variable_declaration", "export_statement", "assignment_expression",
			"parenthesized_expression", "expression_statement", "call_expression", "arguments":
			continue
		default:
			return false
		}
	}
	return false
}

func isNamedObjectUsage(node *sitter.Node) bool {
	parent := node.Parent()
	if parent == nil || (parent.Type() != "member_expression" && parent.Type() != "subscript_expression") {
		return false
	}
	object := parent.ChildByFieldName("object")
	return object != nil && object.ID() == node.ID() && isDirectModuleScope(parent)
}

func collectNamespaceUsage(tree *sitter.Tree, content []byte) map[string]map[string]int {
	counts := make(map[string]map[string]int)
	for _, ref := range collectNamespaceReferences(tree, content) {
		entry, ok := counts[ref.Local]
		if !ok {
			entry = make(map[string]int)
			counts[ref.Local] = entry
		}
		entry[ref.Property] += ref.Count
	}
	return counts
}

func isIdentifierUsage(node *sitter.Node) bool {
	parent := node.Parent()
	if parent == nil {
		return false
	}
	if isStaticObjectDestructureValueUsage(node) {
		return false
	}

	switch parent.Type() {
	case "import_specifier", "import_clause", "namespace_import", "named_imports", "import_statement":
		return false
	case "variable_declarator", "function_declaration", "class_declaration":
		nameNode := parent.ChildByFieldName("name")
		return nameNode == nil || nameNode.ID() != node.ID()
	case "formal_parameters", "required_parameter", "optional_parameter", "rest_parameter":
		return false
	case "shorthand_property_identifier_pattern", "property_identifier":
		return false
	case "pair_pattern":
		key := parent.ChildByFieldName("key")
		return key == nil || key.ID() != node.ID()
	case "object_pattern", "array_pattern":
		return false
	case "member_expression", "subscript_expression":
		// The object side (e.g. `util` in `util.map`) is tracked via namespace
		// property access, so only non-object identifiers count as direct usage.
		objectNode := parent.ChildByFieldName("object")
		if objectNode != nil && objectNode.ID() == node.ID() {
			return false
		}
		return true
	default:
		return true
	}
}

func isStaticObjectDestructureValueUsage(node *sitter.Node) bool {
	expr := node
	parent := expr.Parent()
	for parent != nil && parent.Type() == "parenthesized_expression" {
		expr = parent
		parent = expr.Parent()
	}
	if parent == nil {
		return false
	}

	var pattern, value *sitter.Node
	switch parent.Type() {
	case "variable_declarator":
		pattern = parent.ChildByFieldName("name")
		value = parent.ChildByFieldName("value")
	case "assignment_expression":
		pattern = parent.ChildByFieldName("left")
		value = parent.ChildByFieldName("right")
	default:
		return false
	}

	if value == nil || value.ID() != expr.ID() {
		return false
	}

	return hasOnlyStaticObjectPatternProperties(pattern)
}

func hasOnlyStaticObjectPatternProperties(pattern *sitter.Node) bool {
	if pattern == nil || pattern.Type() != "object_pattern" || pattern.NamedChildCount() == 0 {
		return false
	}

	for i := 0; i < int(pattern.NamedChildCount()); i++ {
		if !isStaticObjectPatternProperty(pattern.NamedChild(i)) {
			return false
		}
	}

	return true
}

func isStaticObjectPatternProperty(node *sitter.Node) bool {
	if node == nil {
		return false
	}

	switch node.Type() {
	case "shorthand_property_identifier_pattern", "property_identifier", "object_assignment_pattern":
		return true
	case "pair_pattern":
		key := node.ChildByFieldName("key")
		if key == nil {
			return false
		}
		switch key.Type() {
		case "property_identifier", "identifier", "string":
			return true
		}
	}

	return false
}
