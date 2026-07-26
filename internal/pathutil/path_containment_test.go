package pathutil

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPathWithinRootRejectsSiblingPrefix(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo", "node_modules")
	child := filepath.Join(root, "pkg", "index.js")
	siblingPrefix := filepath.Join(string(filepath.Separator), "repo", "node_modules-sibling", "pkg")

	if !WithinRoot(root, child) {
		t.Fatalf("expected child to remain within root: root=%q child=%q", root, child)
	}
	if WithinRoot(root, siblingPrefix) {
		t.Fatalf("expected sibling prefix to remain outside root: root=%q child=%q", root, siblingPrefix)
	}
}

func TestEqualPathCleansEquivalentPaths(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "repo", "node_modules")
	aliased := filepath.Join(root, "pkg", "..")

	if !Equal(root, aliased) {
		t.Fatalf("expected cleaned paths to compare equal: left=%q right=%q", root, aliased)
	}
}

func TestRelativeContainedBranches(t *testing.T) {
	if !RelativeContained(".") {
		t.Fatal("expected current directory to stay contained")
	}
	if RelativeContained(filepath.Join("..", "escape")) {
		t.Fatal("expected parent traversal to be rejected")
	}
	absolutePath, err := filepath.Abs(filepath.Join("absolute", "child"))
	if err != nil {
		t.Fatalf("resolve absolute fixture: %v", err)
	}
	if RelativeContained(absolutePath) {
		t.Fatal("expected absolute path to be rejected")
	}
}

func TestWithinRootBranches(t *testing.T) {
	if !WithinRoot("", filepath.Join("any", "child")) {
		t.Fatal("expected empty root to allow containment checks")
	}

	originalRel := pathutilRel
	t.Cleanup(func() {
		pathutilRel = originalRel
	})
	pathutilRel = func(string, string) (string, error) {
		return "", errors.New("rel failed")
	}

	if WithinRoot("/repo", "/repo/child") {
		t.Fatal("expected relative path failure to be treated as outside root")
	}
}
