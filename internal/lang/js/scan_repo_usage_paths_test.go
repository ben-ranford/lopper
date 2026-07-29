package js

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/safeio"
	sitter "github.com/smacker/go-tree-sitter"
)

const oversizedJSFileSize = 2*1024*1024 + 1

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

func TestJSScanRepoRejectsOversizedSource(t *testing.T) {
	repo := t.TempDir()
	oversizedPath := filepath.Join(repo, "index.js")
	if err := os.WriteFile(oversizedPath, []byte(strings.Repeat("a", oversizedJSFileSize)), 0o644); err != nil {
		t.Fatalf("write oversized source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "valid.js"), []byte("export const valid = 1\n"), 0o644); err != nil {
		t.Fatalf("write valid source: %v", err)
	}

	result, err := ScanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("expected oversized source to be skipped, got %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].Path != "valid.js" {
		t.Fatalf("expected valid source analysis to continue, got %#v", result.Files)
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "skipped 1 oversized JS/TS file") || !strings.Contains(warnings, filepath.Base(oversizedPath)) {
		t.Fatalf("expected oversized source warning, got %#v", result.Warnings)
	}

	dependencyReport, _ := buildDependencyReport(dependencyReportOptions{
		RepoPath:                          repo,
		Dependency:                        "oversized-only",
		ScanResult:                        result,
		MinUsagePercentForRecommendations: 1,
	})
	for _, recommendation := range dependencyReport.Recommendations {
		if recommendation.Code == "remove-unused-dependency" {
			t.Fatalf("did not expect removal advice from an incomplete scan, got %#v", dependencyReport.Recommendations)
		}
	}
}

func TestJSScanRepoRejectsSymlinkedSource(t *testing.T) {
	repo := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside.js")
	if err := os.WriteFile(outsidePath, []byte("export const outside = 1\n"), 0o644); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(repo, "index.js")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	result, err := ScanRepo(context.Background(), repo)
	if !errors.Is(err, safeio.ErrTargetPathSymlink) {
		t.Fatalf("expected symlink source error %v, got %v", safeio.ErrTargetPathSymlink, err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("expected outside target not to be parsed, got %#v", result.Files)
	}
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
