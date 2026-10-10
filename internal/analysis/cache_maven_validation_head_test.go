package analysis

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/report/model"
)

func TestMavenPreflightRejectsIncompleteAndMistypedValues(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		shape     mavenJSONShape
	}{
		{"missing-number", "", mavenNumberJSON},
		{"string-version", `"1"`, mavenNumberJSON},
		{"missing-string", "", mavenStringJSON},
		{"number-string", `1`, mavenStringJSON},
		{"truncated-string", `"value`, mavenStringJSON},
		{"missing-object", "", mavenEnvelopeJSON},
		{"unclosed-object", `{`, mavenEnvelopeJSON},
		{"missing-version-value", `{"version":`, mavenEnvelopeJSON},
		{"missing-policy-value", `{"version":1,"policy":`, mavenEnvelopeJSON},
		{"nonstring-policy", `{"version":1,"policy":1}`, mavenEnvelopeJSON},
		{"missing-entries", `{"version":1,"policy":"identity"}`, mavenEnvelopeJSON},
		{"missing-array", "", mavenEntriesJSON},
		{"nonarray-entries", `{}`, mavenEntriesJSON},
		{"unclosed-array", `[`, mavenEntriesJSON},
		{"invalid-array-value", `[false]`, mavenEntriesJSON},
		{"trailing-escape", `"value\`, mavenStringJSON},
		{"short-unicode", `"\u12"`, mavenStringJSON},
		{"invalid-unicode", `"\uxxxx"`, mavenStringJSON},
		{"lone-low-surrogate", `"\udc00"`, mavenStringJSON},
		{"unpaired-high-surrogate", `"\ud800x"`, mavenStringJSON},
		{"wrong-surrogate-pair", `"\ud800\u0041"`, mavenStringJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := preflightMavenJSON([]byte(tc.raw), tc.shape); err == nil {
				t.Fatalf("invalid input accepted: %q", tc.raw)
			}
		})
	}
	if err := preflightMavenJSON([]byte(`"\ud83d\ude00"`), mavenStringJSON); err != nil {
		t.Fatalf("valid surrogate pair rejected: %v", err)
	}
}

func TestMavenPreflightRejectsNextValueAtBudgetBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		remaining int
		parse     func(*mavenJSONPreflight) error
	}{
		{"array-separator", `["a","b"]`, 4, func(s *mavenJSONPreflight) error { return s.array(mavenStringJSON, 2) }},
		{"object-separator", `{"a":"b","c":"d"}`, 8, func(s *mavenJSONPreflight) error { return s.object(mavenPropertiesJSON) }},
		{"object-key", `{"a":"b"}`, 1, func(s *mavenJSONPreflight) error { return s.object(mavenPropertiesJSON) }},
		{"array-entry-count", `[{},{}]`, model.MavenEvidenceByteLimit, func(s *mavenJSONPreflight) error { return s.array(mavenPropertiesJSON, 1) }},
		{"record-declaration-count", `[{}]`, model.MavenEvidenceByteLimit, func(s *mavenJSONPreflight) error {
			s.recordDeclarations = model.MavenEvidenceRecordLimit
			return s.array(mavenDeclarationJSON, model.MavenEvidenceRecordLimit)
		}},
		{"aggregate-declaration-count", `[{}]`, model.MavenEvidenceByteLimit, func(s *mavenJSONPreflight) error {
			s.values = model.MavenEvidenceValueLimit
			return s.array(mavenDeclarationJSON, model.MavenEvidenceRecordLimit)
		}},
		{"aggregate-property-count", `{"v":"1"}`, model.MavenEvidenceByteLimit, func(s *mavenJSONPreflight) error {
			s.values = model.MavenEvidenceValueLimit
			return s.object(mavenPropertiesJSON)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Retain the already-consumed counter, without allocating an oversized cache object.
			scanner := mavenJSONPreflight{decoder: json.NewDecoder(strings.NewReader(tc.raw)), inEvidence: true, canonicalBytes: model.MavenEvidenceByteLimit - tc.remaining}
			scanner.decoder.UseNumber()
			if err := tc.parse(&scanner); !errors.Is(err, model.ErrMavenEvidenceLimit) {
				t.Fatalf("next value admission error = %v", err)
			}
			if scanner.canonicalBytes > model.MavenEvidenceByteLimit {
				t.Fatal("rejected input exceeded retained byte allowance")
			}
		})
	}
	scanner := mavenJSONPreflight{}
	if err := scanner.propertyCount(model.MavenEvidenceRecordLimit); !errors.Is(err, model.ErrMavenEvidenceLimit) || scanner.values != 0 {
		t.Fatalf("record property limit changed aggregate: values=%d error=%v", scanner.values, err)
	}
}

func TestMavenCachePointerRejectsMalformedAndInvalidDigests(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pointer.json")
	for _, raw := range []string{
		`{`,
		`{"inputDigest":"input","objectDigest":"short"}`,
		`{"inputDigest":"input","objectDigest":"` + strings.Repeat("G", 64) + `"}`,
	} {
		mustWriteFile(t, path, []byte(raw))
		if _, err := readMavenCachePointer(root, path); err == nil {
			t.Fatalf("invalid pointer accepted: %s", raw)
		}
	}
	if _, err := readMavenCachePointer(root, filepath.Join(root, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing pointer error = %v", err)
	}
}

func TestMavenCacheObjectFailureReasons(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ digest, reason string }{
		{"short", cacheObjectCorruptReason},
		{strings.Repeat("a", 64), "object-read-error"},
	} {
		payload, reason, err := readMavenCachedPayload(root, tc.digest)
		if err != nil || reason != tc.reason || payload.Report.MavenManifestCatalog {
			t.Fatalf("object rejection: reason=%q error=%v retained=%v", reason, err, payload.Report.MavenManifestCatalog)
		}
	}
	// The envelope passes preflight, but the ordinary report has the wrong JSON type.
	cache, entry := mavenCacheWithRaw(t, []byte(`{"maven":{"version":1,"policy":"identity","entries":[]},"report":[]}`))
	assertMavenCacheMiss(t, cache, entry)
}

func TestMavenRestoreRejectsInvalidBorrowedMetadata(t *testing.T) {
	for _, tc := range []struct{ name, path, stage, kind string }{
		{"encoding", string([]byte{0xff}), "", ""},
		{"path", "../pom.xml", "", ""},
		{"failure", "pom.xml", "read", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			documents := []mavenCacheDocument{{Path: tc.path, Stage: tc.stage, Kind: tc.kind}}
			envelope := mavenCacheEnvelope{Version: mavenCacheEnvelopeVersion, Policy: mavenCacheIdentityPolicy, Entries: &documents}
			if entries, err := envelope.restore(); err == nil || len(entries) != 0 {
				t.Fatalf("invalid restore retained %d entries: %v", len(entries), err)
			}
		})
	}
	if err := mavenCacheableReport(report.Report{}); err == nil {
		t.Fatal("report without evidence admitted to Maven cache")
	}
}

func TestMavenAccumulatorRejectsInvalidEvidenceWithoutMutation(t *testing.T) {
	root := t.TempDir()
	accumulator := newMavenEvidenceAccumulator(root)
	if err := accumulator.add(report.MavenManifest{}); err == nil || len(accumulator.entries) != 0 {
		t.Fatalf("uninitialised evidence admission = %v", err)
	}
	original := mavenCacheTestReport(t, "pom.xml")
	original.RepoPath = root
	if err := accumulator.accept(original); err != nil {
		t.Fatal(err)
	}
	beforeSize, beforeValues := accumulator.size, accumulator.values
	conflicting, err := model.NewMavenManifest("pom.xml", map[string]string{"v": "changed"}, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := accumulator.add(conflicting); err == nil {
		t.Fatal("conflicting evidence accepted")
	}
	outside := original
	outside.RepoPath = "relative"
	if err := accumulator.accept(outside); err == nil {
		t.Fatal("incompatible evidence root accepted")
	}
	retained := accumulator.entries["pom.xml"]
	if accumulator.size != beforeSize || accumulator.values != beforeValues || len(accumulator.entries) != 1 || !retained.Equal(original.MavenManifests[0]) {
		t.Fatal("rejected evidence changed retained state")
	}
}
