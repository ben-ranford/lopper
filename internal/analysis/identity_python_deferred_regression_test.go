//go:build regressionproof

package analysis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pythonlang "github.com/ben-ranford/lopper/internal/lang/python"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestPythonDeferredCatalogIdentitySurvivesCacheWithoutSource(t *testing.T) {
	repo := t.TempDir()
	// Four exactly 16 MiB manifests fill the catalog's 64 MiB raw retention
	// budget. TOML comment padding is discarded during decoding, keeping the
	// retained documents and cache payload small. The fifth manifest overflows.
	header := "[project]\ndependencies=['padding-only==1.0']\n#"
	padding := header + strings.Repeat(" ", int(pythonlang.ManifestReadLimitBytes)-len(header)-1) + "\n"
	paths := make([]string, 0, 5)
	for i := range 4 {
		path := filepath.Join(repo, fmt.Sprintf("padding-%02d", i), "pyproject.toml")
		testutil.MustWriteFile(t, path, padding)
		paths = append(paths, path)
	}
	const deferredSource = "zz-deferred/pyproject.toml"
	deferredPath := filepath.Join(repo, filepath.FromSlash(deferredSource))
	testutil.MustWriteFile(t, deferredPath, "[project]\ndependencies=['requests==2.32.3']\n")
	paths = append(paths, deferredPath)
	testutil.MustWriteFile(t, filepath.Join(repo, "main.py"), "import requests\nrequests.get('https://example.test')\n")

	result, err := pythonlang.NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo, TopN: 10, Features: mustResolveDependencyIdentityPreviewFeatureSet(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.PythonManifestCatalog || len(result.PythonManifests) != len(paths) {
		t.Fatalf("expected all five catalog documents, got %d", len(result.PythonManifests))
	}
	deferred := result.PythonManifests[len(result.PythonManifests)-1]
	if deferred.Path != deferredSource || !deferred.Deferred {
		t.Fatalf("fixture did not overflow the real retention budget: path=%q deferred=%v", deferred.Path, deferred.Deferred)
	}
	cache, entry := cacheWithPayloadForLookupTest(t, newCachedPayload(result), "python-deferred-catalog-regression")
	cached, hit, err := cache.lookup(entry)
	if err != nil || !hit {
		t.Fatalf("cache lookup: hit=%v err=%v", hit, err)
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	result = mergeReports(repo, []report.Report{cached})
	annotateDependencyIdentities(repo, &result)
	assertIdentity(t, findIdentityDependency(t, result, "python", "requests"), report.DependencyIdentity{
		Ecosystem: "pypi", Name: "requests", Version: "2.32.3", VersionStatus: identityStatusDeclared,
		PURL: "pkg:pypi/requests@2.32.3", PURLStatus: identityStatusResolved,
		Source: deferredSource, Confidence: "high",
	})
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "identity manifest") || strings.Contains(warning, "identity evidence projection unavailable") {
			t.Fatalf("cached catalog identity reopened removed source: %s", warning)
		}
	}
}
