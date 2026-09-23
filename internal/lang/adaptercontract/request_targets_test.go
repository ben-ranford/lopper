package adaptercontract_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/lang/python"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestExplicitDependencyOverridesTopN(t *testing.T) {
	t.Run("JVM", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), `dependencies { implementation "com.squareup.okhttp3:okhttp:4.12.0" }`)
		testutil.MustWriteFile(t, filepath.Join(repo, "Main.java"), "import okhttp3.OkHttpClient;\nclass Main { Object client = new OkHttpClient(); }\n")
		assertExplicitDependencyOverridesTopN(t, repo, []string{"okhttp", " OKHTTP ", "missing"}, jvm.NewAdapter().Analyse)
	})
	t.Run("Python", func(t *testing.T) {
		repo := t.TempDir()
		testutil.MustWriteFile(t, filepath.Join(repo, "main.py"), "import requests\nimport numpy as np\nnp.array([1])\n")
		assertExplicitDependencyOverridesTopN(t, repo, []string{"numpy", " NUMPY ", "bs4", "missing"}, python.NewAdapter().Analyse)
	})
}

func assertExplicitDependencyOverridesTopN(t *testing.T, repo string, dependencies []string, analyse func(context.Context, language.Request) (report.Result, error)) {
	t.Helper()
	for _, dependency := range dependencies {
		t.Run(dependency, func(t *testing.T) {
			request := language.Request{RepoPath: repo, Dependency: dependency}
			expected, err := analyse(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if len(expected.Dependencies) != 1 {
				t.Fatalf("expected one explicit dependency, got %d", len(expected.Dependencies))
			}

			request.TopN = 2
			actual, err := analyse(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if len(actual.Dependencies) != 1 || !reflect.DeepEqual(actual.Dependencies, expected.Dependencies) || !reflect.DeepEqual(actual.Warnings, expected.Warnings) {
				t.Fatalf("TopN changed explicit dependency report: got %#v with warnings %#v, want %#v with warnings %#v", actual.Dependencies, actual.Warnings, expected.Dependencies, expected.Warnings)
			}
		})
	}
}
