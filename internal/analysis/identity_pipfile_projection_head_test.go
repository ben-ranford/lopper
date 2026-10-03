package analysis

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestPipfileProjectionPreservesInvalidSectionWarnings(t *testing.T) {
	repo := t.TempDir()
	large := strings.Repeat("ignored malformed payload ", 256)
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"string section", large},
		{"array section", []any{large}},
		{"boolean section", false},
		{"numeric section", float64(42)},
		{"string entry", pipfileSectionWithInvalidEntry(large)},
		{"array entry", pipfileSectionWithInvalidEntry([]any{large})},
		{"boolean entry", pipfileSectionWithInvalidEntry(false)},
		{"numeric entry", pipfileSectionWithInvalidEntry(float64(1e300))},
		{"array version", pipfileSectionWithInvalidEntry(map[string]any{"version": []any{large}})},
		{"object version", pipfileSectionWithInvalidEntry(map[string]any{"version": map[string]any{"ignored": large}})},
		{"boolean version", pipfileSectionWithInvalidEntry(map[string]any{"version": false})},
		{"numeric version", pipfileSectionWithInvalidEntry(map[string]any{"version": float64(1e300)})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			develop := map[string]any{"pytest": map[string]any{"version": "==8.0.0"}}
			original := pipfileIdentityProjectionReport(t, repo, map[string]any{"default": tc.value, "develop": develop}, false)
			projected := pipfileIdentityProjectionReport(t, repo, map[string]any{"default": false, "develop": develop}, true)
			assertPipfileProjectionEquivalent(t, original, projected)
			assertWarningsExact(t, repo, projected.Warnings, []string{"identity manifest parse failed for Pipfile.lock default section: invalid JSON"})
			assertPipfileResolvedIdentity(t, projected, "pytest", "8.0.0")
		})
	}
}

func TestPipfileProjectionPreservesNullSections(t *testing.T) {
	repo := t.TempDir()
	for _, present := range []bool{false, true} {
		document := map[string]any{"develop": map[string]any{"pytest": map[string]any{"version": "==8.0.0"}}}
		if present {
			document["default"] = nil
		}
		original := pipfileIdentityProjectionReport(t, repo, document, false)
		projected := pipfileIdentityProjectionReport(t, repo, document, true)
		assertPipfileProjectionEquivalent(t, original, projected)
		assertWarningsExact(t, repo, projected.Warnings, nil)
		assertPipfileResolvedIdentity(t, projected, "pytest", "8.0.0")
	}
}

func TestPipfileProjectionPreservesNullPackageEvidence(t *testing.T) {
	repo := t.TempDir()
	original := pipfileIdentityProjectionReport(t, repo, map[string]any{
		"default": map[string]any{
			"null-entry":  nil,
			"nil-version": map[string]any{"version": nil, "markers": "ignored"},
			"empty":       map[string]any{},
			"requests":    map[string]any{"version": "==2.32.3", "markers": "ignored"},
		},
		"develop": nil,
	}, false)
	projected := pipfileIdentityProjectionReport(t, repo, map[string]any{
		"default": map[string]any{
			"null-entry":  nil,
			"nil-version": map[string]any{"version": nil},
			"empty":       map[string]any{},
			"requests":    map[string]any{"version": "==2.32.3"},
		},
		"develop": nil,
	}, true)
	assertPipfileProjectionEquivalent(t, original, projected)
	assertWarningsExact(t, repo, projected.Warnings, nil)
	assertPipfileResolvedIdentity(t, projected, "requests", "2.32.3")
	for _, name := range []string{"null-entry", "nil-version", "empty"} {
		assertIdentity(t, findIdentityDependency(t, projected, "python", name), report.DependencyIdentity{
			Ecosystem: "pypi", Name: name, VersionStatus: identityStatusUnknown,
			PURLStatus: identityPURLUnavailable, Source: "Pipfile.lock", Confidence: "high",
		})
	}
}

func pipfileSectionWithInvalidEntry(value any) map[string]any {
	return map[string]any{"invalid": value, "requests": map[string]any{"version": "==2.32.3"}}
}

func pipfileIdentityProjectionReport(t *testing.T, repo string, contents map[string]any, deferred bool) report.Report {
	t.Helper()
	document := report.PythonManifestDocument{Path: "Pipfile.lock", Document: contents}
	if deferred {
		document.Document = nil
		document.Deferred = true
		document.IdentityProjectionSet = true
		document.IdentityProjection = contents
	}
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var cached report.PythonManifestDocument
	if err := json.Unmarshal(payload, &cached); err != nil {
		t.Fatal(err)
	}
	result := report.Report{PythonManifestCatalog: true, PythonManifests: []report.PythonManifestDocument{cached}}
	for _, name := range []string{"requests", "pytest", "null-entry", "nil-version", "empty"} {
		result.Dependencies = append(result.Dependencies, report.DependencyReport{Language: "python", Name: name})
	}
	annotateDependencyIdentities(repo, &result)
	return result
}

func assertPipfileProjectionEquivalent(t *testing.T, original, projected report.Report) {
	t.Helper()
	if !reflect.DeepEqual(original.Dependencies, projected.Dependencies) || !reflect.DeepEqual(original.Warnings, projected.Warnings) {
		t.Fatalf("Pipfile projection changed evidence or warnings:\noriginal=%+v %v\nprojected=%+v %v", original.Dependencies, original.Warnings, projected.Dependencies, projected.Warnings)
	}
}

func assertPipfileResolvedIdentity(t *testing.T, result report.Report, name, version string) {
	t.Helper()
	assertIdentity(t, findIdentityDependency(t, result, "python", name), report.DependencyIdentity{
		Ecosystem: "pypi", Name: name, Version: version, VersionStatus: identityStatusResolved,
		PURL: "pkg:pypi/" + name + "@" + version, PURLStatus: identityStatusResolved,
		Source: "Pipfile.lock", Confidence: "high",
	})
}
