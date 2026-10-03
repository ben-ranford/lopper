package python

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
	"os"
	"path/filepath"
	"reflect"
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

func TestPackagingCatalogNormalizesNonFiniteNumbers(t *testing.T) {
	var document report.PythonManifestDocument
	data := []byte("[tool]\nvalues=[nan,+inf,-inf,1.5]\n[tool.nested]\nlimit=nan\n")
	if err := decodePackagingDocument(pythonPyprojectFile, data, &document); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(document.Document)
	if err != nil {
		t.Fatalf("TOML catalog is not JSON-safe: %v", err)
	}
	if got, want := string(encoded), `{"tool":{"nested":{"limit":null},"values":[null,null,null,1.5]}}`; got != want {
		t.Fatalf("normalized document = %s, want %s", got, want)
	}
}

func TestPackagingCatalogReadFailures(t *testing.T) {
	for _, tc := range []struct {
		name, content string
	}{
		{pythonPyprojectFile, "[invalid"},
		{pythonPipfileLockName, "{invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, tc.name)
			testutil.MustWriteFile(t, path, tc.content)
			catalog := newPackagingCatalog()
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

func TestPackagingCatalogRetentionLimitPreservesInventory(t *testing.T) {
	repo := t.TempDir()
	catalog := newPackagingCatalog()
	catalog.bytes = maxPackagingCatalogBytes
	for _, tc := range []struct{ name, content string }{
		{pythonPyprojectFile, "[project]\ndependencies=['requests==2.32.3']\n"},
		{pythonRequirementsTxt, "requests==2.32.3\n"},
		{pythonPoetryLockName, "[[package]]\nname='requests'\nversion='2.32.3'\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(repo, tc.name)
			testutil.MustWriteFile(t, path, tc.content)
			for range 2 {
				deps, warnings, err := catalog.parse(repo, path)
				if _, ok := deps["requests"]; !ok || err != nil || len(warnings) != 0 {
					t.Fatalf("retention exhaustion changed inventory: %v %v %v", deps, warnings, err)
				}
			}
			doc := catalog.documents[path]
			if !doc.Deferred || doc.Document != nil || doc.Text != "" || doc.Failure != "" {
				t.Fatalf("expected deferred path without retained contents: %+v", doc)
			}
		})
	}
	if catalog.bytes != maxPackagingCatalogBytes || len(catalog.snapshot()) != 3 {
		t.Fatalf("retention accounting or discovery changed: %d %v", catalog.bytes, catalog.snapshot())
	}
}

func TestPackagingCatalogOverflowRetainsBoundedIdentityProjection(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonPyprojectFile)
	testutil.MustWriteFile(t, path, "[project]\ndependencies=['requests==2.32.3']\n[project.optional-dependencies]\ndocs=['docs-only==1.0']\n[tool.poetry.dependencies]\npytest='==8.0.0'\n[tool.unrelated]\nlarge='discard this field'\n")
	catalog := newPackagingCatalog()
	catalog.bytes = maxPackagingCatalogBytes
	_, err := catalog.read(repo, path)
	if err != nil {
		t.Fatal(err)
	}
	stored := catalog.documents[path]
	if !stored.Deferred || !stored.IdentityProjectionSet || stored.IdentityProjectionError != "" {
		t.Fatalf("deferred projection not retained: %+v", stored)
	}
	if stored.Document != nil || stored.Text != "" || catalog.identityBytes == 0 || catalog.identityBytes > maxPackagingIdentityProjectionBytes {
		t.Fatalf("deferred catalog retained full document or exceeded projection budget: doc=%+v bytes=%d", stored, catalog.identityBytes)
	}
	if _, ok := stored.IdentityProjection["project"]; !ok {
		t.Fatalf("project identity fields missing: %#v", stored.IdentityProjection)
	}
	if _, ok := stored.IdentityProjection["dependency-groups"]; ok {
		t.Fatalf("unrelated pyproject fields were retained: %#v", stored.IdentityProjection)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func TestPackagingCatalogOverflowReportsProjectionBudget(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonRequirementsTxt)
	testutil.MustWriteFile(t, path, "requests==2.32.3\n")
	catalog := newPackagingCatalog()
	catalog.bytes = maxPackagingCatalogBytes
	catalog.identityBytes = maxPackagingIdentityProjectionBytes
	document, err := catalog.read(repo, path)
	if err != nil {
		t.Fatal(err)
	}
	stored := catalog.documents[path]
	if !stored.Deferred || !stored.IdentityProjectionSet || stored.IdentityProjectionError == "" || stored.IdentityText != "" {
		t.Fatalf("projection overflow was not represented explicitly: %+v", stored)
	}
	if document.Text != "requests==2.32.3\n" {
		t.Fatalf("inventory lost initial decoded content: %q", document.Text)
	}
}

func TestPackagingCatalogOverflowOmitsUnrelatedProjectMetadata(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, pythonPyprojectFile)
	content := "[project]\nname='metadata-only'\nauthors=[{name='Ignored Author'}]\nreadme='" + strings.Repeat("unrelated metadata ", 256) + "'\ndependencies=['requests==2.32.3']\n[project.optional-dependencies]\ndocs=['sphinx==8.0.2']\n"
	testutil.MustWriteFile(t, path, content)
	catalog := newPackagingCatalog()
	catalog.bytes = maxPackagingCatalogBytes
	catalog.identityBytes = maxPackagingIdentityProjectionBytes - 256
	if _, err := catalog.read(repo, path); err != nil {
		t.Fatal(err)
	}
	document := catalog.documents[path]
	if !document.Deferred || !document.IdentityProjectionSet || document.IdentityProjectionError != "" {
		t.Fatalf("unrelated project metadata exhausted the projection budget: %+v", document)
	}
	encoded, err := json.Marshal(document.IdentityProjection)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"project":{"dependencies":["requests==2.32.3"],"optional-dependencies":{"docs":["sphinx==8.0.2"]}}}`
	if string(encoded) != want {
		t.Fatalf("project identity fields = %s, want %s", encoded, want)
	}
	if catalog.identityBytes <= maxPackagingIdentityProjectionBytes-256 || catalog.identityBytes > maxPackagingIdentityProjectionBytes {
		t.Fatalf("projection budget accounting changed: %d", catalog.identityBytes)
	}
}

func TestPackagingCatalogOverflowProjectionsCoverIdentityFormats(t *testing.T) {
	for _, tc := range []struct {
		name, content, key string
		wantText           string
	}{
		{pythonPyprojectFile, "[project]\ndependencies=['requests==2.32.3']\n[tool.poetry.dependencies]\npytest='==8.0.0'\n", "project", ""},
		{pythonPipfileName, "[packages]\nrequests='==2.32.3'\n", "packages", ""},
		{pythonPoetryLockName, "[[package]]\nname='requests'\nversion='2.32.3'\n", "package", ""},
		{pythonUVLockName, "[[package]]\nname='requests'\nversion='2.32.3'\n", "package", ""},
		{pythonPipfileLockName, `{"default":{"requests":{"version":"==2.32.3"}}}`, "default", ""},
		{pythonRequirementsTxt, "requests==2.32.3\n", "", "requests==2.32.3\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, tc.name)
			testutil.MustWriteFile(t, path, tc.content)
			catalog := newPackagingCatalog()
			catalog.bytes = maxPackagingCatalogBytes
			if _, err := catalog.read(repo, path); err != nil {
				t.Fatal(err)
			}
			document := catalog.documents[path]
			if !document.Deferred || !document.IdentityProjectionSet || document.IdentityProjectionError != "" {
				t.Fatalf("missing format projection: %+v", document)
			}
			if tc.wantText != "" {
				if document.IdentityText != tc.wantText {
					t.Fatalf("identity text = %q, want %q", document.IdentityText, tc.wantText)
				}
				return
			}
			if _, ok := document.IdentityProjection[tc.key]; !ok {
				t.Fatalf("projection omitted %q: %#v", tc.key, document.IdentityProjection)
			}
		})
	}
}

func TestPackagingCatalogOverflowOmitsPackageAndGroupMetadata(t *testing.T) {
	padding := strings.Repeat("irrelevant metadata ", 256)
	for _, tc := range []struct{ name, content, want string }{
		{
			pythonPipfileName,
			"[packages]\nrequests={version='==2.32.3', markers='%[1]s', extras=['%[1]s']}\n[dev-packages]\npytest={version='==8.0.0', hashes=['%[1]s']}\n",
			`{"dev-packages":{"pytest":{"version":"==8.0.0"}},"packages":{"requests":{"version":"==2.32.3"}}}`,
		},
		{
			pythonPyprojectFile,
			"[tool.poetry.dependencies]\nrequests={version='2.32.3', markers='%[1]s', extras=['%[1]s']}\n[tool.poetry.dev-dependencies]\npytest={version='8.0.0', python='%[1]s'}\n[tool.poetry.group.docs]\noptional=false\ndescription='%[1]s'\n[tool.poetry.group.docs.dependencies]\nsphinx={version='8.0.2', markers='%[1]s'}\n",
			`{"tool":{"poetry":{"dependencies":{"requests":{"version":"2.32.3"}},"dev-dependencies":{"pytest":{"version":"8.0.0"}},"group":{"docs":{"dependencies":{"sphinx":{"version":"8.0.2"}},"optional":false}}}}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			path := filepath.Join(repo, tc.name)
			testutil.MustWriteFile(t, path, fmt.Sprintf(tc.content, padding))
			catalog := newPackagingCatalog()
			catalog.bytes = maxPackagingCatalogBytes
			catalog.identityBytes = maxPackagingIdentityProjectionBytes - 512
			if _, err := catalog.read(repo, path); err != nil {
				t.Fatal(err)
			}
			document := catalog.documents[path]
			if !document.IdentityProjectionSet || document.IdentityProjectionError != "" {
				t.Fatalf("unrelated package metadata exhausted the projection budget: %+v", document)
			}
			encoded, err := json.Marshal(document.IdentityProjection)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != tc.want {
				t.Fatalf("identity fields = %s, want %s", encoded, tc.want)
			}
		})
	}
}

func TestPythonIdentityPackageProjectionPreservesFlagsAndShapes(t *testing.T) {
	entries := map[string]any{
		"string":      "2.32.3",
		"nil":         nil,
		"nil-table":   map[string]any(nil),
		"malformed":   []any{"==2.32.3"},
		"bad-version": map[string]any{"version": []any{"==2.32.3"}},
	}
	want := make(map[string]any, len(entries))
	for name, entry := range entries {
		want[name] = entry
	}
	for _, flag := range []struct {
		name, field string
		value       any
	}{
		{"optional-true", "optional", true},
		{"optional-false", "optional", false},
		{"optional-string", "optional", "true"},
		{"file", "file", nil},
		{"git", "git", false},
		{"path", "path", ""},
		{"ref", "ref", int64(0)},
		{"url", "url", []any{"source"}},
	} {
		entries[flag.name] = map[string]any{"version": "==2.32.3", flag.field: flag.value, "markers": "omit", "extras": []any{"omit"}}
		want[flag.name] = map[string]any{"version": "==2.32.3", flag.field: flag.value}
	}
	if projected := pythonIdentityPackageTable(entries); !reflect.DeepEqual(projected, want) {
		t.Fatalf("package projection changed rejection flags or malformed entries: got %#v, want %#v", projected, want)
	}
	for _, shape := range []any{nil, map[string]any(nil), "malformed", []any{"malformed"}} {
		if projected := pythonIdentityPackageTable(shape); !reflect.DeepEqual(projected, shape) {
			t.Fatalf("package table shape changed: %#v to %#v", shape, projected)
		}
		if projected := poetryIdentityGroups(shape); !reflect.DeepEqual(projected, shape) {
			t.Fatalf("Poetry groups shape changed: %#v to %#v", shape, projected)
		}
		if projected := poetryIdentityGroup(shape); !reflect.DeepEqual(projected, shape) {
			t.Fatalf("Poetry group shape changed: %#v to %#v", shape, projected)
		}
	}
}
