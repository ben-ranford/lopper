package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
)

func TestMavenCacheObjectReadExactAdmissionLimit(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			cache, entry := mavenCacheWithRaw(t, []byte(`{"maven":{"version":1,"policy":"identity","entries":[]},"report":{}}`))
			digest := writePaddedMavenCacheObject(t, cache.options.Path, mavenCacheObjectLimit+extra)
			writePointerJSON(t, filepath.Join(cache.options.Path, "keys", entry.KeyDigest+".json"), entry.InputDigest, digest)
			_, hit, err := cache.lookup(entry)
			if err != nil || hit != (extra == 0) {
				t.Fatalf("exact/+1 hit=%v err=%v", hit, err)
			}
		})
	}
}
func writePaddedMavenCacheObject(t *testing.T, root string, size int) string {
	t.Helper()
	file, err := os.CreateTemp(filepath.Join(root, "objects"), "bounded-")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	writer := io.MultiWriter(file, digest)
	prefix := `{"maven":{"version":1,"policy":"identity","entries":[]},"report":{}}`
	if _, err := io.WriteString(writer, prefix); err != nil {
		t.Fatal(err)
	}
	block := strings.Repeat(" ", 32<<10)
	for remaining := size - len(prefix); remaining > 0; {
		count := min(remaining, len(block))
		if _, err := io.WriteString(writer, block[:count]); err != nil {
			t.Fatal(err)
		}
		remaining -= count
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	value := hex.EncodeToString(digest.Sum(nil))
	if err := os.Rename(file.Name(), filepath.Join(root, "objects", value+".json")); err != nil {
		t.Fatal(err)
	}
	return value
}
func TestMavenAccumulatorExactAggregateBeforePublication(t *testing.T) {
	root := t.TempDir()
	entry, err := model.NewMavenManifest("pom.xml", nil, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	accumulator := newMavenEvidenceAccumulator(root)
	accumulator.size = model.MavenEvidenceByteLimit - entry.Size()
	if err := accumulator.add(entry); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.add(entry); err != nil || accumulator.size != model.MavenEvidenceByteLimit {
		t.Fatalf("duplicate size=%d err=%v", accumulator.size, err)
	}
	other, err := entry.WithPath("other/pom.xml")
	if err != nil {
		t.Fatal(err)
	}
	if err := accumulator.add(other); !errors.Is(err, model.ErrMavenEvidenceLimit) || len(accumulator.entries) != 1 {
		t.Fatalf("overflow appended: size=%d entries=%d err=%v", accumulator.size, len(accumulator.entries), err)
	}
}
func TestMavenAccumulatorCountAndValueBoundaries(t *testing.T) {
	entry, err := model.NewMavenManifest("pom.xml", map[string]string{"v": "1"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	accumulator := newMavenEvidenceAccumulator(t.TempDir())
	accumulator.values = model.MavenEvidenceValueLimit - 1
	if err := accumulator.add(entry); err != nil {
		t.Fatal(err)
	}
	next, err := entry.WithPath("next/pom.xml")
	if err != nil {
		t.Fatal(err)
	}
	if err := accumulator.add(next); !errors.Is(err, model.ErrMavenEvidenceLimit) {
		t.Fatalf("aggregate values+1: %v", err)
	}
	accumulator = newMavenEvidenceAccumulator(t.TempDir())
	for i := 0; i < model.MavenEvidenceEntryLimit; i++ {
		current, err := entry.WithPath(fmt.Sprintf("%d/pom.xml", i))
		if err != nil {
			t.Fatal(err)
		}
		if err := accumulator.add(current); err != nil {
			t.Fatal(err)
		}
	}
	if err := accumulator.add(next); !errors.Is(err, model.ErrMavenEvidenceLimit) {
		t.Fatalf("aggregate entries+1: %v", err)
	}
}
func TestMavenCacheEnvelopeEntryExactCount(t *testing.T) {
	documents := make([]mavenCacheDocument, model.MavenAdapterEntryLimit)
	envelope := &mavenCacheEnvelope{Version: 1, Policy: "identity", Entries: &documents}
	for i := range *envelope.Entries {
		(*envelope.Entries)[i] = mavenCacheDocument{Path: fmt.Sprintf("%d/pom.xml", i), Properties: map[string]string{}, Dependencies: []report.MavenDeclaration{}, Managed: []report.MavenDeclaration{}}
	}
	if _, err := envelope.restore(); err != nil {
		t.Fatal(err)
	}
	*envelope.Entries = append(*envelope.Entries, mavenCacheDocument{Path: "next/pom.xml"})
	if _, err := envelope.restore(); err == nil {
		t.Fatal("entry limit+1 restored")
	}
}

func TestMavenMultiRootLimitFailsBeforeOverflowingPublication(t *testing.T) {
	entries := make([]report.MavenManifest, 17)
	for i := range entries {
		entry, err := model.NewMavenManifest(fmt.Sprintf("%d/pom.xml", i), map[string]string{"unused": strings.Repeat("x", 2<<20)}, nil, nil, "", "")
		if err != nil {
			t.Fatal(err)
		}
		entries[i] = entry
	}
	for _, mode := range []string{"jvm", "auto", "all"} {
		t.Run(mode, func(t *testing.T) { assertMavenMultiRootLimit(t, mode, entries) })
	}
}
func assertMavenMultiRootLimit(t *testing.T, mode string, entries []report.MavenManifest) {
	t.Helper()
	repo := t.TempDir()
	roots := []string{filepath.Join(repo, "one"), filepath.Join(repo, "two")}
	for _, root := range roots {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	req := Request{RepoPath: repo, Language: mode, ScopeMode: ScopeModePackage, Cache: &CacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache")}}
	cache := newAnalysisCache(req, repo)
	adapter := &testServiceAdapter{id: "jvm", analyse: report.Report{MavenManifestCatalog: true, MavenManifests: entries}}
	candidate := language.Candidate{Adapter: adapter, Detection: language.Detection{Matched: true, Confidence: 100, Roots: roots}}
	sibling := language.Candidate{Adapter: &testServiceAdapter{id: "other"}, Detection: language.Detection{Matched: true, Confidence: 100, Roots: []string{repo}}}
	reports, _, _, err := (&Service{}).runCandidates(context.Background(), req, repo, []language.Candidate{sibling, candidate}, cache)
	if !errors.Is(err, model.ErrMavenEvidenceLimit) || len(reports) != 0 {
		t.Fatalf("partial success: reports=%v err=%v", reports, err)
	}
	// The sibling and first JVM root can publish; the overflowing second root cannot.
	if cache.metadata.Writes != 2 {
		t.Fatalf("publication count=%d want2", cache.metadata.Writes)
	}
}

func TestMavenEnvelopePreflightExactByteLimit(t *testing.T) {
	prefix := `{"version":1,"policy":"identity","entries":[{"path":"pom.xml","properties":{"x":"`
	suffix := `"},"dependencies":[],"managed":[],"stage":"","kind":""}]}`
	value := strings.Repeat("x", model.MavenEvidenceByteLimit-len(prefix)-len(suffix))
	for _, extra := range []string{"", "x"} {
		raw := []byte(prefix + value + extra + suffix)
		err := preflightMavenJSON(raw, mavenEnvelopeJSON)
		if (err == nil) != (len(extra) == 0) {
			t.Fatalf("envelope bytes=%d error=%v", len(raw), err)
		}
	}
}

func TestMavenCachePreflightCombinedDeclarationLimit(t *testing.T) {
	declaration := `{"groupId":"","artifactId":"","version":"","type":"","scope":""}`
	for _, extra := range []int{0, 1} {
		managed := strings.Repeat(declaration+",", model.MavenEvidenceRecordLimit-2+extra) + declaration
		raw := `{"path":"pom.xml","properties":{},"dependencies":[` + declaration + `],"managed":[` + managed + `],"stage":"","kind":""}`
		err := preflightMavenJSON([]byte(raw), mavenDocumentJSON)
		if (err == nil) != (extra == 0) {
			t.Fatalf("combined declaration count=%d err=%v", model.MavenEvidenceRecordLimit+extra, err)
		}
	}
}
