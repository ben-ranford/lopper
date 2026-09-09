package dotnet

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestDotNetDetectionKeepsNestedRootsWhenRootManifestIsValid(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Root.csproj"), "<Project><ItemGroup><PackageReference Include=\"Root.Package\" /></ItemGroup></Project>")
	testutil.MustWriteFile(t, filepath.Join(repo, "nested", "Nested.csproj"), "<Project><ItemGroup><PackageReference Include=\"Nested.Package\" /></ItemGroup></Project>")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect .NET projects: %v", err)
	}
	want := []string{repo, filepath.Join(repo, "nested")}
	if !slices.Equal(detection.Roots, want) {
		t.Fatalf("expected valid root and nested project roots to remain separate, got %#v", detection.Roots)
	}
}
