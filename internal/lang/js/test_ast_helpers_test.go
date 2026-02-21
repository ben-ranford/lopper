package js

import sitter "github.com/smacker/go-tree-sitter"

func firstNodeByType(root *sitter.Node, nodeType string) *sitter.Node {
	return firstNode(root, func(node *sitter.Node) bool {
		return node.Type() == nodeType
	})
}

func firstNode(root *sitter.Node, match func(*sitter.Node) bool) *sitter.Node {
	var found *sitter.Node
	walkNode(root, func(node *sitter.Node) {
		if found == nil && match(node) {
			found = node
		}
	})
	return found
}
