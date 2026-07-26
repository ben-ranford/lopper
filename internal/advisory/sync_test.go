package advisory

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	downloadSnapshotWriteErrorChildEnv    = "LOPPER_DOWNLOAD_SNAPSHOT_WRITE_ERROR_CHILD"
	publicationLockChildModeEnv           = "LOPPER_PUBLICATION_LOCK_CHILD_MODE"
	publicationLockChildCacheEnv          = "LOPPER_PUBLICATION_LOCK_CHILD_CACHE"
	publicationLockChildMarkerEnv         = "LOPPER_PUBLICATION_LOCK_CHILD_MARKER"
	publicationLockChildModeSync          = "sync"
	publicationLockChildModeExitWhileHeld = "exit-while-held"
	legacyPublicationLockFileName         = ".publication.lock"
)

var publicationLockChildNow = time.Date(2026, time.July, 20, 3, 0, 0, 0, time.UTC)

type advisorySyncResult struct {
	snapshot CacheSnapshot
	err      error
}

func testOSVAdvisory(id string) string {
	return `{"id":"` + id + `","affected":[{"package":{"ecosystem":"Go","name":"example.com/lib"},"ranges":[{"type":"SEMVER","events":[{"introduced":"0"}]}]}]}`
}

func testOSVSnapshot(id string) string {
	return `{"vulns":[` + testOSVAdvisory(id) + `]}`
}

type testOSVZipEntry struct {
	name    string
	payload string
	method  uint16
}

type closerFunc func() error

func (fn closerFunc) Close() error {
	return fn()
}

func testOSVZip(t *testing.T, name, payload string) []byte {
	t.Helper()
	return testOSVZipEntries(t, testOSVZipEntry{name: name, payload: payload, method: zip.Deflate})
}

func testOSVZipEntries(t *testing.T, entries ...testOSVZipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, source := range entries {
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: source.name, Method: source.method})
		if err != nil {
			t.Fatalf("create test ZIP entry: %v", err)
		}
		if _, err := io.WriteString(entry, source.payload); err != nil {
			t.Fatalf("write test ZIP entry: %v", err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close test ZIP: %v", err)
	}
	return buffer.Bytes()
}

func TestSyncOSVWritesJSONSnapshotManifest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(testOSVSnapshot("OSV-1"))); err != nil {
			t.Errorf("write test response: %v", err)
		}
	}))
	defer server.Close()

	cachePath := t.TempDir()
	snapshot, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: cachePath,
		Now:       time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC),
		Client:    server.Client(),
	})
	if err != nil {
		t.Fatalf("sync OSV: %v", err)
	}
	if filepath.Ext(snapshot.Path) != ".json" || snapshot.Schema != "osv-json" {
		t.Fatalf("unexpected snapshot metadata: %#v", snapshot)
	}
	if snapshot.EntryCount != 1 || len(snapshot.Ecosystems) != 1 || snapshot.Ecosystems[0] != "Go" {
		t.Fatalf("unexpected snapshot contents metadata: %#v", snapshot)
	}
	info, err := os.Stat(filepath.Join(cachePath, filepath.FromSlash(snapshot.Path)))
	if err != nil {
		t.Fatalf("stat snapshot file: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("expected snapshot mode 0640, got %#o", info.Mode().Perm())
	}
	manifest, err := LoadCacheManifest(cachePath)
	if err != nil {
		t.Fatalf("load advisory cache manifest: %v", err)
	}
	if manifest.SchemaVersion != manifestSchemaVersion || manifest.Latest != snapshot.ID || len(manifest.Snapshots) != 1 {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
}

func TestSyncOSVWritesSingleAdvisorySnapshotManifest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(testOSVAdvisory("GO-2021-0113"))); err != nil {
			t.Errorf("write test response: %v", err)
		}
	}))
	defer server.Close()

	snapshot, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: t.TempDir(),
		Client:    server.Client(),
	})
	if err != nil {
		t.Fatalf("sync single OSV advisory: %v", err)
	}
	if snapshot.EntryCount != 1 || len(snapshot.Ecosystems) != 1 || snapshot.Ecosystems[0] != "Go" {
		t.Fatalf("unexpected single-advisory metadata: %#v", snapshot)
	}
}

func TestSyncOSVUsesZipExtensionForZipSnapshots(t *testing.T) {
	payload := testOSVZip(t, "GO-2021-0113.json", testOSVAdvisory("GO-2021-0113"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(payload); err != nil {
			t.Errorf("write test response: %v", err)
		}
	}))
	defer server.Close()

	snapshot, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: t.TempDir(),
		Client:    server.Client(),
	})
	if err != nil {
		t.Fatalf("sync OSV zip: %v", err)
	}
	if filepath.Ext(snapshot.Path) != ".zip" || snapshot.Schema != "osv-zip" {
		t.Fatalf("expected zip snapshot metadata, got %#v", snapshot)
	}
}

func TestSyncOSVRejectsUnrecognizedSnapshotSchema(t *testing.T) {
	assertSyncRejectsSnapshot(t, "<html>mirror unavailable</html>", "unrecognized OSV snapshot schema")
}

func TestSyncOSVRejectsInvalidJSONSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
	}{
		{name: "wrong envelope", payload: `{"error":"quota exceeded"}`},
		{name: "unrelated object array", payload: `[{"userId":1,"id":1,"title":"not an advisory"}]`},
		{name: "unusable advisory", payload: `[{"id":"OSV-1","affected":[]}]`},
		{name: "truncated document", payload: `{"vulns":[{"id":"OSV-1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertSyncRejectsSnapshot(t, tc.payload, "invalid OSV JSON snapshot")
		})
	}
}

func TestSyncOSVRejectsInvalidZIPSnapshots(t *testing.T) {
	payload := testOSVZip(t, "response.json", `{"error":"quota exceeded"}`)
	assertSyncRejectsSnapshot(t, string(payload), "invalid OSV ZIP snapshot")
}

func assertSyncRejectsSnapshot(t *testing.T, payload, wantError string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(payload)); err != nil {
			t.Errorf("write test response: %v", err)
		}
	}))
	defer server.Close()

	cachePath := t.TempDir()
	_, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: cachePath,
		Client:    server.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("expected %q error, got %v", wantError, err)
	}
	if _, err := os.Stat(filepath.Join(cachePath, manifestFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected rejected snapshot not to update manifest, got %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(cachePath, "snapshots"))
	if err != nil {
		t.Fatalf("read snapshots directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected rejected snapshot temp file to be cleaned up, got %#v", entries)
	}
}

func TestSyncOSVDefaultsSourceAndNow(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != DefaultOSVSourceURL {
			t.Fatalf("expected default OSV URL, got %s", req.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
		}, nil
	})}

	snapshot, err := SyncOSV(context.Background(), SyncOptions{
		CachePath: t.TempDir(),
		Client:    client,
	})
	if err != nil {
		t.Fatalf("sync OSV with defaults: %v", err)
	}
	if snapshot.SourceURL != DefaultOSVSourceURL || snapshot.RetrievedAt == "" {
		t.Fatalf("expected default source URL and retrieval time, got %#v", snapshot)
	}
}

func TestSyncOSVCreatesMissingCacheRoot(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"vulns":[]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	cachePath := filepath.Join(t.TempDir(), "nested", "cache")
	snapshot, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: cachePath,
		Client:    server.Client(),
	})
	if err != nil {
		t.Fatalf("sync OSV with missing cache root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cachePath, filepath.FromSlash(snapshot.Path))); err != nil {
		t.Fatalf("stat created snapshot: %v", err)
	}
}

func TestSyncOSVStreamsLargeZipSnapshots(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		archive := zip.NewWriter(w)
		entry, err := archive.Create("GO-2021-0113.json")
		if err == nil {
			_, err = io.WriteString(entry, testOSVAdvisory("GO-2021-0113"))
		}
		if err == nil {
			entry, err = archive.CreateHeader(&zip.FileHeader{Name: "padding.bin", Method: zip.Store})
		}
		if err == nil {
			_, err = io.Copy(entry, bytes.NewReader(bytes.Repeat([]byte("z"), 70*1024*1024)))
		}
		if closeErr := archive.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Errorf("write large ZIP response: %v", err)
		}
	}))
	defer server.Close()

	snapshot, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: t.TempDir(),
		Client:    server.Client(),
	})
	if err != nil {
		t.Fatalf("sync large OSV zip: %v", err)
	}
	if snapshot.Schema != "osv-zip" || snapshot.SizeBytes <= maxSyncMetadataBytes {
		t.Fatalf("expected streamed large zip snapshot metadata, got %#v", snapshot)
	}
}

func TestDownloadSnapshotUnderRootRejectsOversizedContentLength(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: maxSyncSnapshotBytes + 1,
			Body:          io.NopCloser(strings.NewReader(`[]`)),
		}, nil
	})}

	root := advisoryOpenTestRoot(t, t.TempDir())
	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.zip", client, root); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized advisory snapshot error, got %v", err)
	}
}

func TestStreamSnapshotResponseEnforcesSizeLimit(t *testing.T) {
	t.Run("exact limit", func(t *testing.T) {
		var destination bytes.Buffer
		preview, sizeBytes, err := streamSnapshotResponseWithLimit(strings.NewReader("1234"), &destination, 4)
		if err != nil || string(preview) != "1234" || sizeBytes != 4 || destination.String() != "1234" {
			t.Fatalf("expected exact-limit stream success, preview=%q size=%d destination=%q err=%v", preview, sizeBytes, destination.String(), err)
		}
	})

	t.Run("unknown length exceeds limit", func(t *testing.T) {
		var destination bytes.Buffer
		preview, sizeBytes, err := streamSnapshotResponseWithLimit(strings.NewReader("12345"), &destination, 4)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("expected streamed size-limit error, got %v", err)
		}
		if len(preview) != 0 || sizeBytes != 0 || destination.String() != "12345" {
			t.Fatalf("unexpected oversized stream result, preview=%q size=%d destination=%q", preview, sizeBytes, destination.String())
		}
	})
}

func TestSyncOSVValidatesSourceAndCachePath(t *testing.T) {
	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: "http://example.test/osv.zip", CachePath: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "must use https") {
		t.Fatalf("expected https validation error, got %v", err)
	}
	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: "https://example.test/osv.zip"}); err == nil || !strings.Contains(err.Error(), "cache path is required") {
		t.Fatalf("expected cache path validation error, got %v", err)
	}
	if err := validateSyncURL("https://"); err == nil || !strings.Contains(err.Error(), "include a host") {
		t.Fatalf("expected host validation error, got %v", err)
	}
	if err := validateSyncURL("https://%zz"); err == nil || !strings.Contains(err.Error(), "invalid advisory source URL") {
		t.Fatalf("expected parse validation error, got %v", err)
	}
}

func TestSyncOSVRejectsHTTPSRedirectDowngrade(t *testing.T) {
	var httpHits atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"vulns":[{"id":"DOWNGRADED"}]}`)); err != nil {
			t.Errorf("write downgraded response: %v", err)
		}
	}))
	defer httpServer.Close()

	httpsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpServer.URL, http.StatusFound)
	}))
	defer httpsServer.Close()

	snapshot, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: httpsServer.URL,
		CachePath: t.TempDir(),
		Client:    httpsServer.Client(),
	})
	if err == nil {
		t.Errorf("expected downgrade redirect error, got snapshot %#v", snapshot)
	} else if !strings.Contains(err.Error(), "download advisory snapshot") || !strings.Contains(err.Error(), "redirect must use https") {
		t.Fatalf("expected wrapped https redirect error, got %v", err)
	}
	if httpHits.Load() != 0 {
		t.Errorf("expected plaintext redirect destination to remain untouched, got %d hits", httpHits.Load())
	}
}

func TestSyncOSVRejectsInvalidHTTPSRedirectBeforeCallerPolicy(t *testing.T) {
	t.Helper()

	if err := validateSyncURL("https://"); err == nil || !strings.Contains(err.Error(), "include a host") {
		t.Fatalf("expected full URL validator to reject empty-host HTTPS redirect, got %v", err)
	}

	var transportCalls atomic.Int32
	var callerPolicyCalls atomic.Int32
	client := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch transportCalls.Add(1) {
			case 1:
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": []string{"https://"}},
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    req,
				}, nil
			default:
				t.Fatalf("redirect destination transport must remain untouched, got request to %q", req.URL.String())
				return nil, nil
			}
		}),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			callerPolicyCalls.Add(1)
			t.Fatalf("caller CheckRedirect must not run for invalid redirect target %q via %d hops", req.URL.String(), len(via))
			return nil
		},
	}

	_, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: "https://example.test/start.json",
		CachePath: t.TempDir(),
		Client:    client,
	})
	if err == nil {
		t.Fatal("expected invalid HTTPS redirect error")
	}
	if !strings.Contains(err.Error(), "download advisory snapshot") || !strings.Contains(err.Error(), "include a host") {
		t.Fatalf("expected wrapped host validation error, got %v", err)
	}
	if got := callerPolicyCalls.Load(); got != 0 {
		t.Fatalf("expected caller CheckRedirect to stay at 0 calls, got %d", got)
	}
	if got := transportCalls.Load(); got != 1 {
		t.Fatalf("expected redirect destination transport to remain untouched after first response, got %d calls", got)
	}
}

func TestSyncOSVAllowsHTTPSRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Header().Set("Content-Type", "application/json")
			if _, err := w.Write([]byte(testOSVSnapshot("OSV-HTTPS"))); err != nil {
				t.Errorf("write redirected response: %v", err)
			}
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusInternalServerError)
			return
		}
	}))
	defer server.Close()

	finalSnapshot, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL + "/start",
		CachePath: t.TempDir(),
		Client:    server.Client(),
	})
	if err != nil {
		t.Fatalf("sync OSV through HTTPS redirect: %v", err)
	}
	if finalSnapshot.EntryCount != 1 || finalSnapshot.Schema != schemaOSVJSON {
		t.Fatalf("expected redirected HTTPS snapshot to be accepted, got %#v", finalSnapshot)
	}
}

func TestSyncOSVComposesCallerCheckRedirectWithoutMutatingClient(t *testing.T) {
	sentinel := errors.New("caller redirect policy")
	redirectServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.test/final.json", http.StatusFound)
	}))
	defer redirectServer.Close()

	recorder := advisoryRedirectPolicyRecorder{sentinel: sentinel}
	client := redirectServer.Client()
	originalPolicy := recorder.policy
	client.CheckRedirect = originalPolicy

	_, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: redirectServer.URL,
		CachePath: t.TempDir(),
		Client:    client,
	})
	if err == nil {
		t.Fatal("expected caller redirect policy error")
	}
	if recorder.checkRedirectCalls.Load() != 1 {
		t.Fatalf("expected CheckRedirect to run once, got %d", recorder.checkRedirectCalls.Load())
	}
	if recorder.recordedTarget != "https://example.test/final.json" {
		t.Fatalf("expected redirect target %q, got %q", "https://example.test/final.json", recorder.recordedTarget)
	}
	if len(recorder.recordedVia) != 1 || recorder.recordedVia[0] != redirectServer.URL {
		t.Fatalf("expected one-element via chain [%q], got %v", redirectServer.URL, recorder.recordedVia)
	}
	if !strings.Contains(err.Error(), "download advisory snapshot") || !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped sentinel redirect error, got %v", err)
	}
	recorder.reset()
	req := advisoryMustNewRequest(t, "http://example.test/plaintext.json", "create manual request")
	viaReq := advisoryMustNewRequest(t, redirectServer.URL, "create manual via request")
	got := client.CheckRedirect(req, []*http.Request{viaReq})
	if !errors.Is(got, sentinel) {
		t.Fatalf("expected original client policy to remain unchanged, got %v", got)
	}
	if recorder.checkRedirectCalls.Load() != 1 {
		t.Fatalf("expected original client policy to remain callable once, got %d calls", recorder.checkRedirectCalls.Load())
	}
	if recorder.recordedTarget != "http://example.test/plaintext.json" {
		t.Fatalf("expected manual target %q, got %q", "http://example.test/plaintext.json", recorder.recordedTarget)
	}
	if len(recorder.recordedVia) != 1 || recorder.recordedVia[0] != redirectServer.URL {
		t.Fatalf("expected manual via chain [%q], got %v", redirectServer.URL, recorder.recordedVia)
	}
}

func TestSyncOSVPreservesDefaultRedirectLimitWhenClientPolicyNil(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer server.Close()

	_, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL + "/loop",
		CachePath: t.TempDir(),
		Client:    server.Client(),
	})
	if err == nil {
		t.Fatal("expected redirect limit error")
	}
	if !strings.Contains(err.Error(), "download advisory snapshot") || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Fatalf("expected default redirect limit error, got %v", err)
	}
}

func TestFetchSnapshotRejectsHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	if _, err := fetchSnapshot(context.Background(), server.URL, server.Client()); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("expected HTTP status error, got %v", err)
	}
}

func TestFetchSnapshotReturnsDownloadedBytes(t *testing.T) {
	payload := testOSVSnapshot("OSV-1")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(payload)); err != nil {
			t.Errorf("write snapshot response: %v", err)
		}
	}))
	defer server.Close()

	data, err := fetchSnapshot(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("fetch snapshot: %v", err)
	}
	if string(data) != payload {
		t.Fatalf("unexpected fetched bytes %q", string(data))
	}
}

func TestFetchSnapshotReturnsDownloadedZipBytes(t *testing.T) {
	payload := testOSVZip(t, "GO-2021-0113.json", testOSVAdvisory("GO-2021-0113"))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(payload); err != nil {
			t.Errorf("write ZIP snapshot response: %v", err)
		}
	}))
	defer server.Close()

	data, err := fetchSnapshot(context.Background(), server.URL, server.Client())
	if err != nil {
		t.Fatalf("fetch ZIP snapshot: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Fatal("expected fetched ZIP bytes to match the response")
	}
}

func TestFetchSnapshotMkdirTempError(t *testing.T) {
	tmpRoot := t.TempDir()
	tmpDirFile := filepath.Join(tmpRoot, "tmpdir-file")
	if err := os.WriteFile(tmpDirFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write TMPDIR file: %v", err)
	}
	t.Setenv("TMPDIR", tmpDirFile)

	if _, err := fetchSnapshot(context.Background(), "https://example.test/osv.json", nil); err == nil || !strings.Contains(err.Error(), "create advisory temp dir") {
		t.Fatalf("expected temp dir creation error, got %v", err)
	}
}

func TestFetchSnapshotTempDirCleanupError(t *testing.T) {
	tmpRoot := t.TempDir()
	t.Setenv("TMPDIR", tmpRoot)
	t.Cleanup(func() {
		if err := os.Chmod(tmpRoot, 0o700); err != nil {
			t.Fatalf("restore TMPDIR root permissions: %v", err)
		}
	})

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if err := os.Chmod(tmpRoot, 0o500); err != nil {
			t.Fatalf("chmod TMPDIR root: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
		}, nil
	})}

	if _, err := fetchSnapshot(context.Background(), "https://example.test/osv.json", client); err == nil || !strings.Contains(err.Error(), "remove advisory temp dir") {
		t.Fatalf("expected temp dir cleanup error, got %v", err)
	}
}

func TestFetchSnapshotErrorBranches(t *testing.T) {
	if _, err := fetchSnapshot(context.Background(), "://bad", nil); err == nil {
		t.Fatalf("expected invalid request URL error")
	}
	clientErr := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})}
	if _, err := fetchSnapshot(context.Background(), "https://example.test/osv.zip", clientErr); err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("expected client error, got %v", err)
	}
	readErr := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: &errReadCloser{}}, nil
	})}
	if _, err := fetchSnapshot(context.Background(), "https://example.test/osv.zip", readErr); err == nil || !strings.Contains(err.Error(), "read advisory snapshot") {
		t.Fatalf("expected read error, got %v", err)
	}
	statusCloseErr := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: &errCloseReadCloser{Reader: strings.NewReader("down")}}, nil
	})}
	if _, err := fetchSnapshot(context.Background(), "https://example.test/osv.zip", statusCloseErr); err == nil || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected status close error, got %v", err)
	}
	successCloseErr := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: &errCloseReadCloser{Reader: strings.NewReader("[]")}}, nil
	})}
	if _, err := fetchSnapshot(context.Background(), "https://example.test/osv.zip", successCloseErr); err == nil || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected success close error, got %v", err)
	}

	readCloseErr := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: &errReadCloseCloser{}}, nil
	})}
	if _, err := fetchSnapshot(context.Background(), "https://example.test/osv.zip", readCloseErr); err == nil || !strings.Contains(err.Error(), "read advisory snapshot") || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected read+close error, got %v", err)
	}
}

func TestDownloadSnapshotTempFileCreationErrors(t *testing.T) {
	tempDirFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(tempDirFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write temp dir file: %v", err)
	}

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
		}, nil
	})}
	if _, err := downloadSnapshot(context.Background(), "https://example.test/osv.json", client, tempDirFile); err == nil || !strings.Contains(err.Error(), "create advisory snapshot temp file") {
		t.Fatalf("expected temp file creation error, got %v", err)
	}

	closeErrClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errCloseReadCloser{Reader: strings.NewReader(`[]`)},
		}, nil
	})}
	if _, err := downloadSnapshot(context.Background(), "https://example.test/osv.json", closeErrClient, tempDirFile); err == nil || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected temp file creation error with close detail, got %v", err)
	}
}

func TestDownloadSnapshotWriteError(t *testing.T) {
	if os.Getenv(downloadSnapshotWriteErrorChildEnv) == "1" {
		runDownloadSnapshotWriteErrorChild(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestDownloadSnapshotWriteError$")
	cmd.Env = append(os.Environ(), downloadSnapshotWriteErrorChildEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run child test process: %v\n%s", err, output)
	}
}

func runDownloadSnapshotWriteErrorChild(t *testing.T) {
	var oldLimit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &oldLimit); err != nil {
		t.Fatalf("get RLIMIT_FSIZE: %v", err)
	}

	defer func() {
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &oldLimit); err != nil {
			t.Fatalf("restore RLIMIT_FSIZE: %v", err)
		}
	}()
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 1, Max: oldLimit.Max}); err != nil {
		t.Fatalf("set RLIMIT_FSIZE: %v", err)
	}

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"vulns":[{"id":"OSV-1"}]}`)),
		}, nil
	})}
	if _, err := downloadSnapshot(context.Background(), "https://example.test/osv.json", client, t.TempDir()); err == nil || !strings.Contains(err.Error(), "write advisory snapshot temp file") {
		t.Fatalf("expected snapshot write error, got %v", err)
	}

	closeErrClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errCloseReadCloser{Reader: strings.NewReader(`{"vulns":[{"id":"OSV-2"}]}`)},
		}, nil
	})}
	if _, err := downloadSnapshot(context.Background(), "https://example.test/osv.json", closeErrClient, t.TempDir()); err == nil || !strings.Contains(err.Error(), "write advisory snapshot temp file") || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected snapshot write+close error, got %v", err)
	}
}

func TestSyncOSVUpdateAndFilesystemErrors(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"vulns":[]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()
	advisoryTestSyncInvalidManifest(t, server)
	advisoryTestSyncOversizedManifest(t, server)
	advisoryTestSyncFileCachePath(t, server)
	advisoryTestSyncSnapshotPlacementFailure(t, server)
	advisoryTestSyncDownloadFailure(t)
}

func advisoryTestSyncInvalidManifest(t *testing.T, server *httptest.Server) {
	t.Helper()
	cachePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cachePath, manifestFileName), []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid manifest: %v", err)
	}
	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: cachePath, Client: server.Client()}); err == nil || !strings.Contains(err.Error(), "parse advisory cache manifest") {
		t.Fatalf("expected manifest parse error, got %v", err)
	}
}

func advisoryTestSyncOversizedManifest(t *testing.T, server *httptest.Server) {
	t.Helper()
	oversizedManifestCache := t.TempDir()
	if err := writeOversizedValidManifest(filepath.Join(oversizedManifestCache, manifestFileName), maxCacheManifestBytes+1); err != nil {
		t.Fatalf("write oversized manifest: %v", err)
	}
	_, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: oversizedManifestCache, Client: server.Client()})
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized manifest error during update merge, got %v", err)
	}
	if !strings.Contains(err.Error(), "read advisory cache manifest") {
		t.Fatalf("expected advisory manifest read context during update merge, got %v", err)
	}
}

func advisoryTestSyncFileCachePath(t *testing.T, server *httptest.Server) {
	t.Helper()
	fileCache := filepath.Join(t.TempDir(), "cache-file")
	if err := os.WriteFile(fileCache, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file cache: %v", err)
	}
	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: fileCache, Client: server.Client()}); err == nil || !strings.Contains(err.Error(), "create advisory cache") {
		t.Fatalf("expected cache directory error, got %v", err)
	}
}

func advisoryTestSyncSnapshotPlacementFailure(t *testing.T, server *httptest.Server) {
	t.Helper()
	data := []byte(`{"vulns":[]}`)
	sum := sha256.Sum256(data)
	id := hex.EncodeToString(sum[:12])
	writeFailCache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(writeFailCache, "snapshots", id+".json"), 0o750); err != nil {
		t.Fatalf("create snapshot path directory: %v", err)
	}
	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: writeFailCache, Client: server.Client()}); err == nil || !strings.Contains(err.Error(), "write advisory snapshot") {
		t.Fatalf("expected snapshot write error, got %v", err)
	}
}

func advisoryTestSyncDownloadFailure(t *testing.T) {
	t.Helper()
	downloadFailClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("snapshot unavailable")
	})}
	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: "https://example.test/osv.json", CachePath: t.TempDir(), Client: downloadFailClient}); err == nil || !strings.Contains(err.Error(), "download advisory snapshot") {
		t.Fatalf("expected snapshot download error, got %v", err)
	}
}

func TestSyncOSVManifestWriteSizeLimit(t *testing.T) {
	snapshotPayload := []byte(`{"vulns":[]}`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(snapshotPayload); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	now := time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC)
	digestSum := sha256.Sum256(snapshotPayload)
	digest := sha256Prefix + hex.EncodeToString(digestSum[:])
	snapshotID := snapshotIDFromDigest(digest)
	snapshot := CacheSnapshot{
		ID:          snapshotID,
		SourceURL:   server.URL,
		RetrievedAt: now.Format(time.RFC3339),
		Digest:      digest,
		Path:        filepath.ToSlash(filepath.Join("snapshots", snapshotID+".json")),
		Schema:      schemaOSVJSON,
		EntryCount:  0,
		SizeBytes:   int64(len(snapshotPayload)),
	}

	for _, tc := range []advisoryManifestSizeCase{
		{name: "exact limit succeeds", target: maxCacheManifestBytes},
		{name: "one byte over limit preserves prior manifest", target: maxCacheManifestBytes + 1, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runAdvisoryManifestSizeCase(t, server, now, snapshot, tc)
		})
	}
}

type advisoryManifestSizeCase struct {
	name      string
	target    int64
	wantError bool
}

type advisoryManifestSizeFixture struct {
	cachePath        string
	manifestPath     string
	priorManifest    CacheManifest
	priorPayload     []byte
	wantFinalPayload []byte
	snapshot         CacheSnapshot
}

func runAdvisoryManifestSizeCase(t *testing.T, server *httptest.Server, now time.Time, snapshot CacheSnapshot, tc advisoryManifestSizeCase) {
	t.Helper()
	fixture := newAdvisoryManifestSizeFixture(t, now, snapshot, tc.target)
	_, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: fixture.cachePath,
		Now:       now,
		Client:    server.Client(),
	})
	gotPayload, readErr := os.ReadFile(fixture.manifestPath)
	if readErr != nil {
		t.Fatalf("read manifest after sync: %v", readErr)
	}
	if tc.wantError {
		advisoryAssertRejectedManifestSizeUpdate(t, fixture, gotPayload, err)
		return
	}
	if err != nil {
		t.Fatalf("sync OSV at manifest size limit: %v", err)
	}
	if !bytes.Equal(gotPayload, fixture.wantFinalPayload) {
		t.Fatal("manifest at size limit did not match expected serialized payload")
	}
	if _, loadErr := LoadCacheManifest(fixture.cachePath); loadErr != nil {
		t.Fatalf("load manifest at exact size limit: %v", loadErr)
	}
}

func newAdvisoryManifestSizeFixture(t *testing.T, now time.Time, snapshot CacheSnapshot, target int64) advisoryManifestSizeFixture {
	t.Helper()
	paddingSnapshot := CacheSnapshot{ID: "zz-padding", Path: "snapshots/zz-padding.json"}
	finalManifest := CacheManifest{
		SchemaVersion: manifestSchemaVersion,
		UpdatedAt:     now.Format(time.RFC3339),
		Latest:        snapshot.ID,
		Snapshots:     []CacheSnapshot{snapshot, paddingSnapshot},
	}
	basePayload := testCacheManifestPayload(t, finalManifest)
	paddingBytes := target - int64(len(basePayload))
	if paddingBytes <= 0 {
		t.Fatalf("manifest fixture has no room for padding: base=%d target=%d", len(basePayload), target)
	}
	paddingSnapshot.SourceURL = strings.Repeat("a", int(paddingBytes))
	finalManifest.Snapshots[1] = paddingSnapshot
	wantFinalPayload := testCacheManifestPayload(t, finalManifest)
	if int64(len(wantFinalPayload)) != target {
		t.Fatalf("manifest fixture size=%d, want %d", len(wantFinalPayload), target)
	}
	priorManifest := CacheManifest{
		SchemaVersion: manifestSchemaVersion,
		UpdatedAt:     "2026-07-12T00:00:00Z",
		Latest:        paddingSnapshot.ID,
		Snapshots:     []CacheSnapshot{paddingSnapshot},
	}
	priorPayload := testCacheManifestPayload(t, priorManifest)
	if int64(len(priorPayload)) > maxCacheManifestBytes {
		t.Fatalf("prior manifest size=%d exceeds read limit", len(priorPayload))
	}
	cachePath := t.TempDir()
	manifestPath := filepath.Join(cachePath, manifestFileName)
	if err := os.WriteFile(manifestPath, priorPayload, 0o600); err != nil {
		t.Fatalf("write prior manifest: %v", err)
	}
	return advisoryManifestSizeFixture{
		cachePath:        cachePath,
		manifestPath:     manifestPath,
		priorManifest:    priorManifest,
		priorPayload:     priorPayload,
		wantFinalPayload: wantFinalPayload,
		snapshot:         snapshot,
	}
}

func advisoryAssertRejectedManifestSizeUpdate(t *testing.T, fixture advisoryManifestSizeFixture, gotPayload []byte, syncErr error) {
	t.Helper()
	if !errors.Is(syncErr, safeio.ErrFileTooLarge) {
		t.Fatalf("expected manifest size error, got %v", syncErr)
	}
	if !strings.Contains(syncErr.Error(), "write advisory cache manifest") {
		t.Fatalf("expected advisory manifest write context, got %v", syncErr)
	}
	if !bytes.Equal(gotPayload, fixture.priorPayload) {
		t.Fatal("rejected manifest update replaced the prior manifest")
	}
	if _, statErr := os.Stat(filepath.Join(fixture.cachePath, fixture.snapshot.Path)); !os.IsNotExist(statErr) {
		t.Fatalf("expected rejected manifest update to remove new snapshot %q, got %v", fixture.snapshot.Path, statErr)
	}
	manifest, loadErr := LoadCacheManifest(fixture.cachePath)
	if loadErr != nil {
		t.Fatalf("load prior manifest after rejected update: %v", loadErr)
	}
	if manifest.Latest != fixture.priorManifest.Latest || len(manifest.Snapshots) != 1 {
		t.Fatalf("unexpected prior manifest after rejected update: latest=%q snapshots=%d", manifest.Latest, len(manifest.Snapshots))
	}
}

func TestSyncOSVManifestWriteFailureKeepsPreexistingSnapshot(t *testing.T) {
	snapshotPayload := []byte(`{"vulns":[]}`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(snapshotPayload); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	now := time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC)
	digestSum := sha256.Sum256(snapshotPayload)
	digest := sha256Prefix + hex.EncodeToString(digestSum[:])
	snapshotID := snapshotIDFromDigest(digest)
	snapshotRel := filepath.ToSlash(filepath.Join("snapshots", snapshotID+".json"))

	paddingSnapshot := CacheSnapshot{
		ID:        "zz-padding",
		Path:      "snapshots/zz-padding.json",
		SourceURL: strings.Repeat("a", int(maxCacheManifestBytes)),
	}
	priorManifest := CacheManifest{
		SchemaVersion: manifestSchemaVersion,
		UpdatedAt:     "2026-07-12T00:00:00Z",
		Latest:        paddingSnapshot.ID,
		Snapshots:     []CacheSnapshot{paddingSnapshot},
	}
	cachePath := t.TempDir()
	manifestPath := filepath.Join(cachePath, manifestFileName)
	if err := os.WriteFile(manifestPath, testCacheManifestPayload(t, priorManifest), 0o600); err != nil {
		t.Fatalf("write oversized prior manifest: %v", err)
	}
	existingSnapshotPath := filepath.Join(cachePath, filepath.FromSlash(snapshotRel))
	if err := os.MkdirAll(filepath.Dir(existingSnapshotPath), 0o750); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	if err := os.WriteFile(existingSnapshotPath, snapshotPayload, 0o640); err != nil {
		t.Fatalf("write existing snapshot: %v", err)
	}

	_, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: cachePath,
		Now:       now,
		Client:    server.Client(),
	})
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected manifest size error, got %v", err)
	}
	if _, statErr := os.Stat(existingSnapshotPath); statErr != nil {
		t.Fatalf("expected preexisting snapshot to remain after manifest failure, got %v", statErr)
	}
}

func TestSyncOSVRejectsSymlinkedSnapshotsDirEscape(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"vulns":[]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	parentDir := t.TempDir()
	cachePath := filepath.Join(parentDir, "cache")
	outsideDir := filepath.Join(parentDir, "outside")
	if err := os.MkdirAll(cachePath, 0o750); err != nil {
		t.Fatalf("create cache dir: %v", err)
	}
	if err := os.MkdirAll(outsideDir, 0o750); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(cachePath, "snapshots")); err != nil {
		t.Fatalf("create snapshots symlink: %v", err)
	}

	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: cachePath, Client: server.Client()}); err == nil || !strings.Contains(err.Error(), "create advisory cache") {
		t.Fatalf("expected symlinked snapshots dir error, got %v", err)
	}
	entries, err := os.ReadDir(outsideDir)
	if err != nil {
		t.Fatalf("read outside dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected outside dir to stay untouched, got %d entries", len(entries))
	}
}

func TestSyncOSVRejectsSnapshotsDirSwapAfterDownload(t *testing.T) {
	server := advisoryEmptyOSVTLSServer(t)
	defer server.Close()

	paths := advisoryNewSwapPaths(t, false)
	advisorySetSyncAfterDownloadHook(t, func(cacheRoot, tempRel string) {
		advisoryRequireTempPresent(t, cacheRoot, tempRel, "before snapshots swap")
		if err := os.Rename(filepath.Join(cacheRoot, "snapshots"), filepath.Join(cacheRoot, "snapshots-holding")); err != nil {
			t.Fatalf("move snapshots dir aside: %v", err)
		}
		if err := os.Symlink(paths.outsideDir, filepath.Join(cacheRoot, "snapshots")); err != nil {
			t.Fatalf("replace snapshots with symlink: %v", err)
		}
	})

	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: paths.cachePath, Client: server.Client()}); err == nil || !strings.Contains(err.Error(), "write advisory snapshot") {
		t.Fatalf("expected snapshot swap error, got %v", err)
	}

	advisoryAssertDirEmpty(t, paths.outsideDir)
	advisoryAssertNoSafeIOTempFiles(t, paths.cachePath)
}

func TestSyncOSVRollsBackThroughPinnedSnapshotsRootAfterDirectoryReplacement(t *testing.T) {
	server := advisoryEmptyOSVTLSServer(t)
	defer server.Close()

	cachePath := t.TempDir()
	holdingPath := filepath.Join(cachePath, "snapshots-published")
	replacementPath := filepath.Join(cachePath, "snapshots")
	replacementMarker := filepath.Join(replacementPath, "concurrent.json")
	advisorySetSyncAfterSnapshotPlacementHook(t, func(cacheRoot, snapshotRel string) {
		if _, err := os.Stat(filepath.Join(cacheRoot, snapshotRel)); err != nil {
			t.Fatalf("stat published snapshot before directory replacement: %v", err)
		}
		if err := os.Rename(filepath.Join(cacheRoot, "snapshots"), holdingPath); err != nil {
			t.Fatalf("move published snapshots directory aside: %v", err)
		}
		if err := os.Mkdir(replacementPath, 0o750); err != nil {
			t.Fatalf("create replacement snapshots directory: %v", err)
		}
		if err := os.WriteFile(replacementMarker, []byte("concurrent"), 0o640); err != nil {
			t.Fatalf("write replacement snapshot marker: %v", err)
		}
	})

	_, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: cachePath,
		Client:    server.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "snapshots directory changed during publication") {
		t.Fatalf("expected snapshots directory replacement error, got %v", err)
	}
	holdingEntries, readErr := os.ReadDir(holdingPath)
	if readErr != nil {
		t.Fatalf("read original pinned snapshots directory: %v", readErr)
	}
	if len(holdingEntries) != 0 {
		t.Fatalf("expected pinned snapshots directory rollback and cleanup, got %#v", holdingEntries)
	}
	marker, readErr := os.ReadFile(replacementMarker)
	if readErr != nil || string(marker) != "concurrent" {
		t.Fatalf("expected replacement snapshots directory to stay untouched, content=%q err=%v", marker, readErr)
	}
}

func TestSyncOSVRejectsMismatchedNoReplaceWinnerBeforeManifestUpdate(t *testing.T) {
	snapshotPayload := []byte(`{"vulns":[]}`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(snapshotPayload); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	cachePath := t.TempDir()
	manifestPath := filepath.Join(cachePath, manifestFileName)
	if err := writeOversizedValidManifest(manifestPath, maxCacheManifestBytes+1); err != nil {
		t.Fatalf("write manifest failure fixture: %v", err)
	}
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest failure fixture: %v", err)
	}
	concurrentPayload := bytes.Repeat([]byte("x"), len(snapshotPayload))
	var concurrentPath string
	advisorySetSyncBeforeSnapshotPlacementHook(t, func(cacheRoot, snapshotRel string) {
		concurrentPath = filepath.Join(cacheRoot, filepath.FromSlash(snapshotRel))
		if err := os.WriteFile(concurrentPath, concurrentPayload, 0o640); err != nil {
			t.Fatalf("publish concurrent snapshot: %v", err)
		}
	})

	_, err = SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: cachePath,
		Client:    server.Client(),
	})
	if !errors.Is(err, errAdvisorySnapshotContentMismatch) || errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected same-size snapshot digest mismatch before manifest loading, got %v", err)
	}
	got, readErr := os.ReadFile(concurrentPath)
	if readErr != nil || !bytes.Equal(got, concurrentPayload) {
		t.Fatalf("expected concurrent snapshot to survive, content=%q err=%v", got, readErr)
	}
	manifestAfter, readErr := os.ReadFile(manifestPath)
	if readErr != nil || !bytes.Equal(manifestAfter, manifestBefore) {
		t.Fatalf("expected old manifest to stay byte-identical, equal=%v err=%v", bytes.Equal(manifestAfter, manifestBefore), readErr)
	}
	advisoryAssertNoSafeIOTempFiles(t, cachePath)
}

func TestSyncOSVAcceptsMatchingNoReplaceWinner(t *testing.T) {
	snapshotPayload := []byte(`{"vulns":[]}`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(snapshotPayload); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	cachePath := t.TempDir()
	var winnerPath string
	advisorySetSyncBeforeSnapshotPlacementHook(t, func(cacheRoot, snapshotRel string) {
		winnerPath = filepath.Join(cacheRoot, filepath.FromSlash(snapshotRel))
		if err := os.WriteFile(winnerPath, snapshotPayload, 0o640); err != nil {
			t.Fatalf("publish matching snapshot winner: %v", err)
		}
	})

	snapshot, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: cachePath,
		Client:    server.Client(),
	})
	if err != nil {
		t.Fatalf("accept matching snapshot winner: %v", err)
	}
	winnerData, err := os.ReadFile(winnerPath)
	if err != nil || !bytes.Equal(winnerData, snapshotPayload) {
		t.Fatalf("expected matching winner content, content=%q err=%v", winnerData, err)
	}
	manifest, err := LoadCacheManifest(cachePath)
	if err != nil {
		t.Fatalf("load matching-winner manifest: %v", err)
	}
	if manifest.Latest != snapshot.ID || len(manifest.Snapshots) != 1 {
		t.Fatalf("expected matching winner in manifest, got %#v", manifest)
	}
	advisoryAssertNoSafeIOTempFiles(t, cachePath)
}

func TestSyncOSVSerializesRollbackBeforeConcurrentPublication(t *testing.T) {
	snapshotPayload := []byte(`{"vulns":[]}`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(snapshotPayload); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	cachePath := t.TempDir()
	firstAtManifest := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondWaiting := make(chan struct{})
	firstManifestErr := errors.New("first manifest publication failed")
	var manifestCalls atomic.Int32
	var waitCalls atomic.Int32
	syncUpdateManifestTestHook = advisoryBlockingFirstManifestHook(firstAtManifest, releaseFirst, firstManifestErr, &manifestCalls)
	syncPublicationLockWaitTestHook = advisoryFirstLockWaitHook(secondWaiting, &waitCalls)
	t.Cleanup(func() {
		syncUpdateManifestTestHook = updateManifest
		syncPublicationLockWaitTestHook = nil
	})

	firstNow := time.Date(2026, time.July, 20, 1, 0, 0, 0, time.UTC)
	secondNow := firstNow.Add(time.Hour)
	firstResult := make(chan advisorySyncResult, 1)
	secondResult := make(chan advisorySyncResult, 1)
	go func() {
		firstResult <- runAdvisorySync(server.URL, cachePath, firstNow, server.Client())
	}()
	advisoryWaitForSignal(t, firstAtManifest, "first sync did not reach manifest publication")
	go func() {
		secondResult <- runAdvisorySync(server.URL, cachePath, secondNow, server.Client())
	}()
	advisoryWaitForSignal(t, secondWaiting, "second sync did not wait for cache publication lock")
	close(releaseFirst)

	first := <-firstResult
	second := <-secondResult
	advisoryAssertSerializedSyncResults(t, cachePath, snapshotPayload, secondNow, first, second, firstManifestErr)
	if manifestCalls.Load() != 2 || waitCalls.Load() != 1 {
		t.Fatalf("expected one serialized waiter and two manifest attempts, waits=%d manifests=%d", waitCalls.Load(), manifestCalls.Load())
	}
}

func TestSyncOSVPublicationLockSurvivesPathReplacementAcrossProcesses(t *testing.T) {
	if os.Getenv(publicationLockChildModeEnv) == publicationLockChildModeSync {
		runPublicationLockSyncChild(t)
		return
	}

	const sourceURL = "https://example.test/osv.json"
	snapshotPayload := []byte(`{"vulns":[]}`)
	cachePath := t.TempDir()
	legacyLockPath := filepath.Join(cachePath, legacyPublicationLockFileName)
	legacyLockInfo := advisoryWriteLegacyLockPath(t, legacyLockPath, "original")
	markerPath := filepath.Join(t.TempDir(), "child-waiting")
	firstAtManifest := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstManifestErr := errors.New("first process manifest publication failed")
	var manifestCalls atomic.Int32
	syncUpdateManifestTestHook = advisoryBlockingFirstManifestHook(firstAtManifest, releaseFirst, firstManifestErr, &manifestCalls)
	t.Cleanup(func() {
		syncUpdateManifestTestHook = updateManifest
	})

	firstResult := make(chan advisorySyncResult, 1)
	go func() {
		firstResult <- runAdvisorySync(sourceURL, cachePath, publicationLockChildNow.Add(-time.Hour), advisoryStaticSnapshotClient(snapshotPayload))
	}()
	advisoryWaitForSignal(t, firstAtManifest, "first process did not reach manifest publication")
	advisoryReplaceLegacyLockPath(t, legacyLockPath, legacyLockInfo)

	command, output := advisoryPublicationLockChildCommand(t, publicationLockChildModeSync, cachePath, markerPath, "TestSyncOSVPublicationLockSurvivesPathReplacementAcrossProcesses")
	if err := command.Start(); err != nil {
		close(releaseFirst)
		t.Fatalf("start concurrent SyncOSV process: %v", err)
	}
	if err := advisoryWaitForFile(markerPath, 5*time.Second); err != nil {
		close(releaseFirst)
		advisoryStopChildProcess(t, command)
		t.Fatalf("wait for concurrent process lock contention: %v\n%s", err, output.String())
	}
	close(releaseFirst)

	first := <-firstResult
	if !errors.Is(first.err, firstManifestErr) {
		t.Fatalf("expected first process manifest failure, got %v", first.err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("concurrent SyncOSV process failed: %v\n%s", err, output.String())
	}
	advisoryAssertSuccessfulChildPublication(t, cachePath, snapshotPayload)
}

func TestAdvisoryPublicationLockReleasesAfterProcessExit(t *testing.T) {
	if os.Getenv(publicationLockChildModeEnv) == publicationLockChildModeExitWhileHeld {
		runPublicationLockExitChild(t)
		return
	}

	cachePath := t.TempDir()
	markerPath := filepath.Join(t.TempDir(), "child-locked")
	command, output := advisoryPublicationLockChildCommand(t, publicationLockChildModeExitWhileHeld, cachePath, markerPath, "TestAdvisoryPublicationLockReleasesAfterProcessExit")
	if err := command.Start(); err != nil {
		t.Fatalf("start lock-holder process: %v", err)
	}
	if err := advisoryWaitForFile(markerPath, 5*time.Second); err != nil {
		advisoryStopChildProcess(t, command)
		t.Fatalf("wait for lock-holder process: %v\n%s", err, output.String())
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lock-holder process failed: %v\n%s", err, output.String())
	}

	root := advisoryOpenTestRoot(t, cachePath)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lock, err := acquireAdvisoryPublicationLock(ctx, root)
	if err != nil {
		t.Fatalf("acquire publication lock after holder exit: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("release publication lock after holder exit: %v", err)
	}
	if _, err := root.Lstat(legacyPublicationLockFileName); !os.IsNotExist(err) {
		t.Fatalf("cache identity lock must not leave a replaceable lock path, got %v", err)
	}
}

func advisoryBlockingFirstManifestHook(firstAtManifest chan<- struct{}, releaseFirst <-chan struct{}, firstErr error, calls *atomic.Int32) func(safeio.Root, CacheSnapshot, time.Time) error {
	return func(root safeio.Root, snapshot CacheSnapshot, now time.Time) error {
		if calls.Add(1) == 1 {
			close(firstAtManifest)
			<-releaseFirst
			return firstErr
		}
		return updateManifest(root, snapshot, now)
	}
}

func advisoryFirstLockWaitHook(waiting chan<- struct{}, calls *atomic.Int32) func() {
	return func() {
		if calls.Add(1) == 1 {
			close(waiting)
		}
	}
}

func runAdvisorySync(sourceURL, cachePath string, now time.Time, client *http.Client) advisorySyncResult {
	snapshot, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: sourceURL,
		CachePath: cachePath,
		Now:       now,
		Client:    client,
	})
	return advisorySyncResult{snapshot: snapshot, err: err}
}

func advisoryWaitForSignal(t *testing.T, signal <-chan struct{}, failureMessage string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(failureMessage)
	}
}

func advisoryAssertSerializedSyncResults(t *testing.T, cachePath string, snapshotPayload []byte, secondNow time.Time, first, second advisorySyncResult, firstManifestErr error) {
	t.Helper()
	if !errors.Is(first.err, firstManifestErr) {
		t.Fatalf("expected first manifest failure, got %v", first.err)
	}
	if second.err != nil {
		t.Fatalf("expected second sync to publish after rollback, got %v", second.err)
	}
	snapshotData, err := os.ReadFile(filepath.Join(cachePath, filepath.FromSlash(second.snapshot.Path)))
	if err != nil || !bytes.Equal(snapshotData, snapshotPayload) {
		t.Fatalf("expected second snapshot to survive, content=%q err=%v", snapshotData, err)
	}
	manifest, err := LoadCacheManifest(cachePath)
	if err != nil {
		t.Fatalf("load second publisher manifest: %v", err)
	}
	if manifest.Latest != second.snapshot.ID || len(manifest.Snapshots) != 1 || manifest.Snapshots[0].RetrievedAt != secondNow.Format(time.RFC3339) {
		t.Fatalf("expected manifest to describe only the successful publisher, got %#v", manifest)
	}
	advisoryAssertNoSafeIOTempFiles(t, cachePath)
}

func advisoryAssertSuccessfulChildPublication(t *testing.T, cachePath string, snapshotPayload []byte) {
	t.Helper()
	manifest, err := LoadCacheManifest(cachePath)
	if err != nil {
		t.Fatalf("load child publisher manifest: %v", err)
	}
	if len(manifest.Snapshots) != 1 || manifest.Latest != manifest.Snapshots[0].ID {
		t.Fatalf("expected one successful child publication, got %#v", manifest)
	}
	snapshot := manifest.Snapshots[0]
	if snapshot.RetrievedAt != publicationLockChildNow.Format(time.RFC3339) {
		t.Fatalf("expected child publication timestamp %q, got %q", publicationLockChildNow.Format(time.RFC3339), snapshot.RetrievedAt)
	}
	snapshotData, err := os.ReadFile(filepath.Join(cachePath, filepath.FromSlash(snapshot.Path)))
	if err != nil || !bytes.Equal(snapshotData, snapshotPayload) {
		t.Fatalf("expected child snapshot to survive, content=%q err=%v", snapshotData, err)
	}
	advisoryAssertNoSafeIOTempFiles(t, cachePath)
}

func advisoryWriteLegacyLockPath(t *testing.T, path, contents string) fs.FileInfo {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write legacy publication lock path: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat legacy publication lock path: %v", err)
	}
	return info
}

func advisoryReplaceLegacyLockPath(t *testing.T, path string, original fs.FileInfo) {
	t.Helper()
	replacementPath, replacementInfo := advisoryCreateDistinctLegacyLockReplacement(t, path, original)
	if err := os.Remove(path); err != nil {
		t.Fatalf("unlink legacy publication lock path: %v", err)
	}
	if err := os.Rename(replacementPath, path); err != nil {
		t.Fatalf("rename distinct legacy publication lock replacement: %v", err)
	}
	renamedInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat renamed legacy publication lock replacement: %v", err)
	}
	if !os.SameFile(replacementInfo, renamedInfo) {
		t.Fatal("legacy publication lock replacement changed identity during rename")
	}
}

func advisoryCreateDistinctLegacyLockReplacement(t *testing.T, path string, original fs.FileInfo) (string, fs.FileInfo) {
	t.Helper()

	dir := filepath.Dir(path)
	pattern := filepath.Base(path) + ".replacement-*"
	for attempt := 0; attempt < 8; attempt++ {
		file, err := os.CreateTemp(dir, pattern)
		if err != nil {
			t.Fatalf("create distinct legacy publication lock replacement: %v", err)
		}
		replacementPath := file.Name()
		if _, err := file.WriteString("replacement"); err != nil {
			if closeErr := file.Close(); closeErr != nil {
				t.Logf("close failed replacement file after write error: %v", closeErr)
			}
			if removeErr := os.Remove(replacementPath); removeErr != nil {
				t.Logf("remove failed replacement file after write error: %v", removeErr)
			}
			t.Fatalf("write distinct legacy publication lock replacement: %v", err)
		}
		if err := file.Close(); err != nil {
			if removeErr := os.Remove(replacementPath); removeErr != nil {
				t.Logf("remove failed replacement file after close error: %v", removeErr)
			}
			t.Fatalf("close distinct legacy publication lock replacement: %v", err)
		}
		replacementInfo, err := os.Lstat(replacementPath)
		if err != nil {
			if removeErr := os.Remove(replacementPath); removeErr != nil {
				t.Logf("remove failed replacement file after lstat error: %v", removeErr)
			}
			t.Fatalf("lstat distinct legacy publication lock replacement: %v", err)
		}
		if os.SameFile(original, replacementInfo) {
			if removeErr := os.Remove(replacementPath); removeErr != nil {
				t.Logf("remove failed reused-identity replacement file: %v", removeErr)
			}
			continue
		}
		return replacementPath, replacementInfo
	}
	t.Fatal("failed to create a distinct legacy publication lock replacement")
	return "", nil
}

func advisoryPublicationLockChildCommand(t *testing.T, mode, cachePath, markerPath, testName string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^"+testName+"$")
	command.Env = append(os.Environ(), publicationLockChildModeEnv+"="+mode, publicationLockChildCacheEnv+"="+cachePath, publicationLockChildMarkerEnv+"="+markerPath)
	output := &bytes.Buffer{}
	command.Stdout = output
	command.Stderr = output
	return command, output
}

func advisoryStopChildProcess(t *testing.T, command *exec.Cmd) {
	t.Helper()
	if err := command.Process.Kill(); err != nil {
		t.Logf("kill child process after test failure: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Logf("wait for killed child process: %v", err)
	}
}

func advisoryWaitForFile(path string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for %s", path)
		case <-ticker.C:
			if _, err := os.Stat(path); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
}

func runPublicationLockSyncChild(t *testing.T) {
	cachePath := os.Getenv(publicationLockChildCacheEnv)
	markerPath := os.Getenv(publicationLockChildMarkerEnv)
	var markerErr error
	syncPublicationLockWaitTestHook = func() {
		markerErr = os.WriteFile(markerPath, []byte("waiting"), 0o600)
	}
	t.Cleanup(func() {
		syncPublicationLockWaitTestHook = nil
	})
	result := runAdvisorySync("https://example.test/osv.json", cachePath, publicationLockChildNow, advisoryStaticSnapshotClient([]byte(`{"vulns":[]}`)))
	if markerErr != nil {
		t.Fatalf("write publication lock contention marker: %v", markerErr)
	}
	if result.err != nil {
		t.Fatalf("publish advisory snapshot from child: %v", result.err)
	}
}

func runPublicationLockExitChild(t *testing.T) {
	cachePath := os.Getenv(publicationLockChildCacheEnv)
	markerPath := os.Getenv(publicationLockChildMarkerEnv)
	root, err := openAdvisoryCacheRoot(cachePath)
	if err != nil {
		t.Fatalf("open child publication root: %v", err)
	}
	if _, err := acquireAdvisoryPublicationLock(context.Background(), root); err != nil {
		t.Fatalf("acquire child publication lock: %v", err)
	}
	if err := os.WriteFile(markerPath, []byte("locked"), 0o600); err != nil {
		t.Fatalf("write child publication marker: %v", err)
	}
	os.Exit(0)
}

func advisoryStaticSnapshotClient(payload []byte) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(payload)),
		}, nil
	})}
}

func TestSyncOSVRollbackPreservesSnapshotWhenOwnershipIdentityChanges(t *testing.T) {
	server := advisoryEmptyOSVTLSServer(t)
	defer server.Close()

	cachePath := t.TempDir()
	if err := writeOversizedValidManifest(filepath.Join(cachePath, manifestFileName), maxCacheManifestBytes+1); err != nil {
		t.Fatalf("write manifest failure fixture: %v", err)
	}
	concurrentPayload := []byte("replacement publisher")
	var snapshotPath string
	advisorySetSyncAfterSnapshotPlacementHook(t, func(cacheRoot, snapshotRel string) {
		snapshotPath = filepath.Join(cacheRoot, filepath.FromSlash(snapshotRel))
		if err := os.Remove(snapshotPath); err != nil {
			t.Fatalf("remove originally published snapshot: %v", err)
		}
		if err := os.WriteFile(snapshotPath, concurrentPayload, 0o640); err != nil {
			t.Fatalf("publish replacement snapshot: %v", err)
		}
	})

	_, err := SyncOSV(context.Background(), SyncOptions{
		SourceURL: server.URL,
		CachePath: cachePath,
		Client:    server.Client(),
	})
	if !errors.Is(err, safeio.ErrFileTooLarge) || !errors.Is(err, errAdvisorySnapshotOwnershipLost) {
		t.Fatalf("expected manifest and ownership-loss identities, got %v", err)
	}
	got, readErr := os.ReadFile(snapshotPath)
	if readErr != nil || !bytes.Equal(got, concurrentPayload) {
		t.Fatalf("expected replacement snapshot to survive rollback, content=%q err=%v", got, readErr)
	}
	advisoryAssertNoSafeIOTempFiles(t, cachePath)
}

func TestSyncOSVCleansDownloadedTempWhenPlacementSetupFails(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"vulns":[]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	cachePath := filepath.Join(t.TempDir(), "cache")
	syncAfterDownloadTestHook = func(cacheRoot, tempRel string) {
		if _, err := os.Stat(filepath.Join(cacheRoot, tempRel)); err != nil {
			t.Fatalf("stat downloaded temp before setup failure: %v", err)
		}
		if err := os.RemoveAll(filepath.Join(cacheRoot, "snapshots")); err != nil {
			t.Fatalf("remove snapshots dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(cacheRoot, "snapshots"), []byte("not-a-dir"), 0o600); err != nil {
			t.Fatalf("replace snapshots dir with file: %v", err)
		}
	}
	t.Cleanup(func() {
		syncAfterDownloadTestHook = nil
	})

	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: cachePath, Client: server.Client()}); err == nil || !strings.Contains(err.Error(), "write advisory snapshot") {
		t.Fatalf("expected placement setup error, got %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(cachePath, ".safeio-atomic-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected downloaded temp to be cleaned up, got %v", matches)
	}
}

func TestSyncOSVManifestStaysUnderAcquiredRootAfterCacheRootSwap(t *testing.T) {
	server := advisoryEmptyOSVTLSServer(t)
	defer server.Close()

	paths := advisoryNewSwapPaths(t, false)
	advisorySetSyncAfterDownloadHook(t, func(cacheRoot, tempRel string) {
		advisorySwapCacheRootForSymlink(t, cacheRoot, paths.renamedCachePath, paths.outsideDir, tempRel)
	})

	snapshot, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: paths.cachePath, Client: server.Client()})
	if err != nil {
		t.Fatalf("sync OSV after cache root swap: %v", err)
	}

	renamedManifestPath := filepath.Join(paths.renamedCachePath, manifestFileName)
	manifestData, err := os.ReadFile(renamedManifestPath)
	if err != nil {
		t.Fatalf("read manifest from renamed cache: %v", err)
	}
	if !strings.Contains(string(manifestData), `"`+snapshot.ID+`"`) {
		t.Fatalf("expected renamed cache manifest to include snapshot %q, got %s", snapshot.ID, manifestData)
	}
	if _, err := os.Stat(filepath.Join(paths.renamedCachePath, filepath.FromSlash(snapshot.Path))); err != nil {
		t.Fatalf("stat snapshot in renamed cache: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.outsideDir, manifestFileName)); !os.IsNotExist(err) {
		t.Fatalf("expected outside manifest to stay absent, got err=%v", err)
	}
	advisoryAssertDirEmpty(t, paths.outsideDir)
}

func TestSyncOSVMergesManifestFromAcquiredRootAfterCacheRootSwap(t *testing.T) {
	server := advisoryEmptyOSVTLSServer(t)
	defer server.Close()

	paths := advisoryNewSwapPaths(t, true)
	originalManifest := []byte(`{
  "schemaVersion": "lopper.advisory-cache.v1",
  "updatedAt": "2026-07-12T00:00:00Z",
  "latest": "old",
  "snapshots": [
    {"id": "old", "path": "snapshots/old.json"}
  ]
}`)
	if err := os.WriteFile(filepath.Join(paths.cachePath, manifestFileName), originalManifest, 0o600); err != nil {
		t.Fatalf("write original manifest: %v", err)
	}
	outsideManifest := []byte(`{
  "schemaVersion": "lopper.advisory-cache.v1",
  "updatedAt": "2026-07-12T00:00:00Z",
  "latest": "poison",
  "snapshots": [
    {"id": "poison", "path": "snapshots/poison.json"}
  ]
}`)
	if err := os.WriteFile(filepath.Join(paths.outsideDir, manifestFileName), outsideManifest, 0o600); err != nil {
		t.Fatalf("write outside manifest: %v", err)
	}

	advisorySetSyncAfterDownloadHook(t, func(cacheRoot, tempRel string) {
		advisorySwapCacheRootForSymlink(t, cacheRoot, paths.renamedCachePath, paths.outsideDir, tempRel)
	})

	snapshot, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: paths.cachePath, Client: server.Client()})
	if err != nil {
		t.Fatalf("sync OSV with existing manifest after cache root swap: %v", err)
	}

	manifestData, err := os.ReadFile(filepath.Join(paths.renamedCachePath, manifestFileName))
	if err != nil {
		t.Fatalf("read merged manifest from renamed cache: %v", err)
	}
	if strings.Contains(string(manifestData), `"poison"`) {
		t.Fatalf("expected merged manifest to ignore outside manifest, got %s", manifestData)
	}
	if !strings.Contains(string(manifestData), `"old"`) || !strings.Contains(string(manifestData), `"`+snapshot.ID+`"`) {
		t.Fatalf("expected merged manifest to include original and new snapshots, got %s", manifestData)
	}
	unchangedOutsideManifest, err := os.ReadFile(filepath.Join(paths.outsideDir, manifestFileName))
	if err != nil {
		t.Fatalf("read outside manifest: %v", err)
	}
	if string(unchangedOutsideManifest) != string(outsideManifest) {
		t.Fatalf("expected outside manifest to stay unchanged, got %s", unchangedOutsideManifest)
	}
}

func TestUpdateManifestAndSnapshotMetadataEdges(t *testing.T) {
	cachePath := t.TempDir()
	manifestPayload := []byte(`{
  "schemaVersion": "lopper.advisory-cache.v1",
  "latest": "same",
  "snapshots": [
    {"id": "", "path": "ignored"},
    {"id": "same", "path": "old-same"},
    {"id": "old", "path": "snapshots/old.json"}
  ]
}`)
	if err := os.WriteFile(filepath.Join(cachePath, manifestFileName), manifestPayload, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	snapshot := CacheSnapshot{ID: "same", Path: "snapshots/same.json"}
	root := advisoryOpenTestRoot(t, cachePath)
	if err := updateManifest(root, snapshot, time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("update manifest: %v", err)
	}
	manifest, err := LoadCacheManifest(cachePath)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if len(manifest.Snapshots) != 2 || manifest.Snapshots[0].ID != "old" || manifest.Snapshots[1].Path != "snapshots/same.json" {
		t.Fatalf("expected old snapshot plus replacement, got %#v", manifest.Snapshots)
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		t.Fatalf("expected advisory cache schema %q, got %q", manifestSchemaVersion, manifest.SchemaVersion)
	}
	if inferSnapshotSchema([]byte("   ")) != "unknown" || inferSnapshotSchema([]byte("not-json")) != "unknown" {
		t.Fatalf("expected blank and opaque snapshots to have unknown schema")
	}
	if count := snapshotEntryCount([]byte(`{"vulns":[{},{}]}`)); count != 2 {
		t.Fatalf("expected wrapped OSV entry count, got %d", count)
	}
}

func TestValidateOSVJSONSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		wantErr bool
	}{
		{name: "empty array", payload: `[]`},
		{name: "advisory array", payload: `[` + testOSVAdvisory("OSV-1") + `]`},
		{name: "advisory with versions", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"versions":[" ","1.0.0"]}]}]`},
		{name: "advisory with skipped affected entry", payload: `[{"id":"OSV-1","affected":[{}, {"package":{"name":"example.com/lib"},"ranges":[{}]}]}]`},
		{name: "wrapped advisories", payload: `{"metadata":{"page":1},"vulns":[` + testOSVAdvisory("OSV-1") + `],"nextPageToken":null}`},
		{name: "empty wrapper", payload: `{"vulns":[]}`},
		{name: "single advisory", payload: testOSVAdvisory("GO-2021-0113")},
		{name: "empty document", payload: ``, wantErr: true},
		{name: "scalar", payload: `"quota exceeded"`, wantErr: true},
		{name: "missing vulns", payload: `{"error":"quota exceeded"}`, wantErr: true},
		{name: "non-array vulns", payload: `{"vulns":{}}`, wantErr: true},
		{name: "non-object entry", payload: `[null]`, wantErr: true},
		{name: "unrelated object entry", payload: `[{"userId":1,"id":1,"title":"not an advisory"}]`, wantErr: true},
		{name: "missing id", payload: `[{"affected":[{"package":{"name":"example.com/lib"},"versions":["1.0.0"]}]}]`, wantErr: true},
		{name: "blank id", payload: `[{"id":" ","affected":[{"package":{"name":"example.com/lib"},"versions":["1.0.0"]}]}]`, wantErr: true},
		{name: "non-string id", payload: `[{"id":1,"affected":[{"package":{"name":"example.com/lib"},"versions":["1.0.0"]}]}]`, wantErr: true},
		{name: "duplicate id", payload: `[{"id":"OSV-1","id":"OSV-2","affected":[{"package":{"name":"example.com/lib"},"versions":["1.0.0"]}]}]`, wantErr: true},
		{name: "missing affected", payload: `[{"id":"OSV-1"}]`, wantErr: true},
		{name: "non-array affected", payload: `[{"id":"OSV-1","affected":{}}]`, wantErr: true},
		{name: "non-object affected", payload: `[{"id":"OSV-1","affected":[null]}]`, wantErr: true},
		{name: "duplicate affected", payload: `[{"id":"OSV-1","affected":[],"affected":[]}]`, wantErr: true},
		{name: "truncated advisory field name", payload: `[{"`, wantErr: true},
		{name: "invalid advisory closing delimiter", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"versions":["1.0.0"]}]]`, wantErr: true},
		{name: "no usable affected package", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"}}]}]`, wantErr: true},
		{name: "truncated affected field name", payload: `[{"id":"OSV-1","affected":[{"`, wantErr: true},
		{name: "invalid affected closing delimiter", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"versions":["1.0.0"]]]}]`, wantErr: true},
		{name: "truncated affected metadata", payload: `[{"id":"OSV-1","affected":[{"database_specific":{`, wantErr: true},
		{name: "non-object package", payload: `[{"id":"OSV-1","affected":[{"package":"example.com/lib","versions":["1.0.0"]}]}]`, wantErr: true},
		{name: "duplicate package", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"package":{"name":"example.com/fork"},"versions":["1.0.0"]}]}]`, wantErr: true},
		{name: "truncated package field name", payload: `[{"id":"OSV-1","affected":[{"package":{"`, wantErr: true},
		{name: "truncated package metadata", payload: `[{"id":"OSV-1","affected":[{"package":{"ecosystem":{`, wantErr: true},
		{name: "blank package name", payload: `[{"id":"OSV-1","affected":[{"package":{"name":" "},"versions":["1.0.0"]}]}]`, wantErr: true},
		{name: "duplicate package name", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib","name":"example.com/fork"},"versions":["1.0.0"]}]}]`, wantErr: true},
		{name: "non-array versions", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"versions":"1.0.0"}]}]`, wantErr: true},
		{name: "non-string version", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"versions":[1]}]}]`, wantErr: true},
		{name: "truncated version", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"versions":[tru`, wantErr: true},
		{name: "duplicate versions", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"versions":[],"versions":["1.0.0"]}]}]`, wantErr: true},
		{name: "non-array ranges", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"ranges":{}}]}]`, wantErr: true},
		{name: "non-object range", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"ranges":[null]}]}]`, wantErr: true},
		{name: "truncated range", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"ranges":[{"events":[`, wantErr: true},
		{name: "duplicate ranges", payload: `[{"id":"OSV-1","affected":[{"package":{"name":"example.com/lib"},"ranges":[],"ranges":[{}]}]}]`, wantErr: true},
		{name: "duplicate vulns", payload: `{"vulns":[],"vulns":[]}`, wantErr: true},
		{name: "trailing document", payload: `{"vulns":[]} {}`, wantErr: true},
		{name: "invalid trailing data", payload: `[] trailing`, wantErr: true},
		{name: "truncated wrapper", payload: `{"vulns":[]`, wantErr: true},
		{name: "truncated wrapper field name", payload: `{"vulns":[],"`, wantErr: true},
		{name: "truncated wrapper field value", payload: `{"metadata":`, wantErr: true},
		{name: "truncated advisory entry", payload: `[{"id":`, wantErr: true},
		{name: "truncated document", payload: `{"vulns":[{}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateOSVJSONSnapshot(strings.NewReader(tc.payload))
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate OSV JSON snapshot: err=%v wantErr=%t", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDownloadedOSVJSONErrors(t *testing.T) {
	openErr := errors.New("open failure")
	for _, tc := range []struct {
		name         string
		openSnapshot snapshotOpener
		wantError    string
	}{
		{
			name: "open failure",
			openSnapshot: func() (io.ReadCloser, error) {
				return nil, openErr
			},
			wantError: openErr.Error(),
		},
		{
			name: "nil file",
			openSnapshot: func() (io.ReadCloser, error) {
				return nil, nil
			},
			wantError: "nil file",
		},
		{
			name: "close failure",
			openSnapshot: func() (io.ReadCloser, error) {
				return &errCloseReadCloser{Reader: strings.NewReader(`[]`)}, nil
			},
			wantError: "close snapshot after validation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDownloadedOSVJSON(tc.openSnapshot)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q validation error, got %v", tc.wantError, err)
			}
		})
	}
}

func TestValidateDownloadedOSVZipErrors(t *testing.T) {
	payload := testOSVZip(t, "GO-2021-0113.json", testOSVAdvisory("GO-2021-0113"))
	openErr := errors.New("open failure")
	for _, tc := range []struct {
		name         string
		openSnapshot snapshotOpener
		wantError    string
	}{
		{
			name: "open failure",
			openSnapshot: func() (io.ReadCloser, error) {
				return nil, openErr
			},
			wantError: openErr.Error(),
		},
		{
			name: "nil file",
			openSnapshot: func() (io.ReadCloser, error) {
				return nil, nil
			},
			wantError: "nil file",
		},
		{
			name: "random access unavailable",
			openSnapshot: func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(payload)), nil
			},
			wantError: "random access unavailable",
		},
		{
			name: "close failure",
			openSnapshot: func() (io.ReadCloser, error) {
				return &errCloseReaderAt{Reader: bytes.NewReader(payload)}, nil
			},
			wantError: "close snapshot after validation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDownloadedOSVZip(tc.openSnapshot, int64(len(payload)))
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("expected %q validation error, got %v", tc.wantError, err)
			}
		})
	}
}

func TestValidateOSVZipSnapshotRejectsUnusableArchives(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{name: "malformed", payload: []byte("PK\x03\x04zip")},
		{name: "no JSON entries", payload: testOSVZip(t, "README.txt", "not an advisory")},
		{name: "directory only", payload: testOSVZip(t, "nested/", "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateOSVZipSnapshot(bytes.NewReader(tc.payload), int64(len(tc.payload))); err == nil {
				t.Fatal("expected unusable ZIP archive to be rejected")
			}
		})
	}
}

func TestValidateOSVZipSnapshotValidatesEveryEntry(t *testing.T) {
	t.Run("multiple valid entries", func(t *testing.T) {
		payload := testOSVZipEntries(t, testOSVZipEntry{name: "GO-1.json", payload: testOSVAdvisory("GO-1"), method: zip.Deflate}, testOSVZipEntry{name: "README.txt", payload: "OSV snapshot", method: zip.Store}, testOSVZipEntry{name: "GO-2.json", payload: testOSVAdvisory("GO-2"), method: zip.Deflate})
		if err := validateOSVZipSnapshot(bytes.NewReader(payload), int64(len(payload))); err != nil {
			t.Fatalf("validate complete OSV ZIP snapshot: %v", err)
		}
	})

	t.Run("invalid later JSON", func(t *testing.T) {
		payload := testOSVZipEntries(t, testOSVZipEntry{name: "GO-1.json", payload: testOSVAdvisory("GO-1"), method: zip.Deflate}, testOSVZipEntry{name: "response.json", payload: `{"error":"quota exceeded"}`, method: zip.Deflate})
		if err := validateOSVZipSnapshot(bytes.NewReader(payload), int64(len(payload))); err == nil {
			t.Fatal("expected invalid later JSON entry to be rejected")
		}
	})

	t.Run("corrupt later entry", func(t *testing.T) {
		laterPayload := "later entry must be checksum verified"
		payload := testOSVZipEntries(t, testOSVZipEntry{name: "GO-1.json", payload: testOSVAdvisory("GO-1"), method: zip.Deflate}, testOSVZipEntry{name: "metadata.txt", payload: laterPayload, method: zip.Store})
		payloadOffset := bytes.Index(payload, []byte(laterPayload))
		if payloadOffset < 0 {
			t.Fatal("locate stored ZIP entry payload")
		}
		payload[payloadOffset] ^= 0xff

		if err := validateOSVZipSnapshot(bytes.NewReader(payload), int64(len(payload))); !errors.Is(err, zip.ErrChecksum) {
			t.Fatalf("expected later ZIP checksum error, got %v", err)
		}
	})

	t.Run("excessive expansion", func(t *testing.T) {
		largeAdvisory := strings.Replace(testOSVAdvisory("GO-1"), `"affected":`, `"details":"`+strings.Repeat("a", 2*1024*1024)+`","affected":`, 1)
		payload := testOSVZip(t, "GO-1.json", largeAdvisory)
		if err := validateOSVZipSnapshot(bytes.NewReader(payload), int64(len(payload))); err == nil {
			t.Fatal("expected excessive ZIP expansion to be rejected")
		}
	})
}

func TestValidateOSVZipBoundsRejectsAbsoluteExpandedSize(t *testing.T) {
	entries := []*zip.File{{FileHeader: zip.FileHeader{UncompressedSize64: maxOSVZipExpandedSize + 1}}}
	if err := validateOSVZipBounds(entries, int64(maxOSVZipExpandedSize)); err == nil || !strings.Contains(err.Error(), "expanded size") {
		t.Fatalf("expected absolute expanded-size error, got %v", err)
	}
}

func TestLoadCacheManifestMissingFile(t *testing.T) {
	cachePath := t.TempDir()

	_, err := LoadCacheManifest(cachePath)
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected missing manifest error, got %v", err)
	}
}

func TestLoadCacheManifestMissingFileUsesManifestPath(t *testing.T) {
	cachePath := t.TempDir()

	_, err := LoadCacheManifest(cachePath)
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("expected missing manifest path error, got %#v", err)
	}
	if pathErr.Op != "open" || pathErr.Path != filepath.Join(cachePath, manifestFileName) {
		t.Fatalf("unexpected missing manifest path error: %#v", pathErr)
	}
}

func TestLoadCacheManifestRejectsOversizedManifest(t *testing.T) {
	cachePath := t.TempDir()
	if err := writeOversizedValidManifest(filepath.Join(cachePath, manifestFileName), maxCacheManifestBytes+1); err != nil {
		t.Fatalf("write oversized manifest: %v", err)
	}

	_, err := LoadCacheManifest(cachePath)
	if !errors.Is(err, safeio.ErrFileTooLarge) {
		t.Fatalf("expected oversized manifest read to fail with ErrFileTooLarge, got %v", err)
	}
	if !strings.Contains(err.Error(), "read advisory cache manifest") {
		t.Fatalf("expected advisory manifest read context, got %v", err)
	}
}

func TestLoadCacheManifestJoinsRootCloseError(t *testing.T) {
	closeErr := errors.New("close advisory cache root")
	manifestPayload := []byte(`{"schemaVersion":"` + manifestSchemaVersion + `","latest":"snapshot-1"}`)
	infoPath := filepath.Join(t.TempDir(), manifestFileName)
	if err := os.WriteFile(infoPath, manifestPayload, 0o600); err != nil {
		t.Fatalf("write manifest fixture: %v", err)
	}
	info, err := os.Stat(infoPath)
	if err != nil {
		t.Fatalf("stat manifest fixture: %v", err)
	}

	withOpenAdvisoryCacheRootHook(t, func(string) (safeio.Root, error) {
		return &advisoryFakeRoot{
			lstat: func(string) (fs.FileInfo, error) { return info, nil },
			open: func(string) (safeio.File, error) {
				return &advisoryStaticFile{info: info, payload: manifestPayload}, nil
			},
			close: func() error { return closeErr },
		}, nil
	})

	manifest, err := LoadCacheManifest(filepath.Join(t.TempDir(), "cache"))
	if !errors.Is(err, closeErr) {
		t.Fatalf("expected root close error, got %v", err)
	}
	if manifest.SchemaVersion != manifestSchemaVersion || manifest.Latest != "snapshot-1" {
		t.Fatalf("expected parsed manifest alongside close error, got %#v", manifest)
	}
}

func TestAdvisoryCachePublicCallersRejectSymlinkedCacheRoots(t *testing.T) {
	server := advisoryEmptyOSVTLSServer(t)
	defer server.Close()

	realCache := filepath.Join(t.TempDir(), "real-cache")
	if err := os.MkdirAll(realCache, 0o750); err != nil {
		t.Fatalf("create real cache root: %v", err)
	}
	cacheLink := filepath.Join(t.TempDir(), "cache-link")
	if err := os.Symlink(realCache, cacheLink); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{
			name: "SyncOSV",
			call: func() error {
				_, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: cacheLink, Client: server.Client()})
				return err
			},
		},
		{
			name: "LoadCacheManifest",
			call: func() error {
				_, err := LoadCacheManifest(cacheLink)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
				t.Fatalf("expected symlinked cache root rejection, got %v", err)
			}
		})
	}
}

func TestPrepareAdvisoryCacheRootRejectsSymlinkAncestorWithoutCreatingChildren(t *testing.T) {
	parentDir := t.TempDir()
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o750); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	linkPath := filepath.Join(parentDir, "cache-link")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	cachePath := filepath.Join(linkPath, "nested", "cache")
	root, err := prepareAdvisoryCacheRoot(cachePath)
	if root != nil {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close unexpected advisory cache root: %v", closeErr)
		}
		t.Fatal("expected symlinked ancestor cache root acquisition to fail")
	}
	if err == nil || !strings.Contains(err.Error(), "root contains symlink") {
		t.Fatalf("expected symlinked ancestor rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outsideDir, "nested")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no outside-root child creation, stat err=%v", statErr)
	}
}

func TestPrepareAdvisoryCacheRootJoinsNestedCreateCloseFailures(t *testing.T) {
	rootCloseErr := errors.New("close root")
	currentCloseErr := errors.New("close current")
	createErr := errors.New("create child")
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "nested" {
				t.Fatalf("unexpected first lstat %q", name)
			}
			return dirInfo, nil
		},
		close: func() error { return rootCloseErr },
	}
	current := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "." {
				return dirInfo, nil
			}
			if name != "leaf" {
				t.Fatalf("unexpected nested lstat %q", name)
			}
			return nil, createErr
		},
		close: func() error { return currentCloseErr },
	}
	root.openRoot = func(name string) (safeio.Root, error) {
		if name != "nested" {
			t.Fatalf("unexpected first child %q", name)
		}
		return current, nil
	}
	realHook := openAdvisoryCacheAncestor
	openAdvisoryCacheAncestor = func(string) (safeio.Root, string, []string, error) {
		return root, "/cache", []string{"nested", "leaf"}, nil
	}
	t.Cleanup(func() {
		openAdvisoryCacheAncestor = realHook
	})

	opened, err := prepareAdvisoryCacheRoot("/cache/nested/leaf")
	if opened != nil {
		t.Fatal("expected nested create failure to return no root")
	}
	if !errors.Is(err, createErr) || !errors.Is(err, currentCloseErr) || !errors.Is(err, rootCloseErr) {
		t.Fatalf("expected create failure joined with current and root close errors, got %v", err)
	}
}

func TestPrepareAdvisoryCacheRootJoinsNestedOpenCloseFailures(t *testing.T) {
	rootCloseErr := errors.New("close root")
	currentCloseErr := errors.New("close current")
	nextCloseErr := errors.New("close next")
	rootDirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	current := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "." {
				return rootDirInfo, nil
			}
			if name != "leaf" {
				t.Fatalf("unexpected nested lstat %q", name)
			}
			return rootDirInfo, nil
		},
		openRoot: func(string) (safeio.Root, error) {
			return &advisoryFakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return rootDirInfo, nil },
				close: func() error { return nextCloseErr },
			}, nil
		},
		close: func() error { return currentCloseErr },
	}
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "nested" {
				t.Fatalf("unexpected first lstat %q", name)
			}
			return rootDirInfo, nil
		},
		close: func() error { return rootCloseErr },
		openRoot: func(name string) (safeio.Root, error) {
			if name != "nested" {
				t.Fatalf("unexpected first child %q", name)
			}
			return current, nil
		},
	}
	realHook := openAdvisoryCacheAncestor
	openAdvisoryCacheAncestor = func(string) (safeio.Root, string, []string, error) {
		return root, "/cache", []string{"nested", "leaf"}, nil
	}
	t.Cleanup(func() {
		openAdvisoryCacheAncestor = realHook
	})

	opened, err := prepareAdvisoryCacheRoot("/cache/nested/leaf")
	if opened != nil {
		t.Fatal("expected nested open failure to return no root")
	}
	if !errors.Is(err, currentCloseErr) || !errors.Is(err, nextCloseErr) || !errors.Is(err, rootCloseErr) {
		t.Fatalf("expected current close failure joined with next and root close errors, got %v", err)
	}
}

func TestAdvisoryOpenOrCreatePinnedChildJoinsTypeMismatchCloseFailure(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	filePath := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(filePath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("stat temp file: %v", err)
	}
	closeErr := errors.New("close mismatched child")
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "child" {
				t.Fatalf("unexpected lstat name %q", name)
			}
			return dirInfo, nil
		},
		openRoot: func(name string) (safeio.Root, error) {
			if name != "child" {
				t.Fatalf("unexpected openRoot name %q", name)
			}
			return &advisoryFakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return fileInfo, nil },
				close: func() error { return closeErr },
			}, nil
		},
	}

	child, err := advisoryOpenOrCreatePinnedChild(root, "/cache", "child")
	if child != nil {
		t.Fatal("expected type mismatch to return no child root")
	}
	if !strings.Contains(err.Error(), "root changed while opening") || !errors.Is(err, closeErr) {
		t.Fatalf("expected type mismatch error joined with child close failure, got %v", err)
	}
}

func TestPrepareAdvisoryCacheRootReturnsAncestorWhenPathAlreadyExists(t *testing.T) {
	root := &advisoryFakeRoot{}
	realHook := openAdvisoryCacheAncestor
	openAdvisoryCacheAncestor = func(string) (safeio.Root, string, []string, error) {
		return root, "/cache", nil, nil
	}
	t.Cleanup(func() {
		openAdvisoryCacheAncestor = realHook
	})

	opened, err := prepareAdvisoryCacheRoot("/cache")
	if err != nil {
		t.Fatalf("prepare existing advisory cache root: %v", err)
	}
	if opened != root {
		t.Fatalf("expected existing ancestor root to be returned, got %#v", opened)
	}
}

func TestAdvisoryOpenOrCreatePinnedChildCreatesMissingDirectory(t *testing.T) {
	fixture := newAdvisoryPinnedChildCreationFixture(t)
	opened, err := advisoryOpenOrCreatePinnedChild(fixture.root, "/cache", "child")
	if err != nil {
		t.Fatalf("open or create missing advisory child: %v", err)
	}
	if opened != fixture.child {
		t.Fatalf("expected child root to be returned, got %#v", opened)
	}
	if fixture.mkdirCalls != 1 || fixture.lstatCalls != 2 {
		t.Fatalf("expected one mkdir and two lstat calls, got mkdir=%d lstat=%d", fixture.mkdirCalls, fixture.lstatCalls)
	}
}

type advisoryPinnedChildCreationFixture struct {
	root       *advisoryFakeRoot
	child      *advisoryFakeRoot
	mkdirCalls int
	lstatCalls int
	directory  fs.FileInfo
}

func newAdvisoryPinnedChildCreationFixture(t *testing.T) *advisoryPinnedChildCreationFixture {
	t.Helper()
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	fixture := &advisoryPinnedChildCreationFixture{directory: dirInfo}
	fixture.child = &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "." {
				t.Fatalf("unexpected child lstat %q", name)
			}
			return fixture.directory, nil
		},
	}
	fixture.root = &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "child" {
				t.Fatalf("unexpected root lstat %q", name)
			}
			fixture.lstatCalls++
			if fixture.lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return fixture.directory, nil
		},
		mkdir: func(name string, perm os.FileMode) error {
			if name != "child" || perm != 0o750 {
				t.Fatalf("unexpected mkdir call %q perm %#o", name, perm)
			}
			fixture.mkdirCalls++
			return os.ErrExist
		},
		openRoot: func(name string) (safeio.Root, error) {
			if name != "child" {
				t.Fatalf("unexpected openRoot %q", name)
			}
			return fixture.child, nil
		},
	}
	return fixture
}

func TestAdvisoryJoinCloseErrorSkipsNilClosers(t *testing.T) {
	primary := errors.New("primary advisory error")
	closeErr := errors.New("close advisory root")
	err := advisoryJoinCloseError(primary, nil, io.NopCloser(strings.NewReader("")), closerFunc(func() error { return closeErr }))
	if !errors.Is(err, primary) || !errors.Is(err, closeErr) {
		t.Fatalf("expected advisory join to preserve primary and close errors, got %v", err)
	}
}

func TestSnapshotIDFromDigestReturnsFullShortDigest(t *testing.T) {
	const digest = "sha256:deadbeef"
	if got := snapshotIDFromDigest(digest); got != "deadbeef" {
		t.Fatalf("expected short digest to remain intact, got %q", got)
	}
}

func TestAdvisorySnapshotAbsentRejectsSymlinkAndLookupFailure(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "target.txt")
	if err := os.WriteFile(targetPath, []byte("target"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	linkPath := filepath.Join(t.TempDir(), "snapshot-link")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat snapshot symlink: %v", err)
	}

	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return linkInfo, nil },
	}
	absent, err := advisorySnapshotAbsent(root, "snapshot.json")
	if absent || err == nil || !strings.Contains(err.Error(), "snapshot path is a symlink") {
		t.Fatalf("expected symlinked snapshot rejection, absent=%v err=%v", absent, err)
	}

	lookupErr := errors.New("lookup snapshot")
	root = &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, lookupErr },
	}
	absent, err = advisorySnapshotAbsent(root, "snapshot.json")
	if absent || !errors.Is(err, lookupErr) {
		t.Fatalf("expected snapshot lookup error, absent=%v err=%v", absent, err)
	}
}

func TestAdvisoryPlaceSnapshotNoReplaceErrorAndIdentityBranches(t *testing.T) {
	tempPath := filepath.Join(t.TempDir(), "temp.json")
	if err := os.WriteFile(tempPath, []byte("temp"), 0o600); err != nil {
		t.Fatalf("write snapshot temp fixture: %v", err)
	}
	tempInfo, err := os.Stat(tempPath)
	if err != nil {
		t.Fatalf("stat snapshot temp fixture: %v", err)
	}
	otherPath := filepath.Join(t.TempDir(), "other.json")
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatalf("write other snapshot fixture: %v", err)
	}
	otherInfo, err := os.Stat(otherPath)
	if err != nil {
		t.Fatalf("stat other snapshot fixture: %v", err)
	}
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat directory fixture: %v", err)
	}
	tempDigestSum := sha256.Sum256([]byte("temp"))
	fixture := &advisoryPlaceSnapshotFixture{
		tempInfo:  tempInfo,
		otherInfo: otherInfo,
		dirInfo:   dirInfo,
		digest:    sha256Prefix + hex.EncodeToString(tempDigestSum[:]),
		size:      int64(len("temp")),
	}
	t.Run("temp is not regular", fixture.testTempNotRegular)
	t.Run("chmod failure", fixture.testChmodFailure)
	t.Run("link failure", fixture.testLinkFailure)
	t.Run("existing target disappears", fixture.testExistingTargetDisappears)
	t.Run("post-link lookup failure rolls back owned target", fixture.testPostLinkLookupFailure)
	t.Run("post-link identity mismatch preserves unknown target", fixture.testPostLinkIdentityMismatch)
}

type advisoryPlaceSnapshotFixture struct {
	tempInfo  fs.FileInfo
	otherInfo fs.FileInfo
	dirInfo   fs.FileInfo
	digest    string
	size      int64
}

func (f *advisoryPlaceSnapshotFixture) place(root safeio.Root) (*advisorySnapshotOwnership, error) {
	return advisoryPlaceSnapshotNoReplace(root, "temp", "snapshot.json", f.digest, f.size)
}

func (f *advisoryPlaceSnapshotFixture) testTempNotRegular(t *testing.T) {
	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return f.dirInfo, nil },
	}
	ownership, err := f.place(root)
	if ownership != nil || err == nil || !strings.Contains(err.Error(), "snapshot temp path is not a regular file") {
		t.Fatalf("expected non-regular temp rejection, ownership=%#v err=%v", ownership, err)
	}
}

func (f *advisoryPlaceSnapshotFixture) testChmodFailure(t *testing.T) {
	chmodErr := errors.New("chmod snapshot temp")
	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return f.tempInfo, nil },
		chmod: func(string, os.FileMode) error {
			return chmodErr
		},
	}
	ownership, err := f.place(root)
	if ownership != nil || !errors.Is(err, chmodErr) {
		t.Fatalf("expected chmod error, ownership=%#v err=%v", ownership, err)
	}
}

func (f *advisoryPlaceSnapshotFixture) testLinkFailure(t *testing.T) {
	linkErr := errors.New("link snapshot")
	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return f.tempInfo, nil },
		link:  func(string, string) error { return linkErr },
	}
	ownership, err := f.place(root)
	if ownership != nil || !errors.Is(err, linkErr) {
		t.Fatalf("expected link error, ownership=%#v err=%v", ownership, err)
	}
}

func (f *advisoryPlaceSnapshotFixture) testExistingTargetDisappears(t *testing.T) {
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "temp" {
				return f.tempInfo, nil
			}
			return nil, os.ErrNotExist
		},
		link: func(string, string) error { return os.ErrExist },
	}
	ownership, err := f.place(root)
	if ownership != nil || err == nil || !strings.Contains(err.Error(), "disappeared during no-replace placement") {
		t.Fatalf("expected disappeared-target error, ownership=%#v err=%v", ownership, err)
	}
}

func (f *advisoryPlaceSnapshotFixture) testPostLinkLookupFailure(t *testing.T) {
	lookupErr := errors.New("lookup published snapshot")
	targetLookups := 0
	removed := false
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "temp" {
				return f.tempInfo, nil
			}
			targetLookups++
			if targetLookups == 1 {
				return nil, lookupErr
			}
			return f.tempInfo, nil
		},
		link: func(string, string) error { return nil },
		remove: func(string) error {
			removed = true
			return nil
		},
	}
	ownership, err := f.place(root)
	if ownership != nil || !errors.Is(err, lookupErr) || !removed {
		t.Fatalf("expected post-link lookup rollback, ownership=%#v removed=%v err=%v", ownership, removed, err)
	}
}

func (f *advisoryPlaceSnapshotFixture) testPostLinkIdentityMismatch(t *testing.T) {
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name == "temp" {
				return f.tempInfo, nil
			}
			return f.otherInfo, nil
		},
		link: func(string, string) error { return nil },
		remove: func(string) error {
			t.Fatal("identity-mismatched target must not be removed")
			return nil
		},
	}
	ownership, err := f.place(root)
	if ownership != nil || !errors.Is(err, errAdvisorySnapshotOwnershipLost) {
		t.Fatalf("expected ownership-loss error, ownership=%#v err=%v", ownership, err)
	}
}

func TestAdvisoryPublicationLockCancellationAndReacquisition(t *testing.T) {
	cachePath := t.TempDir()
	firstRoot := advisoryOpenTestRoot(t, cachePath)
	secondRoot := advisoryOpenTestRoot(t, cachePath)
	firstLock, err := acquireAdvisoryPublicationLock(context.Background(), firstRoot)
	if err != nil {
		t.Fatalf("acquire first publication lock: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	blockedLock, err := acquireAdvisoryPublicationLock(ctx, secondRoot)
	if blockedLock != nil {
		if closeErr := blockedLock.Close(); closeErr != nil {
			t.Errorf("close unexpectedly acquired canceled lock: %v", closeErr)
		}
	}
	if blockedLock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled publication lock waiter, lock=%v err=%v", blockedLock != nil, err)
	}
	if err := firstLock.Close(); err != nil {
		t.Fatalf("release first publication lock: %v", err)
	}
	var nilContext context.Context
	reacquired, err := acquireAdvisoryPublicationLock(nilContext, secondRoot)
	if err != nil {
		t.Fatalf("reacquire publication lock: %v", err)
	}
	if err := reacquired.Close(); err != nil {
		t.Fatalf("release reacquired publication lock: %v", err)
	}
}

func TestAdvisoryPublicationLockIgnoresReplaceableLegacyPath(t *testing.T) {
	cachePath := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside-lock")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside lock fixture: %v", err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(cachePath, legacyPublicationLockFileName)); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	root := advisoryOpenTestRoot(t, cachePath)
	lock, err := acquireAdvisoryPublicationLock(context.Background(), root)
	if err != nil {
		t.Fatalf("acquire cache identity lock with replaceable legacy path: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("release cache identity lock with replaceable legacy path: %v", err)
	}
	data, readErr := os.ReadFile(outsidePath)
	if readErr != nil || string(data) != "outside" {
		t.Fatalf("expected outside lock fixture to stay unchanged, content=%q err=%v", data, readErr)
	}
}

func TestAdvisoryPublicationLockOpenAndDescriptorFailures(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat advisory cache identity fixture: %v", err)
	}
	openErr := errors.New("open advisory cache identity")
	lock, err := acquireAdvisoryPublicationLock(context.Background(), &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		open:  func(string) (safeio.File, error) { return nil, openErr },
	})
	if lock != nil || !errors.Is(err, openErr) {
		t.Fatalf("expected cache identity open failure, lock=%v err=%v", lock != nil, err)
	}

	closed := false
	file := &advisoryFakeDirectory{advisoryFakeFile: &advisoryFakeFile{
		stat: func() (fs.FileInfo, error) { return dirInfo, nil },
		close: func() error {
			closed = true
			return nil
		},
	}}
	root := &advisoryFakeRoot{
		open:  func(string) (safeio.File, error) { return file, nil },
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
	}
	lock, err = acquireAdvisoryPublicationLock(context.Background(), root)
	if lock != nil || err == nil || !strings.Contains(err.Error(), "has no descriptor") || !closed {
		t.Fatalf("expected descriptor rejection and close, lock=%v closed=%v err=%v", lock != nil, closed, err)
	}
}

func TestVerifyAdvisoryCacheIdentityErrors(t *testing.T) {
	firstInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat first advisory cache identity: %v", err)
	}
	secondInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat second advisory cache identity: %v", err)
	}
	fileInfo := advisoryRegularFileInfo(t, "not-a-directory")
	lookupErr := errors.New("relookup advisory cache identity")
	statErr := errors.New("restat advisory cache identity")
	tests := []struct {
		name string
		root safeio.Root
		file safeio.File
		want error
	}{
		{
			name: "lookup",
			root: &advisoryFakeRoot{lstat: func(string) (fs.FileInfo, error) {
				return nil, lookupErr
			}},
			file: &advisoryFakeFile{},
			want: lookupErr,
		},
		{
			name: "stat",
			root: &advisoryFakeRoot{lstat: func(string) (fs.FileInfo, error) {
				return firstInfo, nil
			}},
			file: &advisoryFakeFile{stat: func() (fs.FileInfo, error) { return nil, statErr }},
			want: statErr,
		},
		{
			name: "non-regular",
			root: &advisoryFakeRoot{lstat: func(string) (fs.FileInfo, error) {
				return fileInfo, nil
			}},
			file: &advisoryFakeFile{stat: func() (fs.FileInfo, error) { return fileInfo, nil }},
		},
		{
			name: "identity",
			root: &advisoryFakeRoot{lstat: func(string) (fs.FileInfo, error) {
				return firstInfo, nil
			}},
			file: &advisoryFakeFile{stat: func() (fs.FileInfo, error) { return secondInfo, nil }},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := verifyAdvisoryCacheIdentity(test.root, test.file)
			if err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("expected cache identity verification failure %v, got %v", test.want, err)
			}
		})
	}
}

func advisoryRegularFileInfo(t *testing.T, contents string) fs.FileInfo {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lock")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write publication lock info fixture: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat publication lock info fixture: %v", err)
	}
	return info
}

func TestAdvisoryVerifyExistingSnapshotFailureBranches(t *testing.T) {
	fixture := newAdvisoryExistingSnapshotFixture(t)
	t.Run("open", fixture.assertOpenFailure)
	t.Run("stat", fixture.assertStatFailure)
	t.Run("size", fixture.assertSizeFailure)
	t.Run("read", fixture.assertReadFailure)
}

type advisoryExistingSnapshotFixture struct {
	payload     []byte
	fixturePath string
	info        fs.FileInfo
	digest      string
}

func newAdvisoryExistingSnapshotFixture(t *testing.T) *advisoryExistingSnapshotFixture {
	t.Helper()
	payload := []byte("winner")
	fixturePath := filepath.Join(t.TempDir(), "winner.json")
	if err := os.WriteFile(fixturePath, payload, 0o600); err != nil {
		t.Fatalf("write existing snapshot fixture: %v", err)
	}
	info, err := os.Stat(fixturePath)
	if err != nil {
		t.Fatalf("stat existing snapshot fixture: %v", err)
	}
	digestSum := sha256.Sum256(payload)
	digest := sha256Prefix + hex.EncodeToString(digestSum[:])
	return &advisoryExistingSnapshotFixture{
		payload:     payload,
		fixturePath: fixturePath,
		info:        info,
		digest:      digest,
	}
}

func (f *advisoryExistingSnapshotFixture) assertOpenFailure(t *testing.T) {
	openErr := errors.New("open existing snapshot")
	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return f.info, nil },
		open:  func(string) (safeio.File, error) { return nil, openErr },
	}
	if err := advisoryVerifyExistingSnapshot(root, "winner.json", f.digest, int64(len(f.payload))); !errors.Is(err, openErr) {
		t.Fatalf("expected existing snapshot open failure, got %v", err)
	}
}

func (f *advisoryExistingSnapshotFixture) assertStatFailure(t *testing.T) {
	statErr := errors.New("restat existing snapshot")
	statCalls := 0
	file := &advisoryFakeFile{
		stat: func() (fs.FileInfo, error) {
			statCalls++
			if statCalls == 1 {
				return f.info, nil
			}
			return nil, statErr
		},
	}
	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return f.info, nil },
		open:  func(string) (safeio.File, error) { return file, nil },
	}
	if err := advisoryVerifyExistingSnapshot(root, "winner.json", f.digest, int64(len(f.payload))); !errors.Is(err, statErr) {
		t.Fatalf("expected existing snapshot stat failure, got %v", err)
	}
}

func (f *advisoryExistingSnapshotFixture) assertSizeFailure(t *testing.T) {
	root, err := safeio.OpenRoot(filepath.Dir(f.fixturePath))
	if err != nil {
		t.Fatalf("open existing snapshot root: %v", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Fatalf("close existing snapshot root: %v", closeErr)
		}
	}()
	err = advisoryVerifyExistingSnapshot(root, filepath.Base(f.fixturePath), f.digest, int64(len(f.payload)+1))
	if !errors.Is(err, errAdvisorySnapshotContentMismatch) || !strings.Contains(err.Error(), "snapshot size") {
		t.Fatalf("expected existing snapshot size mismatch, got %v", err)
	}
}

func (f *advisoryExistingSnapshotFixture) assertReadFailure(t *testing.T) {
	readErr := errors.New("hash existing snapshot")
	file := &advisoryFakeFile{
		read: func([]byte) (int, error) { return 0, readErr },
		stat: func() (fs.FileInfo, error) {
			return f.info, nil
		},
	}
	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return f.info, nil },
		open:  func(string) (safeio.File, error) { return file, nil },
	}
	if err := advisoryVerifyExistingSnapshot(root, "winner.json", f.digest, int64(len(f.payload))); !errors.Is(err, readErr) {
		t.Fatalf("expected existing snapshot hash read failure, got %v", err)
	}
}

func TestAdvisoryVerifyPinnedSnapshotsChildPropagatesLookups(t *testing.T) {
	lookupErr := errors.New("lookup snapshots path")
	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, lookupErr },
	}
	if err := advisoryVerifyPinnedSnapshotsChild(root, &advisoryFakeRoot{}); !errors.Is(err, lookupErr) {
		t.Fatalf("expected parent lookup error, got %v", err)
	}

	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat snapshots directory fixture: %v", err)
	}
	openedLookupErr := errors.New("lookup pinned snapshots root")
	root = &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
	}
	snapshotsRoot := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, openedLookupErr },
	}
	err = advisoryVerifyPinnedSnapshotsChild(root, snapshotsRoot)
	if !errors.Is(err, openedLookupErr) {
		t.Fatalf("expected pinned-root lookup error, got %v", err)
	}
}

func TestRollbackOwnedSnapshotMissingLookupAndRemoveErrors(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(targetPath, []byte("snapshot"), 0o600); err != nil {
		t.Fatalf("write rollback identity fixture: %v", err)
	}
	targetInfo, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("stat rollback identity fixture: %v", err)
	}
	ownership := &advisorySnapshotOwnership{name: "snapshot.json", info: targetInfo}
	publicationErr := errors.New("manifest publication")

	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist },
	}
	if err := rollbackOwnedSnapshot(root, ownership, publicationErr); !errors.Is(err, publicationErr) {
		t.Fatalf("expected missing owned target to preserve publication error, got %v", err)
	}

	lookupErr := errors.New("lookup rollback target")
	root = &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return nil, lookupErr },
	}
	err = rollbackOwnedSnapshot(root, ownership, publicationErr)
	if !errors.Is(err, publicationErr) || !errors.Is(err, lookupErr) {
		t.Fatalf("expected publication and rollback lookup errors, got %v", err)
	}

	removeErr := errors.New("remove rollback target")
	root = &advisoryFakeRoot{
		lstat:  func(string) (fs.FileInfo, error) { return targetInfo, nil },
		remove: func(string) error { return removeErr },
	}
	err = rollbackOwnedSnapshot(root, ownership, publicationErr)
	if !errors.Is(err, publicationErr) || !errors.Is(err, removeErr) {
		t.Fatalf("expected publication and rollback removal errors, got %v", err)
	}
}

func TestPrepareAdvisoryCacheRootClosesOriginalRootAfterCreatingChild(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	lstatCalls := 0
	rootClosed := 0
	child := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "." {
				t.Fatalf("unexpected child lstat %q", name)
			}
			return dirInfo, nil
		},
	}
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "leaf" {
				t.Fatalf("unexpected root lstat %q", name)
			}
			lstatCalls++
			if lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return dirInfo, nil
		},
		mkdir: func(string, os.FileMode) error { return nil },
		openRoot: func(name string) (safeio.Root, error) {
			if name != "leaf" {
				t.Fatalf("unexpected openRoot %q", name)
			}
			return child, nil
		},
		close: func() error {
			rootClosed++
			return nil
		},
	}
	realHook := openAdvisoryCacheAncestor
	openAdvisoryCacheAncestor = func(string) (safeio.Root, string, []string, error) {
		return root, "/cache", []string{"leaf"}, nil
	}
	t.Cleanup(func() {
		openAdvisoryCacheAncestor = realHook
	})

	opened, err := prepareAdvisoryCacheRoot("/cache/leaf")
	if err != nil {
		t.Fatalf("prepare advisory cache root after child create: %v", err)
	}
	if opened != child {
		t.Fatalf("expected created child root, got %#v", opened)
	}
	if rootClosed != 1 {
		t.Fatalf("expected original root to close once, got %d", rootClosed)
	}
}

func TestPrepareAdvisoryCacheRootJoinsRootAndOwnedCloseErrorsAfterCreatingChild(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	rootCloseErr := errors.New("close advisory ancestor")
	childCloseErr := errors.New("close created advisory root")
	lstatCalls := 0
	child := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		close: func() error { return childCloseErr },
	}
	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) {
			lstatCalls++
			if lstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return dirInfo, nil
		},
		mkdir:    func(string, os.FileMode) error { return nil },
		openRoot: func(string) (safeio.Root, error) { return child, nil },
		close:    func() error { return rootCloseErr },
	}
	realHook := openAdvisoryCacheAncestor
	openAdvisoryCacheAncestor = func(string) (safeio.Root, string, []string, error) {
		return root, "/cache", []string{"leaf"}, nil
	}
	t.Cleanup(func() {
		openAdvisoryCacheAncestor = realHook
	})

	opened, err := prepareAdvisoryCacheRoot("/cache/leaf")
	if opened != nil {
		t.Fatal("expected root close failure to return no opened root")
	}
	if !errors.Is(err, rootCloseErr) || !errors.Is(err, childCloseErr) {
		t.Fatalf("expected root and child close errors to be joined, got %v", err)
	}
}

func TestAdvisoryOpenOrCreatePinnedChildRejectsSymlink(t *testing.T) {
	targetPath := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(targetPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	linkPath := filepath.Join(t.TempDir(), "child-link")
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	linkInfo, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	root := &advisoryFakeRoot{
		lstat: func(name string) (fs.FileInfo, error) {
			if name != "child" {
				t.Fatalf("unexpected lstat %q", name)
			}
			return linkInfo, nil
		},
	}

	child, err := advisoryOpenOrCreatePinnedChild(root, "/cache", "child")
	if child != nil {
		t.Fatal("expected symlinked advisory child to be rejected")
	}
	if err == nil || !strings.Contains(err.Error(), "root contains symlink: /cache/child") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestAdvisoryOpenOrCreatePinnedChildJoinsOpenedRootLookupAndCloseError(t *testing.T) {
	dirInfo, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatalf("stat temp dir: %v", err)
	}
	lookupErr := errors.New("lstat opened advisory child")
	closeErr := errors.New("close opened advisory child")
	root := &advisoryFakeRoot{
		lstat: func(string) (fs.FileInfo, error) { return dirInfo, nil },
		openRoot: func(string) (safeio.Root, error) {
			return &advisoryFakeRoot{
				lstat: func(string) (fs.FileInfo, error) { return nil, lookupErr },
				close: func() error { return closeErr },
			}, nil
		},
	}

	child, err := advisoryOpenOrCreatePinnedChild(root, "/cache", "child")
	if child != nil {
		t.Fatal("expected opened-child lookup failure to return no root")
	}
	if !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("expected lookup and close errors to be joined, got %v", err)
	}
}

func TestLoadSnapshotMetadataRejectsSymlinkPath(t *testing.T) {
	parentDir := t.TempDir()
	outsidePath := filepath.Join(parentDir, "outside.json")
	if err := os.WriteFile(outsidePath, []byte(`{"vulns":[{"affected":[{"package":{"ecosystem":"Go"}}]}]}`), 0o600); err != nil {
		t.Fatalf("write outside snapshot: %v", err)
	}
	linkPath := filepath.Join(parentDir, "linked.json")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Fatalf("create snapshot symlink: %v", err)
	}

	ecosystems, entryCount := loadSnapshotMetadata(linkPath, 64, "osv-json")
	if len(ecosystems) != 0 || entryCount != 0 {
		t.Fatalf("expected symlinked metadata read to be rejected, got ecosystems=%v entryCount=%d", ecosystems, entryCount)
	}
}

func TestSnapshotMetadataHelpersAdditionalBranches(t *testing.T) {
	if count := snapshotEntryCount([]byte(`[{"id":"A"},{"id":"B"},{"id":"C"}]`)); count != 3 {
		t.Fatalf("expected array entry count, got %d", count)
	}
	single := []byte(testOSVAdvisory("GO-2021-0113"))
	if count := snapshotEntryCount(single); count != 1 {
		t.Fatalf("expected single advisory entry count, got %d", count)
	}
	if ecosystems := snapshotEcosystems(single); len(ecosystems) != 1 || ecosystems[0] != "Go" {
		t.Fatalf("expected single advisory ecosystem, got %v", ecosystems)
	}
	if count := snapshotEntryCount([]byte(`{"error":"quota exceeded"}`)); count != 0 {
		t.Fatalf("expected error envelope entry count to be zero, got %d", count)
	}
	if count := snapshotEntryCount([]byte(`not-json`)); count != 0 {
		t.Fatalf("expected invalid entry count to be zero, got %d", count)
	}
	if ecosystems, count := loadSnapshotMetadata(filepath.Join(t.TempDir(), "missing.json"), 1, "osv-zip"); len(ecosystems) != 0 || count != 0 {
		t.Fatalf("expected non-json metadata read to be skipped, got ecosystems=%v count=%d", ecosystems, count)
	}
	if ecosystems, count := loadSnapshotMetadata(filepath.Join(t.TempDir(), "missing.json"), maxSyncMetadataBytes+1, "osv-json"); len(ecosystems) != 0 || count != 0 {
		t.Fatalf("expected oversized metadata read to be skipped, got ecosystems=%v count=%d", ecosystems, count)
	}
}

func TestDownloadSnapshotUnderRootUsesDefaultClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(testOSVSnapshot("OSV-1"))); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	root := advisoryOpenTestRoot(t, t.TempDir())
	fetched, err := downloadSnapshotUnderRoot(context.Background(), server.URL, nil, root)
	if err != nil {
		t.Fatalf("downloadSnapshotUnderRoot with default client: %v", err)
	}
	if fetched.schema != "osv-json" || fetched.entryCount != 1 || len(fetched.ecosystems) != 1 || fetched.ecosystems[0] != "Go" {
		t.Fatalf("unexpected fetched metadata: %#v", fetched)
	}
}

func TestDownloadSnapshotUnderRootRejectsTempSymlinkSwapBeforeValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(testOSVSnapshot("OSV-1"))); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	rootDir := t.TempDir()
	realRoot := advisoryOpenTestRoot(t, rootDir)
	outsidePath := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outsidePath, []byte(testOSVSnapshot("OSV-2")), 0o600); err != nil {
		t.Fatalf("write outside snapshot: %v", err)
	}

	swapped := false
	root := &advisoryFakeRoot{
		open:     realRoot.Open,
		openFile: realRoot.OpenFile,
		openRoot: realRoot.OpenRoot,
		lstat: func(name string) (fs.FileInfo, error) {
			if !swapped && strings.HasPrefix(filepath.Base(name), ".safeio-atomic-") {
				swapped = true
				tempPath := filepath.Join(rootDir, name)
				if err := os.Remove(tempPath); err != nil {
					t.Fatalf("remove temp snapshot before symlink swap: %v", err)
				}
				if err := os.Symlink(outsidePath, tempPath); err != nil {
					t.Fatalf("replace temp snapshot with symlink: %v", err)
				}
			}
			return realRoot.Lstat(name)
		},
		remove: realRoot.Remove,
	}

	_, err := downloadSnapshotUnderRoot(context.Background(), server.URL, nil, root)
	if !errors.Is(err, safeio.ErrTargetPathSymlink) {
		t.Fatalf("expected target symlink sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "invalid OSV JSON snapshot") {
		t.Fatalf("expected validation error context, got %v", err)
	}
	advisoryAssertNoSafeIOTempFiles(t, rootDir)
}

func TestDownloadSnapshotUnderRootSuppressesMetadataFromOversizedReopenedTemp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(testOSVSnapshot("OSV-1"))); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer server.Close()

	rootDir := t.TempDir()
	realRoot := advisoryOpenTestRoot(t, rootDir)
	metadataGrew := false
	root := &advisoryFakeRoot{
		open: func(name string) (safeio.File, error) {
			file, err := realRoot.Open(name)
			if err != nil {
				return nil, err
			}
			if metadataGrew || !strings.HasPrefix(filepath.Base(name), ".safeio-atomic-") {
				return file, nil
			}
			tempPath := filepath.Join(rootDir, name)
			return &advisoryWrappedFile{
				File: file,
				closeHook: func() error {
					metadataGrew = true
					return writeOversizedValidSnapshot(tempPath, maxSyncMetadataBytes+1024)
				},
			}, nil
		},
		openFile: realRoot.OpenFile,
		openRoot: realRoot.OpenRoot,
		lstat:    realRoot.Lstat,
		remove:   realRoot.Remove,
	}

	fetched, err := downloadSnapshotUnderRoot(context.Background(), server.URL, nil, root)
	if err != nil {
		t.Fatalf("downloadSnapshotUnderRoot with oversized reopened metadata: %v", err)
	}
	if !metadataGrew {
		t.Fatal("expected metadata growth hook to run")
	}
	if fetched.schema != schemaOSVJSON {
		t.Fatalf("expected JSON schema, got %#v", fetched)
	}
	if fetched.sizeBytes >= maxSyncMetadataBytes {
		t.Fatalf("expected streamed size below metadata limit, got %d", fetched.sizeBytes)
	}
	if len(fetched.ecosystems) != 0 || fetched.entryCount != 0 {
		t.Fatalf("expected oversized reopened metadata to be suppressed, got ecosystems=%v entryCount=%d", fetched.ecosystems, fetched.entryCount)
	}
}

func TestDefaultSnapshotHTTPClientUsesExtendedTimeout(t *testing.T) {
	client := defaultSnapshotHTTPClient()
	if client.Timeout != defaultHTTPTimeout {
		t.Fatalf("expected default timeout %s, got %s", defaultHTTPTimeout, client.Timeout)
	}
	if client.Timeout <= 5*time.Minute {
		t.Fatalf("expected default timeout to exceed 5 minutes, got %s", client.Timeout)
	}
}

func TestDownloadSnapshotUnderRootRejectsHTTPStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()

	root := advisoryOpenTestRoot(t, t.TempDir())
	if _, err := downloadSnapshotUnderRoot(context.Background(), server.URL, server.Client(), root); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("expected HTTP status error, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootReturnsStatusCloseError(t *testing.T) {
	root := &advisoryFakeRoot{}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       &errCloseReadCloser{Reader: strings.NewReader("down")},
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected status close error, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootReturnsRequestCreationError(t *testing.T) {
	root := &advisoryFakeRoot{}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "://bad", nil, root); err == nil {
		t.Fatal("expected request creation error")
	}
}

func TestDownloadSnapshotUnderRootReturnsTempCreationError(t *testing.T) {
	expectedErr := errors.New("open temp failure")
	root := &advisoryFakeRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return nil, expectedErr
		},
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !errors.Is(err, expectedErr) {
		t.Fatalf("expected temp creation error, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootReturnsTempCreationErrorWithResponseCloseDetail(t *testing.T) {
	expectedErr := errors.New("open temp failure")
	root := &advisoryFakeRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return nil, expectedErr
		},
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errCloseReadCloser{Reader: strings.NewReader(`[]`)},
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !strings.Contains(err.Error(), "close advisory response") || !errors.Is(err, expectedErr) {
		t.Fatalf("expected temp creation error with close detail, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootRejectsNilTempFile(t *testing.T) {
	root := &advisoryFakeRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return nil, nil
		},
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !strings.Contains(err.Error(), "nil temp file") {
		t.Fatalf("expected nil temp file error, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootReturnsResponseCloseError(t *testing.T) {
	root := &advisoryFakeRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return &advisoryFakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				close: func() error { return nil },
				chmod: func(os.FileMode) error { return nil },
			}, nil
		},
		remove: func(string) error { return nil },
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errCloseReadCloser{Reader: strings.NewReader(`[]`)},
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected response close error, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootReturnsTempFileCloseError(t *testing.T) {
	expectedErr := errors.New("temp close failure")
	root := &advisoryFakeRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return &advisoryFakeFile{
				write: func(p []byte) (int, error) { return len(p), nil },
				close: func() error { return expectedErr },
				chmod: func(os.FileMode) error { return nil },
			}, nil
		},
		remove: func(string) error { return nil },
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`[]`)),
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !errors.Is(err, expectedErr) {
		t.Fatalf("expected temp close error, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootReturnsWriteError(t *testing.T) {
	expectedErr := errors.New("write failure")
	root := &advisoryFakeRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return &advisoryFakeFile{
				write: func([]byte) (int, error) { return 0, expectedErr },
				close: func() error { return nil },
				chmod: func(os.FileMode) error { return nil },
			}, nil
		},
		remove: func(string) error { return nil },
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"vulns":[{"id":"OSV-1"}]}`)),
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !errors.Is(err, expectedErr) {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootReturnsWriteErrorWithResponseCloseDetail(t *testing.T) {
	expectedErr := errors.New("write failure")
	root := &advisoryFakeRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return &advisoryFakeFile{
				write: func([]byte) (int, error) { return 0, expectedErr },
				close: func() error { return nil },
				chmod: func(os.FileMode) error { return nil },
			}, nil
		},
		remove: func(string) error { return nil },
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errCloseReadCloser{Reader: strings.NewReader(`{"vulns":[{"id":"OSV-1"}]}`)},
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !errors.Is(err, expectedErr) || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected write error with close detail, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootReturnsReadError(t *testing.T) {
	root := &advisoryFakeRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return &advisoryFakeFile{
				write: func([]byte) (int, error) { return 0, nil },
				close: func() error { return nil },
				chmod: func(os.FileMode) error { return nil },
			}, nil
		},
		remove: func(string) error { return nil },
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errReadCloser{},
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !strings.Contains(err.Error(), "read advisory snapshot") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func TestDownloadSnapshotUnderRootReturnsReadErrorWithResponseCloseDetail(t *testing.T) {
	root := &advisoryFakeRoot{
		openFile: func(string, int, os.FileMode) (safeio.File, error) {
			return &advisoryFakeFile{
				write: func([]byte) (int, error) { return 0, nil },
				close: func() error { return nil },
				chmod: func(os.FileMode) error { return nil },
			}, nil
		},
		remove: func(string) error { return nil },
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &errReadCloseCloser{},
		}, nil
	})}

	if _, err := downloadSnapshotUnderRoot(context.Background(), "https://example.test/osv.json", client, root); err == nil || !strings.Contains(err.Error(), "read advisory snapshot") || !strings.Contains(err.Error(), "close advisory response") {
		t.Fatalf("expected read error with close detail, got %v", err)
	}
}

func TestUpdateManifestReturnsWriteError(t *testing.T) {
	cachePath := t.TempDir()
	if err := os.Mkdir(filepath.Join(cachePath, manifestFileName), 0o755); err != nil {
		t.Fatalf("mkdir manifest path: %v", err)
	}

	root := advisoryOpenTestRoot(t, cachePath)
	err := updateManifest(root, CacheSnapshot{ID: "new", Path: "snapshots/new.json"}, time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC))
	if !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("expected manifest write error, got %v", err)
	}
}

func TestUpdateManifestRootLocalWriteFailuresPreserveEvidenceAndCleanup(t *testing.T) {
	now := time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC)
	cleanupWriteErr := errors.New("write failure")
	cleanupErr := errors.New("cleanup failure")
	for _, tc := range []struct {
		name   string
		root   *advisoryFakeRoot
		expect func(error) bool
	}{
		{
			name: "setup",
			root: advisoryRootWithoutManifest(&advisoryFakeRoot{
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return nil, errors.New("open temp failure")
				},
			}),
			expect: func(err error) bool { return err != nil && strings.Contains(err.Error(), "open temp failure") },
		},
		{
			name: "write",
			root: advisoryRootWithoutManifest(&advisoryFakeRoot{
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return &advisoryFakeFile{
						write: func([]byte) (int, error) { return 0, errors.New("write failure") },
						close: func() error { return nil },
						chmod: func(os.FileMode) error { return nil },
					}, nil
				},
				remove: advisoryExpectAtomicTempCleanup(t),
			}),
			expect: func(err error) bool { return err != nil && strings.Contains(err.Error(), "write failure") },
		},
		{
			name: "close",
			root: advisoryRootWithoutManifest(&advisoryFakeRoot{
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return &advisoryFakeFile{
						write: func(p []byte) (int, error) { return len(p), nil },
						close: func() error { return errors.New("temp close failure") },
						chmod: func(os.FileMode) error { return nil },
					}, nil
				},
				remove: advisoryExpectAtomicTempCleanup(t),
			}),
			expect: func(err error) bool { return err != nil && strings.Contains(err.Error(), "temp close failure") },
		},
		{
			name: "rename",
			root: advisoryRootWithoutManifest(&advisoryFakeRoot{
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return &advisoryFakeFile{
						write: func(p []byte) (int, error) { return len(p), nil },
						close: func() error { return nil },
						chmod: func(os.FileMode) error { return nil },
					}, nil
				},
				rename: func(oldName, newName string) error {
					if newName != manifestFileName {
						t.Fatalf("expected rename target %q, got %q", manifestFileName, newName)
					}
					return errors.New("rename failure")
				},
				remove: advisoryExpectAtomicTempCleanup(t),
			}),
			expect: func(err error) bool { return err != nil && strings.Contains(err.Error(), "rename failure") },
		},
		{
			name: "cleanup",
			root: advisoryRootWithoutManifest(&advisoryFakeRoot{
				openFile: func(string, int, os.FileMode) (safeio.File, error) {
					return &advisoryFakeFile{
						write: func([]byte) (int, error) { return 0, cleanupWriteErr },
						close: func() error { return nil },
						chmod: func(os.FileMode) error { return nil },
					}, nil
				},
				remove: func(string) error {
					return cleanupErr
				},
			}),
			expect: func(err error) bool {
				return errors.Is(err, cleanupWriteErr) && errors.Is(err, cleanupErr)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := updateManifest(tc.root, CacheSnapshot{ID: "new", Path: "snapshots/new.json"}, now)
			if !tc.expect(err) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadSnapshotMetadataUnderRootReturnsZeroWhenOpenFails(t *testing.T) {
	root := &advisoryFakeRoot{
		open: func(string) (safeio.File, error) {
			return nil, errors.New("open failure")
		},
	}
	ecosystems, entryCount := loadSnapshotMetadataUnderRoot(root, "snapshot.json", 64, "osv-json")
	if len(ecosystems) != 0 || entryCount != 0 {
		t.Fatalf("expected open failure to suppress metadata, got ecosystems=%v entryCount=%d", ecosystems, entryCount)
	}
}

func TestLoadSnapshotMetadataUnderRootReturnsZeroWhenReadFails(t *testing.T) {
	root := &advisoryFakeRoot{
		open: func(string) (safeio.File, error) {
			return &advisoryFakeFile{
				read:  func([]byte) (int, error) { return 0, errors.New("read failure") },
				close: func() error { return nil },
				chmod: func(os.FileMode) error { return nil },
			}, nil
		},
	}
	ecosystems, entryCount := loadSnapshotMetadataUnderRoot(root, "snapshot.json", 64, "osv-json")
	if len(ecosystems) != 0 || entryCount != 0 {
		t.Fatalf("expected read failure to suppress metadata, got ecosystems=%v entryCount=%d", ecosystems, entryCount)
	}
}

func TestLoadSnapshotMetadataUnderRootReturnsZeroWhenCloseFails(t *testing.T) {
	root := &advisoryFakeRoot{
		open: func(string) (safeio.File, error) {
			return &advisoryFakeFile{
				read: func(p []byte) (int, error) {
					copy(p, `{"vulns":[{"affected":[{"package":{"ecosystem":"Go"}}]}]}`)
					return len(`{"vulns":[{"affected":[{"package":{"ecosystem":"Go"}}]}]}`), io.EOF
				},
				close: func() error { return errors.New("close failure") },
				chmod: func(os.FileMode) error { return nil },
			}, nil
		},
	}
	ecosystems, entryCount := loadSnapshotMetadataUnderRoot(root, "snapshot.json", 64, "osv-json")
	if len(ecosystems) != 0 || entryCount != 0 {
		t.Fatalf("expected close failure to suppress metadata, got ecosystems=%v entryCount=%d", ecosystems, entryCount)
	}
}

type advisorySwapPaths struct {
	cachePath        string
	renamedCachePath string
	outsideDir       string
}

type advisoryRedirectPolicyRecorder struct {
	checkRedirectCalls atomic.Int32
	recordedTarget     string
	recordedVia        []string
	sentinel           error
}

func (r *advisoryRedirectPolicyRecorder) policy(req *http.Request, via []*http.Request) error {
	r.checkRedirectCalls.Add(1)
	if req.URL != nil {
		r.recordedTarget = req.URL.String()
	}
	r.recordedVia = r.recordedVia[:0]
	for _, prev := range via {
		if prev != nil && prev.URL != nil {
			r.recordedVia = append(r.recordedVia, prev.URL.String())
			continue
		}
		r.recordedVia = append(r.recordedVia, "")
	}
	return r.sentinel
}

func (r *advisoryRedirectPolicyRecorder) reset() {
	r.checkRedirectCalls.Store(0)
	r.recordedTarget = ""
	r.recordedVia = nil
}

func advisoryEmptyOSVTLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"vulns":[]}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
}

func advisoryNewSwapPaths(t *testing.T, createCache bool) advisorySwapPaths {
	t.Helper()
	parentDir := t.TempDir()
	paths := advisorySwapPaths{
		cachePath:        filepath.Join(parentDir, "cache"),
		renamedCachePath: filepath.Join(parentDir, "cache-acquired"),
		outsideDir:       filepath.Join(parentDir, "outside"),
	}
	if createCache {
		if err := os.MkdirAll(paths.cachePath, 0o750); err != nil {
			t.Fatalf("create cache dir: %v", err)
		}
	}
	if err := os.MkdirAll(paths.outsideDir, 0o750); err != nil {
		t.Fatalf("create outside dir: %v", err)
	}
	return paths
}

func advisorySetSyncAfterDownloadHook(t *testing.T, hook func(cacheRoot, tempRel string)) {
	t.Helper()
	syncAfterDownloadTestHook = hook
	t.Cleanup(func() {
		syncAfterDownloadTestHook = nil
	})
}

func advisorySetSyncBeforeSnapshotPlacementHook(t *testing.T, hook func(cacheRoot, snapshotRel string)) {
	t.Helper()
	syncBeforeSnapshotPlacementHook = hook
	t.Cleanup(func() {
		syncBeforeSnapshotPlacementHook = nil
	})
}

func advisorySetSyncAfterSnapshotPlacementHook(t *testing.T, hook func(cacheRoot, snapshotRel string)) {
	t.Helper()
	syncAfterSnapshotPlacementHook = hook
	t.Cleanup(func() {
		syncAfterSnapshotPlacementHook = nil
	})
}

func withOpenAdvisoryCacheRootHook(t *testing.T, hook func(string) (safeio.Root, error)) {
	t.Helper()
	original := openAdvisoryCacheRootHook
	openAdvisoryCacheRootHook = hook
	t.Cleanup(func() {
		openAdvisoryCacheRootHook = original
	})
}

func advisoryRequireTempPresent(t *testing.T, cacheRoot, tempRel, context string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(cacheRoot, tempRel)); err != nil {
		t.Fatalf("stat downloaded temp %s: %v", context, err)
	}
}

func advisorySwapCacheRootForSymlink(t *testing.T, cacheRoot, renamedCachePath, outsideDir, tempRel string) {
	t.Helper()
	if err := os.Rename(cacheRoot, renamedCachePath); err != nil {
		t.Fatalf("rename acquired cache root: %v", err)
	}
	if err := os.Symlink(outsideDir, cacheRoot); err != nil {
		t.Fatalf("replace cache root with symlink: %v", err)
	}
	advisoryRequireTempPresent(t, renamedCachePath, tempRel, "in renamed cache")
}

func advisoryAssertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected %q to stay untouched, got %d entries", dir, len(entries))
	}
}

func advisoryAssertNoSafeIOTempFiles(t *testing.T, cachePath string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(cachePath, ".safeio-atomic-*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	nestedMatches, err := filepath.Glob(filepath.Join(cachePath, "snapshots", ".safeio-atomic-*"))
	if err != nil {
		t.Fatalf("glob nested temp files: %v", err)
	}
	matches = append(matches, nestedMatches...)
	if len(matches) != 0 {
		t.Fatalf("expected downloaded temp to be cleaned up, got %v", matches)
	}
}

func advisoryRootWithoutManifest(root *advisoryFakeRoot) *advisoryFakeRoot {
	root.open = func(string) (safeio.File, error) {
		return nil, os.ErrNotExist
	}
	if root.lstat == nil {
		root.lstat = func(string) (fs.FileInfo, error) {
			return nil, os.ErrNotExist
		}
	}
	return root
}

func advisoryExpectAtomicTempCleanup(t *testing.T) func(string) error {
	t.Helper()
	return func(name string) error {
		if !strings.HasPrefix(filepath.Base(name), ".safeio-atomic-") {
			t.Fatalf("expected cleanup of temp file, got %q", name)
		}
		return nil
	}
}

func advisoryOpenTestRoot(t *testing.T, rootDir string) safeio.Root {
	t.Helper()
	root, err := safeio.OpenRoot(rootDir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Fatalf("close root: %v", closeErr)
		}
	})
	return root
}

func advisoryMustNewRequest(t *testing.T, url, failurePrefix string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("%s: %v", failurePrefix, err)
	}
	return req
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type errReadCloser struct{}

func (*errReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (*errReadCloser) Close() error {
	return nil
}

var _ io.ReadCloser = (*errReadCloser)(nil)

type errCloseReadCloser struct {
	*strings.Reader
}

func (*errCloseReadCloser) Close() error {
	return errors.New("close failed")
}

type errCloseReaderAt struct {
	*bytes.Reader
}

func (*errCloseReaderAt) Close() error {
	return errors.New("close failed")
}

type errReadCloseCloser struct{}

func (*errReadCloseCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (*errReadCloseCloser) Close() error {
	return errors.New("close failed")
}

type advisoryFakeRoot struct {
	open     func(name string) (safeio.File, error)
	openFile func(name string, flag int, perm os.FileMode) (safeio.File, error)
	openRoot func(name string) (safeio.Root, error)
	lstat    func(name string) (fs.FileInfo, error)
	mkdir    func(name string, perm os.FileMode) error
	chmod    func(name string, perm os.FileMode) error
	mkdirAll func(name string, perm os.FileMode) error
	link     func(oldName, newName string) error
	rename   func(oldName, newName string) error
	remove   func(name string) error
	close    func() error
}

func (r *advisoryFakeRoot) Open(name string) (safeio.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return nil, errors.New("unexpected open")
}

func (r *advisoryFakeRoot) OpenFile(name string, flag int, perm os.FileMode) (safeio.File, error) {
	if r.openFile != nil {
		return r.openFile(name, flag, perm)
	}
	return nil, errors.New("unexpected open file")
}

func (r *advisoryFakeRoot) OpenRoot(name string) (safeio.Root, error) {
	if r.openRoot != nil {
		return r.openRoot(name)
	}
	return nil, errors.New("unexpected open root")
}

func (r *advisoryFakeRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.lstat != nil {
		return r.lstat(name)
	}
	return nil, errors.New("unexpected lstat")
}

func (r *advisoryFakeRoot) Mkdir(name string, perm os.FileMode) error {
	if r.mkdir != nil {
		return r.mkdir(name, perm)
	}
	return errors.New("unexpected mkdir")
}

func (r *advisoryFakeRoot) Chmod(name string, perm os.FileMode) error {
	if r.chmod != nil {
		return r.chmod(name, perm)
	}
	return nil
}

func (r *advisoryFakeRoot) MkdirAll(name string, perm os.FileMode) error {
	if r.mkdirAll != nil {
		return r.mkdirAll(name, perm)
	}
	return nil
}

func (r *advisoryFakeRoot) Link(oldName, newName string) error {
	if r.link != nil {
		return r.link(oldName, newName)
	}
	return errors.New("unexpected link")
}

func (r *advisoryFakeRoot) Rename(oldName, newName string) error {
	if r.rename != nil {
		return r.rename(oldName, newName)
	}
	return nil
}

func (r *advisoryFakeRoot) Remove(name string) error {
	if r.remove != nil {
		return r.remove(name)
	}
	return nil
}

func (r *advisoryFakeRoot) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

type advisoryFakeFile struct {
	read  func([]byte) (int, error)
	write func([]byte) (int, error)
	close func() error
	stat  func() (fs.FileInfo, error)
	chmod func(os.FileMode) error
}

type advisoryFakeDirectory struct {
	*advisoryFakeFile
}

func (*advisoryFakeDirectory) ReadDir(int) ([]fs.DirEntry, error) {
	return nil, io.EOF
}

func (f *advisoryFakeFile) Read(p []byte) (int, error) {
	if f.read != nil {
		return f.read(p)
	}
	return 0, io.EOF
}

func (f *advisoryFakeFile) Write(p []byte) (int, error) {
	if f.write != nil {
		return f.write(p)
	}
	return len(p), nil
}

func (f *advisoryFakeFile) Close() error {
	if f.close != nil {
		return f.close()
	}
	return nil
}

func (f *advisoryFakeFile) Stat() (os.FileInfo, error) {
	if f.stat != nil {
		return f.stat()
	}
	return nil, errors.New("unexpected stat")
}

func (f *advisoryFakeFile) Chmod(perm os.FileMode) error {
	if f.chmod != nil {
		return f.chmod(perm)
	}
	return nil
}

type advisoryStaticFile struct {
	info    fs.FileInfo
	payload []byte
	offset  int
}

func (f *advisoryStaticFile) Read(p []byte) (int, error) {
	if f.offset >= len(f.payload) {
		return 0, io.EOF
	}
	count := copy(p, f.payload[f.offset:])
	f.offset += count
	if f.offset >= len(f.payload) {
		return count, io.EOF
	}
	return count, nil
}

func (f *advisoryStaticFile) Write([]byte) (int, error) {
	return 0, errors.New("unexpected write")
}

func (f *advisoryStaticFile) Close() error {
	return nil
}

func (f *advisoryStaticFile) Stat() (os.FileInfo, error) {
	return f.info, nil
}

func (f *advisoryStaticFile) Chmod(os.FileMode) error {
	return nil
}

type advisoryWrappedFile struct {
	safeio.File
	closeHook func() error
}

func (f *advisoryWrappedFile) Close() error {
	closeErr := f.File.Close()
	if f.closeHook == nil {
		return closeErr
	}
	return errors.Join(closeErr, f.closeHook())
}

func writeOversizedValidSnapshot(path string, sizeBytes int64) (returnErr error) {
	if sizeBytes <= 0 {
		return fmt.Errorf("invalid oversized snapshot size %d", sizeBytes)
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			returnErr = errors.Join(returnErr, file.Close())
		}
	}()

	prefix := `{"vulns":[{"id":"GO-OVERSIZED","affected":[{"package":{"ecosystem":"Go"}}],"details":"`
	suffix := `"}]}`
	if _, err := file.WriteString(prefix); err != nil {
		return err
	}

	remaining := sizeBytes - int64(len(prefix)) - int64(len(suffix))
	if remaining < 0 {
		return fmt.Errorf("oversized snapshot target too small: %d", sizeBytes)
	}
	chunk := strings.Repeat("a", 1<<20)
	for remaining > 0 {
		writeLen := len(chunk)
		if int64(writeLen) > remaining {
			writeLen = int(remaining)
		}
		if _, err := file.WriteString(chunk[:writeLen]); err != nil {
			return err
		}
		remaining -= int64(writeLen)
	}
	if _, err := file.WriteString(suffix); err != nil {
		return err
	}
	closed = true
	return file.Close()
}

func writeOversizedValidManifest(path string, sizeBytes int64) (returnErr error) {
	if sizeBytes <= 0 {
		return fmt.Errorf("invalid oversized manifest size %d", sizeBytes)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			returnErr = errors.Join(returnErr, file.Close())
		}
	}()

	prefix := `{"schemaVersion":"` + manifestSchemaVersion + `","updatedAt":"2026-07-13T00:00:00Z","latest":"`
	suffix := `","snapshots":[]}`
	if _, err := file.WriteString(prefix); err != nil {
		return err
	}

	remaining := sizeBytes - int64(len(prefix)) - int64(len(suffix))
	if remaining < 0 {
		return fmt.Errorf("oversized manifest target too small: %d", sizeBytes)
	}
	chunk := strings.Repeat("a", 1<<20)
	for remaining > 0 {
		writeLen := len(chunk)
		if int64(writeLen) > remaining {
			writeLen = int(remaining)
		}
		if _, err := file.WriteString(chunk[:writeLen]); err != nil {
			return err
		}
		remaining -= int64(writeLen)
	}
	if _, err := file.WriteString(suffix); err != nil {
		return err
	}
	closed = true
	return file.Close()
}

func testCacheManifestPayload(t *testing.T, manifest CacheManifest) []byte {
	t.Helper()
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal cache manifest fixture: %v", err)
	}
	return append(payload, '\n')
}
