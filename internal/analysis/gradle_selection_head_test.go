package analysis

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	jvmlang "github.com/ben-ranford/lopper/internal/lang/jvm"
	kotlinlang "github.com/ben-ranford/lopper/internal/lang/kotlinandroid"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestGradleFatalDiscoveryAcrossSelectionModes(t *testing.T) {
	for _, adapter := range []language.CandidateAdapter{jvmlang.NewAdapter(), kotlinlang.NewAdapter()} {
		for _, mode := range []string{adapter.ID(), "auto", "all"} {
			t.Run(adapter.ID()+"/"+mode, func(t *testing.T) { assertGradleSelectionFailure(t, adapter, mode) })
		}
	}
}

func assertGradleSelectionFailure(t *testing.T, adapter language.CandidateAdapter, mode string) {
	t.Helper()
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "build.gradle"), "implementation 'org.example:demo:1'\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "settings.gradle"), `dependencyResolutionManagement { versionCatalogs { create("libs") { from(files("missing.toml")) } } }`)
	registry := language.NewRegistry()
	if err := registry.Register(&testServiceAdapter{id: adapter.ID(), detect: language.Detection{Matched: true, Confidence: 100}, analyseFn: adapter.Analyse}); err != nil {
		t.Fatal(err)
	}
	req := newCacheRequest(t, repo, t.TempDir(), false)
	req.Language = mode
	result, err := (&Service{Registry: registry}).Analyse(t.Context(), req)
	var failure *shared.GradleDiscoveryError
	if !errors.As(err, &failure) || !errors.Is(err, os.ErrNotExist) || !reflect.DeepEqual(result, report.Report{}) {
		t.Fatalf("selection %s downgraded Gradle failure: %+v %v", mode, result, err)
	}
	assertGradleCacheUnpublished(t, req.Cache.Path)
}
