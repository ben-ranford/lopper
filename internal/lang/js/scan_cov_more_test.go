package js

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
)

func TestJSScanRepoAndReadHelpers(t *testing.T) {
	if _, err := ScanRepo(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatalf("expected missing repo to fail ScanRepo")
	}

	sourcePath := filepath.Join(t.TempDir(), "index.js")
	if err := os.WriteFile(sourcePath, []byte("const value = 1;\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	content, tree, relPath, err := readAndParseFile(context.Background(), newSourceParser(), "", sourcePath)
	if err != nil {
		t.Fatalf("readAndParseFile with empty repoPath: %v", err)
	}
	if len(content) == 0 || tree == nil || relPath != sourcePath {
		t.Fatalf("expected absolute path fallback, got len=%d tree=%v relPath=%q", len(content), tree != nil, relPath)
	}

	unsupportedPath := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(unsupportedPath, []byte("notes"), 0o644); err != nil {
		t.Fatalf("write unsupported source: %v", err)
	}
	if _, _, _, err := readAndParseFile(context.Background(), newSourceParser(), "", unsupportedPath); err == nil {
		t.Fatalf("expected unsupported extension to fail")
	}
}

func TestJSScanEntryAndIdentifierUsageBranches(t *testing.T) {
	repo := t.TempDir()
	skipDir := filepath.Join(repo, ".next")
	if err := os.MkdirAll(skipDir, 0o755); err != nil {
		t.Fatalf("mkdir skip dir: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var dirEntry fs.DirEntry
	for _, entry := range entries {
		if entry.Name() == ".next" {
			dirEntry = entry
			break
		}
	}
	if dirEntry == nil {
		t.Fatalf("expected .next entry")
	}

	state := scanRepoState{parser: newSourceParser(), repoPath: repo, result: &ScanResult{}}
	if err := scanRepoEntry(context.Background(), &state, skipDir, dirEntry); !errors.Is(err, fs.SkipDir) {
		t.Fatalf("expected .next directory to be skipped, got %v", err)
	}

	parser := newSourceParser()
	source := []byte(`
function demo(param) {
  const { key: alias } = pkg;
  const [item] = list;
  const list = items;
  const first = list[index];
  return util.map(alias) + first;
}
class Widget {}
`)
	tree, err := parser.Parse(context.Background(), "index.js", source)
	if err != nil {
		t.Fatalf("parse identifier branches: %v", err)
	}

	assertIdentifierUsageState(t, tree, source, "demo", "function_declaration", false)
	assertIdentifierUsageState(t, tree, source, "param", "formal_parameters", false)
	assertIdentifierUsageState(t, tree, source, "item", "array_pattern", false)
	assertIdentifierUsageState(t, tree, source, "util", "member_expression", false)
	assertIdentifierUsageState(t, tree, source, "list", "subscript_expression", false)
	assertIdentifierUsageState(t, tree, source, "index", "subscript_expression", true)
}

func assertIdentifierUsageState(t *testing.T, tree *sitter.Tree, source []byte, name string, parentType string, want bool) {
	t.Helper()
	node := findIdentifierNode(tree.RootNode(), source, name, parentType)
	if node == nil {
		t.Fatalf("expected identifier %q under %s", name, parentType)
	}
	if got := isIdentifierUsage(node); got != want {
		t.Fatalf("expected isIdentifierUsage(%q/%s)=%v, got %v", name, parentType, want, got)
	}
}

func findIdentifierNode(root *sitter.Node, source []byte, name, parentType string) *sitter.Node {
	var found *sitter.Node
	walkNode(root, func(node *sitter.Node) {
		if found != nil || node.Type() != "identifier" {
			return
		}
		parent := node.Parent()
		if parent == nil || parent.Type() != parentType {
			return
		}
		if nodeText(node, source) == name {
			found = node
		}
	})
	return found
}
