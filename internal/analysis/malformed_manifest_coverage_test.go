package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestServiceRequireCompleteCoverageRejectsMalformedManifestDeclarations(t *testing.T) {
	tests := []struct {
		name     string
		language string
		setup    func(t *testing.T, repo string)
	}{
		{
			name:     "dotnet project",
			language: "dotnet",
			setup: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, "Broken.csproj"), "<Project><PackageReference Include=\"broken\"")
				writeFile(t, filepath.Join(repo, "Working.csproj"), "<Project><ItemGroup><PackageReference Include=\"Newtonsoft.Json\" /></ItemGroup></Project>")
				writeFile(t, filepath.Join(repo, "Program.cs"), "using Newtonsoft.Json;\n")
			},
		},
		{
			name:     "rust workspace member",
			language: "rust",
			setup: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, "Cargo.toml"), "[workspace]\nmembers = [\"crates/*\"]\n")
				writeFile(t, filepath.Join(repo, "crates", "broken", "Cargo.toml"), "not valid = ")
				writeFile(t, filepath.Join(repo, "crates", "working", "Cargo.toml"), "[package]\nname = \"working\"\nversion = \"0.1.0\"\n[dependencies]\nserde = \"1\"\n")
				writeFile(t, filepath.Join(repo, "crates", "working", "src", "lib.rs"), "use serde::Serialize;\n")
			},
		},
		{
			name:     "rust root fallback",
			language: "rust",
			setup: func(t *testing.T, repo string) {
				writeFile(t, filepath.Join(repo, "Cargo.toml"), "[package\nname = \"broken-root\"\n")
				writeFile(t, filepath.Join(repo, "child", "Cargo.toml"), "[package]\nname = \"child\"\nversion = \"0.1.0\"\n[dependencies]\nserde = \"1\"\n")
				writeFile(t, filepath.Join(repo, "src", "main.rs"), "use serde::Serialize;\n")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			tc.setup(t, repo)

			_, err := NewService().Analyse(context.Background(), Request{
				RepoPath:                repo,
				Language:                tc.language,
				RequireCompleteCoverage: true,
				Cache:                   &CacheOptions{Enabled: false},
			})
			if !errors.Is(err, ErrIncompleteCoverage) {
				t.Fatalf("expected malformed manifest declaration to fail complete coverage, got %v", err)
			}
		})
	}
}
