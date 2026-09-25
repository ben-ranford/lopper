package python

import (
	"context"
	"fmt"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagingCatalogReadsAndDecodesEachManifestOnce(t *testing.T) {
	repo := t.TempDir()
	catalog := newPackagingCatalog()
	for _, tc := range []struct{ name, content string }{
		{pythonPyprojectFile, "[project]\ndependencies=['requests==2.32.3']\n"},
		{pythonPipfileName, "[packages]\nrequests='==2.32.3'\n"},
		{pythonPoetryLockName, "[[package]]\nname='requests'\nversion='2.32.3'\n"},
		{pythonUVLockName, "[[package]]\nname='requests'\nversion='2.32.3'\n"},
		{pythonPipfileLockName, `{"default":{"requests":{"version":"==2.32.3"}}}`},
		{pythonRequirementsTxt, "requests==2.32.3\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(repo, tc.name)
			testutil.MustWriteFile(t, path, tc.content)
			before, warnings, err := catalog.parse(repo, path)
			if err != nil || len(warnings) > 0 || len(before) != 1 {
				t.Fatalf("initial parse: %v %v %v", before, warnings, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			after, warnings, err := catalog.parse(repo, path)
			if _, ok := after["requests"]; !ok || err != nil || len(warnings) > 0 {
				t.Fatalf("cached parse: %v %v %v", after, warnings, err)
			}
		})
	}
	if len(catalog.snapshot()) != 6 {
		t.Fatalf("catalog size %d", len(catalog.snapshot()))
	}
}

func TestPackagingCatalogReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		parse         bool
	}{
		{pythonPyprojectFile, "[invalid", true},
		{pythonPipfileLockName, "{invalid", true},
		{pythonRequirementsTxt, "requests==1.0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, tc.name)
			testutil.MustWriteFile(t, path, tc.content)
			catalog := newPackagingCatalog()
			if !tc.parse {
				catalog.bytes = maxPackagingCatalogBytes
			}
			doc, err := catalog.read(repo, path)
			if err == nil || doc.Failure == "" {
				t.Fatalf("expected bounded decode error: %#v %v", doc, err)
			}
			deps, warnings, err := catalog.parse(repo, path)
			if err != nil || len(deps) != 0 || len(warnings) != 1 {
				t.Fatalf("parse failure: %v %v %v", deps, warnings, err)
			}
		})
	}
	catalog := newPackagingCatalog()
	repo := t.TempDir()
	deps, warnings, err := catalog.parse(repo, filepath.Join(repo, pythonPyprojectFile))
	if err != nil || len(warnings) != 0 || len(deps) != 0 {
		t.Fatalf("missing file: %v %v %v", deps, warnings, err)
	}
}

func TestAdapterCatalogKeepsIdentityEvidenceSeparateFromInventory(t *testing.T) {
	for _, manifest := range []bool{false, true} {
		t.Run(fmt.Sprint(manifest), func(t *testing.T) {
			assertAdapterCatalogInventory(t, manifest)
		})
	}
}

func TestPackagingCatalogBoundedReadAndFailurePolicies(t *testing.T) {
	for _, name := range []string{pythonPyprojectFile, pythonPipfileLockName, pythonPoetryLockName, pythonRequirementsTxt} {
		t.Run(name, func(t *testing.T) {
			assertPackagingCatalogBoundedRead(t, name)
		})
	}
	repo := t.TempDir()
	path := filepath.Join(repo, pythonRequirementsTxt)
	if _, _, err := parseRequirementsDependencies(repo, path); err != nil {
		t.Fatal(err)
	}
	testutil.MustWriteFile(t, path, strings.Repeat(" ", int(PackagingReadLimitBytes)+1))
	if _, warnings, err := parseRequirementsDependencies(repo, path); err != nil || len(warnings) != 1 {
		t.Fatalf("requirements bounds: %v %v", warnings, err)
	}
}

func assertAdapterCatalogInventory(t *testing.T, manifest bool) {
	t.Helper()
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, pythonPoetryLockName), "[[package]]\nname='requests'\nversion='2.32.3'\n")
	if manifest {
		testutil.MustWriteFile(t, filepath.Join(repo, pythonPyprojectFile), "[project]\ndependencies=['requests>=2']\n[project.optional-dependencies]\ndocs=['optional-only==1.0']\n")
	}
	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{Channel: featureflags.ChannelDev, Enable: []string{report.DependencyIdentityPreviewFeature}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10, Features: features})
	if err != nil {
		t.Fatal(err)
	}
	if !result.PythonManifestCatalog || len(result.PythonManifests) == 0 {
		t.Fatal("adapter did not retain catalog")
	}
	if len(result.Dependencies) != 1 || result.Dependencies[0].Name != "requests" {
		t.Fatalf("optional evidence changed inventory: %+v", result.Dependencies)
	}
}

func assertPackagingCatalogBoundedRead(t *testing.T, name string) {
	t.Helper()
	repo := t.TempDir()
	path := filepath.Join(repo, name)
	limit := PackagingReadLimitBytes
	if name == pythonPyprojectFile {
		limit = ManifestReadLimitBytes
	}
	testutil.MustWriteFile(t, path, strings.Repeat(" ", int(limit)+1))
	document, err := ReadPackagingDocument(repo, path)
	if err == nil || document.FailureKind != "large" {
		t.Fatalf("bounded read: %+v %v", document, err)
	}
	dependencies, warnings, err := newPackagingCatalog().parse(repo, path)
	if name == pythonPyprojectFile {
		if err == nil {
			t.Fatal("required manifest read failure was ignored")
		}
		return
	}
	if err != nil || len(warnings) != 1 || len(dependencies) != 0 {
		t.Fatalf("bounded optional parse: %v %v %v", dependencies, warnings, err)
	}
}
