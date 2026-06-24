package analysis

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkChangedRootsIndexedLookup(b *testing.B) {
	repo := filepath.Join(b.TempDir(), "repo")
	const count = 4096
	roots := make([]string, 0, count)
	changedFiles := make([]string, 0, count)
	for i := 0; i < count; i++ {
		pkg := fmt.Sprintf("pkg-%04d", i)
		roots = append(roots, filepath.Join(repo, "packages", pkg))
		changedFiles = append(changedFiles, filepath.ToSlash(filepath.Join("packages", pkg, "src", "index.ts")))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := changedRoots(roots, repo, changedFiles); len(got) != count {
			b.Fatalf("expected %d changed roots, got %d", count, len(got))
		}
	}
}
