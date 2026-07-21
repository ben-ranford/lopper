package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/advisory"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestExecuteAdvisoryStatusRequiresPreviewAndWritesManifest(t *testing.T) {
	cachePath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(cachePath, "manifest.json"), `{
  "schemaVersion": "lopper.advisory-cache.v1",
  "updatedAt": "2026-07-13T00:00:00Z",
  "latest": "abc123",
  "snapshots": [
    {
      "id": "abc123",
      "sourceUrl": "https://example.test/osv.zip",
      "retrievedAt": "2026-07-13T00:00:00Z",
      "digest": "sha256:abc",
      "path": "snapshots/abc123.zip",
      "schema": "osv-zip",
      "entryCount": 10,
      "sizeBytes": 123
    }
  ]
}`)
	application := &App{}
	req := DefaultRequest()
	req.Mode = ModeAdvisory
	req.Advisory = AdvisoryRequest{Command: "status", Provider: "osv", CachePath: cachePath}

	_, err := application.Execute(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), report.AdvisoryOSVSyncPreviewFeature) {
		t.Fatalf("expected advisory preview feature error, got %v", err)
	}

	outputPath := filepath.Join(t.TempDir(), "status.json")
	req.Advisory.OutputPath = outputPath
	req.Advisory.Features = mustResolveAppTestFeatures(t, report.AdvisoryOSVSyncPreviewFeature)
	output, err := application.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute advisory status: %v", err)
	}
	assertContainsAll(t, output, []string{"advisory cache status written", outputPath})
	var manifest advisory.CacheManifest
	if err := json.Unmarshal(mustReadAdvisoryTestFile(t, outputPath), &manifest); err != nil {
		t.Fatalf("decode advisory status output: %v", err)
	}
	if manifest.Latest != "abc123" || len(manifest.Snapshots) != 1 {
		t.Fatalf("unexpected advisory status manifest: %#v", manifest)
	}
}

func mustReadAdvisoryTestFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestExecuteAdvisoryRejectsUnsupportedCommands(t *testing.T) {
	req := DefaultRequest()
	req.Mode = ModeAdvisory
	req.Advisory = AdvisoryRequest{
		Command:  "sync",
		Provider: "ghsa",
		Features: mustResolveAppTestFeatures(t, report.AdvisoryOSVSyncPreviewFeature),
	}
	if _, err := (&App{}).Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "unsupported advisory sync provider") {
		t.Fatalf("expected unsupported provider error, got %v", err)
	}
	req.Advisory.Command = "prune"
	req.Advisory.Provider = "osv"
	if _, err := (&App{}).Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "unknown advisory command") {
		t.Fatalf("expected unknown command error, got %v", err)
	}
}

func TestExecuteAdvisoryErrorBranches(t *testing.T) {
	features := mustResolveAppTestFeatures(t, report.AdvisoryOSVSyncPreviewFeature)
	req := DefaultRequest()
	req.Mode = ModeAdvisory
	req.Advisory = AdvisoryRequest{
		Command:   "sync",
		Provider:  "osv",
		CachePath: t.TempDir(),
		SourceURL: "http://example.test/osv.zip",
		Features:  features,
	}
	if _, err := (&App{}).Execute(context.Background(), req); err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("expected advisory sync error, got %v", err)
	}
	req.Advisory.Command = "status"
	req.Advisory.CachePath = filepath.Join(t.TempDir(), "missing")
	if _, err := (&App{}).Execute(context.Background(), req); err == nil {
		t.Fatalf("expected advisory status load error")
	}
	if _, err := persistJSONCommandOutput(func() {}, "", "bad json"); err == nil {
		t.Fatalf("expected JSON marshal error")
	}
}

func TestExecuteAdvisorySyncDownloadsSnapshot(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"vulns":[{"id":"OSV-1","affected":[{"package":{"ecosystem":"npm","name":"lib"},"versions":["1.0.0"]}]}]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()
	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
	})

	req := DefaultRequest()
	req.Mode = ModeAdvisory
	req.Advisory = AdvisoryRequest{
		Command:   "sync",
		Provider:  "osv",
		CachePath: t.TempDir(),
		SourceURL: server.URL,
		Features:  mustResolveAppTestFeatures(t, report.AdvisoryOSVSyncPreviewFeature),
	}

	output, err := (&App{}).Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute advisory sync: %v", err)
	}
	assertContainsAll(t, output, []string{`"sourceUrl": "` + server.URL + `"`, `"entryCount": 1`, `"ecosystems": [`})
	if _, err := advisory.LoadCacheManifest(req.Advisory.CachePath); err != nil {
		t.Fatalf("expected advisory cache manifest: %v", err)
	}
}
