package baseline

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if wantSuffix := filepath.Join(dir, "label_weekly_1.json"); path != wantSuffix {
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
