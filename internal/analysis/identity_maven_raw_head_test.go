//go:build linux

package analysis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jvmlang "github.com/ben-ranford/lopper/internal/lang/jvm"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

// Mandatory native Linux proof: setup and byte mismatches fail, never skip.
func TestMavenRawFilenameLiveReuseAndCacheAdmission(t *testing.T) {
	root := t.TempDir()
	adapterRoot := filepath.Join(root, "scope")
	name := string([]byte{'m', 0xff})
	repo := createMavenRawFixture(t, adapterRoot, name)
	result := analyseMavenRawFixture(t, adapterRoot)
	if len(result.MavenManifests) != 1 || result.MavenManifests[0].Path() != name+"/pom.xml" {
		t.Fatalf("raw capture=%#v", result.MavenManifests)
	}
	assertMavenRawCacheMiss(t, result, adapterRoot)
	writeMavenServiceFixture(t, repo, "2.0.0")
	recomputed := analyseMavenRawFixture(t, adapterRoot)
	if err := os.Remove(filepath.Join(repo, "pom.xml")); err != nil {
		t.Fatal(err)
	}
	reads, decodes := observeMavenIdentityIO(t)
	assertMavenRawRebase(t, root, "scope/"+name+"/pom.xml", "1.2.3", result)
	assertMavenRawRebase(t, root, "scope/"+name+"/pom.xml", "2.0.0", recomputed)
	if *reads != 0 || *decodes != 0 {
		t.Fatalf("captured raw path reread: %d/%d", *reads, *decodes)
	}
}

func createMavenRawFixture(t *testing.T, root, name string) string {
	t.Helper()
	repo := filepath.Join(root, name)
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	children, err := os.ReadDir(root)
	if err != nil || len(children) != 1 || children[0].Name() != name {
		t.Fatalf("raw filename roundtrip: %v %v", children, err)
	}
	writeMavenServiceFixture(t, repo, "1.2.3")
	return repo
}

func analyseMavenRawFixture(t *testing.T, root string) report.Report {
	t.Helper()
	result, err := jvmlang.NewAdapter().Analyse(context.Background(), language.Request{RepoPath: root, Dependency: "widgets"})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertMavenRawCacheMiss(t *testing.T, result report.Report, root string) {
	t.Helper()
	cache, entry := cacheWithPayloadForLookupTest(t, newCachedPayload(report.Report{}), "unused")
	entry.AdapterID = "jvm"
	entry.RootPath = root
	if err := os.Remove(filepath.Join(cache.options.Path, "keys", entry.KeyDigest+".json")); err != nil {
		t.Fatal(err)
	}
	storeCachedReport(cache, "jvm", root, entry, result)
	if warnings := cache.takeWarnings(); len(warnings) != 1 || !strings.Contains(warnings[0], "filename encoding") {
		t.Fatalf("cache warning=%v", warnings)
	}
	if _, hit, err := cache.lookup(entry); err != nil || hit || cache.metadata.Writes != 0 || cache.metadata.Misses != 1 {
		t.Fatalf("unsupported cache hit=%v err=%v metadata=%+v", hit, err, cache.metadata)
	}
}

func assertMavenRawRebase(t *testing.T, root, source, version string, result report.Report) {
	t.Helper()
	documents, _, err := mergedMavenEvidence(root, []report.Report{result})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].Path() != source {
		t.Fatalf("raw rebase=%v", documents)
	}
	result.MavenManifests = documents
	annotateDependencyIdentities(root, &result)
	assertMavenServiceIdentity(t, result, version, source)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var public report.Report
	if err := json.Unmarshal(encoded, &public); err != nil {
		t.Fatal(err)
	}
	expected := strings.ToValidUTF8(source, "\ufffd")
	if got := findIdentityDependency(t, public, "jvm", "widgets").Identity.Source; got != expected {
		t.Fatalf("public replacement semantics: got %q want %q", got, expected)
	}
}
