package baseline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type testSnapshotReport struct {
	Value string `json:"value"`
}

type testSortedSnapshotItem struct {
	Group string
	Name  string
}

var (
	testSortedSnapshotItemsUnorderedByPrimary = []testSortedSnapshotItem{
		{Group: "charlie", Name: "three"},
		{Group: "alpha", Name: "one"},
		{Group: "bravo", Name: "two"},
	}
	testSortedSnapshotItemsOrderedByPrimary = []testSortedSnapshotItem{
		{Group: "alpha", Name: "one"},
		{Group: "bravo", Name: "two"},
		{Group: "charlie", Name: "three"},
	}
	testSortedSnapshotItemsTiedOnPrimary = []testSortedSnapshotItem{
		{Group: "alpha", Name: "gamma"},
		{Group: "alpha", Name: "beta"},
		{Group: "alpha", Name: "alpha"},
	}
	testSortedSnapshotItemsOrderedBySecondary = []testSortedSnapshotItem{
		{Group: "alpha", Name: "alpha"},
		{Group: "alpha", Name: "beta"},
		{Group: "alpha", Name: "gamma"},
	}
)

func sortedSnapshotGroup(item testSortedSnapshotItem) string {
	return item.Group
}

func sortedSnapshotName(item testSortedSnapshotItem) string {
	return item.Name
}

func assertSortedCopyByStringsReturnsEmptyResult(t *testing.T, name string, items []testSortedSnapshotItem) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		t.Parallel()

		got := SortedCopyByStrings(items, sortedSnapshotGroup, sortedSnapshotName)

		if len(got) != 0 {
			t.Fatalf("SortedCopyByStrings(%s) length = %d, want 0", name, len(got))
		}
	})
}

func assertSortedCopyByStringsOrder(t *testing.T, items, want []testSortedSnapshotItem) {
	t.Helper()

	got := SortedCopyByStrings(items, sortedSnapshotGroup, sortedSnapshotName)

	if !slices.Equal(got, want) {
		t.Fatalf("SortedCopyByStrings() = %#v, want %#v", got, want)
	}
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
		Repair:       normalizeTestSnapshotReport,
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

type snapshotDecodeCompatibilityCase struct {
	name      string
	data      string
	wantKey   string
	wantValue string
	wantErr   string
}

func assertSnapshotDecodeCompatibility(t *testing.T, tc snapshotDecodeCompatibilityCase) {
	t.Helper()

	got, key, err := DecodeSnapshot([]byte(tc.data), normalizedSnapshotDecodeOptions())
	if tc.wantErr != "" {
		if err == nil || err.Error() != tc.wantErr {
			t.Fatalf("DecodeSnapshot() error = %v, want %q", err, tc.wantErr)
		}
		if got != (testSnapshotReport{}) || key != "" {
			t.Fatalf("DecodeSnapshot() error result = %#v, %q, want zero values", got, key)
		}
		return
	}
	if err != nil {
		t.Fatalf("DecodeSnapshot() error = %v", err)
	}
	if got.Value != tc.wantValue || key != tc.wantKey {
		t.Fatalf("DecodeSnapshot() = %#v, %q, want value=%q key=%q", got, key, tc.wantValue, tc.wantKey)
	}
}

func TestDecodeSnapshotSchemaVersionCompatibility(t *testing.T) {
	t.Parallel()

	tests := []snapshotDecodeCompatibilityCase{
		{
			name:      "exact version accepted",
			data:      `{"baselineSchemaVersion":"1.0.0","key":" label:test ","savedAt":"2026-07-12T00:00:00Z","report":{"value":"snapshot"}}`,
			wantKey:   "label:test",
			wantValue: "SNAPSHOT",
		},
		{
			name:    "padded version rejected with raw value",
			data:    `{"baselineSchemaVersion":" 1.0.0 ","key":"label:test","savedAt":"2026-07-12T00:00:00Z","report":{"value":"snapshot"}}`,
			wantErr: "unsupported baseline schema version:  1.0.0 ",
		},
		{
			name:      "whitespace version falls back to missing behavior",
			data:      `{"baselineSchemaVersion":"   ","value":"legacy"}`,
			wantValue: "LEGACY",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertSnapshotDecodeCompatibility(t, tc)
		})
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
	assertLoadStoreSnapshotReadError(t, dir, decodeCalled, unexpectedDecode)

	decodeErr := errors.New("decode snapshot")
	failDecode := func([]byte) (testSnapshotReport, string, error) {
		return testSnapshotReport{}, "", decodeErr
	}
	assertLoadStoreSnapshotDecodeError(t, dir, savedPath, decodeErr, failDecode)

	decode := func([]byte) (testSnapshotReport, string, error) {
		return testSnapshotReport{Value: "decoded"}, "label:stored", nil
	}
	validateErr := errors.New("validate snapshot key")
	assertLoadStoreSnapshotSuccess(t, dir, savedPath, decode)
	assertLoadStoreSnapshotValidationError(t, dir, savedPath, decode, validateErr)
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

func TestConfiguredSnapshotHelpersRoundTrip(t *testing.T) {
	t.Parallel()
	store := configuredSnapshotRoundTripStore()
	dir := t.TempDir()
	now := time.Date(2026, time.July, 12, 11, 15, 0, 0, time.FixedZone("AEST", 10*60*60))

	wantSnapshot := NewConfiguredSnapshot(" label:configured ", testSnapshotReport{Value: "ok"}, now, store)
	if wantSnapshot.Key != "label:configured" || wantSnapshot.Report.Value != "OK" || !wantSnapshot.SavedAt.Equal(now.UTC()) {
		t.Fatalf("NewConfiguredSnapshot() = %#v", wantSnapshot)
	}

	path, err := SaveConfiguredSnapshot(dir, " label:configured ", now, testSnapshotReport{Value: "ok"}, store)
	if err != nil {
		t.Fatalf("SaveConfiguredSnapshot() error = %v", err)
	}
	assertConfiguredSnapshotLoadRoundTrip(t, path, store)
	assertConfiguredSnapshotDecodeRoundTrip(t, path, store)
	assertConfiguredStoreSnapshotRoundTrip(t, dir, path, store)
}

func assertLoadStoreSnapshotReadError(t *testing.T, dir string, decodeCalled bool, unexpectedDecode func([]byte) (testSnapshotReport, string, error)) {
	t.Helper()
	got, key, path, err := LoadStoreSnapshot(dir, "label:missing", MaxSnapshotBytes, unexpectedDecode, nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LoadStoreSnapshot() read error = %v, want os.ErrNotExist", err)
	}
	if decodeCalled || got != (testSnapshotReport{}) || key != "" || path != ResolveSnapshotPath(dir, "label:missing") {
		t.Fatalf("LoadStoreSnapshot() read failure = %#v, %q, %q, decodeCalled=%t", got, key, path, decodeCalled)
	}
}

func assertLoadStoreSnapshotDecodeError(t *testing.T, dir, savedPath string, decodeErr error, failDecode func([]byte) (testSnapshotReport, string, error)) {
	t.Helper()
	got, key, path, err := LoadStoreSnapshot(dir, "label:weekly", MaxSnapshotBytes, failDecode, nil)
	if !errors.Is(err, decodeErr) {
		t.Fatalf("LoadStoreSnapshot() decode error = %v, want %v", err, decodeErr)
	}
	if got != (testSnapshotReport{}) || key != "" || path != savedPath {
		t.Fatalf("LoadStoreSnapshot() decode failure = %#v, %q, %q", got, key, path)
	}
}

func assertLoadStoreSnapshotSuccess(t *testing.T, dir, savedPath string, decode func([]byte) (testSnapshotReport, string, error)) {
	t.Helper()
	got, key, path, err := LoadStoreSnapshot(dir, "label:weekly", MaxSnapshotBytes, decode, nil)
	if err != nil || got.Value != "decoded" || key != "label:stored" || path != savedPath {
		t.Fatalf("LoadStoreSnapshot() without validation = %#v, %q, %q, %v", got, key, path, err)
	}
}

func assertLoadStoreSnapshotValidationError(t *testing.T, dir, savedPath string, decode func([]byte) (testSnapshotReport, string, error), validateErr error) {
	t.Helper()
	rejectKey := func(string, string) error { return validateErr }
	got, key, path, err := LoadStoreSnapshot(dir, "label:weekly", MaxSnapshotBytes, decode, rejectKey)
	if !errors.Is(err, validateErr) {
		t.Fatalf("LoadStoreSnapshot() validation error = %v, want %v", err, validateErr)
	}
	if got != (testSnapshotReport{}) || key != "label:stored" || path != savedPath {
		t.Fatalf("LoadStoreSnapshot() validation failure = %#v, %q, %q", got, key, path)
	}
}

func configuredSnapshotRoundTripStore() SnapshotStore[testSnapshotReport] {
	repair := func(report testSnapshotReport) testSnapshotReport {
		report.Value = "repaired:" + report.Value
		return report
	}
	return SnapshotStore[testSnapshotReport]{
		Repair:            repair,
		Normalize:         normalizeTestSnapshotReport,
		UnsupportedSchema: func(version string) error { return fmt.Errorf("unsupported configured schema version: %s", version) },
		ValidateKey: func(requestedKey, storedKey string) error {
			return ValidateSnapshotKey(requestedKey, storedKey, errors.New("configured key mismatch"))
		},
		ExistsErr: errors.New("configured snapshot already exists"),
	}
}

func assertConfiguredSnapshotLoadRoundTrip(t *testing.T, path string, store SnapshotStore[testSnapshotReport]) {
	t.Helper()
	got, key, err := LoadConfiguredSnapshot(path, store)
	if err != nil {
		t.Fatalf("LoadConfiguredSnapshot() error = %v", err)
	}
	if key != "label:configured" || got.Value != "repaired:OK" {
		t.Fatalf("LoadConfiguredSnapshot() = key=%q report=%#v", key, got)
	}
}

func assertConfiguredSnapshotDecodeRoundTrip(t *testing.T, path string, store SnapshotStore[testSnapshotReport]) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read configured snapshot: %v", err)
	}
	decoded, decodedKey, err := DecodeConfiguredSnapshot(data, store)
	if err != nil {
		t.Fatalf("DecodeConfiguredSnapshot() error = %v", err)
	}
	if decodedKey != "label:configured" || decoded.Value != "repaired:OK" {
		t.Fatalf("DecodeConfiguredSnapshot() = key=%q report=%#v", decodedKey, decoded)
	}
}

func assertConfiguredStoreSnapshotRoundTrip(t *testing.T, dir, path string, store SnapshotStore[testSnapshotReport]) {
	t.Helper()
	loaded, loadedKey, resolvedPath, err := LoadConfiguredStoreSnapshot(dir, " label:configured ", MaxSnapshotBytes, store)
	if err != nil {
		t.Fatalf("LoadConfiguredStoreSnapshot() error = %v", err)
	}
	if resolvedPath != path || loadedKey != "label:configured" || loaded.Value != "repaired:OK" {
		t.Fatalf("LoadConfiguredStoreSnapshot() = path=%q key=%q report=%#v", resolvedPath, loadedKey, loaded)
	}
}

func TestConfiguredSnapshotHelpersReturnCustomErrors(t *testing.T) {
	t.Parallel()

	unsupportedMarker := errors.New("unsupported configured schema")
	validateMarker := errors.New("configured key mismatch")
	existsMarker := errors.New("configured snapshot already exists")
	store := SnapshotStore[testSnapshotReport]{
		Normalize: normalizeTestSnapshotReport,
		UnsupportedSchema: func(version string) error {
			return fmt.Errorf("%w: %s", unsupportedMarker, version)
		},
		ValidateKey: func(string, string) error {
			return validateMarker
		},
		ExistsErr: existsMarker,
	}
	dir := t.TempDir()
	now := time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC)

	path, err := SaveConfiguredSnapshot(dir, "label:configured", now, testSnapshotReport{Value: "ok"}, store)
	if err != nil {
		t.Fatalf("SaveConfiguredSnapshot() initial save error = %v", err)
	}

	if _, err := SaveConfiguredSnapshot(dir, "label:configured", now, testSnapshotReport{Value: "again"}, store); !errors.Is(err, existsMarker) {
		t.Fatalf("SaveConfiguredSnapshot() duplicate error = %v, want %v", err, existsMarker)
	}

	if _, _, err := DecodeConfiguredSnapshot([]byte(`{"baselineSchemaVersion":"9.9.9","key":"label:bad","report":{"value":"x"}}`), store); !errors.Is(err, unsupportedMarker) {
		t.Fatalf("DecodeConfiguredSnapshot() unsupported schema error = %v, want %v", err, unsupportedMarker)
	}

	_, loadedKey, resolvedPath, err := LoadConfiguredStoreSnapshot(dir, "label:configured", MaxSnapshotBytes, store)
	if !errors.Is(err, validateMarker) {
		t.Fatalf("LoadConfiguredStoreSnapshot() validation error = %v, want %v", err, validateMarker)
	}
	if loadedKey != "label:configured" || resolvedPath != path {
		t.Fatalf("LoadConfiguredStoreSnapshot() validation result = key=%q path=%q", loadedKey, resolvedPath)
	}
}

func TestSaveConfiguredSnapshotWithinRoot(t *testing.T) {
	rootDir := t.TempDir()
	root, err := safeio.OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open rooted baseline store: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close rooted baseline store: %v", err)
		}
	})

	existsMarker := errors.New("configured snapshot already exists")
	store := SnapshotStore[testSnapshotReport]{
		Normalize: normalizeTestSnapshotReport,
		ExistsErr: existsMarker,
	}
	now := time.Date(2026, time.July, 25, 23, 15, 0, 0, time.FixedZone("AEST", 10*60*60))
	dir := filepath.Join("state", "baselines")
	displayDir := filepath.Join("/display", "state", "baselines")

	path, err := SaveConfiguredSnapshotWithinRoot(root, dir, displayDir, " label:rooted ", now, testSnapshotReport{Value: "ok"}, store)
	if err != nil {
		t.Fatalf("save rooted baseline: %v", err)
	}
	wantPath := filepath.Join(displayDir, SnapshotFileName("label:rooted"))
	if path != wantPath {
		t.Fatalf("rooted baseline path = %q, want %q", path, wantPath)
	}
	data, err := os.ReadFile(filepath.Join(rootDir, dir, SnapshotFileName("label:rooted")))
	if err != nil {
		t.Fatalf("read rooted baseline: %v", err)
	}
	var snapshot Snapshot[testSnapshotReport]
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("decode rooted baseline: %v", err)
	}
	if snapshot.Key != "label:rooted" || snapshot.Report.Value != "OK" || !snapshot.SavedAt.Equal(now.UTC()) {
		t.Fatalf("unexpected rooted baseline: %#v", snapshot)
	}
	if _, err := SaveConfiguredSnapshotWithinRoot(root, dir, displayDir, "label:rooted", now, testSnapshotReport{}, store); !errors.Is(err, existsMarker) {
		t.Fatalf("duplicate rooted baseline error = %v, want %v", err, existsMarker)
	}
	if _, err := SaveConfiguredSnapshotWithinRoot(root, dir, displayDir, "label:rooted", now, testSnapshotReport{}, SnapshotStore[testSnapshotReport]{}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("raw duplicate rooted baseline error = %v, want os.ErrExist", err)
	}
}

func TestSaveConfiguredSnapshotWithinRootAllowsRepositoryRootStore(t *testing.T) {
	rootDir := t.TempDir()
	root, err := safeio.OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open rooted baseline store: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close rooted baseline store: %v", err)
		}
	})

	now := time.Date(2026, time.July, 26, 9, 0, 0, 0, time.UTC)
	path, err := SaveConfiguredSnapshotWithinRoot(root, ".", rootDir, "label:root", now, testSnapshotReport{Value: "ok"}, SnapshotStore[testSnapshotReport]{})
	if err != nil {
		t.Fatalf("save rooted baseline at repository root: %v", err)
	}
	if path != filepath.Join(rootDir, SnapshotFileName("label:root")) {
		t.Fatalf("unexpected rooted baseline path: %q", path)
	}
	if _, err := os.Stat(filepath.Join(rootDir, SnapshotFileName("label:root"))); err != nil {
		t.Fatalf("expected rooted baseline file: %v", err)
	}
}

func TestSaveConfiguredSnapshotWithinRootLegacyCompatibility(t *testing.T) {
	rootDir := t.TempDir()
	root, err := safeio.OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open rooted baseline store: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close rooted baseline store: %v", err)
		}
	})

	dir := "baselines"
	if err := os.Mkdir(filepath.Join(rootDir, dir), 0o750); err != nil {
		t.Fatalf("create baseline directory: %v", err)
	}
	key := "label:a/b"
	legacyPath := filepath.Join(rootDir, dir, LegacySnapshotFileName(key))
	if err := os.WriteFile(legacyPath, []byte(`{"key":"label:a_b"}`), 0o600); err != nil {
		t.Fatalf("write non-matching legacy baseline: %v", err)
	}
	store := SnapshotStore[testSnapshotReport]{ExistsErr: errors.New("baseline exists")}
	if _, err := SaveConfiguredSnapshotWithinRoot(root, dir, dir, key, time.Now(), testSnapshotReport{}, store); err != nil {
		t.Fatalf("non-matching legacy key should not block rooted save: %v", err)
	}

	matchingKey := "label:matching"
	matchingPath := filepath.Join(rootDir, dir, LegacySnapshotFileName(matchingKey))
	if err := os.WriteFile(matchingPath, []byte(`{"key":"label:matching"}`), 0o600); err != nil {
		t.Fatalf("write matching legacy baseline: %v", err)
	}
	if _, err := SaveConfiguredSnapshotWithinRoot(root, dir, dir, matchingKey, time.Now(), testSnapshotReport{}, store); !errors.Is(err, store.ExistsErr) {
		t.Fatalf("matching legacy baseline error = %v, want %v", err, store.ExistsErr)
	}

	matchingWithoutMarker := "label:no-marker"
	if err := os.WriteFile(filepath.Join(rootDir, dir, LegacySnapshotFileName(matchingWithoutMarker)), []byte(`{"key":"label:no-marker"}`), 0o600); err != nil {
		t.Fatalf("write marker-free legacy baseline: %v", err)
	}
	if _, err := SaveConfiguredSnapshotWithinRoot(root, dir, dir, matchingWithoutMarker, time.Now(), testSnapshotReport{}, SnapshotStore[testSnapshotReport]{}); err == nil || !strings.Contains(err.Error(), "baseline snapshot already exists") {
		t.Fatalf("marker-free matching legacy baseline error = %v", err)
	}
}

func TestSaveConfiguredSnapshotWithinRootRejectsUnsafeLegacyEntries(t *testing.T) {
	rootDir := t.TempDir()
	root, err := safeio.OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open rooted baseline store: %v", err)
	}
	dir := "baselines"
	if err := os.Mkdir(filepath.Join(rootDir, dir), 0o750); err != nil {
		t.Fatalf("create baseline directory: %v", err)
	}
	store := SnapshotStore[testSnapshotReport]{}
	assertRejected := func(key string, create func(string) error, want error) {
		t.Helper()
		assertUnsafeLegacySnapshotRejected(t, rootDir, root, dir, key, create, want, store)
	}
	assertRejected("label:directory", func(path string) error { return os.Mkdir(path, 0o750) }, nil)
	assertRejected("label:oversized", createOversizedLegacySnapshot, ErrSnapshotTooLarge)
	assertSymlinkedLegacySnapshotRejected(t, rootDir, root, dir, store)

	if err := root.Close(); err != nil {
		t.Fatalf("close rooted baseline store: %v", err)
	}
	if _, err := SaveConfiguredSnapshotWithinRoot(root, dir, dir, "label:closed", time.Now(), testSnapshotReport{}, store); err == nil {
		t.Fatal("expected closed rooted baseline store rejection")
	}
}

func assertUnsafeLegacySnapshotRejected(t *testing.T, rootDir string, root *safeio.WriteRoot, dir, key string, create func(string) error, want error, store SnapshotStore[testSnapshotReport]) {
	t.Helper()
	path := filepath.Join(rootDir, dir, LegacySnapshotFileName(key))
	if err := create(path); err != nil {
		t.Fatalf("create unsafe legacy entry: %v", err)
	}
	_, err := SaveConfiguredSnapshotWithinRoot(root, dir, dir, key, time.Now(), testSnapshotReport{}, store)
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("legacy entry error = %v, want %v", err, want)
	}
	if want == nil && err == nil {
		t.Fatal("expected unsafe legacy entry rejection")
	}
}

func createOversizedLegacySnapshot(path string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := file.Truncate(MaxSnapshotBytes + 1); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

func assertSymlinkedLegacySnapshotRejected(t *testing.T, rootDir string, root *safeio.WriteRoot, dir string, store SnapshotStore[testSnapshotReport]) {
	t.Helper()
	symlinkKey := "label:symlink"
	symlinkTarget := filepath.Join(rootDir, dir, "target.json")
	if err := os.WriteFile(symlinkTarget, []byte(`{"key":"target"}`), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	symlinkPath := filepath.Join(rootDir, dir, LegacySnapshotFileName(symlinkKey))
	if err := os.Symlink(filepath.Base(symlinkTarget), symlinkPath); err != nil {
		t.Logf("symlink creation unsupported: %v", err)
		return
	}
	if _, err := SaveConfiguredSnapshotWithinRoot(root, dir, dir, symlinkKey, time.Now(), testSnapshotReport{}, store); err == nil {
		t.Fatal("expected symlinked legacy entry rejection")
	}
}

func TestSaveConfiguredSnapshotWithinRootRejectsUnsafeInputsAndWriteFailures(t *testing.T) {
	rootDir := t.TempDir()
	root, err := safeio.OpenWriteRoot(rootDir)
	if err != nil {
		t.Fatalf("open rooted baseline store: %v", err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Errorf("close rooted baseline store: %v", err)
		}
	})
	now := time.Date(2026, time.July, 25, 0, 0, 0, 0, time.UTC)
	store := SnapshotStore[any]{ExistsErr: errors.New("baseline exists")}

	testCases := []struct {
		name string
		root *safeio.WriteRoot
		dir  string
		key  string
	}{
		{name: "missing root", root: nil, dir: "baselines", key: "label:test"},
		{name: "missing key", root: root, dir: "baselines", key: "  "},
		{name: "absolute directory", root: root, dir: filepath.Join(string(os.PathSeparator), "outside"), key: "label:test"},
		{name: "escaping directory", root: root, dir: filepath.Join("..", "outside"), key: "label:test"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := SaveConfiguredSnapshotWithinRoot(testCase.root, testCase.dir, testCase.dir, testCase.key, now, "report", store); err == nil {
				t.Fatal("expected rooted baseline input rejection")
			}
		})
	}

	if err := os.WriteFile(filepath.Join(rootDir, "blocking"), []byte("file"), 0o600); err != nil {
		t.Fatalf("write blocking parent: %v", err)
	}
	if _, err := SaveConfiguredSnapshotWithinRoot(root, filepath.Join("blocking", "child"), "display", "label:test", now, "report", store); err == nil {
		t.Fatal("expected rooted baseline parent creation failure")
	}
	if _, err := SaveConfiguredSnapshotWithinRoot[any](root, "marshal", "display", "label:test", now, make(chan int), store); err == nil {
		t.Fatal("expected rooted baseline marshal failure")
	}
}

func TestSortedCopyByStringsReturnsEmptyResultWhenInputIsNil(t *testing.T) {
	t.Parallel()

	assertSortedCopyByStringsReturnsEmptyResult(t, "nil", nil)
}

func TestSortedCopyByStringsReturnsEmptyResultWhenInputIsEmpty(t *testing.T) {
	t.Parallel()

	assertSortedCopyByStringsReturnsEmptyResult(t, "empty", []testSortedSnapshotItem{})
}

func TestSortedCopyByStringsOrdersItemsByConfiguredKeys(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		items []testSortedSnapshotItem
		want  []testSortedSnapshotItem
	}{
		{
			name:  "orders items by primary key",
			items: testSortedSnapshotItemsUnorderedByPrimary,
			want:  testSortedSnapshotItemsOrderedByPrimary,
		},
		{
			name:  "uses secondary key to break primary ties",
			items: testSortedSnapshotItemsTiedOnPrimary,
			want:  testSortedSnapshotItemsOrderedBySecondary,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertSortedCopyByStringsOrder(t, tc.items, tc.want)
		})
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
