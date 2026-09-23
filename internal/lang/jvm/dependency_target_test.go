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
