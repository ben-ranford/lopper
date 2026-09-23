package jvm

import (
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAdapterAnalyseExplicitDependencyOverridesTopN(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), `dependencies { implementation "com.squareup.okhttp3:okhttp:4.12.0" }`)
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "import okhttp3.OkHttpClient;\nimport org.junit.jupiter.api.Assertions;\nclass Main { Object client = new OkHttpClient(); }\n")
	testutil.AssertExplicitDependencyOverridesTopN(t,
		[]string{"okhttp", " OKHTTP ", "missing"},
		func(dependency string, topN int) language.Request {
			return language.Request{RepoPath: repo, Dependency: dependency, TopN: topN}
		},
		NewAdapter().Analyse,
		func(result report.Result) (int, any) {
			return len(result.Dependencies), []any{result.Dependencies, result.Warnings}
		},
	)
}

func TestAdapterAnalyseWhitespaceDependencyPreservesTopN(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), `dependencies { implementation "com.squareup.okhttp3:okhttp:4.12.0" }`)
	testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "import okhttp3.OkHttpClient;\nclass Main { Object client = new OkHttpClient(); }\n")

	result, err := NewAdapter().Analyse(t.Context(), language.Request{RepoPath: repo, Dependency: " \t ", TopN: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Dependencies) != 1 || result.Dependencies[0].Name != "okhttp" {
		t.Fatalf("whitespace dependency disabled TopN: got %#v", result.Dependencies)
	}
}
