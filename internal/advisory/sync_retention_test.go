package advisory

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

func TestSyncOSVRetainsBoundedSnapshotHistory(t *testing.T) {
	for _, schema := range []string{schemaOSVJSON, schemaOSVZip} {
		t.Run(schema, func(t *testing.T) {
			server := retentionSnapshotServer(t, schema)
			cache := t.TempDir()
			var snapshots []CacheSnapshot
			for i, index := range []int{0, 1, 2, 3, 2, 0} {
				snapshot, err := SyncOSV(context.Background(), SyncOptions{SourceURL: fmt.Sprintf("%s/%d", server.URL, index), CachePath: cache, Client: server.Client(), Now: time.Date(2026, 7, 13, 0, i, 0, 0, time.UTC)})
				if err != nil {
					t.Fatalf("sync %d: %v", i, err)
				}
				snapshots = append(snapshots, snapshot)
				previousID := ""
				if i > 0 {
					previousID = snapshots[i-1].ID
				}
				assertRetainedSnapshotHistory(t, cache, snapshot.ID, previousID, min(i+1, 2))
			}
			assertSnapshotBlobsExist(t, cache, snapshots)
		})
	}
}

func TestUpdateManifestRetentionOrder(t *testing.T) {
	for _, timestamps := range [][]string{
		{"2026-07-13T01:00:00+01:00", "2026-07-13T00:00:00Z", "2026-07-14T00:00:00Z"},
		{"", "invalid", "2026-07-14T00:00:00Z"},
	} {
		cache := t.TempDir()
		root := advisoryOpenTestRoot(t, cache)
		history := []CacheSnapshot{
			{ID: "b", RetrievedAt: timestamps[1], Ecosystems: []string{strings.Repeat("<", 500000)}},
			{ID: "a", RetrievedAt: timestamps[0], Ecosystems: []string{strings.Repeat("<", 500000)}},
		}
		prior := CacheManifest{Latest: "a", Snapshots: history}
		if err := os.WriteFile(filepath.Join(cache, manifestFileName), testCacheManifestPayload(t, prior), 0o600); err != nil {
			t.Fatal(err)
		}
		current := CacheSnapshot{ID: "c", RetrievedAt: timestamps[2], Ecosystems: []string{strings.Repeat("<", 500000)}}
		if err := updateManifest(root, current, time.Now()); err != nil {
			t.Fatal(err)
		}
		manifest, err := LoadCacheManifest(cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(manifest.Snapshots) != 2 || manifest.Snapshots[0].ID != "b" || manifest.Snapshots[1].ID != "c" {
			t.Fatalf("expected ID tie-break to evict a: got %d records, latest %s", len(manifest.Snapshots), manifest.Latest)
		}
	}
}

func TestSyncOSVCurrentMetadataTooLargePreservesPriorManifest(t *testing.T) {
	oversized, err := json.Marshal(map[string]any{"id": "huge", "affected": []any{map[string]any{"package": map[string]string{"name": "lib", "ecosystem": strings.Repeat("<", 1400000)}, "versions": []string{"1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := []byte(testOSVSnapshot("prior"))
		if r.URL.Path == "/large" {
			payload = oversized
		}
		if _, err := w.Write(payload); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cache := t.TempDir()
	opts := SyncOptions{SourceURL: server.URL, CachePath: cache, Client: server.Client()}
	if _, err := SyncOSV(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	prior, err := os.ReadFile(filepath.Join(cache, manifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	opts.SourceURL += "/large"
	if _, err := SyncOSV(context.Background(), opts); !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized current metadata rejection, got %v", err)
	}
	after, err := os.ReadFile(filepath.Join(cache, manifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(prior, after) {
		t.Fatal("failed publication changed prior manifest")
	}
	entries, err := os.ReadDir(filepath.Join(cache, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("failed publication left snapshot or temporary file: %v", entries)
	}
}

func retentionSnapshotServer(t *testing.T, schema string) *httptest.Server {
	t.Helper()
	payloads := make([][]byte, 4)
	for i := range payloads {
		ecosystem := strings.Repeat("<&", 250000)
		payload, err := json.Marshal(map[string]any{"id": fmt.Sprintf("OSV-%d", i), "affected": []any{map[string]any{"package": map[string]string{"name": "lib", "ecosystem": ecosystem}, "versions": []string{"1.0"}}}})
		if err != nil {
			t.Fatal(err)
		}
		if schema == schemaOSVZip {
			payload = testOSVZipEntries(t, testOSVZipEntry{name: "entry.json", payload: string(payload), method: zip.Store})
		}
		payloads[i] = payload
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var index int
		if _, err := fmt.Sscanf(r.URL.Path, "/%d", &index); err != nil || index < 0 || index >= len(payloads) {
			t.Errorf("invalid path %q", r.URL.Path)
			return
		}
		if _, err := w.Write(payloads[index]); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSyncOSVConcurrentRetention(t *testing.T) {
	server := retentionSnapshotServer(t, schemaOSVZip)
	cache := t.TempDir()
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Go(func() {
			_, err := SyncOSV(context.Background(), SyncOptions{SourceURL: fmt.Sprintf("%s/%d", server.URL, i), CachePath: cache, Client: server.Client(), Now: time.Date(2026, 7, 13, 0, i, 0, 0, time.UTC)})
			if err != nil {
				t.Errorf("concurrent sync %d: %v", i, err)
			}
		})
	}
	group.Wait()
	manifest, err := LoadCacheManifest(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Snapshots) != 2 {
		t.Fatalf("expected two retained records, got %d", len(manifest.Snapshots))
	}
	foundCurrent := false
	for _, snapshot := range manifest.Snapshots {
		foundCurrent = foundCurrent || snapshot.ID == manifest.Latest
	}
	if !foundCurrent {
		t.Fatal("current snapshot evicted")
	}
	entries, err := os.ReadDir(filepath.Join(cache, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected all four snapshot blobs, got %d", len(entries))
	}
}

func TestUpdateManifestRetentionFitsExactSerializedBudget(t *testing.T) {
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	current := CacheSnapshot{ID: "current", RetrievedAt: "2000-01-01T00:00:00Z", Ecosystems: []string{"line\nquote\"<&", strings.Repeat("<", 500000)}}
	fixture := newAdvisoryManifestSizeFixture(t, now, current, maxCacheManifestBytes)
	prior, err := LoadCacheManifest(fixture.cachePath)
	if err != nil {
		t.Fatal(err)
	}
	prior.Snapshots = append(prior.Snapshots, CacheSnapshot{ID: "000-old", SourceURL: "quote\"\n<&"})
	if err := os.WriteFile(fixture.manifestPath, testCacheManifestPayload(t, prior), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateManifest(advisoryOpenTestRoot(t, fixture.cachePath), current, now); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(fixture.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, fixture.wantFinalPayload) {
		t.Fatalf("expected exact %d-byte retained manifest, got %d bytes", len(fixture.wantFinalPayload), len(got))
	}
}

func assertRetainedSnapshotHistory(t *testing.T, cache, currentID, previousID string, count int) {
	t.Helper()
	manifest, err := LoadCacheManifest(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Snapshots) != count || manifest.Latest != currentID {
		t.Fatalf("latest=%s records=%d, want latest=%s records=%d", manifest.Latest, len(manifest.Snapshots), currentID, count)
	}
	for _, retained := range manifest.Snapshots {
		if retained.ID != currentID && (previousID == "" || retained.ID != previousID) {
			t.Fatalf("old metadata retained: %s", retained.ID)
		}
	}
	info, err := os.Stat(filepath.Join(cache, manifestFileName))
	if err != nil || info.Size() > maxCacheManifestBytes {
		t.Fatalf("manifest size: %v %v", info, err)
	}
}

func assertSnapshotBlobsExist(t *testing.T, cache string, snapshots []CacheSnapshot) {
	t.Helper()
	for _, snapshot := range snapshots {
		if _, err := os.Stat(filepath.Join(cache, snapshot.Path)); err != nil {
			t.Fatalf("historical snapshot blob removed: %v", err)
		}
	}
}
