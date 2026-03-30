package js

import sitter "github.com/smacker/go-tree-sitter"

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
