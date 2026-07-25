package baseline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

type testSnapshotReport struct {
	Value string `json:"value"`
}

type testSortedSnapshotItem struct {
	Group string
	Name  string
}

func sortedSnapshotGroup(item testSortedSnapshotItem) string {
	return item.Group
}

func sortedSnapshotName(item testSortedSnapshotItem) string {
	return item.Name
}

func decodeTestSnapshotReport(data []byte) (testSnapshotReport, error) {
	var report testSnapshotReport
	return report, json.Unmarshal(data, &report)
}

func normalizeTestSnapshotReport(report testSnapshotReport) testSnapshotReport {
	report.Value = strings.ToUpper(report.Value)
	return report
}

func normalizedSnapshotDecodeOptions() SnapshotDecodeOptions[testSnapshotReport] {
	return SnapshotDecodeOptions[testSnapshotReport]{
		DecodeLegacy: decodeTestSnapshotReport,
		Normalize:    normalizeTestSnapshotReport,
	}
}

func TestSaveSnapshotWritesEnvelopeWithUTCTimestamp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 12, 8, 30, 0, 0, time.FixedZone("AEST", 10*60*60))
	path, err := SaveSnapshot(t.TempDir(), " label:weekly ", now, testSnapshotReport{Value: "ok"}, nil, normalizeTestSnapshotReport)
	if err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}

	var snapshot Snapshot[testSnapshotReport]
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snapshot.BaselineSchemaVersion != SnapshotSchemaVersion {
		t.Fatalf("schema version = %q, want %q", snapshot.BaselineSchemaVersion, SnapshotSchemaVersion)
	}
	if snapshot.Key != "label:weekly" {
		t.Fatalf("key = %q, want %q", snapshot.Key, "label:weekly")
	}
	if snapshot.SavedAt.Location() != time.UTC || !snapshot.SavedAt.Equal(now.UTC()) {
		t.Fatalf("savedAt = %s, want %s", snapshot.SavedAt, now.UTC())
	}
	if snapshot.Report.Value != "OK" {
		t.Fatalf("report = %#v, want normalized value", snapshot.Report)
	}
}

func TestDecodeSnapshotSupportsEnvelopeAndLegacyFallback(t *testing.T) {
	t.Parallel()

	snapshotData := []byte(`{"baselineSchemaVersion":"1.0.0","key":" label:test ","savedAt":"2026-07-12T00:00:00Z","report":{"value":"snapshot"}}`)
	reportData, key, err := DecodeSnapshot(snapshotData, normalizedSnapshotDecodeOptions())
	if err != nil {
		t.Fatalf("DecodeSnapshot(snapshot) error = %v", err)
	}
	if key != "label:test" || reportData.Value != "SNAPSHOT" {
		t.Fatalf("unexpected snapshot decode result: key=%q report=%#v", key, reportData)
	}

	legacyData := []byte(`{"value":"legacy"}`)
	legacyReport, legacyKey, err := DecodeSnapshot(legacyData, normalizedSnapshotDecodeOptions())
	if err != nil {
		t.Fatalf("DecodeSnapshot(legacy) error = %v", err)
	}
	if legacyKey != "" || legacyReport.Value != "LEGACY" {
		t.Fatalf("unexpected legacy decode result: key=%q report=%#v", legacyKey, legacyReport)
	}
}

func TestDecodeSnapshotRejectsUnsupportedSchemaWithCustomError(t *testing.T) {
	t.Parallel()

	_, _, err := DecodeSnapshot([]byte(`{"baselineSchemaVersion":"9.9.9","key":"label:bad","report":{"value":"x"}}`), SnapshotDecodeOptions[testSnapshotReport]{
		DecodeLegacy: decodeTestSnapshotReport,
		UnsupportedSchema: func(version string) error {
			return fmt.Errorf("unsupported custom baseline schema version: %s", version)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported custom baseline schema version: 9.9.9") {
		t.Fatalf("expected custom unsupported schema error, got %v", err)
	}
}

func TestLoadSnapshotFileLoadsEnvelopeAndReturnsReadError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC)
	path, err := SaveSnapshot(dir, "label:weekly", now, testSnapshotReport{Value: "ok"}, nil, nil)
	if err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}

	got, key, err := LoadSnapshotFile(path, SnapshotDecodeOptions[testSnapshotReport]{})
	if err != nil {
		t.Fatalf("LoadSnapshotFile() error = %v", err)
	}
	if got.Value != "ok" || key != "label:weekly" {
		t.Fatalf("LoadSnapshotFile() = %#v, %q, want value and key", got, key)
	}

	got, key, err = LoadSnapshotFile(path+".missing", SnapshotDecodeOptions[testSnapshotReport]{})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadSnapshotFile() missing error = %v, want os.ErrNotExist", err)
	}
	if got != (testSnapshotReport{}) || key != "" {
		t.Fatalf("LoadSnapshotFile() missing = %#v, %q, want zero values", got, key)
	}
}

func TestDecodeSnapshotUsesDefaultLegacyAndSchemaErrors(t *testing.T) {
	t.Parallel()

	got, key, err := DecodeSnapshot([]byte(`{"value":"legacy"}`), SnapshotDecodeOptions[testSnapshotReport]{})
	if err != nil {
		t.Fatalf("DecodeSnapshot() legacy error = %v", err)
	}
	if got.Value != "legacy" || key != "" {
		t.Fatalf("DecodeSnapshot() legacy = %#v, %q, want legacy value and empty key", got, key)
	}

	_, _, err = DecodeSnapshot([]byte(`{`), SnapshotDecodeOptions[testSnapshotReport]{})
	if err == nil {
		t.Fatal("DecodeSnapshot() malformed legacy input returned nil error")
	}

	_, _, err = DecodeSnapshot([]byte(`{"baselineSchemaVersion":"9.9.9"}`), SnapshotDecodeOptions[testSnapshotReport]{})
	if err == nil || err.Error() != "unsupported baseline schema version: 9.9.9" {
		t.Fatalf("DecodeSnapshot() unsupported schema error = %v", err)
	}
}

func TestLoadStoreSnapshotReadsResolvedPathAndValidatesKey(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC)
	path, err := SaveSnapshot(dir, "label:weekly", now, testSnapshotReport{Value: "ok"}, nil, nil)
	if err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	decode := func(data []byte) (testSnapshotReport, string, error) {
		return DecodeSnapshot(data, SnapshotDecodeOptions[testSnapshotReport]{DecodeLegacy: decodeTestSnapshotReport})
	}
	validate := func(requestedKey, storedKey string) error {
		return ValidateSnapshotKey(requestedKey, storedKey, errors.New("snapshot key mismatch"))
	}
	reportData, key, resolvedPath, err := LoadStoreSnapshot(dir, " label:weekly ", MaxSnapshotBytes, decode, validate)
	if err != nil {
		t.Fatalf("LoadStoreSnapshot() error = %v", err)
	}
	if resolvedPath != path || key != "label:weekly" || reportData.Value != "ok" {
		t.Fatalf("unexpected keyed load result: path=%q key=%q report=%#v", resolvedPath, key, reportData)
	}
}

func TestLoadStoreSnapshotReturnsReadDecodeAndValidationErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC)
	savedPath, err := SaveSnapshot(dir, "label:weekly", now, testSnapshotReport{Value: "ok"}, nil, nil)
	if err != nil {
		t.Fatalf("SaveSnapshot() error = %v", err)
	}

	decodeCalled := false
	unexpectedDecode := func([]byte) (testSnapshotReport, string, error) {
		decodeCalled = true
		return testSnapshotReport{}, "", nil
	}
	got, key, path, err := LoadStoreSnapshot(dir, "label:missing", MaxSnapshotBytes, unexpectedDecode, nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadStoreSnapshot() read error = %v, want os.ErrNotExist", err)
	}
	if decodeCalled || got != (testSnapshotReport{}) || key != "" || path != ResolveSnapshotPath(dir, "label:missing") {
		t.Fatalf("LoadStoreSnapshot() read failure = %#v, %q, %q, decodeCalled=%t", got, key, path, decodeCalled)
	}

	decodeErr := errors.New("decode snapshot")
	failDecode := func([]byte) (testSnapshotReport, string, error) {
		return testSnapshotReport{}, "", decodeErr
	}
	got, key, path, err = LoadStoreSnapshot(dir, "label:weekly", MaxSnapshotBytes, failDecode, nil)
	if !errors.Is(err, decodeErr) {
		t.Fatalf("LoadStoreSnapshot() decode error = %v, want %v", err, decodeErr)
	}
	if got != (testSnapshotReport{}) || key != "" || path != savedPath {
		t.Fatalf("LoadStoreSnapshot() decode failure = %#v, %q, %q", got, key, path)
	}

	decode := func([]byte) (testSnapshotReport, string, error) {
		return testSnapshotReport{Value: "decoded"}, "label:stored", nil
	}
	got, key, path, err = LoadStoreSnapshot(dir, "label:weekly", MaxSnapshotBytes, decode, nil)
	if err != nil || got.Value != "decoded" || key != "label:stored" || path != savedPath {
		t.Fatalf("LoadStoreSnapshot() without validation = %#v, %q, %q, %v", got, key, path, err)
	}

	validateErr := errors.New("validate snapshot key")
	rejectKey := func(string, string) error {
		return validateErr
	}
	got, key, path, err = LoadStoreSnapshot(dir, "label:weekly", MaxSnapshotBytes, decode, rejectKey)
	if !errors.Is(err, validateErr) {
		t.Fatalf("LoadStoreSnapshot() validation error = %v, want %v", err, validateErr)
	}
	if got != (testSnapshotReport{}) || key != "label:stored" || path != savedPath {
		t.Fatalf("LoadStoreSnapshot() validation failure = %#v, %q, %q", got, key, path)
	}
}

func TestValidateSnapshotKeyWrapsCustomMismatchError(t *testing.T) {
	t.Parallel()

	errMarker := errors.New("snapshot key mismatch")
	err := ValidateSnapshotKey(" label:a ", "label:b", errMarker)
	if !errors.Is(err, errMarker) {
		t.Fatalf("expected wrapped mismatch error, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), `requested "label:a", stored "label:b"`) {
		t.Fatalf("expected detailed mismatch error, got %v", err)
	}
}

func TestSortedCopyByStringsReturnsEmptyResultWhenInputIsNil(t *testing.T) {
	t.Parallel()

	got := SortedCopyByStrings[testSortedSnapshotItem](nil, sortedSnapshotGroup, sortedSnapshotName)

	if len(got) != 0 {
		t.Fatalf("SortedCopyByStrings(nil) length = %d, want 0", len(got))
	}
}

func TestSortedCopyByStringsReturnsEmptyResultWhenInputIsEmpty(t *testing.T) {
	t.Parallel()

	got := SortedCopyByStrings([]testSortedSnapshotItem{}, sortedSnapshotGroup, sortedSnapshotName)

	if len(got) != 0 {
		t.Fatalf("SortedCopyByStrings(empty) length = %d, want 0", len(got))
	}
}

func TestSortedCopyByStringsOrdersItemsByPrimaryKey(t *testing.T) {
	t.Parallel()

	items := []testSortedSnapshotItem{
		{Group: "charlie", Name: "three"},
		{Group: "alpha", Name: "one"},
		{Group: "bravo", Name: "two"},
	}

	got := SortedCopyByStrings(items, sortedSnapshotGroup, sortedSnapshotName)

	want := []testSortedSnapshotItem{
		{Group: "alpha", Name: "one"},
		{Group: "bravo", Name: "two"},
		{Group: "charlie", Name: "three"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SortedCopyByStrings() = %#v, want %#v", got, want)
	}
}

func TestSortedCopyByStringsUsesSecondaryKeyToBreakPrimaryTies(t *testing.T) {
	t.Parallel()

	items := []testSortedSnapshotItem{
		{Group: "alpha", Name: "gamma"},
		{Group: "alpha", Name: "beta"},
		{Group: "alpha", Name: "alpha"},
	}

	got := SortedCopyByStrings(items, sortedSnapshotGroup, sortedSnapshotName)

	want := []testSortedSnapshotItem{
		{Group: "alpha", Name: "alpha"},
		{Group: "alpha", Name: "beta"},
		{Group: "alpha", Name: "gamma"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SortedCopyByStrings() = %#v, want %#v", got, want)
	}
}

func TestSortedCopyByStringsDoesNotMutateInputSlice(t *testing.T) {
	t.Parallel()

	items := []testSortedSnapshotItem{
		{Group: "bravo", Name: "two"},
		{Group: "alpha", Name: "one"},
	}
	original := append([]testSortedSnapshotItem(nil), items...)

	got := SortedCopyByStrings(items, sortedSnapshotGroup, sortedSnapshotName)

	if !slices.Equal(items, original) {
		t.Fatalf("input mutated: got %#v, want %#v", items, original)
	}
	if len(got) != len(items) {
		t.Fatalf("sorted copy length = %d, want %d", len(got), len(items))
	}
	if len(got) > 0 && &got[0] == &items[0] {
		t.Fatal("SortedCopyByStrings() reused input backing array")
	}
}
