package advisory

import (
	"os"
	"path/filepath"
	"slices"
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
      "versions": ["2.31.0"],
      "ranges": [{"type": "SEMVER", "events": [{"introduced": "0"}, {"fixed": "2.32.0"}]}]
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
	if !slices.Equal(got.AffectedVersions, []string{"2.31.0"}) {
		t.Fatalf("expected exact OSV versions to be preserved, got %#v", got.AffectedVersions)
	}
	if len(got.VersionRanges) != 1 || got.VersionRanges[0].Type != "SEMVER" || len(got.VersionRanges[0].Events) != 2 || got.VersionRanges[0].Events[0].Introduced != "0" || got.VersionRanges[0].Events[1].Fixed != "2.32.0" {
		t.Fatalf("expected OSV range metadata to be preserved, got %#v", got.VersionRanges)
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
        - type: SEMVER
          events:
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
          "ranges": [{"type": "SEMVER", "events": [{"introduced": "0"}]}]
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
    versions:
      - "1.0.0"
`)
	advisories, err := parse("osv.yml", data)
	if err != nil {
		t.Fatalf("parse YAML OSV fallback: %v", err)
	}
	if len(advisories) != 1 || advisories[0].ID != "GHSA-yaml-osv" || advisories[0].Package != "yaml-osv-lib" {
		t.Fatalf("unexpected fallback advisories: %#v", advisories)
	}
}

func TestParseSequenceWithoutUsableAdvisoriesReturnsError(t *testing.T) {
	data := []byte(`- affected:
    - package:
        ecosystem: npm
        name: missing-id
`)
	if _, err := parse("sequence.yml", data); err == nil || !strings.Contains(err.Error(), "no advisories found") {
		t.Fatalf("expected no advisories error for sequence document, got %v", err)
	}
}

func TestParseSequenceStructuredDecodeErrorIsReturned(t *testing.T) {
	if _, err := parse("broken.json", []byte("[")); err == nil || !strings.Contains(err.Error(), "invalid JSON advisory source") {
		t.Fatalf("expected sequence parse error, got %v", err)
	}
}

func TestParseFallsBackToOSVAndReturnsStructuredError(t *testing.T) {
	if _, err := parse("advisories.json", []byte(`{"advisories":null,"id":[]}`)); err == nil || !strings.Contains(err.Error(), "invalid JSON advisory source") {
		t.Fatalf("expected OSV fallback parse error, got %v", err)
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
		{ID: "GHSA-missing-constraints", Affected: []osvAffected{{Package: osvPackage{Name: "all-lib"}}}},
		{ID: "GHSA-blank-version", Affected: []osvAffected{{Package: osvPackage{Name: "blank-lib"}, Versions: []string{" "}}}},
		{ID: "GHSA-ok", Affected: []osvAffected{{Package: osvPackage{Name: "ok-lib"}, Versions: []string{"1.0.0"}}}},
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
					Versions:         []string{"1.0.0"},
					DatabaseSpecific: map[string]any{"severity": "low"},
				},
				{
					Package:          osvPackage{Ecosystem: "npm", Name: "reachable-lib"},
					Versions:         []string{"1.0.0"},
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

func TestAdvisoriesFromOSVUsesConservativeFixedVersionExtraction(t *testing.T) {
	items := []osvAdvisory{
		{
			ID: "GHSA-fixed-version-shapes",
			Affected: []osvAffected{
				{
					Package: osvPackage{Ecosystem: "npm", Name: "simple-lib"},
					Ranges: []osvRange{{
						Type:   "SEMVER",
						Events: []osvEvent{{Introduced: "0"}, {Fixed: "1.2.3"}},
					}},
				},
				{
					Package: osvPackage{Ecosystem: "npm", Name: "multi-range-lib"},
					Ranges: []osvRange{
						{Type: "SEMVER", Events: []osvEvent{{Introduced: "0"}, {Fixed: "1.2.3"}}},
						{Type: "SEMVER", Events: []osvEvent{{Introduced: "2.0.0"}, {Fixed: "2.1.0"}}},
					},
				},
				{
					Package: osvPackage{Ecosystem: "npm", Name: "reintroduced-lib"},
					Ranges: []osvRange{{
						Type:   "SEMVER",
						Events: []osvEvent{{Introduced: "0"}, {Fixed: "1.2.3"}, {Introduced: "2.0.0"}},
					}},
				},
				{
					Package: osvPackage{Ecosystem: "npm", Name: "last-affected-lib"},
					Ranges: []osvRange{{
						Type:   "SEMVER",
						Events: []osvEvent{{Introduced: "0"}, {LastAffected: "1.2.3"}},
					}},
				},
				{
					Package: osvPackage{Ecosystem: "npm", Name: "limited-lib"},
					Ranges: []osvRange{{
						Type:   "SEMVER",
						Events: []osvEvent{{Introduced: "0"}, {Limit: "2.0.0"}, {Fixed: "1.2.3"}},
					}},
				},
			},
		},
	}

	advisories := advisoriesFromOSV(items)
	if len(advisories) != 5 {
		t.Fatalf("expected five affected advisories, got %#v", advisories)
	}

	fixedVersions := map[string]string{}
	for _, advisory := range advisories {
		fixedVersions[advisory.Package] = advisory.FixedVersion
	}
	if fixedVersions["simple-lib"] != "1.2.3" {
		t.Fatalf("expected simple OSV range to keep fixed version, got %#v", fixedVersions)
	}
	for _, pkg := range []string{"multi-range-lib", "reintroduced-lib", "last-affected-lib", "limited-lib"} {
		if fixedVersions[pkg] != "" {
			t.Fatalf("expected ambiguous OSV range for %s to clear fixed version, got %#v", pkg, fixedVersions)
		}
	}
}

func TestFixedVersionFromRangesConservativeBranches(t *testing.T) {
	cases := []struct {
		name   string
		ranges []osvRange
		want   string
	}{
		{
			name: "no ranges",
			want: "",
		},
		{
			name:   "empty events",
			ranges: []osvRange{{Type: "SEMVER"}},
			want:   "",
		},
		{
			name:   "fixed without introduced",
			ranges: []osvRange{{Type: "SEMVER", Events: []osvEvent{{Fixed: "1.2.3"}}}},
			want:   "",
		},
		{
			name:   "simple ecosystem range",
			ranges: []osvRange{{Type: "ECOSYSTEM", Events: []osvEvent{{Introduced: "0"}, {Fixed: "1.2.3"}}}},
			want:   "1.2.3",
		},
		{
			name:   "git range uses commit identifiers",
			ranges: []osvRange{{Type: "GIT", Events: []osvEvent{{Introduced: "abc"}, {Fixed: "def"}}}},
			want:   "",
		},
		{
			name:   "unknown range type",
			ranges: []osvRange{{Type: "custom", Events: []osvEvent{{Introduced: "0"}, {Fixed: "1.2.3"}}}},
			want:   "",
		},
		{
			name:   "simple semantic range",
			ranges: []osvRange{{Type: " semver ", Events: []osvEvent{{Introduced: "2.0.0"}, {Fixed: "2.0.1"}}}},
			want:   "2.0.1",
		},
		{
			name:   "reordered simple range",
			ranges: []osvRange{{Type: "SEMVER", Events: []osvEvent{{Fixed: "2.0.1"}, {Introduced: "2.0.0"}}}},
			want:   "2.0.1",
		},
		{
			name:   "duplicate fixed events",
			ranges: []osvRange{{Type: "SEMVER", Events: []osvEvent{{Fixed: "1.2.3"}, {Fixed: "1.2.4"}}}},
			want:   "",
		},
		{
			name:   "duplicate introduced events",
			ranges: []osvRange{{Type: "SEMVER", Events: []osvEvent{{Introduced: "0"}, {Introduced: "1.0.0"}}}},
			want:   "",
		},
		{
			name:   "empty event",
			ranges: []osvRange{{Type: "SEMVER", Events: []osvEvent{{}, {Fixed: "1.2.3"}}}},
			want:   "",
		},
		{
			name:   "multi field event",
			ranges: []osvRange{{Type: "SEMVER", Events: []osvEvent{{Introduced: "0", Fixed: "1.2.3"}, {Fixed: "1.2.4"}}}},
			want:   "",
		},
		{
			name:   "last affected event",
			ranges: []osvRange{{Type: "SEMVER", Events: []osvEvent{{Introduced: "0"}, {LastAffected: "1.2.3"}}}},
			want:   "",
		},
		{
			name:   "limit event",
			ranges: []osvRange{{Type: "SEMVER", Events: []osvEvent{{Introduced: "0"}, {Limit: "2.0.0"}}}},
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fixedVersionFromRanges(tc.ranges); got != tc.want {
				t.Fatalf("fixedVersionFromRanges(%#v) = %q, want %q", tc.ranges, got, tc.want)
			}
		})
	}
}

func TestOSVEventKindBranches(t *testing.T) {
	cases := []struct {
		name  string
		event osvEvent
		want  string
		ok    bool
	}{
		{name: "introduced", event: osvEvent{Introduced: "0"}, want: "introduced", ok: true},
		{name: "fixed", event: osvEvent{Fixed: "1.2.3"}, want: "fixed", ok: true},
		{name: "last affected", event: osvEvent{LastAffected: "1.2.3"}, want: "last_affected", ok: true},
		{name: "limit", event: osvEvent{Limit: "2.0.0"}, want: "limit", ok: true},
		{name: "blank", event: osvEvent{}, want: "", ok: false},
		{name: "multiple fields", event: osvEvent{Fixed: "1.2.3", Limit: "2.0.0"}, want: "", ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := osvEventKind(tc.event)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("osvEventKind(%#v) = (%q, %t), want (%q, %t)", tc.event, got, ok, tc.want, tc.ok)
			}
		})
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
