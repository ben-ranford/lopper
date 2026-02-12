package js

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestAddMemberAndSubscriptReferenceBranches(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`
const value = {};
value["name"];
value.name;
value[prop];
call().name;
`)
	tree, err := parser.Parse(context.Background(), "index.js", source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}

	root := tree.RootNode()
	var memberNode *sitter.Node
	var subscriptNode *sitter.Node
	walkNode(root, func(node *sitter.Node) {
		if memberNode == nil && node.Type() == "member_expression" {
			memberNode = node
		}
		if subscriptNode == nil && node.Type() == "subscript_expression" {
			subscriptNode = node
		}
	})
	if memberNode == nil || subscriptNode == nil {
		t.Fatalf("expected member and subscript nodes")
	}

	refs := []NamespaceReference{}
	addMemberReference(memberNode, source, &refs)
	addSubscriptReference(subscriptNode, source, &refs)
	if len(refs) == 0 {
		t.Fatalf("expected collected namespace references")
	}
}

func TestAddReferenceNoOpBranches(t *testing.T) {
	parser := newSourceParser()
	source := []byte(`const obj = {}; obj[unknown];`)
	tree, err := parser.Parse(context.Background(), "index.js", source)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	root := tree.RootNode()

	var refs []NamespaceReference
	// Non-identifier object/property cases should be ignored.

	walkNode(root, func(node *sitter.Node) {
		switch node.Type() {
		case "subscript_expression":
			addSubscriptReference(node, source, &refs)
		case "member_expression":
			addMemberReference(node, source, &refs)
		}
	})
	if len(refs) == 0 {
		t.Fatalf("expected at least one valid subscript reference")
	}

	if got := extractPropertyString(nil, source); got != "" {
		t.Fatalf("expected empty property string for nil node, got %q", got)
	}
}
