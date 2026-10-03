package analysis

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestPythonCatalogDeferredIdentityProjectionSurvivesMissingSource(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "pyproject.toml")
	testutil.MustWriteFile(t, path, "[project]\ndependencies=['requests==2.32.3']\n")
	result := report.Report{PythonManifestCatalog: true, PythonManifests: []report.PythonManifestDocument{
		{Path: "pyproject.toml", Deferred: true, IdentityProjectionSet: true, IdentityProjection: map[string]any{
			"project": map[string]any{"dependencies": []any{"requests==2.32.3"}},
		}},
		{Path: "missing/requirements.txt", Deferred: true},
	}, Dependencies: []report.DependencyReport{{Language: "python", Name: "requests"}}}
	payload, err := json.Marshal(result.PythonManifests)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &result.PythonManifests); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	annotateDependencyIdentities(repo, &result)
	if result.Dependencies[0].Identity == nil || result.Dependencies[0].Identity.Version != "2.32.3" {
		t.Fatalf("deferred evidence lost: %+v", result.Dependencies)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("expected missing deferred document warning")
	}
	if strings.Contains(strings.Join(result.Warnings, "\n"), "pyproject.toml") {
		t.Fatalf("deferred projection reread the removed source: %v", result.Warnings)
	}
}

func TestPythonCatalogDeferredProjectionLimitIsExplicitAndDoesNotReadSource(t *testing.T) {
	repo := t.TempDir()
	result := report.Report{PythonManifestCatalog: true, PythonManifests: []report.PythonManifestDocument{{
		Path:                    "pyproject.toml",
		Deferred:                true,
		IdentityProjectionSet:   true,
		IdentityProjectionError: "Python identity projection exceeds the 67108864-byte catalog limit",
	}}, Dependencies: []report.DependencyReport{{Language: "python", Name: "requests"}}}
	annotateDependencyIdentities(repo, &result)
	identity := result.Dependencies[0].Identity
	if identity == nil || identity.Version != "" || len(identity.Evidence) != 0 {
		t.Fatalf("projection-budget failure produced package evidence: %+v", identity)
	}
	joined := strings.Join(result.Warnings, "\n")
	if !strings.Contains(joined, "identity projection exceeds") || !strings.Contains(joined, "pyproject.toml") {
		t.Fatalf("projection cap was not made explicit: %v", result.Warnings)
	}
}
