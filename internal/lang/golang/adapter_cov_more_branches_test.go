package golang

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestGoAdditionalBranchCoverage(t *testing.T) {
	t.Run("helper guard branches", testGoHelperGuardBranches)
	t.Run("repo bounded path guards", testGoRepoBoundedPathGuards)
	t.Run("module loading error branches", testGoModuleLoadingErrorBranches)
	t.Run("go work escape errors bubble up", testGoWorkEscapeErrorsBubbleUp)
	t.Run("nested replacements populate missing entries", testGoNestedReplacementImports)
}

func testGoHelperGuardBranches(t *testing.T) {
	t.Helper()

	weights := resolveRemovalCandidateWeights(&report.RemovalCandidateWeights{
		Usage:      -1,
		Impact:     2,
		Confidence: 3,
	})
	if weights.Usage < 0 || weights.Impact > 1 || weights.Confidence > 1 {
		t.Fatalf("expected removal weights to be normalized, got %#v", weights)
	}

	if walkGoFiles(context.Background(), filepath.Join(t.TempDir(), "missing"), nil, moduleInfo{}, &scanResult{}) == nil {
		t.Fatalf("expected walkGoFiles to fail for missing repo")
	}

	imports, metadata := parseImports([]byte("package main\nimport \"\"\n"), "main.go", moduleInfo{})
	if len(imports) != 0 || len(metadata) != 0 {
		t.Fatalf("expected blank import paths to be ignored, imports=%#v metadata=%#v", imports, metadata)
	}

	goBuildExpr, plusBuildExprs := extractBuildConstraintExpressions([]byte("\n//go:build linux\npackage main\n"))
	if goBuildExpr == nil || len(plusBuildExprs) != 0 {
		t.Fatalf("expected go:build expression after blank lines, go=%v plus=%#v", goBuildExpr, plusBuildExprs)
	}

	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git dir: %v", err)
	}
	nested, err := nestedModuleDirs(repo)
	if err != nil {
		t.Fatalf("nestedModuleDirs: %v", err)
	}
	if len(nested) != 0 {
		t.Fatalf("expected skipped .git directory not to count as nested module, got %#v", nested)
	}
}

func testGoRepoBoundedPathGuards(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	outside := filepath.Join(repo, "..", "outside")
	if _, ok := resolveRepoBoundedPath(repo, outside); ok {
		t.Fatalf("expected outside absolute path to be rejected")
	}
}

func testGoModuleLoadingErrorBranches(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, goModName), []byte(moduleDemoLine+"\n"), 0o600); err != nil {
		t.Fatalf("write root go.mod: %v", err)
	}

	goWorkDir := filepath.Join(repo, goWorkName)
	if err := os.Mkdir(goWorkDir, 0o755); err != nil {
		t.Fatalf("mkdir go.work dir: %v", err)
	}
	if _, err := loadGoModuleInfo(repo); err != nil {
		t.Fatalf("expected go.work directory to be ignored, got: %v", err)
	}
	if err := os.RemoveAll(goWorkDir); err != nil {
		t.Fatalf("remove go.work dir: %v", err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("permission-based nested module errors are not portable on windows")
	}

	blockedDir := filepath.Join(repo, "blocked")
	if err := os.Mkdir(blockedDir, 0o700); err != nil {
		t.Fatalf("mkdir blocked dir: %v", err)
	}
	if err := os.Chmod(blockedDir, 0o000); err != nil {
		t.Fatalf("chmod blocked dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(blockedDir, 0o700); err != nil {
			t.Fatalf("restore blocked dir perms: %v", err)
		}
	})
	if _, err := loadGoModuleInfo(repo); err == nil {
		t.Fatalf("expected unreadable nested directory to fail nested module discovery")
	}
}

func testGoWorkEscapeErrorsBubbleUp(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	outside := t.TempDir()
	outsideWork := filepath.Join(outside, goWorkName)
	writeFile(t, outsideWork, go125Line+"\n\nuse ./\n")
	if err := os.Symlink(outsideWork, filepath.Join(repo, goWorkName)); err != nil {
		t.Skipf(errSymlinkFmt, err)
	}

	if _, err := loadGoModuleInfo(repo); err == nil {
		t.Fatalf("expected escaping go.work symlink to fail module loading")
	}

	detection := language.Detection{}
	roots := map[string]struct{}{}
	if err := applyGoRootSignals(repo, &detection, roots); err == nil {
		t.Fatalf("expected escaping go.work symlink to fail root signal loading")
	}
}

func testGoNestedReplacementImports(t *testing.T) {
	t.Helper()

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "nested", "x", goModName), "module example.com/x\n\nreplace example.com/other => "+sharedForkImport+" v1.1.0\n")

	info := moduleInfo{ReplacementImports: map[string]string{}}
	if err := loadNestedModules(repo, &info); err != nil {
		t.Fatalf("loadNestedModules: %v", err)
	}
	if got := info.ReplacementImports[sharedForkImport]; got != "example.com/other" {
		t.Fatalf("expected nested replacement to be added, got %q", got)
	}
}
