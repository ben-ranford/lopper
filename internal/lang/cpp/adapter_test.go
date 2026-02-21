package cpp

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAdapterDetectWithCompileDatabaseAndCMake(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "CMakeLists.txt"), "cmake_minimum_required(VERSION 3.20)\nproject(demo)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "compile_commands.json"), `[
  {
    "directory": ".",
    "file": "src/main.cpp",
    "command": "c++ -Iinclude -c src/main.cpp"
  }
]`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main.cpp"), "#include <fmt/core.h>\nint main() { return 0; }\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected cpp adapter to match")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected confidence > 0, got %d", detection.Confidence)
	}
	if len(detection.Roots) == 0 {
		t.Fatalf("expected at least one detection root")
	}
}

func TestAdapterAnalyseDependency(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "compile_commands.json"), `[
  {
    "directory": ".",
    "file": "src/main.cpp",
    "command": "c++ -Iinclude -isystem /usr/include -c src/main.cpp"
  }
]`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main.cpp"), `#include <fmt/core.h>
#include <fmt/format.h>
#include <vector>

int main() { return 0; }
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "fmt",
	})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}

	dependency := reportData.Dependencies[0]
	if dependency.Name != "fmt" {
		t.Fatalf("expected dependency fmt, got %q", dependency.Name)
	}
	if dependency.UsedExportsCount != 2 || dependency.TotalExportsCount != 2 {
		t.Fatalf("expected two mapped includes, got used=%d total=%d", dependency.UsedExportsCount, dependency.TotalExportsCount)
	}
	if dependency.UsedPercent != 100 {
		t.Fatalf("expected used percent 100, got %f", dependency.UsedPercent)
	}
	if len(dependency.UsedImports) != 2 {
		t.Fatalf("expected used imports for fmt includes, got %#v", dependency.UsedImports)
	}
}

func TestAdapterAnalyseTopNAndUnresolvedWarnings(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "Makefile"), "all:\n\t@echo building\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main.cpp"), `#include <openssl/ssl.h>
#include SOME_HEADER
#include "missing_header.hpp"

int main() { return 0; }
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}

	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected top dependencies")
	}
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "openssl") {
		t.Fatalf("expected openssl dependency, got %#v", names)
	}
	if !containsWarning(reportData.Warnings, "compile_commands.json not found") {
		t.Fatalf("expected compile database warning, got %#v", reportData.Warnings)
	}
	if !containsWarning(reportData.Warnings, "include mapping unresolved") {
		t.Fatalf("expected unresolved include warning, got %#v", reportData.Warnings)
	}
}

func TestAdapterMetadataAndAliases(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "cpp" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	aliases := adapter.Aliases()
	if !slices.Contains(aliases, "c++") || !slices.Contains(aliases, "cxx") {
		t.Fatalf("expected aliases to include c++ and cxx, got %#v", aliases)
	}
}

func containsWarning(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(strings.ToLower(warning), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
