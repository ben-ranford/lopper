package report

import "testing"

type identityNormalizationStringCase struct {
	name            string
	input           string
	wantCanonical   string
	wantVersionless string
	wantVersion     string
}

var sharedIdentityNormalizationCases = []identityNormalizationStringCase{
	{
		name:            "blank stays blank",
		input:           " \t ",
		wantCanonical:   "",
		wantVersionless: "",
		wantVersion:     "",
	},
	{
		name:            "non package URL stays trimmed",
		input:           " https://example.com/pkg ",
		wantCanonical:   "https://example.com/pkg",
		wantVersionless: "https://example.com/pkg",
		wantVersion:     "",
	},
	{
		name:            "ecosystem only packagist remains invalid",
		input:           "pkg:packagist",
		wantCanonical:   "pkg:packagist",
		wantVersionless: "pkg:packagist",
		wantVersion:     "",
	},
	{
		name:            "incomplete npm scope is preserved instead of mangled",
		input:           "pkg:npm/@scope",
		wantCanonical:   "pkg:npm/@scope",
		wantVersionless: "pkg:npm/@scope",
		wantVersion:     "",
	},
	{
		name:            "pep503 package name",
		input:           "pkg:pypi/My_Package.Name@1.2.3",
		wantCanonical:   "pkg:pypi/my-package-name@1.2.3",
		wantVersionless: "pkg:pypi/my-package-name",
		wantVersion:     "1.2.3",
	},
	{
		name:            "legacy encoded npm scope",
		input:           "pkg:npm/%40scope/lib@1.2.3",
		wantCanonical:   "pkg:npm/%40scope/lib@1.2.3",
		wantVersionless: "pkg:npm/%40scope/lib",
		wantVersion:     "1.2.3",
	},
	{
		name:            "incomplete scoped npm remains distinct",
		input:           "pkg:npm/@other",
		wantCanonical:   "pkg:npm/@other",
		wantVersionless: "pkg:npm/@other",
		wantVersion:     "",
	},
	{
		name:            "malformed non purl containing at remains trimmed",
		input:           " custom@value ",
		wantCanonical:   "custom@value",
		wantVersionless: "custom@value",
		wantVersion:     "",
	},
	{
		name:            "valid scoped npm version strips",
		input:           "pkg:npm/@scope/lib@1.2.3",
		wantCanonical:   "pkg:npm/%40scope/lib@1.2.3",
		wantVersionless: "pkg:npm/%40scope/lib",
		wantVersion:     "1.2.3",
	},
	{
		name:            "different incomplete scoped npm stays distinct",
		input:           "pkg:npm/@other",
		wantCanonical:   "pkg:npm/@other",
		wantVersionless: "pkg:npm/@other",
		wantVersion:     "",
	},
	{
		name:            "non purl string with at stays intact",
		input:           "name@not-a-purl",
		wantCanonical:   "name@not-a-purl",
		wantVersionless: "name@not-a-purl",
		wantVersion:     "",
	},
}

func runIdentityNormalizationStringCases(t *testing.T, fnName string, fn func(string) string, want func(identityNormalizationStringCase) string, cases []identityNormalizationStringCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fn(tc.input); got != want(tc) {
				t.Fatalf("%s(%s) = %q, want %q", fnName, tc.name, got, want(tc))
			}
		})
	}
}

func wantCanonicalNormalizationCase(tc identityNormalizationStringCase) string {
	return tc.wantCanonical
}

func wantVersionlessNormalizationCase(tc identityNormalizationStringCase) string {
	return tc.wantVersionless
}

func wantPURLVersionNormalizationCase(tc identityNormalizationStringCase) string {
	return tc.wantVersion
}

func TestCanonicalPURLNormalizesComposerAliasesAndLegacyEscapedSlashPaths(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "packagist alias",
			input: "pkg:packagist/acme/lib@1.2.3",
			want:  "pkg:composer/acme/lib@1.2.3",
		},
		{
			name:  "legacy escaped slash preserves version qualifiers and fragment",
			input: "pkg:composer/acme%2Flib@1.2.3?repository_url=https://repo.example/pkg#docs",
			want:  "pkg:composer/acme/lib@1.2.3?repository_url=https://repo.example/pkg#docs",
		},
		{
			name:  "npm scope remains encoded",
			input: "pkg:npm/@scope/lib@1.0.0",
			want:  "pkg:npm/%40scope/lib@1.0.0",
		},
		{
			name:  "literal plus remains build metadata",
			input: "pkg:golang/example.com/acme/lib@v1.2.3+incompatible",
			want:  "pkg:golang/example.com/acme/lib@v1.2.3%2Bincompatible",
		},
		{
			name:  "encoded plus remains canonical",
			input: "pkg:golang/example.com/acme/lib@v1.2.3%2Bincompatible",
			want:  "pkg:golang/example.com/acme/lib@v1.2.3%2Bincompatible",
		},
	} {
		if got := CanonicalPURL(tc.input); got != tc.want {
			t.Fatalf("CanonicalPURL(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := canonicalizePURLVersion("1.0%zz"); got != "@1.0%zz" {
		t.Fatalf("canonicalizePURLVersion() should preserve an invalid escape for its caller to reject, got %q", got)
	}
}

func TestCanonicalPackageNameForEcosystemNormalizesPEP503Names(t *testing.T) {
	for _, input := range []string{"My_Package", "my.package", "my-package"} {
		if got := CanonicalPackageNameForEcosystem("pypi", input); got != "my-package" {
			t.Fatalf("CanonicalPackageNameForEcosystem(pypi, %q) = %q, want %q", input, got, "my-package")
		}
	}
	if got := CanonicalPackageNameForEcosystem("npm", "My_Package"); got != "my_package" {
		t.Fatalf("expected non-PyPI ecosystems to keep package separators, got %q", got)
	}
	if got := CanonicalPackageNameForEcosystem("pypi", " \t "); got != "" {
		t.Fatalf("expected blank package names to stay empty after normalization, got %q", got)
	}
}

func TestCanonicalPackageEcosystemNormalizesRubyAndElixirAliases(t *testing.T) {
	for _, alias := range []string{"ruby", "rubygems", "gem"} {
		if got := CanonicalPackageEcosystem(alias); got != "gem" {
			t.Fatalf("CanonicalPackageEcosystem(%q) = %q, want gem", alias, got)
		}
	}
	for _, alias := range []string{"elixir", "hex"} {
		if got := CanonicalPackageEcosystem(alias); got != "hex" {
			t.Fatalf("CanonicalPackageEcosystem(%q) = %q, want hex", alias, got)
		}
	}
}

func TestCanonicalPURLHandlesBlankNonPackageAndEcosystemOnlyValues(t *testing.T) {
	runIdentityNormalizationStringCases(t, "CanonicalPURL", CanonicalPURL, wantCanonicalNormalizationCase, sharedIdentityNormalizationCases[:4])
}

func TestVersionlessCanonicalPURLRemovesVersionsButKeepsQualifiers(t *testing.T) {
	if got := VersionlessCanonicalPURL("pkg:composer/acme%2Flib@1.2.3?repository_url=https://repo.example/pkg#docs"); got != "pkg:composer/acme/lib?repository_url=https://repo.example/pkg#docs" {
		t.Fatalf("VersionlessCanonicalPURL() should drop the version while preserving canonical path, query, and fragment, got %q", got)
	}
	if got := VersionlessCanonicalPURL(" \t "); got != "" {
		t.Fatalf("expected blank PURLs to stay blank after version stripping, got %q", got)
	}
}

func TestCanonicalPURLNormalizesPypiNamesWithoutCollapsingMalformedInputs(t *testing.T) {
	runIdentityNormalizationStringCases(t, "CanonicalPURL", CanonicalPURL, wantCanonicalNormalizationCase, sharedIdentityNormalizationCases[4:8])
}

func TestVersionlessCanonicalPURLOnlyStripsSyntacticVersions(t *testing.T) {
	runIdentityNormalizationStringCases(t, "VersionlessCanonicalPURL", VersionlessCanonicalPURL, wantVersionlessNormalizationCase, sharedIdentityNormalizationCases[8:])
}

func TestPURLVersionOnlyReadsSyntacticVersions(t *testing.T) {
	runIdentityNormalizationStringCases(t, "PURLVersion", PURLVersion, wantPURLVersionNormalizationCase, sharedIdentityNormalizationCases)
}

func TestInvalidPURLInputsRemainUnchangedAndDistinct(t *testing.T) {
	cases := []identityNormalizationStringCase{
		{
			name:            "missing type",
			input:           "pkg:/foo@1",
			wantCanonical:   "pkg:/foo@1",
			wantVersionless: "pkg:/foo@1",
			wantVersion:     "",
		},
		{
			name:            "missing name after type",
			input:           "pkg:npm",
			wantCanonical:   "pkg:npm",
			wantVersionless: "pkg:npm",
			wantVersion:     "",
		},
		{
			name:            "missing name after slash",
			input:           "pkg:npm/",
			wantCanonical:   "pkg:npm/",
			wantVersionless: "pkg:npm/",
			wantVersion:     "",
		},
		{
			name:            "double slash malformed separator",
			input:           "pkg:npm//foo@1",
			wantCanonical:   "pkg:npm//foo@1",
			wantVersionless: "pkg:npm//foo@1",
			wantVersion:     "",
		},
		{
			name:            "invalid percent escape in name",
			input:           "pkg:npm/%zz@1",
			wantCanonical:   "pkg:npm/%zz@1",
			wantVersionless: "pkg:npm/%zz@1",
			wantVersion:     "",
		},
		{
			name:            "invalid percent escape in version",
			input:           "pkg:npm/foo@1%zz",
			wantCanonical:   "pkg:npm/foo@1%zz",
			wantVersionless: "pkg:npm/foo@1%zz",
			wantVersion:     "",
		},
		{
			name:            "invalid percent escape in query",
			input:           "pkg:npm/foo@1?key=%zz",
			wantCanonical:   "pkg:npm/foo@1?key=%zz",
			wantVersionless: "pkg:npm/foo@1?key=%zz",
			wantVersion:     "",
		},
		{
			name:            "invalid percent escape in fragment",
			input:           "pkg:npm/foo@1#frag=%zz",
			wantCanonical:   "pkg:npm/foo@1#frag=%zz",
			wantVersionless: "pkg:npm/foo@1#frag=%zz",
			wantVersion:     "",
		},
		{
			name:            "type starts with digit",
			input:           "pkg:1npm/foo@1.0.0",
			wantCanonical:   "pkg:1npm/foo@1.0.0",
			wantVersionless: "pkg:1npm/foo@1.0.0",
			wantVersion:     "",
		},
		{
			name:            "type starts with punctuation",
			input:           "pkg:-npm/foo@1.0.0",
			wantCanonical:   "pkg:-npm/foo@1.0.0",
			wantVersionless: "pkg:-npm/foo@1.0.0",
			wantVersion:     "",
		},
		{
			name:            "type contains underscore",
			input:           "pkg:np_m/foo@1.0.0",
			wantCanonical:   "pkg:np_m/foo@1.0.0",
			wantVersionless: "pkg:np_m/foo@1.0.0",
			wantVersion:     "",
		},
		{
			name:            "type contains illegal character",
			input:           "pkg:np!m/foo@1.0.0",
			wantCanonical:   "pkg:np!m/foo@1.0.0",
			wantVersionless: "pkg:np!m/foo@1.0.0",
			wantVersion:     "",
		},
		{
			name:            "type too short",
			input:           "pkg:p/foo@1.0.0",
			wantCanonical:   "pkg:p/foo@1.0.0",
			wantVersionless: "pkg:p/foo@1.0.0",
			wantVersion:     "",
		},
	}

	runIdentityNormalizationStringCases(t, "CanonicalPURL", CanonicalPURL, wantCanonicalNormalizationCase, cases)
	runIdentityNormalizationStringCases(t, "VersionlessCanonicalPURL", VersionlessCanonicalPURL, wantVersionlessNormalizationCase, cases)
	runIdentityNormalizationStringCases(t, "PURLVersion", PURLVersion, wantPURLVersionNormalizationCase, cases)

	for _, pair := range []struct {
		name  string
		left  string
		right string
	}{
		{name: "missing type collision pair", left: "pkg:/foo@1", right: "pkg:/foo@2"},
		{name: "invalid escape collision pair", left: "pkg:npm/%zz@1", right: "pkg:npm/%2G@1"},
		{name: "invalid suffix collision pair", left: "pkg:npm/foo@1?key=%zz", right: "pkg:npm/foo@2?key=%zz"},
	} {
		t.Run(pair.name, func(t *testing.T) {
			if got := VersionlessCanonicalPURL(pair.left); got != pair.left {
				t.Fatalf("VersionlessCanonicalPURL(%q) = %q, want unchanged invalid input", pair.left, got)
			}
			if got := VersionlessCanonicalPURL(pair.right); got != pair.right {
				t.Fatalf("VersionlessCanonicalPURL(%q) = %q, want unchanged invalid input", pair.right, got)
			}
			if VersionlessCanonicalPURL(pair.left) == VersionlessCanonicalPURL(pair.right) {
				t.Fatalf("expected invalid PURL collision pair to remain distinct: %q vs %q", pair.left, pair.right)
			}
		})
	}
}

func TestCompareSemanticVersionsRejectsInvalidValues(t *testing.T) {
	if got, ok := CompareSemanticVersions("V1.2.3", "1.2.3"); !ok || got != 0 {
		t.Fatalf("expected uppercase semantic versions to normalize and compare equal, got (%d, %t)", got, ok)
	}
	if got, ok := CompareSemanticVersions("version-one", "1.2.3"); ok || got != 0 {
		t.Fatalf("expected invalid semantic versions to be rejected, got (%d, %t)", got, ok)
	}
}
