package analysis

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
)

func mavenCacheTestReport(t *testing.T, path string) report.Report {
	t.Helper()
	entry, err := model.NewMavenManifest(path, map[string]string{"v": "1"}, []report.MavenDeclaration{{GroupID: "example", ArtifactID: "widgets", Version: "${v}"}}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return report.Report{RepoPath: t.TempDir(), MavenManifestCatalog: true, MavenManifests: []report.MavenManifest{entry}, Dependencies: []report.DependencyReport{{Language: "jvm", Name: "widgets"}}}
}
func mavenCacheRaw(t *testing.T, result report.Report) []byte {
	t.Helper()
	raw, err := json.Marshal(newCachedPayload(result))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func mavenCacheWithRaw(t *testing.T, raw []byte) (*analysisCache, cacheEntryDescriptor) {
	t.Helper()
	result := mavenCacheTestReport(t, "pom.xml")
	cache, entry := cacheWithCapturedMavenReport(t, result)
	digest := sha256Hex(raw)
	mustWriteFile(t, filepath.Join(cache.options.Path, "objects", digest+".json"), raw)
	writePointerJSON(t, filepath.Join(cache.options.Path, "keys", entry.KeyDigest+".json"), entry.InputDigest, digest)
	return cache, entry
}
func TestMavenCacheEnvelopeCanonicalAccounting(t *testing.T) {
	result := mavenCacheTestReport(t, "space <&>/pom.xml")
	raw, err := json.Marshal(newMavenCacheEnvelope(result))
	if err != nil {
		t.Fatal(err)
	}
	size, err := model.MavenEvidenceSize(result.MavenManifests)
	if err != nil || size != len(raw) {
		t.Fatalf("size=%d actual=%d err=%v\n%s", size, len(raw), err, raw)
	}
}
func TestMavenCacheStrictEnvelopeRejection(t *testing.T) {
	raw := string(mavenCacheRaw(t, mavenCacheTestReport(t, "pom.xml")))
	cases := map[string]string{
		"version":             strings.Replace(raw, `"version":1`, `"version":2`, 1),
		"policy":              strings.Replace(raw, `"policy":"identity"`, `"policy":"inventory"`, 1),
		"unknown":             strings.Replace(raw, `"policy":"identity"`, `"policy":"identity","extra":1`, 1),
		"duplicate":           strings.Replace(raw, `"version":1`, `"version":1,"version":1`, 1),
		"case-alias":          strings.Replace(raw, `"version":1`, `"Version":1`, 1),
		"property-duplicate":  strings.Replace(raw, `"v":"1"`, `"v":"1","v":"2"`, 1),
		"type":                strings.Replace(raw, `"properties":{"v":"1"}`, `"properties":[]`, 1),
		"escape":              strings.Replace(raw, `"path":"pom.xml"`, `"path":"../pom.xml"`, 1),
		"dot-segment":         strings.Replace(raw, `"path":"pom.xml"`, `"path":"a/./pom.xml"`, 1),
		"parent-segment":      strings.Replace(raw, `"path":"pom.xml"`, `"path":"a/../pom.xml"`, 1),
		"empty-segment":       strings.Replace(raw, `"path":"pom.xml"`, `"path":"a//pom.xml"`, 1),
		"invalid-utf8":        strings.Replace(raw, `"path":"pom.xml"`, `"path":"`+string([]byte{0xff})+`/pom.xml"`, 1),
		"missing-fixed-field": strings.Replace(raw, `"kind":""`, `"other":""`, 1),
		"null-entries":        strings.Replace(raw, `"entries":[`, `"entries":null,"discarded":[`, 1),

		"absolute":  strings.Replace(raw, `"path":"pom.xml"`, `"path":"/pom.xml"`, 1),
		"nul":       strings.Replace(raw, `"path":"pom.xml"`, `"path":"a\u0000/pom.xml"`, 1),
		"surrogate": strings.Replace(raw, `"path":"pom.xml"`, `"path":"a\ud800/pom.xml"`, 1),
		"failure":   strings.Replace(raw, `"stage":"","kind":""`, `"stage":"parse","kind":"xml"`, 1),
		"trailing":  raw + `{}`,
		"missing":   `{"report":{}}`,
		"null":      `{"maven":null,"report":{}}`,
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			cache, entry := mavenCacheWithRaw(t, []byte(value))
			assertMavenCacheMiss(t, cache, entry)
		})
	}
}
func assertMavenCacheMiss(t *testing.T, cache *analysisCache, entry cacheEntryDescriptor) {
	t.Helper()
	_, hit, err := cache.lookup(entry)
	if err != nil || hit || cache.metadata.Hits != 0 || cache.metadata.Misses != 1 {
		t.Fatalf("invalid cache hit=%v err=%v metadata=%+v", hit, err, cache.metadata)
	}
}
func TestMavenCacheEmptyAndCurrentRootBinding(t *testing.T) {
	result := report.Report{RepoPath: "/untrusted/old/root", MavenManifestCatalog: true}
	cache, entry := mavenCacheWithRaw(t, mavenCacheRaw(t, result))
	cached, hit, err := cache.lookup(entry)
	if err != nil || !hit || !cached.MavenManifestCatalog || cached.RepoPath != entry.RootPath || len(cached.MavenManifests) != 0 {
		t.Fatalf("empty/root hit=%v err=%v result=%#v", hit, err, cached)
	}
}
func TestMavenCacheObjectDigestBinding(t *testing.T) {
	cache, entry := mavenCacheWithRaw(t, mavenCacheRaw(t, mavenCacheTestReport(t, "pom.xml")))
	pointerPath := filepath.Join(cache.options.Path, "keys", entry.KeyDigest+".json")
	pointer, err := readMavenCachePointer(cache.options.Path, pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache.options.Path, "objects", pointer.ObjectDigest+".json"), []byte(`{"maven":{"version":1,"policy":"identity","entries":[]},"report":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	assertMavenCacheMiss(t, cache, entry)
}
func TestMavenCachePointerExactLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pointer.json")
	base := `{"inputDigest":"input","objectDigest":"` + strings.Repeat("a", 64) + `"}`
	for _, size := range []int{mavenCachePointerLimit, mavenCachePointerLimit + 1} {
		mustWriteFile(t, path, []byte(base+strings.Repeat(" ", size-len(base))))
		_, err := readMavenCachePointer(root, path)
		if (err == nil) != (size == mavenCachePointerLimit) {
			t.Fatalf("size%d err%v", size, err)
		}
	}
}
func TestMavenCachePropertyCaseAndReplacementCharacter(t *testing.T) {
	result := mavenCacheTestReport(t, "replacement-\ufffd/pom.xml")
	raw := strings.Replace(string(mavenCacheRaw(t, result)), `"v":"1"`, `"v":"1","V":"2"`, 1)
	cache, entry := mavenCacheWithRaw(t, []byte(raw))
	cached, hit, err := cache.lookup(entry)
	if err != nil || !hit {
		t.Fatalf("valid property case/cache path: %v %v", hit, err)
	}
	if cached.MavenManifests[0].Properties()["V"] != "2" || cached.MavenManifests[0].Path() != result.MavenManifests[0].Path() {
		t.Fatal("case/path changed")
	}
}

func TestMavenEnvelopeAccountingMatchesActiveJSONEncoding(t *testing.T) {
	for _, value := range []string{"", "plain", "\"\\\b\f\n\r\t", "\x00\x01\x1f", "<&>\u2028\u2029", "é日本語", "\ufffd", string([]byte{0xff, 0xfe}), "before" + string([]byte{0xff}) + "after"} {
		t.Run(value, func(t *testing.T) {
			entry, err := model.NewMavenManifest("pom.xml", map[string]string{value: value}, []report.MavenDeclaration{{GroupID: value, ArtifactID: value, Version: value, Type: value, Scope: value}}, nil, "", "")
			if err != nil {
				t.Fatal(err)
			}
			result := report.Report{MavenManifestCatalog: true, MavenManifests: []report.MavenManifest{entry}}
			raw, err := json.Marshal(newMavenCacheEnvelope(result))
			if err != nil {
				t.Fatal(err)
			}
			size, err := model.MavenEvidenceSize(result.MavenManifests)
			if err != nil || size != len(raw) {
				t.Fatalf("size=%d actual=%d err=%v", size, len(raw), err)
			}
		})
	}
}

func TestMavenCacheRestoreOwnershipAndDuplicatePolicy(t *testing.T) {
	result := mavenCacheTestReport(t, "pom.xml")
	envelope := newMavenCacheEnvelope(result)
	*envelope.Entries = append(*envelope.Entries, (*envelope.Entries)[0])
	restored, err := envelope.restore()
	if err != nil || len(restored) != 1 {
		t.Fatalf("identical duplicate: %v %v", restored, err)
	}
	(*envelope.Entries)[0].Properties["v"] = "changed"
	(*envelope.Entries)[0].Dependencies[0].Version = "changed"
	if restored[0].Properties()["v"] != "1" || restored[0].Dependencies()[0].Version != "${v}" {
		t.Fatal("restored evidence aliases cache DTO")
	}
	(*envelope.Entries)[1] = (*newMavenCacheEnvelope(result).Entries)[0]
	if _, err := envelope.restore(); err == nil {
		t.Fatal("conflicting duplicate restored")
	}
	(*envelope.Entries)[0] = (*newMavenCacheEnvelope(result).Entries)[0]
	(*envelope.Entries)[0].Path = "A/pom.xml"
	(*envelope.Entries)[1].Path = "a/pom.xml"
	restored, err = envelope.restore()
	if err != nil || len(restored) != 2 || restored[0].Path() != "A/pom.xml" || restored[1].Path() != "a/pom.xml" {
		t.Fatalf("case-preserving distinct paths: %v %v", restored, err)
	}
}

func TestMavenPreflightCanonicalEscapingAtLimit(t *testing.T) {
	prefix := `{"version":1,"policy":"identity","entries":[{"path":"pom.xml","properties":{"x":"`
	suffix := `"},"dependencies":[],"managed":[],"stage":"","kind":""}]}`
	remaining := model.MavenEvidenceByteLimit - len(prefix) - len(suffix)
	unit := "<>&\u2028\u2029"
	value := strings.Repeat(unit, remaining/30) + strings.Repeat("x", remaining%30)
	for _, extra := range []string{"", "x"} {
		encoded, err := json.Marshal(value + extra)
		if err != nil {
			t.Fatal(err)
		}
		if len(prefix)+len(encoded)-2+len(suffix) != model.MavenEvidenceByteLimit+len(extra) {
			t.Fatal("canonical byte oracle differs")
		}
		for _, spelling := range []string{value + extra, string(encoded[1 : len(encoded)-1])} {
			err := preflightMavenJSON([]byte(prefix+spelling+suffix), mavenEnvelopeJSON)
			if (err == nil) != (extra == "") {
				t.Fatalf("canonical limit+%d, input bytes=%d: %v", len(extra), len(prefix)+len(spelling)+len(suffix), err)
			}
		}
	}
}

func TestMavenPreflightRejectsSemanticStatesBeforeExpansion(t *testing.T) {
	for _, fields := range []string{
		`"version":2,"policy":"identity","entries":[]`,
		`"version":1,"policy":"inventory","entries":[]`,
		`"entries":[],"policy":"identity","version":2`,
	} {
		if err := preflightMavenJSON([]byte("{"+fields+"}"), mavenEnvelopeJSON); err == nil {
			t.Errorf("semantic envelope accepted: %s", fields)
		}
	}
	for _, tc := range []struct{ path, stage, kind, properties string }{
		{"../pom.xml", "", "", `{}`},
		{"pom.xml", "parse", "xml", `{"x":"1"}`},
		{"pom.xml", "read", "unknown", `{}`},
		{"pom.xml", "", "xml", `{}`},
	} {
		metadata := `"path":"` + tc.path + `","stage":"` + tc.stage + `","kind":"` + tc.kind + `"`
		contents := `"properties":` + tc.properties + `,"dependencies":[],"managed":[]`
		for _, fields := range []string{metadata + "," + contents, contents + "," + metadata} {
			if err := preflightMavenJSON([]byte("{"+fields+"}"), mavenDocumentJSON); err == nil {
				t.Errorf("semantic record accepted: %s", fields)
			}
		}
	}
}

func TestMavenPreflightCanonicalTokenAccounting(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"policy":"identity","entries":[{"path":"pom.xml","properties":{"<&":"\u003c\u003e\u0026\u2028\u2029\ud83d\ude00"},"dependencies":[{"groupId":"a","artifactId":"b","version":"<1>","type":"","scope":""}],"managed":[],"stage":"","kind":""}]}`,
		`{"entries":[{"kind":"","stage":"","managed":[],"dependencies":[],"properties":{"literal":"é😀","escapes":"\t\n\\\"/"},"path":"a/pom.xml"},{"path":"b/pom.xml","properties":{},"dependencies":[],"managed":[],"stage":"read","kind":"missing"}],"policy":"identity","version":1}`,
	} {
		var envelope mavenCacheEnvelope
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatal(err)
		}
		canonical, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		scanner := mavenJSONPreflight{decoder: json.NewDecoder(strings.NewReader(raw))}
		scanner.decoder.UseNumber()
		if err := scanner.value(mavenEnvelopeJSON); err != nil {
			t.Fatal(err)
		}
		if scanner.canonicalBytes != len(canonical) {
			t.Fatalf("canonical bytes=%d actual=%d", scanner.canonicalBytes, len(canonical))
		}
	}
}

func TestMavenPreflightSemanticFailureCombinations(t *testing.T) {
	for _, state := range []struct {
		stage, kind string
		valid       bool
	}{
		{"", "", true}, {"read", "permission", true}, {"read", "missing", true}, {"read", "large", true}, {"read", "io", true}, {"parse", "xml", true}, {"parse", "io", false}, {"read", "xml", false}, {"unknown", "io", false}, {"", "missing", false},
	} {
		for _, nonempty := range []bool{false, true} {
			declarations := `[]`
			if nonempty {
				declarations = `[{"groupId":"a","artifactId":"b","version":"1","type":"","scope":""}]`
			}
			stateFields := `"path":"pom.xml","stage":"` + state.stage + `","kind":"` + state.kind + `"`
			contentFields := `"properties":{},"dependencies":[],"managed":` + declarations
			for _, fields := range []string{stateFields + "," + contentFields, contentFields + "," + stateFields} {
				err := preflightMavenJSON([]byte("{"+fields+"}"), mavenDocumentJSON)
				valid := state.valid && (state.stage == "" || !nonempty)
				if (err == nil) != valid {
					t.Fatalf("state %s/%s nonempty=%v error=%v", state.stage, state.kind, nonempty, err)
				}
			}
		}
	}
}

func mavenRepresentationReports(t *testing.T) []report.Report {
	t.Helper()
	entry, err := model.NewMavenManifest("quote-\"<&>/pom.xml", map[string]string{"v": "1\n<&>\u2028"}, []report.MavenDeclaration{{GroupID: "example", ArtifactID: "widgets", Version: "${v}"}}, []report.MavenDeclaration{{ArtifactID: "managed", Version: "2"}}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	failure, err := model.NewMavenManifest("broken/pom.xml", nil, nil, nil, "parse", "xml")
	if err != nil {
		t.Fatal(err)
	}
	rich := report.Report{RepoPath: "/wire/root", MavenManifestCatalog: true, MavenManifests: []report.MavenManifest{entry}, UsageIncomplete: true, Dependencies: []report.DependencyReport{{Language: "jvm", Name: "widgets", UsageIncomplete: true, SuppressedUnusedImports: []report.ImportUse{{Name: "Hidden", Module: "example", Locations: []report.Location{{File: "Main.java", Line: 2}}}}}}}
	return []report.Report{rich, {RepoPath: "/wire/root", MavenManifestCatalog: true}, {RepoPath: "/wire/root", MavenManifestCatalog: true, MavenManifests: []report.MavenManifest{failure}}}
}

func TestMavenRepresentationPreservesArchivedWireBytes(t *testing.T) {
	want := []string{
		`{"maven":{"version":1,"policy":"identity","entries":[{"path":"quote-\"\u003c\u0026\u003e/pom.xml","properties":{"v":"1\n\u003c\u0026\u003e\u2028"},"dependencies":[{"groupId":"example","artifactId":"widgets","version":"${v}","type":"","scope":""}],"managed":[{"groupId":"","artifactId":"managed","version":"2","type":"","scope":""}],"stage":"","kind":""}]},"report":{"schemaVersion":"","generatedAt":"0001-01-01T00:00:00Z","repoPath":"/wire/root","dependencies":[{"language":"jvm","name":"widgets","usedExportsCount":0,"totalExportsCount":0,"usedPercent":0,"estimatedUnusedBytes":0}]},"usageIncompleteReport":true,"usageIncompleteDependencies":[0],"suppressedUnusedImportsByDependency":{"0":[{"name":"Hidden","module":"example","locations":[{"file":"Main.java","line":2,"column":0}]}]}}`,
		`{"maven":{"version":1,"policy":"identity","entries":[]},"report":{"schemaVersion":"","generatedAt":"0001-01-01T00:00:00Z","repoPath":"/wire/root","dependencies":null}}`,
		`{"maven":{"version":1,"policy":"identity","entries":[{"path":"broken/pom.xml","properties":{},"dependencies":[],"managed":[],"stage":"parse","kind":"xml"}]},"report":{"schemaVersion":"","generatedAt":"0001-01-01T00:00:00Z","repoPath":"/wire/root","dependencies":null}}`,
	}
	for i, result := range mavenRepresentationReports(t) {
		raw := mavenCacheRaw(t, result)
		if string(raw) != want[i] {
			t.Fatalf("wire case%d changed\ngot %s\nwant %s", i, raw, want[i])
		}
		cache, entry := mavenCacheWithRaw(t, []byte(want[i]))
		restored, hit, err := cache.lookup(entry)
		if err != nil || !hit || !restored.MavenManifestCatalog || restored.RepoPath != entry.RootPath {
			t.Fatalf("old wire case%d hit=%v err=%v report=%#v", i, hit, err, restored)
		}
	}
}

func TestMavenOptionalArrayFreshDecodeAndLookup(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		valid       bool
	}{
		{"absent", "", false}, {"null", `,"entries":null`, false}, {"empty", `,"entries":[]`, true}, {"object", `,"entries":{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertMavenOptionalArray(t, tc.field, tc.valid)
		})
	}
}
func TestMavenOptionalArrayRealServiceHitAccounting(t *testing.T) {
	for _, field := range []string{"absent", "null", "empty", "object", "legacy"} {
		t.Run(field, func(t *testing.T) {
			service, request := mavenServiceWithOptionalArray(t, field)
			result, err := service.Analyse(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Cache == nil {
				t.Fatal("missing cache metadata")
			}
			wantHits, wantMisses := 0, 1
			if field == "empty" {
				wantHits, wantMisses = 1, 0
			}
			if result.Cache.Hits != wantHits || result.Cache.Misses != wantMisses || result.RepoPath != request.RepoPath {
				t.Fatalf("actual service %s: cache=%#v root=%q", field, result.Cache, result.RepoPath)
			}
		})
	}
}
func mavenServiceWithOptionalArray(t *testing.T, field string) (*Service, Request) {
	t.Helper()
	repo := t.TempDir()
	writeMavenServiceFixture(t, repo, "1.2.3")
	service := mavenTestService(t)
	request := newCacheRequest(t, repo, filepath.Join(t.TempDir(), "cache"), false)
	request.Language = "jvm"
	request.Dependency = "widgets"
	cold, err := service.Analyse(context.Background(), request)
	if err != nil || cold.Cache == nil || cold.Cache.Writes != 1 {
		t.Fatalf("cold service: %#v %v", cold.Cache, err)
	}
	keys, err := os.ReadDir(filepath.Join(request.Cache.Path, "keys"))
	if err != nil || len(keys) != 1 {
		t.Fatalf("cold keys=%v error=%v", keys, err)
	}
	pointerPath := filepath.Join(request.Cache.Path, "keys", keys[0].Name())
	pointer, err := readMavenCachePointer(request.Cache.Path, pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(request.Cache.Path, "objects", pointer.ObjectDigest+".json"))
	if err != nil {
		t.Fatal(err)
	}
	raw = mavenPayloadWithOptionalArray(t, raw, field)
	digest := sha256Hex(raw)
	mustWriteFile(t, filepath.Join(request.Cache.Path, "objects", digest+".json"), raw)
	writePointerJSON(t, pointerPath, pointer.InputDigest, digest)
	return service, request
}
func mavenPayloadWithOptionalArray(t *testing.T, raw []byte, field string) []byte {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{"absent": "", "null": `,"entries":null`, "empty": `,"entries":[]`, "object": `,"entries":{}`}
	payload["maven"] = json.RawMessage(`{"version":1,"policy":"identity"` + fields[field] + `}`)
	if field == "legacy" {
		delete(payload, "maven")
	}
	result, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertMavenOptionalArray(t *testing.T, field string, valid bool) {
	t.Helper()
	raw := []byte(`{"version":1,"policy":"identity"` + field + `}`)
	var envelope mavenCacheEnvelope
	err := json.Unmarshal(raw, &envelope)
	if err == nil {
		_, err = envelope.restore()
	}
	if (err == nil) != valid {
		t.Fatalf("fresh DTO validity=%v error=%v", valid, err)
	}
	cache, entry := mavenCacheWithRaw(t, append(append([]byte(`{"report":{},"maven":`), raw...), '}'))
	result, hit, err := cache.lookup(entry)
	if err != nil || hit != valid {
		t.Fatalf("lookup hit=%v error=%v", hit, err)
	}
	if !valid {
		if cache.metadata.Hits != 0 || cache.metadata.Misses != 1 {
			t.Fatalf("miss counters: %#v", cache.metadata)
		}
		return
	}
	if cache.metadata.Hits != 1 || cache.metadata.Misses != 0 || !result.MavenManifestCatalog || len(result.MavenManifests) != 0 || result.RepoPath != entry.RootPath {
		t.Fatalf("empty hit lost catalog/root/counters: %#v %#v", result, cache.metadata)
	}
}
