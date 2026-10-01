package golang

import (
	"reflect"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestAdapterAnalyseDependencyTakesPrecedenceOverTopN(t *testing.T) {
	repo := t.TempDir()
	writeRepoGoMod(t, repo, goModDemoWithUUID)
	writeRepoMain(t, repo, mainUUIDNoopProgram)

	for _, dependency := range []string{depUUID, depLo, "  GITHUB.COM/GOOGLE/UUID  "} {
		t.Run(dependency, func(t *testing.T) {
			request := language.Request{RepoPath: repo, Dependency: dependency}
			want := analyseReport(t, request)
			request.TopN = 2
			got := analyseReport(t, request)
			requireDependencyCount(t, got, 1)
			if !reflect.DeepEqual(got.Dependencies, want.Dependencies) {
				t.Fatalf("dependency with TopN differs from explicit dependency: got %#v, want %#v", got.Dependencies, want.Dependencies)
			}
			if !reflect.DeepEqual(got.Warnings, want.Warnings) || !reflect.DeepEqual(got.Summary, want.Summary) {
				t.Fatalf("dependency with TopN changed warnings or summary: got %#v, want %#v", got, want)
			}
		})
	}
}
