package advisory

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestSyncOSVZipMetadata(t *testing.T) {
	goAdvisory := testOSVAdvisory("GO-1")
	npmAdvisory := strings.ReplaceAll(testOSVAdvisory("NPM-1"), `"Go"`, `"npm"`)
	for _, tc := range []struct {
		name       string
		entries    []testOSVZipEntry
		count      int
		ecosystems []string
	}{
		{name: "empty advisory array", entries: []testOSVZipEntry{{name: "empty.json", payload: `[]`}}},
		{name: "empty envelope", entries: []testOSVZipEntry{{name: "empty.json", payload: `{"vulns":[]}`}}},
		{name: "single advisory", entries: []testOSVZipEntry{{name: "GO-1.json", payload: goAdvisory}}, count: 1, ecosystems: []string{"Go"}},
		{
			name: "multiple ecosystems and document shapes",
			entries: []testOSVZipEntry{
				{name: "npm.JSON", payload: npmAdvisory},
				{name: "go.json", payload: `[` + goAdvisory + `,` + goAdvisory + `]`},
				{name: "wrapped.json", payload: `{"vulns":[` + npmAdvisory + `]}`},
				{name: "README.txt", payload: "not an advisory"},
				{name: "directory/"},
			},
			count: 4, ecosystems: []string{"Go", "npm"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := testOSVZipEntries(t, tc.entries...)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if _, err := w.Write(payload); err != nil {
					t.Errorf("write snapshot: %v", err)
				}
			}))
			defer server.Close()
			cachePath := t.TempDir()
			snapshot, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: cachePath, Client: server.Client()})
			if err != nil {
				t.Fatalf("sync ZIP: %v", err)
			}
			if snapshot.EntryCount != tc.count || !reflect.DeepEqual(snapshot.Ecosystems, tc.ecosystems) {
				t.Fatalf("metadata = %d, %v; want %d, %v", snapshot.EntryCount, snapshot.Ecosystems, tc.count, tc.ecosystems)
			}
			manifest, err := LoadCacheManifest(cachePath)
			if err != nil {
				t.Fatalf("load manifest: %v", err)
			}
			if len(manifest.Snapshots) != 1 || !reflect.DeepEqual(manifest.Snapshots[0], snapshot) {
				t.Fatalf("persisted metadata differs: %#v", manifest)
			}
		})
	}
}

func TestSyncOSVZipMetadataRejectsInvalidArchives(t *testing.T) {
	oversized := testOSVZip(t, "oversized.json", testOSVAdvisory("GO-1"))
	centralDirectory := bytes.Index(oversized, []byte("PK\x01\x02"))
	if centralDirectory < 0 {
		t.Fatal("missing central directory")
	}
	binary.LittleEndian.PutUint32(oversized[centralDirectory+24:], maxSyncMetadataBytes+1)
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{name: "empty archive", data: testOSVZipEntries(t)},
		{name: "malformed archive", data: []byte("PK\x03\x04bad")},
		{name: "malformed advisory", data: testOSVZip(t, "bad.json", `{"id":`)},
		{name: "oversized declared entry", data: oversized},
		{name: "excessive compression ratio", data: testOSVZip(t, "huge.json", strings.Replace(testOSVAdvisory("GO-1"), `"affected":`, `"details":"`+strings.Repeat("a", 1024*1024)+`","affected":`, 1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertSyncRejectsSnapshot(t, string(tc.data), "invalid OSV ZIP snapshot")
		})
	}
}

func TestValidateOSVZipBoundsRejectsEntryCount(t *testing.T) {
	if err := validateOSVZipBounds(make([]*zip.File, maxOSVZipEntries+1), 1); err == nil || !strings.Contains(err.Error(), "entries; limit") {
		t.Fatalf("expected entry-count limit, got %v", err)
	}
}
