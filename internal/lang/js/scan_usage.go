package js

import sitter "github.com/smacker/go-tree-sitter"

func collectIdentifierUsage(tree *sitter.Tree, content []byte) map[string]int {
	counts := make(map[string]int)
	root := tree.RootNode()
	walkNode(root, func(node *sitter.Node) {
		if node.Type() != "identifier" {
			return
		}
		if !isIdentifierUsage(node) {
			return
		}
		name := nodeText(node, content)
		if name == "" {
			return
		}
		counts[name]++
	})

	return counts
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
