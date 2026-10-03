package analysis

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestManifestProjectionPreservesSortedGroupEvidence(t *testing.T) {
	for _, kind := range []string{"optional", "dependency", "poetry"} {
		t.Run(kind, func(t *testing.T) {
			repo := t.TempDir()
			original := manifestIdentityGroupFixture(kind, map[string]any{
				"z" + strings.Repeat("last", 1024):  manifestIdentityGroupValue(kind, "1.0", false),
				"a" + strings.Repeat("first", 1024): manifestIdentityGroupValue(kind, "2.0", false),
				"middle-empty":                      nil,
			})
			projected := manifestIdentityGroupFixture(kind, map[string]any{
				"00000000000000000000": manifestIdentityGroupValue(kind, "2.0", true),
				"00000000000000000001": manifestIdentityGroupValue(kind, "1.0", true),
			})
			want := make(identityIndex)
			collectPyprojectManifestEvidenceDocument(repo, filepath.Join(repo, "pyproject.toml"), want, original)
			got := make(identityIndex)
			warnings := newIdentityWarningCollector(repo)
			collectPythonCatalogEvidence(context.Background(), repo, got, []report.PythonManifestDocument{{
				Path: "pyproject.toml", Deferred: true, IdentityProjectionSet: true, IdentityProjection: projected,
			}}, warnings)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("group projection changed evidence or order: got %#v; want %#v", got, want)
			}
			if len(warnings.warnings) != 0 {
				t.Fatalf("projection read missing source: %v", warnings.warnings)
			}
			assertManifestGroupConflictEvidence(t, got)
		})
	}
}

func manifestIdentityGroupFixture(kind string, groups map[string]any) map[string]any {
	switch kind {
	case "optional":
		return map[string]any{"project": map[string]any{"optional-dependencies": groups}}
	case "dependency":
		return map[string]any{"dependency-groups": groups}
	default:
		groups["before-optional"] = map[string]any{"optional": true, "dependencies": map[string]any{"sample": "3.0"}}
		return map[string]any{"tool": map[string]any{"poetry": map[string]any{"group": groups}}}
	}
}

func manifestIdentityGroupValue(kind, version string, compact bool) any {
	if kind == "poetry" {
		if compact {
			return map[string]any{"dependencies": map[string]any{"sample": "==" + version}}
		}
		return map[string]any{"optional": "false", "dependencies": map[string]any{
			"sample": map[string]any{"version": " " + version + " ", "markers": strings.Repeat("ignored", 100)},
			"Python": "3.12",
		}}
	}
	if compact {
		return []any{"sample==" + version}
	}
	return []any{" sample[extra-name] ( == " + version + " ) ; " + strings.Repeat("ignored", 100), "ignored>=9", nil}
}

func assertManifestGroupConflictEvidence(t *testing.T, index identityIndex) {
	t.Helper()
	evidence := index[identityKey("python", "sample")]
	if len(index) != 1 || len(evidence) != 2 {
		t.Fatalf("expected only two conflicting sample pins: %#v", index)
	}
	for i, version := range []string{"2.0", "1.0"} {
		item := evidence[i]
		if item.Version != version || item.Source != "pyproject.toml" || item.Status != identityStatusDeclared || item.Confidence != "high" {
			t.Fatalf("evidence %d changed order or metadata: %+v", i, item)
		}
	}
}
