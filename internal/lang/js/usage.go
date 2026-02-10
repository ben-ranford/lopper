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
