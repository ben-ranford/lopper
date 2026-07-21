package advisory

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

const downloadSnapshotWriteErrorChildEnv = "LOPPER_DOWNLOAD_SNAPSHOT_WRITE_ERROR_CHILD"

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

	cachePath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cachePath, manifestFileName), []byte("{"), 0o600); err != nil {
		t.Fatalf("write invalid manifest: %v", err)
	}
	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: cachePath, Client: server.Client()}); err == nil || !strings.Contains(err.Error(), "parse advisory cache manifest") {
		t.Fatalf("expected manifest parse error, got %v", err)
	}

	fileCache := filepath.Join(t.TempDir(), "cache-file")
	if err := os.WriteFile(fileCache, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file cache: %v", err)
	}
	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: server.URL, CachePath: fileCache, Client: server.Client()}); err == nil || !strings.Contains(err.Error(), "create advisory cache") {
		t.Fatalf("expected cache directory error, got %v", err)
	}

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

	downloadFailClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("snapshot unavailable")
	})}
	if _, err := SyncOSV(context.Background(), SyncOptions{SourceURL: "https://example.test/osv.json", CachePath: t.TempDir(), Client: downloadFailClient}); err == nil || !strings.Contains(err.Error(), "download advisory snapshot") {
		t.Fatalf("expected snapshot download error, got %v", err)
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
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("expected manifest write error, got %v", err)
	}
}

func TestUpdateManifestRootLocalWriteFailuresPreserveEvidenceAndCleanup(t *testing.T) {
	now := time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC)
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
						write: func([]byte) (int, error) { return 0, errors.New("write failure") },
						close: func() error { return nil },
						chmod: func(os.FileMode) error { return nil },
					}, nil
				},
				remove: func(string) error {
					return errors.New("cleanup failure")
				},
			}),
			expect: func(err error) bool {
				return err != nil && strings.Contains(err.Error(), "write failure") && !strings.Contains(err.Error(), "cleanup failure")
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
	if len(matches) != 0 {
		t.Fatalf("expected downloaded temp to be cleaned up, got %v", matches)
	}
}

func advisoryRootWithoutManifest(root *advisoryFakeRoot) *advisoryFakeRoot {
	root.open = func(string) (safeio.File, error) {
		return nil, os.ErrNotExist
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
	chmod func(os.FileMode) error
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
	return nil, errors.New("unexpected stat")
}

func (f *advisoryFakeFile) Chmod(perm os.FileMode) error {
	if f.chmod != nil {
		return f.chmod(perm)
	}
	return nil
}
