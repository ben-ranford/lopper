package cpp

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAdapterSmoke(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "CMakeLists.txt"), "project(demo)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "compile_commands.json"), `[
  {"directory":".","file":"src/main.cpp","command":"c++ -Iinclude -c src/main.cpp"}
]`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main.cpp"), `#include <fmt/core.h>
#include <fmt/format.h>
int main() { return 0; }
`)

	adapter := NewAdapter()
	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil || !detection.Matched || detection.Confidence <= 0 {
		t.Fatalf("unexpected detection result: detection=%#v err=%v", detection, err)
	}

	reportData, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: "fmt"})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Name != "fmt" || dep.UsedExportsCount != 2 || dep.TotalExportsCount != 2 || dep.UsedPercent != 100 {
		t.Fatalf("unexpected dependency report: %#v", dep)
	}
}

func TestAdapterMetadata(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "cpp" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	if !slices.Contains(adapter.Aliases(), "c++") || !slices.Contains(adapter.Aliases(), "cxx") {
		t.Fatalf("unexpected aliases: %#v", adapter.Aliases())
	}
}
