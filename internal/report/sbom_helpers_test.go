package report

import (
	"path/filepath"
	"testing"
	"time"
)

func TestIsLikelyVersion(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "1.2.3", want: true},
		{value: "v2", want: true},
		{value: "beta", want: false},
		{value: "1 2", want: false},
		{value: "", want: false},
	}

	for _, tc := range tests {
		if got := isLikelyVersion(tc.value); got != tc.want {
			t.Fatalf("isLikelyVersion(%q) = %t, want %t", tc.value, got, tc.want)
		}
	}
}

func TestSplitInlineVersion(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantName    string
		wantVersion string
		wantOK      bool
	}{
		{name: "maven", input: "org.slf4j:slf4j-api:2.0.12", wantName: "org.slf4j:slf4j-api", wantVersion: "2.0.12", wantOK: true},
		{name: "npm", input: "lodash@4.17.21", wantName: "lodash", wantVersion: "4.17.21", wantOK: true},
		{name: "scoped npm", input: "@scope/pkg@1.0.0", wantName: "@scope/pkg", wantVersion: "1.0.0", wantOK: true},
		{name: "no inline", input: "requests", wantName: "requests", wantVersion: "", wantOK: false},
		{name: "invalid at", input: "@scope/pkg", wantName: "@scope/pkg", wantVersion: "", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotVersion, gotOK := splitInlineVersion(tc.input)
			if gotName != tc.wantName || gotVersion != tc.wantVersion || gotOK != tc.wantOK {
				t.Fatalf("splitInlineVersion(%q) = (%q,%q,%t), want (%q,%q,%t)", tc.input, gotName, gotVersion, gotOK, tc.wantName, tc.wantVersion, tc.wantOK)
			}
		})
	}
}

func TestNormalizeDependencyIdentity(t *testing.T) {
	depWithSignal := DependencyReport{
		Name: "lodash@4.17.21",
		Provenance: &DependencyProvenance{
			Signals: []string{"version:5.0.0"},
		},
	}
	name, version := normalizeDependencyIdentity(depWithSignal)
	if name != "lodash" || version != "5.0.0" {
		t.Fatalf("normalizeDependencyIdentity with signal = (%q,%q), want (%q,%q)", name, version, "lodash", "5.0.0")
	}

	depInlineOnly := DependencyReport{Name: "requests@2.31.0"}
	name, version = normalizeDependencyIdentity(depInlineOnly)
	if name != "requests" || version != "2.31.0" {
		t.Fatalf("normalizeDependencyIdentity inline = (%q,%q), want (%q,%q)", name, version, "requests", "2.31.0")
	}
}

func TestPURLTypeAndNameHelpers(t *testing.T) {
	if got := purlTypeForLanguage("jvm"); got != "maven" {
		t.Fatalf("purlTypeForLanguage(jvm) = %q", got)
	}
	if got := purlTypeForLanguage("unknown"); got != "generic" {
		t.Fatalf("purlTypeForLanguage(unknown) = %q", got)
	}

	if got := purlName("maven", "org.slf4j:slf4j-api"); got != "org.slf4j/slf4j-api" {
		t.Fatalf("purlName maven = %q", got)
	}
	if got := purlName("npm", "@scope/pkg"); got != "@scope/pkg" {
		t.Fatalf("purlName scoped npm = %q", got)
	}
}

func TestSBOMTimestampAndDocumentName(t *testing.T) {
	ts := sbomTimestamp(Report{})
	if !ts.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("expected zero timestamp fallback, got %s", ts)
	}

	repoTS := sbomTimestamp(Report{GeneratedAt: time.Date(2026, time.March, 1, 10, 20, 30, 987654321, time.UTC)})
	if repoTS.Nanosecond() != 0 {
		t.Fatalf("expected timestamp truncation to seconds, got %s", repoTS)
	}

	if got := sbomDocumentName(""); got != "lopper-sbom" {
		t.Fatalf("sbomDocumentName empty = %q", got)
	}
	if got := sbomDocumentName("."); got != "lopper-sbom" {
		t.Fatalf("sbomDocumentName dot = %q", got)
	}
	if got := sbomDocumentName(filepath.Join("path", "to", "repo")); got != "repo" {
		t.Fatalf("sbomDocumentName path = %q", got)
	}
}
