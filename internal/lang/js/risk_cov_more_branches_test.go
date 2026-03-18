package js

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRiskAdditionalBranches(t *testing.T) {
	testJSDepthRiskCueAdditionalBranch(t)
	testJSNodeBinaryScannerSkipBranch(t)
	testJSTransitiveDepthAndDependencyHelpers(t)
}

func testJSDepthRiskCueAdditionalBranch(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, "node_modules", "root")
	deps := []string{"a", "b", "c", "d", "e", "f", "g"}
	current := root
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatalf("mkdir root dep: %v", err)
	}
	for i, dep := range deps {
		nextRoot := filepath.Join(current, "node_modules", dep)
		if err := os.MkdirAll(nextRoot, 0o755); err != nil {
			t.Fatalf("mkdir dep %s: %v", dep, err)
		}
		content := `{"dependencies":{}}`
		if i < len(deps)-1 {
			content = `{"dependencies":{"` + deps[i+1] + `":"1.0.0"}}`
		}
		if err := os.WriteFile(filepath.Join(nextRoot, packageJSONFile), []byte(content), 0o600); err != nil {
			t.Fatalf("write package.json for %s: %v", dep, err)
		}
		current = nextRoot
	}

	cues, warnings := appendDepthRiskCue(nil, nil, "root", repo, filepath.Join(root, "node_modules", "a"), packageJSON{
		Dependencies: map[string]string{"b": "1.0.0"},
	})
	if len(warnings) != 0 || len(cues) != 1 || cues[0].Severity != "high" {
		t.Fatalf("expected high-severity deep graph cue, got cues=%#v warnings=%#v", cues, warnings)
	}
}

func testJSNodeBinaryScannerSkipBranch(t *testing.T) {
	repo := t.TempDir()
	nodeModulesDir := filepath.Join(repo, "node_modules")
	if err := os.MkdirAll(nodeModulesDir, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("readdir repo: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != "node_modules" {
			continue
		}
		scanner := &nodeBinaryScanner{maxVisited: 1}
		if err := scanner.walk(nodeModulesDir, entry, nil); !errors.Is(err, filepath.SkipDir) {
			t.Fatalf("expected nodeBinaryScanner to skip nested node_modules dir, got %v", err)
		}
	}
}

func testJSTransitiveDepthAndDependencyHelpers(t *testing.T) {
	repo := t.TempDir()
	root := filepath.Join(repo, "node_modules", "root")
	memo := map[string]int{"cached": 4}
	depth, err := transitiveDepth(repo, "cached", packageJSON{}, memo, map[string]struct{}{}, 5)
	if err != nil || depth != 4 {
		t.Fatalf("expected memoized transitive depth, got depth=%d err=%v", depth, err)
	}

	if root, ok := resolveInstalledDependencyRoot(repo, root, "missing"); ok || root != "" {
		t.Fatalf("expected missing dependency root lookup to fail, got root=%q ok=%v", root, ok)
	}

	names := collectDependencyNames(packageJSON{
		Dependencies:         map[string]string{"b": "1.0.0"},
		OptionalDependencies: map[string]string{"a": "1.0.0"},
	})
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("expected dependency names to merge and sort, got %#v", names)
	}
}
