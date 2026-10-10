package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
)

const mavenTestPublicationBudget = 4096

func newMavenPublicationFixture(t *testing.T) (*analysisCache, cacheEntryDescriptor, Request, report.Report) {
	t.Helper()
	result := mavenCacheTestReport(t, "pom.xml")
	result.UsageIncomplete = true
	result.Dependencies[0].UsageIncomplete = true
	result.Dependencies[0].SuppressedUnusedImports = []report.ImportUse{{Name: "Hidden", Module: "example", Locations: []report.Location{{File: "Main.java", Line: 2}}}}
	req := Request{RepoPath: result.RepoPath, Language: "jvm", Dependency: "widgets", ScopeMode: ScopeModePackage, Cache: &CacheOptions{Enabled: true, Path: filepath.Join(t.TempDir(), "cache")}}
	cache := newAnalysisCache(req, result.RepoPath)
	entry, err := cache.prepareEntry(req, "jvm", result.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	return cache, entry, req, result
}

func mavenReportWithEncodedSize(t *testing.T, result report.Report, size int) (report.Report, []byte) {
	t.Helper()
	result.Warnings = []string{""}
	raw := mavenCacheRaw(t, result)
	if size < len(raw) {
		t.Fatalf("fixture overhead%d exceeds requested%d", len(raw), size)
	}
	result.Warnings[0] = strings.Repeat("x", size-len(raw))
	raw = mavenCacheRaw(t, result)
	if len(raw) != size {
		t.Fatalf("actual encoded%d want%d", len(raw), size)
	}
	return result, raw
}

func mavenPublishedFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[relative] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestMavenPublicationExactEncodedBoundary(t *testing.T) {
	for _, delta := range []int{-1, 0, 1} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			cache, entry, _, result := newMavenPublicationFixture(t)
			cache.mavenPublicationLimit = mavenTestPublicationBudget
			result, raw := mavenReportWithEncodedSize(t, result, mavenTestPublicationBudget+delta)
			before := mavenPublishedFiles(t, cache.options.Path)
			err := cache.store(entry, result)
			if delta > 0 {
				assertMavenPublicationRejected(t, cache, before, 0, err)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			assertMavenPublishedPayload(t, cache, entry, raw, result)
		})
	}
}

func assertMavenPublicationRejected(t *testing.T, cache *analysisCache, before map[string]string, writes int, err error) {
	t.Helper()
	if err == nil || errors.Is(err, model.ErrMavenEvidenceLimit) || !strings.Contains(err.Error(), "admission limit") {
		t.Fatalf("publication failure=%v", err)
	}
	if cache.metadata.Writes != writes || !reflect.DeepEqual(before, mavenPublishedFiles(t, cache.options.Path)) {
		t.Fatal("rejected JVM publication changed objects, pointers or Writes")
	}
}

func assertMavenPublishedPayload(t *testing.T, cache *analysisCache, entry cacheEntryDescriptor, raw []byte, result report.Report) {
	t.Helper()
	pointer, err := readMavenCachePointer(cache.options.Path, filepath.Join(cache.options.Path, "keys", entry.KeyDigest+".json"))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(filepath.Join(cache.options.Path, "objects", pointer.ObjectDigest+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if pointer.ObjectDigest != sha256Hex(raw) || pointer.InputDigest != entry.InputDigest || string(actual) != string(raw) || cache.metadata.Writes != 1 {
		t.Fatal("published bytes/digest/pointer/Writes mismatch")
	}
	restored, hit, err := cache.lookup(entry)
	if err != nil || !hit || !restored.UsageIncomplete || !restored.Dependencies[0].UsageIncomplete || !reflect.DeepEqual(restored.Dependencies[0].SuppressedUnusedImports, result.Dependencies[0].SuppressedUnusedImports) {
		t.Fatalf("private sidecars lost: hit=%v error=%v", hit, err)
	}
	if !restored.MavenManifests[0].Equal(result.MavenManifests[0]) {
		t.Fatal("published evidence changed")
	}
}

func TestMavenRejectedPublicationPreservesExistingEntry(t *testing.T) {
	cache, entry, _, result := newMavenPublicationFixture(t)
	cache.mavenPublicationLimit = mavenTestPublicationBudget
	if err := cache.store(entry, result); err != nil {
		t.Fatal(err)
	}
	before := mavenPublishedFiles(t, cache.options.Path)
	result, _ = mavenReportWithEncodedSize(t, result, mavenTestPublicationBudget+1)
	entry.InputDigest = "replacement-input"
	assertMavenPublicationRejected(t, cache, before, 1, cache.store(entry, result))
}

func TestMavenPublicationWarningPreservesCompleteLiveEvidence(t *testing.T) {
	cache, _, req, result := newMavenPublicationFixture(t)
	cache.mavenPublicationLimit = mavenTestPublicationBudget
	result, _ = mavenReportWithEncodedSize(t, result, mavenTestPublicationBudget+1)
	before := mavenPublishedFiles(t, cache.options.Path)
	current := runMavenPublicationCandidate(t, cache, req, result)
	if !reflect.DeepEqual(current, result) {
		t.Fatal("cache rejection changed complete live report or evidence")
	}
	warnings := cache.takeWarnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "admission limit") {
		t.Fatalf("cache warning=%v", warnings)
	}
	if cache.metadata.Writes != 0 || !reflect.DeepEqual(before, mavenPublishedFiles(t, cache.options.Path)) {
		t.Fatal("service published rejected root")
	}
	reads, decodes := observeMavenIdentityIO(t)
	annotateDependencyIdentities(result.RepoPath, &current)
	assertMavenServiceIdentity(t, current, "1", "pom.xml")
	if *reads != 0 || *decodes != 0 {
		t.Fatal("cache warning caused evidence reread/decode")
	}
}

func runMavenPublicationCandidate(t *testing.T, cache *analysisCache, req Request, result report.Report) report.Report {
	t.Helper()
	candidate := language.Candidate{Adapter: &testServiceAdapter{id: "jvm", analyse: result}}
	current, _, err := (&Service{}).runCandidateRoot(context.Background(), req, candidateRootScope{repoPath: result.RepoPath, root: result.RepoPath}, candidate, cache, newMavenEvidenceAccumulator(result.RepoPath))
	if err != nil {
		t.Fatalf("cache publication became analysis failure: %v", err)
	}
	return current
}

func TestMavenPublicationBudgetIsPrivateAndCannotIncrease(t *testing.T) {
	for _, limit := range []int{0, -1, mavenCacheObjectLimit, mavenCacheObjectLimit + 1} {
		cache := analysisCache{mavenPublicationLimit: limit}
		if cache.mavenPublicationBudget() != 128<<20 {
			t.Fatalf("limit%d changed production cap", limit)
		}
	}
	lowered, entry, _, result := newMavenPublicationFixture(t)
	lowered.mavenPublicationLimit = 1
	if err := lowered.store(entry, result); err == nil {
		t.Fatal("lowered instance admitted payload")
	}
	separate, separateEntry, _, separateResult := newMavenPublicationFixture(t)
	if err := separate.store(separateEntry, separateResult); err != nil {
		t.Fatal(err)
	}
	entry.AdapterID = "other"
	if err := lowered.store(entry, result); err != nil || lowered.metadata.Writes != 1 {
		t.Fatalf("JVM limit affected non-JVM: %v", err)
	}
}

func TestMavenPublicationPreservesExistingEncoding(t *testing.T) {
	cache, entry, _, result := newMavenPublicationFixture(t)
	result.Warnings = []string{"\"\\\n\t\x00<&>\u2028\u2029é" + string([]byte{0xff})}
	result.Dependencies[0].Codemod = &report.CodemodReport{Mode: "suggest", Suggestions: []report.CodemodSuggestion{{File: "what?\".java", Patch: "line\n<&>"}}}
	result.PythonManifests = []report.PythonManifestDocument{{Path: "pyproject.toml", Document: map[string]any{"value": "unchanged"}}}
	result.PythonManifestCatalog = true
	raw, err := json.Marshal(newCachedPayload(result))
	if err != nil {
		t.Fatal(err)
	}
	cache.mavenPublicationLimit = len(raw)
	if err := cache.store(entry, result); err != nil {
		t.Fatal(err)
	}
	assertMavenPublishedPayload(t, cache, entry, raw, result)
}

func TestMavenPublicationMarshalErrorIsNonfatal(t *testing.T) {
	cache, _, req, result := newMavenPublicationFixture(t)
	result.Dependencies[0].UsedPercent = math.Inf(1)
	before := mavenPublishedFiles(t, cache.options.Path)
	current := runMavenPublicationCandidate(t, cache, req, result)
	if !math.IsInf(current.Dependencies[0].UsedPercent, 1) || !current.MavenManifests[0].Equal(result.MavenManifests[0]) {
		t.Fatal("Marshal failure changed live result")
	}
	if warnings := cache.takeWarnings(); len(warnings) != 1 || !strings.Contains(warnings[0], "unsupported value") {
		t.Fatalf("Marshal warning=%v", warnings)
	}
	if cache.metadata.Writes != 0 || !reflect.DeepEqual(before, mavenPublishedFiles(t, cache.options.Path)) {
		t.Fatal("Marshal error published cache")
	}
}
