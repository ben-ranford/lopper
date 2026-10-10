package model

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestMavenRebaseValidatesRootsAndPreservesEvidence(t *testing.T) {
	entry, err := NewMavenManifest("pom.xml", map[string]string{"version": "1"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, tc := range []struct {
		name, from, to, path string
		wantError            bool
	}{
		{"nested", filepath.Join(root, "module"), root, "module/pom.xml", false},
		{"same", root, root, "pom.xml", false},
		{"outside", root, filepath.Join(root, "module"), "", true},
		{"incompatible", "relative", root, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rebased, err := RebaseMavenManifest(entry, tc.from, tc.to)
			if (err != nil) != tc.wantError {
				t.Fatalf("rebase error = %v", err)
			}
			if tc.wantError {
				if rebased.Size() != 0 || rebased.Path() != "" {
					t.Fatal("failed rebase returned retained evidence")
				}
			} else {
				if rebased.Path() != tc.path || rebased.Properties()["version"] != "1" {
					t.Fatalf("rebased evidence = %#v", rebased)
				}
				rebased.Properties()["version"] = "changed"
			}
			if entry.Path() != "pom.xml" || entry.Properties()["version"] != "1" {
				t.Fatal("rebase changed original evidence")
			}
		})
	}
}

func TestMavenJSONStringSizeMatchesEscapedEncoding(t *testing.T) {
	for _, value := range []string{"", "ordinary", "\"\\\b\f\n\r\t", "\x00\x01\x1f", "<&>\u2028\u2029", "é日本語😀"} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if size := MavenJSONStringSize(value); size != len(encoded) {
			t.Errorf("size of %q = %d, JSON bytes = %d", value, size, len(encoded))
		}
	}
}

func TestMavenEvidenceRejectsInvalidAggregateState(t *testing.T) {
	entry, err := NewMavenManifest("pom.xml", nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]MavenManifest, MavenEvidenceEntryLimit+1)
	for i := range entries {
		entries[i] = entry
	}
	if _, err := MavenEvidenceSize(entries[:MavenEvidenceEntryLimit]); err != nil {
		t.Fatalf("exact entry limit rejected: %v", err)
	}
	if _, err := MavenEvidenceSize(entries); !errors.Is(err, ErrMavenEvidenceLimit) {
		t.Fatalf("excess entry count error = %v", err)
	}
	if _, err := MavenEvidenceSize([]MavenManifest{{}}); err == nil {
		t.Fatal("uninitialised evidence accepted")
	}
	// Seed the aggregate counter at its boundary without allocating a million values.
	entry.values = MavenEvidenceValueLimit
	if _, err := MavenEvidenceSize([]MavenManifest{entry}); err != nil {
		t.Fatalf("exact aggregate value allowance rejected: %v", err)
	}
	extra, err := NewMavenManifest("other/pom.xml", map[string]string{"v": "1"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MavenEvidenceSize([]MavenManifest{entry, extra}); !errors.Is(err, ErrMavenEvidenceLimit) {
		t.Fatalf("excess aggregate values error = %v", err)
	}
}

func TestMavenWindowsComponentCharacterRules(t *testing.T) {
	for _, component := range []string{`a\b`, "a<b", "a>b", "a:b", `a"b`, "a|b", "a?b", "a*b", "trailing.", "trailing ", "line\nbreak", "control" + string(rune(1))} {
		if !invalidWindowsMavenComponent(component) {
			t.Errorf("invalid component accepted: %q", component)
		}
	}
	for _, component := range []string{"pom.xml", "valid space", "module-name"} {
		if invalidWindowsMavenComponent(component) {
			t.Errorf("local component rejected: %q", component)
		}
	}
}
