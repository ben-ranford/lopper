package js

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

const (
	usageHelpersIndexJS = "index.js"
	parseSourceErrF     = "parse source: %v"
)

func parseRootNode(t *testing.T, source []byte) *sitter.Node {
	t.Helper()
	parser := newSourceParser()
	tree, err := parser.Parse(context.Background(), usageHelpersIndexJS, source)
	if err != nil {
		t.Fatalf(parseSourceErrF, err)
	}
	return tree.RootNode()
}

func collectReferencesByNodeType(root *sitter.Node, source []byte) []NamespaceReference {
	refs := []NamespaceReference{}
	walkNode(root, func(node *sitter.Node) {
		switch node.Type() {
		case "member_expression":
			addMemberReference(node, source, &refs)
		case "subscript_expression":
			addSubscriptReference(node, source, &refs)
		}
	})
	return refs
}

func TestAddMemberAndSubscriptReferenceBranches(t *testing.T) {
	source := []byte(`
const value = {};
value["name"];
value.name;
value[prop];
call().name;
`)
	root := parseRootNode(t, source)
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
	source := []byte(`const obj = {}; obj[unknown];`)
	root := parseRootNode(t, source)
	refs := collectReferencesByNodeType(root, source)
	if len(refs) == 0 {
		t.Fatalf("expected at least one valid subscript reference")
	}

	if got := extractPropertyString(nil, source); got != "" {
		t.Fatalf("expected empty property string for nil node, got %q", got)
	}
}

func TestAddSubscriptReferenceEarlyReturns(t *testing.T) {
	source := []byte(`
const obj = {};
obj[''];
obj[1];
call()[prop];
`)
	root := parseRootNode(t, source)
	var refs []NamespaceReference
	subscriptCount := 0
	walkNode(root, func(node *sitter.Node) {
		if node.Type() == "subscript_expression" {
			subscriptCount++
			addSubscriptReference(node, source, &refs)
		}
	})
	if subscriptCount == 0 {
		t.Fatal("expected at least one subscript expression")
	}

	// All subscript references above are intentionally invalid for usage extraction.
	if len(refs) != 0 {
		t.Fatalf("expected no refs, got %#v", refs)
	}
}

func TestAddMemberReferenceDefaultPropertyType(t *testing.T) {
	source := []byte(`
class C {
  #value = 1;
}
const c = new C();
c.#value;
`)
	root := parseRootNode(t, source)
	var member *sitter.Node
	walkNode(root, func(node *sitter.Node) {
		if member != nil || node.Type() != "member_expression" {
			return
		}
		object := node.ChildByFieldName("object")
		property := node.ChildByFieldName("property")
		if object == nil || property == nil {
			return
		}
		if object.Type() == "identifier" && property.Type() == "private_property_identifier" {
			member = node
		}
	})
	if member == nil {
		t.Fatalf("expected member expression node with private property")
	}

	refs := []NamespaceReference{}
	addMemberReference(member, source, &refs)
	if len(refs) != 0 {
		t.Fatalf("expected no refs for private member expression, got %#v", refs)
	}
}

func TestAddReferenceAdditionalNoOpBranches(t *testing.T) {
	source := []byte(`
const obj = {};
obj[""];
obj[0];
({}).name;
fn().name;
`)
	root := parseRootNode(t, source)
	refs := collectReferencesByNodeType(root, source)

	// All expressions above should be ignored by the namespace collector rules.
	if len(refs) != 0 {
		t.Fatalf("expected no namespace references, got %#v", refs)
	}
}

func TestAddReferenceSuccessBranches(t *testing.T) {
	source := []byte(`
const value = {};
const prop = "dynamic";
value.name;
value[prop];
`)
	root := parseRootNode(t, source)
	refs := collectReferencesByNodeType(root, source)

	if len(refs) < 2 {
		t.Fatalf("expected member+subscript references, got %#v", refs)
	}
}

func TestAddReferenceMismatchedNodeTypeBranches(t *testing.T) {
	source := []byte(`
const value = {};
value.name;
value["name"];
`)
	root := parseRootNode(t, source)

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
	// These calls intentionally mismatch node kinds to cover nil field early-return paths.
	addMemberReference(subscriptNode, source, &refs)
	addSubscriptReference(memberNode, source, &refs)
	if len(refs) != 0 {
		t.Fatalf("expected no references for mismatched node type calls, got %#v", refs)
	}
}
