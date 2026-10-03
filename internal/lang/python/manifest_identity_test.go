package python

import "testing"

func TestExactManifestRequirementPinCharacterization(t *testing.T) {
	for _, tc := range []struct {
		label, input, name, version string
		valid                       bool
	}{
		{"whitespace", " \t requests == 2.32.3 \r\n", "requests", "2.32.3", true},
		{"extras and marker", "Requests[security, socks]==2.32.3; python_version >= '3.10'", "Requests", "2.32.3", true},
		{"parentheses", "my_pkg.extra-name ( == 1!2.0rc1+local )", "my_pkg.extra-name", "1!2.0rc1+local", true},
		{"hyphenated extras", "pkg[extra-name] (== 1.0.post1.dev2); sys_platform == 'linux'", "pkg", "1.0.post1.dev2", true},
		{"marker is not evaluated", "pkg==1.0; arbitrary marker text", "pkg", "1.0", true},
		{"empty marker", "pkg==1.0;", "pkg", "1.0", true},
		{"empty", "", "", "", false},
		{"wildcard", "pkg==1.*", "", "", false},
		{"comment suffix", "pkg==1.0 # ignored elsewhere", "", "", false},
		{"multiple constraints", "pkg==1.0,!=1.1", "", "", false},
		{"Unicode version padding", "pkg==1.0\u00a0", "pkg", "1.0", true},
		{"Unicode name padding", "\u00a0pkg==1.0", "", "", false},
		{"arbitrary equality", "pkg===1.0", "pkg", "", false},
		{"invalid version retains matched name", "pkg==not-a-version", "pkg", "", false},
		{"malformed local version", "pkg==1.0+bad..local", "pkg", "", false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			assertExactManifestRequirementPin(t, tc.input, tc.name, tc.version, tc.valid)
		})
	}
}

func TestExactManifestVersionSpecCharacterization(t *testing.T) {
	for _, tc := range []struct {
		spec, version string
		bare, valid   bool
	}{
		{"==1.0RC1.POST2.DEV3+LOCAL_4", "1.0RC1.POST2.DEV3+LOCAL_4", false, true},
		{"\u00a0==1.0\u00a0", "1.0", false, true},
		{"1.2.3", "1.2.3", true, true},
		{"1.2.3", "", false, false},
		{"", "", true, false},
		{"==", "", false, false},
		{"===1.0", "", true, false},
		{"==1.*", "", false, false},
	} {
		t.Run(tc.spec, func(t *testing.T) {
			version, valid := ExactManifestVersionSpec(tc.spec, tc.bare)
			if version != tc.version || valid != tc.valid {
				t.Fatalf("version spec %q (bare=%v) = %q, %v; want %q, %v", tc.spec, tc.bare, version, valid, tc.version, tc.valid)
			}
		})
	}
}

func TestExactManifestPackageVersionCharacterization(t *testing.T) {
	for _, tc := range []struct {
		label, version string
		value          any
		bare, valid    bool
	}{
		{"Pipfile rejects bare scalar", "", "2.0", false, false},
		{"Poetry bare scalar", "2.0", "2.0", true, true},
		{"Pipfile table", "2.0", map[string]any{"version": " == 2.0 "}, false, true},
		{"Pipfile rejects bare table", "", map[string]any{"version": "2.0"}, false, false},
		{"Poetry bare table", "2.0", map[string]any{"version": "2.0"}, true, true},
		{"optional true", "", map[string]any{"version": "==2.0", "optional": true}, false, false},
		{"optional string ignored", "2.0", map[string]any{"version": "==2.0", "optional": "true"}, false, true},
		{"unconsumed metadata", "2.0", map[string]any{"version": "==2.0", "markers": "ignored", "extras": []any{"ignored"}}, false, true},
		{"nil", "", nil, false, false},
		{"nil table", "", map[string]any(nil), false, false},
		{"numeric version", "", map[string]any{"version": 2}, true, false},
		{"non-table array", "", []any{"==2.0"}, true, false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			version, valid := ExactManifestPackageVersion(tc.value, tc.bare)
			if version != tc.version || valid != tc.valid {
				t.Fatalf("package version (bare=%v) = %q, %v; want %q, %v", tc.bare, version, valid, tc.version, tc.valid)
			}
		})
	}
}

func TestManifestDependencyUnsupportedPreservesKeyPresence(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{
		{"file", nil}, {"git", false}, {"path", ""}, {"ref", 0}, {"url", []any{"ignored"}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			metadata := map[string]any{"version": "==2.0", tc.field: tc.value}
			if !ManifestDependencyUnsupported(metadata) {
				t.Fatalf("present %q key was accepted", tc.field)
			}
			if version, valid := ExactManifestPackageVersion(metadata, true); valid || version != "" {
				t.Fatalf("unsupported source produced version %q, %v", version, valid)
			}
		})
	}
	for _, metadata := range []map[string]any{nil, {}, {"optional": false}, {"optional": "true"}, {"File": "case-sensitive"}} {
		if ManifestDependencyUnsupported(metadata) {
			t.Fatalf("unconsumed metadata was rejected: %#v", metadata)
		}
	}
	if !ManifestDependencyUnsupported(map[string]any{"optional": true}) {
		t.Fatal("optional dependency was accepted")
	}
}

func assertExactManifestRequirementPin(t *testing.T, input, wantName, wantVersion string, wantValid bool) {
	t.Helper()
	name, version, valid := ExactManifestRequirementPin(input)
	if name != wantName || version != wantVersion || valid != wantValid {
		t.Fatalf("requirement %q = (%q, %q, %v), want (%q, %q, %v)", input, name, version, valid, wantName, wantVersion, wantValid)
	}
	if !valid {
		return
	}
	compactName, compactVersion, compactValid := ExactManifestRequirementPin(name + "==" + version)
	if compactName != name || compactVersion != version || !compactValid {
		t.Fatalf("compact requirement changed pin: (%q, %q, %v)", compactName, compactVersion, compactValid)
	}
}
