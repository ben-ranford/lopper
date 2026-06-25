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

func TestAdvisoriesFromOSVUsesAffectedSpecificSeverity(t *testing.T) {
	items := []osvAdvisory{
		{
			ID: "GHSA-multi-affected",
			Affected: []osvAffected{
				{
					Package:          osvPackage{Ecosystem: "npm", Name: "safe-lib"},
					DatabaseSpecific: map[string]any{"severity": "low"},
				},
				{
					Package:          osvPackage{Ecosystem: "npm", Name: "reachable-lib"},
					DatabaseSpecific: map[string]any{"severity": "high"},
				},
			},
		},
	}

	advisories := advisoriesFromOSV(items)
	if len(advisories) != 2 {
		t.Fatalf("expected two affected advisories, got %#v", advisories)
	}
	severities := map[string]string{}
	for _, advisory := range advisories {
		severities[advisory.Package] = advisory.Severity
	}
	if severities["safe-lib"] != "low" || severities["reachable-lib"] != "high" {
		t.Fatalf("expected affected-specific severities, got %#v", advisories)
	}
}

func TestOSVSeverityFallbacks(t *testing.T) {
	cases := []struct {
		name string
		item osvAdvisory
		want string
	}{
		{
			name: "cvss critical",
			item: osvAdvisory{Severity: []osvSeverity{{Score: "9.0"}}},
			want: "critical",
		},
		{
			name: "cvss v3 vector critical",
			item: osvAdvisory{Severity: []osvSeverity{{Type: "CVSS_V3", Score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"}}},
			want: "critical",
		},
		{
			name: "cvss v2 vector critical",
			item: osvAdvisory{Severity: []osvSeverity{{Type: "CVSS_V2", Score: "AV:N/AC:L/Au:N/C:C/I:C/A:C"}}},
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

func TestOSVAffectedSeverityFallbacks(t *testing.T) {
	cases := []struct {
		name     string
		affected osvAffected
		fallback string
		want     string
	}{
		{
			name:     "affected database specific",
			affected: osvAffected{DatabaseSpecific: map[string]any{"severity": "high"}},
			fallback: "low",
			want:     "high",
		},
		{
			name:     "affected ecosystem specific",
			affected: osvAffected{EcosystemSpecific: map[string]any{"severity": "low"}},
			fallback: "high",
			want:     "low",
		},
		{
			name:     "item fallback",
			affected: osvAffected{},
			fallback: "critical",
			want:     "critical",
		},
		{
			name:     "unknown",
			affected: osvAffected{},
			want:     "unknown",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := osvAffectedSeverity(tc.affected, tc.fallback); got != tc.want {
				t.Fatalf("affected severity = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSmallParsingHelpers(t *testing.T) {
	if got := stringValue(map[string]any{"other": "value"}, "severity"); got != "" {
		t.Fatalf("expected missing string value to be empty, got %q", got)
	}
	if got := cvssSeverity("", " "); got != "" {
		t.Fatalf("expected blank CVSS score to be empty, got %q", got)
	}
}

func TestCVSSVectorSeverityBranches(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		score string
		want  string
	}{
		{
			name:  "v3 changed scope high",
			kind:  "CVSS_V3",
			score: "CVSS:3.1/AV:N/AC:L/PR:L/UI:R/S:C/C:H/I:H/A:H",
			want:  "critical",
		},
		{
			name:  "v3 zero impact",
			kind:  "CVSS_V3",
			score: "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:N/I:N/A:N",
			want:  "",
		},
		{
			name:  "v2 medium",
			kind:  "CVSS_V2",
			score: "AV:N/AC:L/Au:N/C:P/I:P/A:N",
			want:  "medium",
		},
		{
			name:  "v2 zero impact",
			kind:  "CVSS_V2",
			score: "AV:N/AC:L/Au:N/C:N/I:N/A:N",
			want:  "",
		},
		{
			name:  "unknown vector version",
			kind:  "CVSS_V4",
			score: "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:H",
			want:  "",
		},
		{
			name:  "malformed v3 vector",
			kind:  "CVSS_V3",
			score: "CVSS:3.1/AV:N",
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cvssSeverity(tc.kind, tc.score); got != tc.want {
				t.Fatalf("cvssSeverity(%q, %q) = %q, want %q", tc.kind, tc.score, got, tc.want)
			}
		})
	}
}

func TestCVSSVectorVersionBranches(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		score string
		want  int
	}{
		{name: "kind v2 compact", kind: "CVSSV2", want: 2},
		{name: "kind v3 compact", kind: "CVSSV3", want: 3},
		{name: "score v2 prefix", score: "CVSS:2.0/AV:N/AC:L/Au:N/C:C/I:C/A:C", want: 2},
		{name: "score v3 prefix", score: "CVSS:3.0/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H", want: 3},
		{name: "v2 au inference", score: "AV:N/AC:L/Au:N/C:C/I:C/A:C", want: 2},
		{name: "unknown", kind: "other", score: "not-a-vector", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cvssVectorVersion(tc.kind, tc.score); got != tc.want {
				t.Fatalf("cvssVectorVersion(%q, %q) = %d, want %d", tc.kind, tc.score, got, tc.want)
			}
		})
	}
}

func TestCVSSBaseScoreValidationBranches(t *testing.T) {
	validV3 := cvssVectorMetrics("CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
	score, ok := cvss3BaseScore(validV3)
	assertCVSSScoreOK(t, score, ok)
	for _, key := range []string{"AV", "AC", "PR", "UI", "C", "I", "A", "S"} {
		metrics := cloneStringMap(validV3)
		delete(metrics, key)
		score, ok = cvss3BaseScore(metrics)
		assertCVSSScoreInvalid(t, score, ok, "v3 missing "+key)
	}
	metrics := cloneStringMap(validV3)
	metrics["AV"] = "X"
	score, ok = cvss3BaseScore(metrics)
	assertCVSSScoreInvalid(t, score, ok, "v3 invalid AV")
	metrics = cloneStringMap(validV3)
	metrics["S"] = "X"
	score, ok = cvss3BaseScore(metrics)
	assertCVSSScoreInvalid(t, score, ok, "v3 invalid scope")

	validV2 := cvssVectorMetrics("AV:N/AC:L/Au:N/C:C/I:C/A:C")
	score, ok = cvss2BaseScore(validV2)
	assertCVSSScoreOK(t, score, ok)
	for _, key := range []string{"AV", "AC", "AU", "C", "I", "A"} {
		metrics := cloneStringMap(validV2)
		delete(metrics, key)
		score, ok = cvss2BaseScore(metrics)
		assertCVSSScoreInvalid(t, score, ok, "v2 missing "+key)
	}
	metrics = cloneStringMap(validV2)
	metrics["AC"] = "X"
	score, ok = cvss2BaseScore(metrics)
	assertCVSSScoreInvalid(t, score, ok, "v2 invalid AC")
}

func assertCVSSScoreOK(t *testing.T, score float64, ok bool) {
	t.Helper()
	if !ok || score <= 0 {
		t.Fatalf("expected valid positive CVSS score, got %.1f ok=%v", score, ok)
	}
}

func assertCVSSScoreInvalid(t *testing.T, score float64, ok bool, label string) {
	t.Helper()
	if ok || score != 0 {
		t.Fatalf("%s: expected invalid CVSS score, got %.1f ok=%v", label, score, ok)
	}
}

func cloneStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
