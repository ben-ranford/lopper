package golang

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestGoAdditionalBranchCoverage(t *testing.T) {
	t.Run("helper guard branches", func(t *testing.T) {
		weights := resolveRemovalCandidateWeights(&report.RemovalCandidateWeights{
			Usage:      -1,
			Impact:     2,
			Confidence: 3,
		})
		if weights.Usage < 0 || weights.Impact > 1 || weights.Confidence > 1 {
			t.Fatalf("expected removal weights to be normalized, got %#v", weights)
		}

		if err := walkGoFiles(context.Background(), filepath.Join(t.TempDir(), "missing"), nil, moduleInfo{}, &scanResult{}); err == nil {
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
	})

	t.Run("repo bounded path guards", func(t *testing.T) {
		repo := t.TempDir()
		outside := filepath.Join(repo, "..", "outside")
		if _, ok := resolveRepoBoundedPath(repo, outside); ok {
			t.Fatalf("expected outside absolute path to be rejected")
		}
	})
}
