//go:build !regressionproof

package rust

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestLateMalformedFallbackSkipsSelectedDescendantSubtree(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, cargoTomlName), `[package]
name = "root"
version = "0.1.0"

[dependencies]
foo = "1"
`)
	writeFile(t, filepath.Join(repo, "src", testRustMainRS), "fn main() {}\n")
	brokenRoot := filepath.Join(repo, "apps")
	childRoot := filepath.Join(brokenRoot, "child")
	writeFile(t, filepath.Join(brokenRoot, cargoTomlName), "[package\nname = \"broken\"\n")
	writeFile(t, filepath.Join(brokenRoot, "src", testRustMainRS), "use foo::Broken;\nfn main() { let _ = Broken; }\n")
	writeFile(t, filepath.Join(childRoot, cargoTomlName), `[package]
name = "child"
version = "0.1.0"

[dependencies]
bar = "1"
`)
	writeFile(t, filepath.Join(childRoot, "nested", "src", rustLibFile), "use bar::Child;\npub fn child() { let _ = Child; }\n")

	adapter := NewAdapter()
	parent, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:             repo,
		ScopeMode:            "package",
		IsolatedProjectRoots: []string{repo, childRoot},
		Dependency:           "bar",
	})
	if err != nil {
		t.Fatalf("analyse parent: %v", err)
	}
	if len(parent.Dependencies) != 1 || parent.Dependencies[0].UsedExportsCount != 0 || parent.Dependencies[0].TotalExportsCount != 0 {
		t.Fatalf("expected parent fallback to exclude selected child subtree, got %#v", parent.Dependencies)
	}

	child, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:             childRoot,
		ScopeMode:            "package",
		IsolatedProjectRoots: []string{repo, childRoot},
		Dependency:           "bar",
	})
	if err != nil {
		t.Fatalf("analyse selected child: %v", err)
	}
	if len(child.Dependencies) != 1 || child.Dependencies[0].UsedExportsCount != 1 || child.Dependencies[0].TotalExportsCount != 1 || len(child.Dependencies[0].UsedImports) != 1 {
		t.Fatalf("expected selected child to own its nested source exactly once, got %#v", child.Dependencies)
	}
}
