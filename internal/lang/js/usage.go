package js

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
)

type NamespaceReference struct {
	Local    string
	Property string
	Count    int
}

func collectNamespaceReferences(tree *sitter.Tree, content []byte) []NamespaceReference {
	refs := make([]NamespaceReference, 0)
	root := tree.RootNode()
	walkNode(root, func(node *sitter.Node) {
		switch node.Type() {
		case "member_expression":
			addMemberReference(node, content, &refs)
		case "subscript_expression":
			addSubscriptReference(node, content, &refs)
		case "variable_declarator":
			addVariableDestructureReference(node, content, &refs)
		case "assignment_expression":
			addAssignmentDestructureReference(node, content, &refs)
		}
	})

	return refs
}

func addMemberReference(node *sitter.Node, content []byte, refs *[]NamespaceReference) {
	object := node.ChildByFieldName("object")
	property := node.ChildByFieldName("property")
	if object == nil || property == nil {
		return
	}
	if object.Type() != "identifier" {
		return
	}

	propertyName := ""
	switch property.Type() {
	case "property_identifier", "identifier":
		propertyName = nodeText(property, content)
	case "string":
		propertyName = extractPropertyString(property, content)
	default:
		return
	}
	if propertyName == "" {
		return
	}

	objectName := nodeText(object, content)
	if objectName == "" {
		return
	}

	*refs = append(*refs, NamespaceReference{
		Local:    objectName,
		Property: propertyName,
		Count:    1,
	})
}

func addSubscriptReference(node *sitter.Node, content []byte, refs *[]NamespaceReference) {
	object := node.ChildByFieldName("object")
	index := node.ChildByFieldName("index")
	if object == nil || index == nil {
		return
	}
	if object.Type() != "identifier" {
		return
	}

	propertyName := ""
	switch index.Type() {
	case "string":
		propertyName = extractPropertyString(index, content)
	case "identifier":
		propertyName = nodeText(index, content)
	default:
		return
	}
	if propertyName == "" {
		return
	}

	objectName := nodeText(object, content)
	if objectName == "" {
		return
	}

	*refs = append(*refs, NamespaceReference{
		Local:    objectName,
		Property: propertyName,
		Count:    1,
	})
}

func addVariableDestructureReference(node *sitter.Node, content []byte, refs *[]NamespaceReference) {
	pattern := node.ChildByFieldName("name")
	value := node.ChildByFieldName("value")
	addObjectPatternDestructureReferences(pattern, value, content, refs)
}

func addAssignmentDestructureReference(node *sitter.Node, content []byte, refs *[]NamespaceReference) {
	pattern := node.ChildByFieldName("left")
	value := node.ChildByFieldName("right")
	addObjectPatternDestructureReferences(pattern, value, content, refs)
}

func addObjectPatternDestructureReferences(pattern, value *sitter.Node, content []byte, refs *[]NamespaceReference) {
	if pattern == nil || pattern.Type() != "object_pattern" {
		return
	}
	local := extractIdentifierExpression(value, content)
	if local == "" {
		return
	}

	for i := 0; i < int(pattern.NamedChildCount()); i++ {
		property := extractObjectPatternProperty(pattern.NamedChild(i), content)
		if property == "" {
			continue
		}
		*refs = append(*refs, NamespaceReference{
			Local:    local,
			Property: property,
			Count:    1,
		})
	}
}

func extractIdentifierExpression(node *sitter.Node, content []byte) string {
	for node != nil && node.Type() == "parenthesized_expression" && node.NamedChildCount() == 1 {
		node = node.NamedChild(0)
	}
	if node == nil || node.Type() != "identifier" {
		return ""
	}
	return nodeText(node, content)
}

func extractObjectPatternProperty(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}

	switch node.Type() {
	case "shorthand_property_identifier_pattern", "property_identifier":
		return nodeText(node, content)
	case "object_assignment_pattern":
		return nodeText(node.ChildByFieldName("left"), content)
	case "pair_pattern":
		key := node.ChildByFieldName("key")
		if key == nil {
			return ""
		}
		switch key.Type() {
		case "property_identifier", "identifier":
			return nodeText(key, content)
		case "string":
			return extractPropertyString(key, content)
		}
	}

	return ""
}

func extractPropertyString(node *sitter.Node, content []byte) string {
	text := nodeText(node, content)
	if text == "" {
		return ""
	}
	text = strings.TrimSpace(text)
	if len(text) >= 2 {
		quote := text[0]
		if (quote == '"' || quote == '\'') && text[len(text)-1] == quote {
			return text[1 : len(text)-1]
		}
	}
	return strings.Trim(text, "\"'`")
}
