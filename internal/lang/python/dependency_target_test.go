package python

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAdapterAnalyseExplicitDependencyOverridesTopN(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "main.py"), "import requests\nimport numpy as np\nnp.array([1])\n")
	testutil.AssertExplicitDependencyOverridesTopN(t,
		[]string{"numpy", " NUMPY ", "bs4", "missing"},
		func(dependency string, topN int) language.Request {
			return language.Request{RepoPath: repo, Dependency: dependency, TopN: topN}
		},
		NewAdapter().Analyse,
		func(result report.Result) (int, any) {
			return len(result.Dependencies), []any{result.Dependencies, result.Warnings}
		},
	)
}

func TestAdapterAnalyseEmptyNormalizedDependencyUsesTopN(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "main.py"), "import requests\nimport numpy\n")
	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, Dependency: " \t ", TopN: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dependencies) != 2 {
		t.Fatalf("expected top-N dependencies for blank target, got %#v", result.Dependencies)
	}
}
