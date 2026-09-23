package golang

import (
	"go/build"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyBuildConstraintInPackageDocsDoesNotExcludeFile(t *testing.T) {
	content := []byte(strings.Join([]string{
		"// Package main documents the package.",
		"// +build lopper_never_enabled",
		packageMainLine,
		"",
	}, "\n"))
	path := filepath.Join(t.TempDir(), fileMainGo)
	writeFile(t, path, string(content))

	included, err := build.Default.MatchFile(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		t.Fatalf("MatchFile: %v", err)
	}
	if !included {
		t.Fatal("Go toolchain excluded file with legacy constraint in package documentation")
	}
	if !matchesActiveBuild(content) {
		t.Fatal("adapter excluded file with legacy constraint in package documentation")
	}
}

func TestInactiveLegacyBuildConstraintWithHeaderSeparationExcludesFile(t *testing.T) {
	content := []byte(strings.Join([]string{
		"// +build lopper_never_enabled",
		"",
		packageMainLine,
		"",
	}, "\n"))
	if matchesActiveBuild(content) {
		t.Fatal("adapter included file with inactive legacy build constraint")
	}
}
