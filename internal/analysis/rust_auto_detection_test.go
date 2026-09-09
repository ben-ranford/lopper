package analysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/powershell"
	"github.com/ben-ranford/lopper/internal/lang/rust"
	"github.com/ben-ranford/lopper/internal/language"
)

func TestServiceResolveCandidatesPrefersRustCargoLocksOverPowerShellScripts(t *testing.T) {
	repo := t.TempDir()
	for _, path := range []string{
		filepath.Join(repo, "Cargo.lock"),
		filepath.Join(repo, "crates", "one", "Cargo.lock"),
		filepath.Join(repo, "crates", "two", "Cargo.lock"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("make lock directory: %v", err)
		}
		if err := os.WriteFile(path, []byte("version = 3\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	for i := range 5 {
		path := filepath.Join(repo, "script"+string(rune('a'+i))+".ps1")
		if err := os.WriteFile(path, []byte("Write-Host test\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	registry := language.NewRegistry()
	for _, adapter := range []language.Adapter{rust.NewAdapter(), powershell.NewAdapter()} {
		if err := registry.Register(adapter); err != nil {
			t.Fatalf("register %s: %v", adapter.ID(), err)
		}
	}

	candidates, err := registry.Resolve(context.Background(), repo, language.Auto)
	if err != nil {
		t.Fatalf("resolve automatic candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Adapter.ID() != "rust" {
		t.Fatalf("automatic candidates = %#v, want Rust selected from Cargo.lock confidence", candidates)
	}
}
