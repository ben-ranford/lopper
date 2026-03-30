package js

import sitter "github.com/smacker/go-tree-sitter"

func walkNode(node *sitter.Node, visit func(*sitter.Node)) {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		visit(child)
		walkNode(child, visit)
	}
}

func nodeText(node *sitter.Node, content []byte) string {
	if node == nil {
		return ""
	}
	return string(content[node.StartByte():node.EndByte()])
}

func firstNamedChildOfType(node *sitter.Node, types ...string) *sitter.Node {
	for i := 0; i < int(node.NamedChildCount()); i++ {
		child := node.NamedChild(i)
		for _, typ := range types {
			if child.Type() == typ {
				return child
			}
		}
	}
	return nil
}
