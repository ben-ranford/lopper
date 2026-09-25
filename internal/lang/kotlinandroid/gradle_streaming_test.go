package kotlinandroid

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

// Sampling live memory inside the parser catches discovery that buffers every
// input before invoking the first parser, without depending on GC scheduling.
func TestBuildFilesParseWithoutRetainingAggregateContents(t *testing.T) {
	repo := t.TempDir()
	const fileCount = 16
	const fileBytes = 1 << 20
	for i := range fileCount {
		testutil.MustWriteFile(t, filepath.Join(repo, fmt.Sprintf("module-%02d", i), buildGradleName), strings.Repeat(" ", fileBytes))
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	parsed := 0
	descriptors, warnings := parseBuildFilesWithWarnings(repo, func(content string) []dependencyDescriptor {
		runtime.GC()
		var current runtime.MemStats
		runtime.ReadMemStats(&current)
		if current.HeapAlloc > before.HeapAlloc+8*fileBytes {
			t.Fatalf("parser retains aggregate input: live heap grew by %d bytes", current.HeapAlloc-before.HeapAlloc)
		}
		if len(content) != fileBytes {
			t.Fatalf("content length = %d", len(content))
		}
		parsed++
		return []dependencyDescriptor{{
			Name:     fmt.Sprintf("dependency-%d", parsed),
			Group:    content[:1],
			Artifact: fmt.Sprintf("dependency-%d", parsed),
			Version:  content[:1],
		}}
	}, buildGradleName)
	if parsed != fileCount || len(descriptors) != fileCount || len(warnings) != 0 {
		t.Fatalf("parsed %d files; warnings %v", parsed, warnings)
	}
}

func TestGradleStreamingPreservesDuplicatePrecedence(t *testing.T) {
	repo := t.TempDir()
	for i, version := range []string{"1", "2"} {
		dir := filepath.Join(repo, fmt.Sprintf("module-%d", i))
		testutil.MustWriteFile(t, filepath.Join(dir, buildGradleName), "implementation 'org.example:widget:"+version+"'\n")
		testutil.MustWriteFile(t, filepath.Join(dir, gradleLockfileName), "org.example:widget:"+version+"=runtimeClasspath\n")
	}
	manifest, lock, matched, warnings := collectGradleDeclaredDependencyDescriptors(repo)
	if !matched || len(warnings) != 0 || len(manifest) != 1 || len(lock) != 1 {
		t.Fatalf("manifest=%v lock=%v matched=%v warnings=%v", manifest, lock, matched, warnings)
	}
	if manifest[0].Version != "1" || !manifest[0].FromManifest || lock[0].Version != "1" {
		t.Fatalf("first file precedence changed: manifest=%v lock=%v", manifest, lock)
	}
}
