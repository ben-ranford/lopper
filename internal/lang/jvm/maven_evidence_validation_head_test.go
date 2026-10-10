package jvm

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/report/model"
	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestMavenCatalogRetainsReadFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"permission", fs.ErrPermission},
		{"missing", fs.ErrNotExist},
		{"large", safeio.ErrFileTooLarge},
		{"io", errors.New("reader unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := newMavenManifestCatalog()
			catalog.retain("pom.xml", shared.ParsedPOM{}, "read", tc.err)
			if catalog.err != nil || len(catalog.entries) != 1 {
				t.Fatalf("retained read error = %v, entries = %d", catalog.err, len(catalog.entries))
			}
			stage, kind := catalog.entries[0].Failure()
			if stage != "read" || kind != tc.name || catalog.entries[0].Values() != 0 {
				t.Fatalf("retained failure = %s/%s, values = %d", stage, kind, catalog.entries[0].Values())
			}
		})
	}
}

func TestMavenCatalogAdmissionFailureRemainsSticky(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []model.MavenManifest
		limited bool
	}{
		{"entry-cap", make([]model.MavenManifest, model.MavenAdapterEntryLimit), true},
		{"uninitialised", []model.MavenManifest{{}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := newMavenManifestCatalog()
			catalog.entries = tc.entries
			catalog.retain("pom.xml", shared.ParsedPOM{}, "read", fs.ErrNotExist)
			first := catalog.err
			if first == nil || (tc.limited && !errors.Is(first, model.ErrMavenEvidenceLimit)) {
				t.Fatalf("admission failure = %v", first)
			}
			catalog.retain("other/pom.xml", shared.ParsedPOM{}, "read", fs.ErrPermission)
			if !errors.Is(catalog.err, first) || len(catalog.entries) != len(tc.entries) {
				t.Fatal("later retention replaced the failure or appended evidence")
			}
			for _, entry := range catalog.entries {
				if entry.Size() != 0 || entry.Path() != "" {
					t.Fatal("failed retention changed existing evidence")
				}
			}
		})
	}
}
