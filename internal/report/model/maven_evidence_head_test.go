package model

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestMavenManifestDefensiveOwnership(t *testing.T) {
	properties := map[string]string{"v": "1", "blank": ""}
	deps := []MavenDeclaration{{ArtifactID: "one", Version: "${v}"}}
	entry, err := NewMavenManifest("pom.xml", properties, deps, deps, "", "")
	if err != nil {
		t.Fatal(err)
	}
	properties["v"] = "outside"
	deps[0].ArtifactID = "outside"
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Go(func() {
			p := entry.Properties()
			p["v"] = "changed"
			d := entry.Dependencies()
			d[0].ArtifactID = "changed"
			m := entry.ManagedDependencies()
			m[0].Version = "changed"
		})
	}
	workers.Wait()
	if entry.Properties()["v"] != "1" || entry.Properties()["blank"] != "" || entry.Dependencies()[0].ArtifactID != "one" || entry.ManagedDependencies()[0].Version != "${v}" {
		t.Fatal("evidence ownership leaked")
	}
}
func TestMavenManifestExactRetentionBytes(t *testing.T) {
	empty, err := NewMavenManifest("pom.xml", map[string]string{"v": ""}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := MavenEvidenceSize([]MavenManifest{empty})
	if err != nil {
		t.Fatal(err)
	}
	value := strings.Repeat("x", MavenEvidenceByteLimit-envelope)
	entry, err := NewMavenManifest("pom.xml", map[string]string{"v": value}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if size, err := MavenEvidenceSize([]MavenManifest{entry}); err != nil || size != MavenEvidenceByteLimit {
		t.Fatalf("exact: size=%d err=%v", size, err)
	}
	extra, err := entry.WithPath("xpom.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MavenEvidenceSize([]MavenManifest{extra}); !errors.Is(err, ErrMavenEvidenceLimit) {
		t.Fatalf("limit+1: %v", err)
	}
}
func TestMavenManifestRecordCountLimits(t *testing.T) {
	dependencies := make([]MavenDeclaration, MavenEvidenceRecordLimit+1)
	if _, err := NewMavenManifest("pom.xml", nil, dependencies[:MavenEvidenceRecordLimit], nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMavenManifest("pom.xml", nil, dependencies, nil, "", ""); !errors.Is(err, ErrMavenEvidenceLimit) {
		t.Fatalf("dependency count+1: %v", err)
	}
	properties := make(map[string]string, MavenEvidenceRecordLimit+1)
	for i := 0; i <= MavenEvidenceRecordLimit; i++ {
		properties[strconv.Itoa(i)] = ""
	}
	if _, err := NewMavenManifest("pom.xml", properties, nil, nil, "", ""); !errors.Is(err, ErrMavenEvidenceLimit) {
		t.Fatalf("property count+1: %v", err)
	}
	delete(properties, "0")
	if _, err := NewMavenManifest("pom.xml", properties, nil, nil, "", ""); err != nil {
		t.Fatal(err)
	}
}
func TestMavenManifestFailureStateValidation(t *testing.T) {
	for _, tc := range []struct {
		stage, kind string
		valid       bool
	}{{"", "", true}, {"read", "permission", true}, {"read", "missing", true}, {"read", "large", true}, {"read", "io", true}, {"parse", "xml", true}, {"parse", "io", false}, {"", "xml", false}, {"other", "io", false}} {
		_, err := NewMavenManifest("pom.xml", nil, nil, nil, tc.stage, tc.kind)
		if (err == nil) != tc.valid {
			t.Fatalf("%#v: %v", tc, err)
		}
	}
	if _, err := NewMavenManifest("pom.xml", map[string]string{"x": "1"}, nil, nil, "parse", "xml"); err == nil {
		t.Fatal("mixed success/failure accepted")
	}
	for _, path := range []string{"", ".", "../pom.xml", "a/../pom.xml", "/pom.xml", "a//pom.xml", "a/./pom.xml", "a\x00/pom.xml"} {
		if _, err := NewMavenManifest(path, nil, nil, nil, "", ""); err == nil {
			t.Fatalf("invalid path %q", path)
		}
	}
}

func TestMavenManifestRemainingBudgetAdmission(t *testing.T) {
	properties := map[string]string{"v": "one"}
	entry, err := NewMavenManifest("pom.xml", properties, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		bytes, values int
		valid         bool
	}{
		{"exact", entry.Size(), entry.Values(), true},
		{"byte-short", entry.Size() - 1, entry.Values(), false},
		{"value-short", entry.Size(), 0, false},
		{"zero", 0, 0, false},
		{"negative", -1, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retained, err := NewMavenManifestWithinBudget("pom.xml", properties, nil, nil, "", "", MavenEvidenceBudget{Bytes: tc.bytes, Values: tc.values})
			if tc.valid {
				if err != nil || !retained.Equal(entry) {
					t.Fatalf("exact admission: %v %v", retained, err)
				}
			} else if !errors.Is(err, ErrMavenEvidenceLimit) || retained.Size() != 0 {
				t.Fatalf("remaining admission: %v %v", retained, err)
			}
		})
	}
}

func TestMavenManifestPointerReceiverPreservesValueOwnership(t *testing.T) {
	entry, err := NewMavenManifest("pom.xml", map[string]string{"v": "1"}, []MavenDeclaration{{ArtifactID: "one"}}, []MavenDeclaration{{ArtifactID: "managed"}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		limited    bool
		wantError  bool
	}{{"success", "nested/pom.xml", false, false}, {"invalid", "../pom.xml", false, true}, {"limit", "xpom.xml", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			assertMavenRebaseOwnership(t, entry, tc.path, tc.limited, tc.wantError)
		})
	}
}
func assertMavenProjectionCopies(t *testing.T, entries ...*MavenManifest) {
	t.Helper()
	for _, entry := range entries {
		entry.Properties()["v"] = "mutated"
		entry.Dependencies()[0].ArtifactID = "mutated"
		entry.ManagedDependencies()[0].ArtifactID = "mutated"
	}
	for _, entry := range entries {
		if entry.Properties()["v"] != "1" || entry.Dependencies()[0].ArtifactID != "one" || entry.ManagedDependencies()[0].ArtifactID != "managed" {
			t.Fatal("projection escaped immutable ownership")
		}
	}
}
func TestMavenManifestPointerZeroAndContentEquality(t *testing.T) {
	var zero MavenManifest
	stage, kind := zero.Failure()
	if zero.Path() != "" || zero.Size() != 0 || zero.Values() != 0 || stage != "" || kind != "" {
		t.Fatal("zero scalar projections changed")
	}
	if !reflect.DeepEqual(zero.Properties(), map[string]string(nil)) || !reflect.DeepEqual(zero.Dependencies(), []MavenDeclaration(nil)) || !reflect.DeepEqual(zero.ManagedDependencies(), []MavenDeclaration(nil)) {
		t.Fatal("zero collection projections changed")
	}
	copied := zero
	if !zero.Equal(copied) {
		t.Fatal("equal values compare by identity")
	}
	copied.properties = make(map[string]string)
	copied.dependencies = make([]MavenDeclaration, 0)
	if !zero.Equal(copied) {
		t.Fatal("existing nil/empty content equality changed")
	}
	copied.path = "pom.xml"
	if zero.Equal(copied) {
		t.Fatal("different content compares equal")
	}
}

func assertMavenRebaseOwnership(t *testing.T, entry MavenManifest, path string, limited, wantError bool) {
	t.Helper()
	original := entry
	if limited {
		original.size = MavenEvidenceByteLimit
	}
	copied := original
	next, err := original.WithPath(path)
	if (err != nil) != wantError {
		t.Fatalf("WithPath error=%v", err)
	}
	if !original.Equal(copied) || original.Size() != copied.Size() {
		t.Fatal("WithPath mutated source or copy")
	}
	if wantError {
		var zero MavenManifest
		if !next.Equal(zero) || next.Size() != 0 {
			t.Fatal("failure returned nonzero evidence")
		}
		return
	}
	if next.Path() != path || next.Size() != original.Size()+MavenJSONStringSize(path)-MavenJSONStringSize(original.Path()) {
		t.Fatal("rebased path/size changed")
	}
	assertMavenProjectionCopies(t, &original, &copied, &next)
}
