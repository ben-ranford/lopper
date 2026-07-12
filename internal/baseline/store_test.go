package baseline

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestSaveJSONWritesImmutableSnapshot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	existsErr := errors.New("snapshot exists")

	path, err := SaveJSON(dir, " label:weekly/1 ", existsErr, func(trimmedKey string) map[string]string {
		return map[string]string{"key": trimmedKey}
	})
	if err != nil {
		t.Fatalf("SaveJSON() error = %v", err)
	}
	if wantSuffix := SnapshotPath(dir, "label:weekly/1"); path != wantSuffix {
		t.Fatalf("SaveJSON() path = %q, want %q", path, wantSuffix)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if payload["key"] != "label:weekly/1" {
		t.Fatalf("expected trimmed key in payload, got %#v", payload)
	}

	if _, err := SaveJSON(dir, "label:weekly/1", existsErr, func(string) map[string]string {
		return map[string]string{}
	}); !errors.Is(err, existsErr) {
		t.Fatalf("expected duplicate save to wrap exists error, got %v", err)
	}
}

func TestSaveJSONValidationAndFilesystemErrors(t *testing.T) {
	t.Parallel()

	if _, err := SaveJSON("", "key", nil, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "directory is required") {
		t.Fatalf("expected missing directory validation error, got %v", err)
	}
	if _, err := SaveJSON(t.TempDir(), "  ", nil, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "key is required") {
		t.Fatalf("expected missing key validation error, got %v", err)
	}

	root := t.TempDir()
	blockingFile := filepath.Join(root, "blocked")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	if _, err := SaveJSON(filepath.Join(blockingFile, "nested"), "key", nil, func(string) string { return "" }); err == nil {
		t.Fatalf("expected mkdir failure below a regular file")
	}
}

func TestSaveJSONRejectsSymlinkedStoreDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("create target: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := SaveJSON(link, "key", nil, func(string) string { return "" }); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestSaveJSONRemovesPartialFileAfterEncodeFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	_, err := SaveJSON(dir, "bad", nil, func(string) map[string]float64 {
		return map[string]float64{"nan": math.NaN()}
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported value: NaN") {
		t.Fatalf("expected json encode error, got %v", err)
	}
	if _, err := os.Stat(SnapshotPath(dir, "bad")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected partial snapshot to be removed, stat err=%v", err)
	}
}

func TestSaveJSONReportsCleanupFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	snapshotPath := SnapshotPath(dir, "bad-cleanup")
	_, err := SaveJSON(dir, "bad-cleanup", nil, func(string) map[string]float64 {
		if err := os.Remove(snapshotPath); err != nil {
			t.Fatalf("remove open snapshot path: %v", err)
		}
		if err := os.Mkdir(snapshotPath, 0o700); err != nil {
			t.Fatalf("replace snapshot path with directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(snapshotPath, "child"), []byte("x"), 0o600); err != nil {
			t.Fatalf("make cleanup directory non-empty: %v", err)
		}
		return map[string]float64{"nan": math.NaN()}
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported value: NaN") || !strings.Contains(err.Error(), snapshotPath) {
		t.Fatalf("expected encode and cleanup errors, got %v", err)
	}
	if removeErr := os.RemoveAll(snapshotPath); removeErr != nil {
		t.Fatalf("remove cleanup failure fixture: %v", removeErr)
	}
}

func TestSanitizeKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "empty", key: "", want: "baseline"},
		{name: "valid", key: "Release-1.2_prod", want: "Release-1.2_prod"},
		{name: "replaces invalid and trims separators", key: "../feature branch#", want: "feature_branch"},
		{name: "all separators fallback", key: "._-", want: "baseline"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := SanitizeKey(tc.key); got != tc.want {
				t.Fatalf("SanitizeKey(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

func TestSnapshotPathIsCollisionResistantAndBounded(t *testing.T) {
	t.Parallel()

	first := SnapshotPath("store", "label:a/b")
	second := SnapshotPath("store", "label:a?b")
	if first == second {
		t.Fatalf("distinct keys resolved to the same snapshot path: %q", first)
	}
	if !strings.HasPrefix(filepath.Base(first), "label_a_b--") || !strings.HasPrefix(filepath.Base(second), "label_a_b--") {
		t.Fatalf("expected readable slugs with digests, got %q and %q", first, second)
	}

	longKey := "label:" + strings.Repeat("a", 300)
	if got := len(SnapshotFileName(longKey)); got > maxSnapshotSlugLength+2+64+len(".json") {
		t.Fatalf("snapshot filename exceeded bounded length: %d", got)
	}
}

func TestSaveJSONRejectsSymlinkedAncestorBeforeCreatingStore(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "actual")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatalf("create target: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	store := filepath.Join(link, "nested", "store")
	if _, err := SaveJSON(store, "label:nightly", nil, func(key string) map[string]string {
		return map[string]string{"key": key}
	}); err == nil || !strings.Contains(err.Error(), "path component must not be a symlink") {
		t.Fatalf("expected ancestor symlink rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("save wrote through symlinked ancestor, stat err=%v", err)
	}
}

func TestLegacySnapshotCompatibilityAndCollisionMigration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	legacyPath := LegacySnapshotPath(dir, "label:a/b")
	if err := os.WriteFile(legacyPath, []byte(`{"key":"label:a/b"}`), 0o600); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}
	if got := ResolveSnapshotPath(dir, "label:a/b"); got != legacyPath {
		t.Fatalf("expected legacy fallback %q, got %q", legacyPath, got)
	}

	existsErr := errors.New("snapshot exists")
	if _, err := SaveJSON(dir, "label:a/b", existsErr, func(key string) map[string]string {
		return map[string]string{"key": key}
	}); !errors.Is(err, existsErr) {
		t.Fatalf("expected legacy snapshot to preserve immutability, got %v", err)
	}
	newPath, err := SaveJSON(dir, "label:a?b", existsErr, func(key string) map[string]string {
		return map[string]string{"key": key}
	})
	if err != nil {
		t.Fatalf("save colliding logical key with new mapping: %v", err)
	}
	if newPath == legacyPath || ResolveSnapshotPath(dir, "label:a?b") != newPath {
		t.Fatalf("expected colliding key to use unique new path, legacy=%q new=%q", legacyPath, newPath)
	}
}

func TestStoreListingAndRegularEntryRead(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing")
	if names, err := ListStoreEntries(missing); err != nil || len(names) != 0 {
		t.Fatalf("missing store should list safely, names=%v err=%v", names, err)
	}

	dir := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(dir, "b.json"), "bb")
	testutil.MustWriteFile(t, filepath.Join(dir, "a.json"), "a")
	if err := os.Mkdir(filepath.Join(dir, "directory.json"), 0o700); err != nil {
		t.Fatalf("create directory entry: %v", err)
	}
	names, err := ListStoreEntries(dir)
	if err != nil || len(names) != 3 || names[0] != "a.json" || names[1] != "b.json" {
		t.Fatalf("unexpected deterministic listing: names=%v err=%v", names, err)
	}
	if data, err := ReadStoreEntry(dir, "a.json", 0); err != nil || string(data) != "a" {
		t.Fatalf("read regular entry: data=%q err=%v", data, err)
	}
}

func TestStoreEntryReadRejectsInvalidTargets(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(dir, "oversized.json"), "bb")
	if err := os.Mkdir(filepath.Join(dir, "directory.json"), 0o700); err != nil {
		t.Fatalf("create directory entry: %v", err)
	}
	if _, err := ReadStoreEntry(dir, "oversized.json", 1); !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("expected oversized entry error, got %v", err)
	}
	if _, err := ReadStoreEntry(dir, "directory.json", 10); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("expected directory entry rejection, got %v", err)
	}
	if _, err := ReadStoreEntry(dir, "../outside.json", 10); err == nil || !strings.Contains(err.Error(), "invalid baseline store entry") {
		t.Fatalf("expected out-of-root entry rejection, got %v", err)
	}
}

func TestStoreEntryReadRejectsSymlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	testutil.MustWriteFile(t, target, "a")
	link := filepath.Join(dir, "outside.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := ReadStoreEntry(dir, "outside.json", 10); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected entry symlink rejection, got %v", err)
	}
}

func TestStoreValidationErrors(t *testing.T) {
	t.Parallel()

	if err := ValidateStorePath(" "); err == nil || !strings.Contains(err.Error(), "directory is required") {
		t.Fatalf("expected empty store validation error, got %v", err)
	}

	root := t.TempDir()
	blockingFile := filepath.Join(root, "store-file")
	testutil.MustWriteFile(t, blockingFile, "x")
	if _, err := ListStoreEntries(blockingFile); err == nil || !strings.Contains(err.Error(), "list baseline store") {
		t.Fatalf("expected regular-file store listing error, got %v", err)
	}
	if _, err := ReadStoreEntry(filepath.Join(root, "missing"), "snapshot.json", 10); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing store entry error, got %v", err)
	}
	if _, err := ReadStoreEntry(root, "missing.json", 10); err == nil || strings.Count(err.Error(), "lstat") != 1 {
		t.Fatalf("expected one lstat layer for missing entry, got %v", err)
	}
	if _, err := ReadStoreEntry(root, " ", 10); err == nil || !strings.Contains(err.Error(), "invalid baseline store entry") {
		t.Fatalf("expected empty entry name error, got %v", err)
	}
}

func TestSystemRootSymlinkAllowanceIsNarrow(t *testing.T) {
	t.Parallel()

	if isAllowedSystemRootSymlink(string(filepath.Separator) + "link") {
		t.Fatal("arbitrary direct-root symlink must not be allowed")
	}
}

func TestStoreValidationRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	linkedTarget := filepath.Join(root, "linked-target")
	if err := os.Mkdir(linkedTarget, 0o700); err != nil {
		t.Fatalf("create linked target: %v", err)
	}
	linkedStore := filepath.Join(root, "linked-store")
	if err := os.Symlink(linkedTarget, linkedStore); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := ListStoreEntries(linkedStore); err == nil || !strings.Contains(err.Error(), "path component must not be a symlink") {
		t.Fatalf("expected linked store listing rejection, got %v", err)
	}
	if _, err := ReadStoreEntry(linkedStore, "snapshot.json", 10); err == nil || !strings.Contains(err.Error(), "path component must not be a symlink") {
		t.Fatalf("expected linked store read rejection, got %v", err)
	}
}

func TestCorruptLegacyFileDoesNotBlockUniqueMapping(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.MustWriteFile(t, LegacySnapshotPath(dir, "label:corrupt"), "{")
	if _, err := SaveJSON(dir, "label:corrupt", nil, baselineKeyPayload); err != nil {
		t.Fatalf("corrupt legacy file should not collide with new mapping: %v", err)
	}
}

func TestMatchingLegacyKeyReportsDefaultImmutabilityError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	testutil.MustWriteFile(t, LegacySnapshotPath(dir, "label:weekly"), `{"key":"label:weekly"}`)
	if _, err := SaveJSON(dir, "label:weekly", nil, baselineKeyPayload); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected default immutable snapshot error, got %v", err)
	}
}

func TestSymlinkedLegacyCandidateIsRejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside.json")
	testutil.MustWriteFile(t, target, `{"key":"label:linked"}`)
	if err := os.Symlink(target, LegacySnapshotPath(dir, "label:linked")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := SaveJSON(dir, "label:linked", nil, baselineKeyPayload); err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("expected symlinked legacy rejection, got %v", err)
	}
}

func TestDuplicateNewPathReportsRawExistsError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := SaveJSON(dir, "label:new", nil, baselineKeyPayload); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if _, err := SaveJSON(dir, "label:new", nil, baselineKeyPayload); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected raw exists error, got %v", err)
	}
}

func baselineKeyPayload(key string) map[string]string {
	return map[string]string{"key": key}
}

func TestResolveStorePathReportsAbsolutePathFailure(t *testing.T) {
	testutil.ChdirRemovedDir(t)

	_, err := resolveStorePath("relative")
	if err == nil {
		t.Skip("platform retains an absolute path for a removed working directory")
	}
	if !strings.Contains(err.Error(), "resolve baseline store directory") {
		t.Fatalf("expected absolute path resolution error, got %v", err)
	}
}

func TestOpenSnapshotWriterRejectsInvalidStoreTargets(t *testing.T) {
	root := t.TempDir()
	blockingFile := filepath.Join(root, "blocking")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	if _, _, _, err := openSnapshotWriter(filepath.Join(blockingFile, "nested"), "label:x", nil); err == nil {
		t.Fatalf("expected mkdir failure below regular file")
	}

	target := filepath.Join(root, "actual")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("create symlink target: %v", err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, _, _, err := openSnapshotWriter(link, "label:x", nil); err == nil || !strings.Contains(err.Error(), "path component must not be a symlink") {
		t.Fatalf("expected post-mkdir symlink validation error, got %v", err)
	}
}
