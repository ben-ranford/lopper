package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jvmlang "github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestMavenServiceCacheHitAddsNoSourceReadOrDecode(t *testing.T) {
	repo := t.TempDir()
	expectedBytes := writeMavenServiceFixture(t, repo, "1.2.3")
	service := mavenTestService(t)
	inputBytes := observeMavenServiceInputBytes(service)
	request := newCacheRequest(t, repo, filepath.Join(t.TempDir(), "cache"), false)
	request.Language = "jvm"
	request.Dependency = "widgets"
	request.Features = mustResolveDependencyIdentityPreviewFeatureSet(t)
	first, err := service.Analyse(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cache == nil || first.Cache.Writes != 1 {
		t.Fatalf("cold cache: %#v", first.Cache)
	}
	assertMavenServiceInputBytes(t, inputBytes, expectedBytes)
	reads, decodes := observeMavenIdentityIO(t)
	validated := 0
	previous := afterMavenCacheInputValidated
	t.Cleanup(func() { afterMavenCacheInputValidated = previous })
	afterMavenCacheInputValidated = func(root string) {
		validated++
		if err := os.Remove(filepath.Join(root, "pom.xml")); err != nil {
			t.Fatal(err)
		}
	}
	cached, err := service.Analyse(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if cached.Cache == nil || cached.Cache.Hits != 1 || validated != 1 || *reads != 0 || *decodes != 0 {
		t.Fatalf("warm cache=%#v validated=%d enrichment reads=%d decodes=%d", cached.Cache, validated, *reads, *decodes)
	}
	assertMavenServiceInputBytes(t, inputBytes, expectedBytes)
	assertMavenServiceIdentity(t, cached, "1.2.3", "pom.xml")
	assertMavenNormalizedServiceParity(t, first, cached)
	afterMavenCacheInputValidated = previous
	expectedBytes = writeMavenServiceFixture(t, repo, "2.0.0")
	changed, err := service.Analyse(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Cache == nil || changed.Cache.Hits != 0 || changed.Cache.Misses != 1 {
		t.Fatalf("changed input cache=%#v", changed.Cache)
	}
	assertMavenServiceInputBytes(t, inputBytes, expectedBytes)
	assertMavenServiceIdentity(t, changed, "2.0.0", "pom.xml")
	assertMavenServiceObserverIsolation(t, request, changed, inputBytes)
}
func mavenTestService(t *testing.T) *Service {
	t.Helper()
	registry := language.NewRegistry()
	if err := registry.Register(jvmlang.NewAdapter()); err != nil {
		t.Fatal(err)
	}
	return &Service{Registry: registry}
}
func writeMavenServiceFixture(t *testing.T, repo, version string) map[string]int64 {
	t.Helper()
	files := map[string]string{
		"pom.xml":   `<project><dependencies><dependency><groupId>org.example</groupId><artifactId>widgets</artifactId><version>` + version + `</version></dependency></dependencies></project>`,
		"Main.java": "import org.example.widgets.Widget;\nclass Main { Widget widget; }\n",
	}
	expected := make(map[string]int64, len(files))
	for name, content := range files {
		path := filepath.Join(repo, name)
		testutil.MustWriteFile(t, path, content)
		expected[path] = int64(len(content))
	}
	return expected
}
func observeMavenServiceInputBytes(service *Service) map[string][]int64 {
	observed := make(map[string][]int64)
	service.observeCacheInputRead = func(path string, copied int64) {
		observed[path] = append(observed[path], copied)
	}
	return observed
}
func assertMavenServiceInputBytes(t *testing.T, observed map[string][]int64, expected map[string]int64) {
	t.Helper()
	if len(observed) != len(expected) {
		t.Fatalf("input digest files: got %v want %v", observed, expected)
	}
	for path, size := range expected {
		copies := observed[path]
		if len(copies) != 1 || copies[0] != size {
			t.Fatalf("input digest reads for %s: got %v want one copy of %d bytes", path, copies, size)
		}
	}
	t.Logf("ordinary input digest copies: %v", observed)
	clear(observed)
}
func assertMavenServiceObserverIsolation(t *testing.T, request Request, observedResult report.Report, observed map[string][]int64) {
	t.Helper()
	result, err := mavenTestService(t).Analyse(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Cache == nil || result.Cache.Hits != 1 || len(observed) != 0 {
		t.Fatalf("default independent service: cache=%#v foreign observations=%v", result.Cache, observed)
	}
	assertMavenNormalizedServiceParity(t, observedResult, result)
}
func observeMavenIdentityIO(t *testing.T) (*int, *int) {
	t.Helper()
	reads, decodes := 0, 0
	read, decode := readMavenIdentityFile, decodeMavenIdentityFile
	t.Cleanup(func() { readMavenIdentityFile = read; decodeMavenIdentityFile = decode })
	readMavenIdentityFile = func(root, path string, limit int64) ([]byte, error) { reads++; return read(root, path, limit) }
	decodeMavenIdentityFile = func(data []byte) (shared.ParsedPOM, error) { decodes++; return decode(data) }
	return &reads, &decodes
}
func assertMavenServiceIdentity(t *testing.T, result report.Report, version, source string) {
	t.Helper()
	dep := findIdentityDependency(t, result, "jvm", "widgets")
	if dep.Identity.Version != version || dep.Identity.Source != source || dep.Identity.Confidence != "high" {
		t.Fatalf("identity=%#v", dep.Identity)
	}
	if result.MavenManifestCatalog || len(result.MavenManifests) != 0 {
		t.Fatal("service retained private evidence")
	}
}
func TestMavenArtifactsNeverEnterPublicFormats(t *testing.T) {
	result := mavenCacheTestReport(t, "secret-marker/pom.xml")
	for _, format := range []report.Format{report.FormatJSON, report.FormatCSV, report.FormatSPDX} {
		output, err := report.NewFormatter().Format(result, format)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "secret-marker") || strings.Contains(strings.ToLower(output), "mavenmanifest") {
			t.Fatalf("artifact leaked to %s", format)
		}
	}
	public, err := json.Marshal(result)
	if err != nil || strings.Contains(string(public), "secret-marker") {
		t.Fatalf("JSON: %s err=%v", public, err)
	}
}

func assertMavenNormalizedServiceParity(t *testing.T, live, cached report.Report) {
	t.Helper()
	cached.GeneratedAt = live.GeneratedAt
	live.Cache, cached.Cache = nil, nil
	liveJSON, err := json.Marshal(live)
	if err != nil {
		t.Fatal(err)
	}
	cachedJSON, err := json.Marshal(cached)
	if err != nil {
		t.Fatal(err)
	}
	if string(liveJSON) != string(cachedJSON) {
		t.Fatalf("normalized live/cache output differs:\nlive %s\ncache %s", liveJSON, cachedJSON)
	}
}

func TestCacheInputObserverPreservesDigestRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	content := strings.Repeat("input\xff\x00", 8192)
	testutil.MustWriteFile(t, path, content)
	for _, input := range []cacheDigestInput{
		{sortKey: "file", path: path},
		{sortKey: "optional", path: path, allowMissing: true},
		{sortKey: "missing", path: path + ".missing", allowMissing: true},
		{sortKey: "literal", literal: "directory"},
	} {
		t.Run(input.sortKey, func(t *testing.T) {
			assertObservedDigestRecord(t, input, int64(len(content)))
		})
	}
}
func assertObservedDigestRecord(t *testing.T, input cacheDigestInput, size int64) {
	t.Helper()
	var ordinary, observed strings.Builder
	if err := writeInputDigestRecord(&ordinary, input); err != nil {
		t.Fatal(err)
	}
	var copies []int64
	err := writeInputDigestRecord(&observed, input, func(path string, copied int64) {
		if path != input.path {
			t.Fatalf("observed path %q, expected %q", path, input.path)
		}
		copies = append(copies, copied)
	})
	if err != nil || ordinary.String() != observed.String() {
		t.Fatalf("observer changed digest record: err=%v ordinary=%q observed=%q", err, ordinary.String(), observed.String())
	}
	if input.sortKey == "file" || input.sortKey == "optional" {
		if len(copies) != 1 || copies[0] != size {
			t.Fatalf("copied bytes=%v, expected [%d]", copies, size)
		}
	} else if len(copies) != 0 {
		t.Fatalf("non-file input unexpectedly observed reads: %v", copies)
	}
}
func TestCacheInputObserverPreservesReadErrors(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{dir, filepath.Join(dir, "missing")} {
		plainDigest, plainErr := hashFileDigest(path)
		var copies []int64
		observedDigest, observedErr := hashFileDigest(path, func(_ string, copied int64) { copies = append(copies, copied) })
		if plainErr == nil || observedErr == nil || plainErr.Error() != observedErr.Error() || plainDigest != observedDigest {
			t.Fatalf("observer changed failing digest: ordinary=%v observed=%v", plainErr, observedErr)
		}
		if len(copies) != 0 {
			t.Fatalf("rejected file open must not report a copy: %v", copies)
		}
	}
}

type mavenCapRecordingAdapter struct {
	*jvmlang.Adapter
	captured     int
	lateCaptured bool
	warnings     []string
}

func (a *mavenCapRecordingAdapter) Analyse(ctx context.Context, req language.Request) (report.Report, error) {
	result, err := a.Adapter.Analyse(ctx, req)
	a.captured = len(result.MavenManifests)
	a.warnings = append([]string(nil), result.Warnings...)
	for _, entry := range result.MavenManifests {
		a.lateCaptured = a.lateCaptured || entry.Path() == "zzz/pom.xml"
	}
	return result, err
}
func TestMavenIdentityDiscoversPOMBeyondActualAdapterCandidateCap(t *testing.T) {
	repo := t.TempDir()
	writeMavenServiceFixture(t, repo, "")
	for i := 0; i < 2047; i++ {
		testutil.MustWriteFile(t, filepath.Join(repo, fmt.Sprintf("m%04d", i), "pom.xml"), "<project/>")
	}
	testutil.MustWriteFile(t, filepath.Join(repo, "zzz", "pom.xml"), `<project><dependencies><dependency><groupId>org.example</groupId><artifactId>widgets</artifactId><version>7.9.1</version></dependency></dependencies></project>`)
	adapter := &mavenCapRecordingAdapter{Adapter: jvmlang.NewAdapter()}
	registry := language.NewRegistry()
	if err := registry.Register(adapter); err != nil {
		t.Fatal(err)
	}
	reads, decodes := observeMavenIdentityIO(t)
	result, err := (&Service{Registry: registry}).Analyse(context.Background(), Request{RepoPath: repo, Language: "jvm", ScopeMode: ScopeModeRepo, Dependency: "widgets", Features: mustResolveDependencyIdentityPreviewFeatureSet(t), Cache: &CacheOptions{Enabled: false}})
	if err != nil {
		t.Fatal(err)
	}
	if adapter.captured != 2048 || adapter.lateCaptured || *reads != 1 || *decodes != 1 {
		t.Fatalf("cap recovery: captured=%d late=%v fallback reads/decode=%d/%d", adapter.captured, adapter.lateCaptured, *reads, *decodes)
	}
	if !strings.Contains(strings.Join(adapter.warnings, "\n"), "candidate files") {
		t.Fatalf("missing real adapter cap warning: %v", adapter.warnings)
	}
	assertMavenServiceIdentity(t, result, "7.9.1", "pom.xml")
	evidence := findIdentityDependency(t, result, "jvm", "widgets").Identity.Evidence
	if len(evidence) != 2 || evidence[0] != "pom.xml" || evidence[1] != "zzz/pom.xml" {
		t.Fatalf("captured plus recovered evidence: %v", evidence)
	}
}
