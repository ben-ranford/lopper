package advisory

import (
	"archive/zip"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSyncOSVZipPreservesInventory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		entries    []testOSVZipEntry
		count      int
		ecosystems []string
	}{
		{name: "single", entries: []testOSVZipEntry{{name: "a.json", payload: testOSVAdvisory("GO-1"), method: zip.Deflate}}, count: 1, ecosystems: []string{"Go"}},
		{name: "multiple ecosystems", entries: []testOSVZipEntry{
			{name: "z.json", payload: strings.ReplaceAll(testOSVAdvisory("PY-1"), `"Go"`, `"PyPI"`), method: zip.Deflate},
			{name: "a.json", payload: `[` + testOSVAdvisory("GO-1") + `,` + testOSVAdvisory("GO-2") + `]`, method: zip.Deflate},
			{name: "README.txt", payload: "not an advisory", method: zip.Store},
		}, count: 3, ecosystems: []string{"Go", "PyPI"}},
		{name: "wrapped advisories", entries: []testOSVZipEntry{{name: "wrapped.json", payload: `{"vulns":[` + testOSVAdvisory("GO-1") + `]}`, method: zip.Deflate}}, count: 1, ecosystems: []string{"Go"}},
		{name: "empty advisory array", entries: []testOSVZipEntry{{name: "empty.json", payload: `[]`, method: zip.Deflate}}, ecosystems: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertSyncedOSVZipInventory(t, testOSVZipEntries(t, tc.entries...), tc.count, tc.ecosystems)
		})
	}
}

func TestSyncOSVZipRejectsDuplicatePackageEcosystem(t *testing.T) {
	advisory := strings.Replace(testOSVAdvisory("GO-1"), `"ecosystem":"Go"`, `"ecosystem":"Go","ecosystem":"PyPI"`, 1)
	payload := testOSVZip(t, "GO-1.json", advisory)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(payload); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	_, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: t.TempDir(), Client: server.Client()})
	if err == nil || !strings.Contains(err.Error(), "duplicate package ecosystem field") {
		t.Fatalf("expected duplicate ecosystem field to be rejected, got %v", err)
	}
}

func assertSyncedOSVZipInventory(t *testing.T, payload []byte, count int, ecosystems []string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, err := w.Write(payload)
		if err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cache := t.TempDir()
	snapshot, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: cache, Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EntryCount != count || !reflect.DeepEqual(snapshot.Ecosystems, ecosystems) {
		t.Fatalf("inventory = %d %v, want %d %v", snapshot.EntryCount, snapshot.Ecosystems, count, ecosystems)
	}
	manifest, err := LoadCacheManifest(cache)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Snapshots[0].EntryCount != count || !reflect.DeepEqual(manifest.Snapshots[0].Ecosystems, snapshot.Ecosystems) && len(snapshot.Ecosystems) > 0 {
		t.Fatalf("manifest inventory = %#v", manifest.Snapshots)
	}
}
