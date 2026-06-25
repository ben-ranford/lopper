package advisory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadLocalAdvisoryYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "advisories.yml")
	content := `advisories:
  - id: GHSA-local-1
    package: example-lib
    ecosystem: npm
    severity: high
    fixedVersion: 1.2.3
    source: team-advisories
    aliases:
      - CVE-2026-0001
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write advisory fixture: %v", err)
	}

	advisories, err := Load(path)
	if err != nil {
		t.Fatalf("load advisory fixture: %v", err)
	}
	if len(advisories) != 1 {
		t.Fatalf("expected one advisory, got %#v", advisories)
	}
	got := advisories[0]
	if got.ID != "GHSA-local-1" || got.Package != "example-lib" || got.Ecosystem != "npm" || got.Severity != "high" || got.FixedVersion != "1.2.3" || got.Source != "team-advisories" {
		t.Fatalf("unexpected local advisory: %#v", got)
	}
	if len(got.Aliases) != 1 || got.Aliases[0] != "CVE-2026-0001" {
		t.Fatalf("expected aliases to load, got %#v", got.Aliases)
	}
}

func TestLoadOSVAdvisoryJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "osv.json")
	content := `{
  "id": "GHSA-osv-1",
  "aliases": ["CVE-2026-0002"],
  "database_specific": {"severity": "CRITICAL"},
  "affected": [
    {
      "package": {"ecosystem": "PyPI", "name": "requests"},
      "ranges": [{"events": [{"introduced": "0"}, {"fixed": "2.32.0"}]}]
    }
  ]
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write OSV fixture: %v", err)
	}

	advisories, err := Load(path)
	if err != nil {
		t.Fatalf("load OSV fixture: %v", err)
	}
	if len(advisories) != 1 {
		t.Fatalf("expected one OSV advisory, got %#v", advisories)
	}
	got := advisories[0]
	if got.ID != "GHSA-osv-1" || got.Package != "requests" || got.Ecosystem != "PyPI" || got.Severity != "CRITICAL" || got.FixedVersion != "2.32.0" {
		t.Fatalf("unexpected OSV advisory: %#v", got)
	}
	if got.Source == "" {
		t.Fatalf("expected default local source to be attached: %#v", got)
	}
}

func TestLoadRejectsDocumentWithoutAdvisories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.yml")
	if err := os.WriteFile(path, []byte("advisories: []\n"), 0o600); err != nil {
		t.Fatalf("write empty advisory fixture: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatalf("expected no advisories error")
	}
}

func TestLoadEmptyPathReturnsNil(t *testing.T) {
	advisories, err := Load(" \t ")
	if err != nil {
		t.Fatalf("load empty path: %v", err)
	}
	if len(advisories) != 0 {
		t.Fatalf("expected nil advisories for empty path, got %#v", advisories)
	}
}

func TestLoadWrapsReadAndDecodeErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yml")); err == nil || !strings.Contains(err.Error(), "read advisory source") {
		t.Fatalf("expected wrapped read error, got %v", err)
	}

	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatalf("write broken advisory fixture: %v", err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "invalid JSON advisory source") {
		t.Fatalf("expected wrapped JSON parse error, got %v", err)
	}
}

func TestLoadOSVSequenceAndWrappedDocuments(t *testing.T) {
	dir := t.TempDir()
	sequencePath := filepath.Join(dir, "sequence.yml")
	sequence := `- id: GHSA-sequence
  severity:
    - type: CVSS_V3
      score: "8.9"
  affected:
    - package:
        ecosystem: npm
        name: sequence-lib
      ranges:
        - events:
            - introduced: "0"
            - fixed: "2.0.0"
`
	if err := os.WriteFile(sequencePath, []byte(sequence), 0o600); err != nil {
		t.Fatalf("write OSV sequence fixture: %v", err)
	}
	sequenceAdvisories, err := Load(sequencePath)
	if err != nil {
		t.Fatalf("load OSV sequence fixture: %v", err)
	}
	if len(sequenceAdvisories) != 1 || sequenceAdvisories[0].Severity != "high" || sequenceAdvisories[0].FixedVersion != "2.0.0" {
		t.Fatalf("unexpected sequence advisories: %#v", sequenceAdvisories)
	}

	wrappedPath := filepath.Join(dir, "wrapped.json")
	wrapped := `{
  "vulns": [
    {
      "id": "GHSA-wrapped",
      "affected": [
        {
          "package": {"ecosystem": "PyPI", "name": "wrapped-lib"},
          "ecosystem_specific": {"severity": "moderate"},
          "ranges": [{"events": [{"introduced": "0"}]}]
        }
      ]
    }
  ]
}`
	if err := os.WriteFile(wrappedPath, []byte(wrapped), 0o600); err != nil {
		t.Fatalf("write wrapped OSV fixture: %v", err)
	}
	wrappedAdvisories, err := Load(wrappedPath)
	if err != nil {
		t.Fatalf("load wrapped OSV fixture: %v", err)
	}
	if len(wrappedAdvisories) != 1 || wrappedAdvisories[0].ID != "GHSA-wrapped" || wrappedAdvisories[0].Severity != "moderate" {
		t.Fatalf("unexpected wrapped advisories: %#v", wrappedAdvisories)
	}
}

func TestParseFallsBackFromEmptyLocalDocumentToOSV(t *testing.T) {
	data := []byte(`id: GHSA-yaml-osv
affected:
  - package:
      ecosystem: npm
      name: yaml-osv-lib
`)
	advisories, err := parse("osv.yml", data)
	if err != nil {
		t.Fatalf("parse YAML OSV fallback: %v", err)
	}
	if len(advisories) != 1 || advisories[0].ID != "GHSA-yaml-osv" || advisories[0].Package != "yaml-osv-lib" {
		t.Fatalf("unexpected fallback advisories: %#v", advisories)
	}
}

func TestParseOSVDocumentRejectsInvalidStructuredData(t *testing.T) {
	if _, err := parseOSVDocument("broken.yml", []byte(":\n")); err == nil {
		t.Fatalf("expected invalid OSV document error")
	}
}

func TestAdvisoriesFromOSVSkipsIncompleteEntries(t *testing.T) {
	items := []osvAdvisory{
		{Affected: []osvAffected{{Package: osvPackage{Name: "missing-id"}}}},
		{ID: "GHSA-missing-package", Affected: []osvAffected{{Package: osvPackage{}}}},
		{ID: "GHSA-ok", Affected: []osvAffected{{Package: osvPackage{Name: "ok-lib"}}}},
	}
	advisories := advisoriesFromOSV(items)
	if len(advisories) != 1 || advisories[0].ID != "GHSA-ok" {
		t.Fatalf("unexpected OSV advisory filtering: %#v", advisories)
	}
}

func TestOSVSeverityFallbacks(t *testing.T) {
	cases := []struct {
		name string
		item osvAdvisory
		want string
	}{
		{
			name: "affected database specific",
			item: osvAdvisory{Affected: []osvAffected{{DatabaseSpecific: map[string]any{"severity": "high"}}}},
			want: "high",
		},
		{
			name: "affected ecosystem specific",
			item: osvAdvisory{Affected: []osvAffected{{EcosystemSpecific: map[string]any{"severity": "low"}}}},
			want: "low",
		},
		{
			name: "cvss critical",
			item: osvAdvisory{Severity: []osvSeverity{{Score: "9.0"}}},
			want: "critical",
		},
		{
			name: "cvss medium",
			item: osvAdvisory{Severity: []osvSeverity{{Score: "4.1"}}},
			want: "medium",
		},
		{
			name: "cvss low",
			item: osvAdvisory{Severity: []osvSeverity{{Score: "0.1"}}},
			want: "low",
		},
		{
			name: "unknown",
			item: osvAdvisory{DatabaseSpecific: map[string]any{"severity": 1}, Severity: []osvSeverity{{Score: "none"}, {Score: "0"}}},
			want: "unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := osvAdvisorySeverity(tc.item); got != tc.want {
				t.Fatalf("severity = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSmallParsingHelpers(t *testing.T) {
	if got := stringValue(map[string]any{"other": "value"}, "severity"); got != "" {
		t.Fatalf("expected missing string value to be empty, got %q", got)
	}
	if got := cvssSeverity(" "); got != "" {
		t.Fatalf("expected blank CVSS score to be empty, got %q", got)
	}
}
