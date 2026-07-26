package baseline

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const SnapshotSchemaVersion = "1.0.0"

type Snapshot[T any] struct {
	BaselineSchemaVersion string    `json:"baselineSchemaVersion"`
	Key                   string    `json:"key"`
	SavedAt               time.Time `json:"savedAt"`
	Report                T         `json:"report"`
}

type SnapshotDecodeOptions[T any] struct {
	DecodeLegacy      func([]byte) (T, error)
	Repair            func(T) T
	UnsupportedSchema func(string) error
}

type SnapshotStore[T any] struct {
	Repair            func(T) T
	Normalize         func(T) T
	UnsupportedSchema func(string) error
	ValidateKey       func(string, string) error
	ExistsErr         error
}

func NewSnapshot[T any](key string, report T, now time.Time, normalize func(T) T) Snapshot[T] {
	if normalize != nil {
		report = normalize(report)
	}
	return Snapshot[T]{
		BaselineSchemaVersion: SnapshotSchemaVersion,
		Key:                   strings.TrimSpace(key),
		SavedAt:               now.UTC(),
		Report:                report,
	}
}

func SaveSnapshot[T any](dir, key string, now time.Time, report T, existsErr error, normalize func(T) T) (string, error) {
	return SaveJSON(dir, key, existsErr, func(trimmedKey string) Snapshot[T] {
		return NewSnapshot(trimmedKey, report, now, normalize)
	})
}

func SortedCopyByStrings[T any](items []T, primaryKey func(T) string, secondaryKey func(T) string) []T {
	copied := append([]T(nil), items...)
	slices.SortFunc(copied, func(left, right T) int {
		if diff := strings.Compare(primaryKey(left), primaryKey(right)); diff != 0 {
			return diff
		}
		return strings.Compare(secondaryKey(left), secondaryKey(right))
	})
	return copied
}

func LoadSnapshotFile[T any](path string, options SnapshotDecodeOptions[T]) (T, string, error) {
	data, err := safeio.ReadFile(path)
	if err != nil {
		var zero T
		return zero, "", err
	}
	return DecodeSnapshot(data, options)
}

func DecodeSnapshot[T any](data []byte, options SnapshotDecodeOptions[T]) (T, string, error) {
	var snapshot Snapshot[T]
	if err := json.Unmarshal(data, &snapshot); err == nil && strings.TrimSpace(snapshot.BaselineSchemaVersion) != "" {
		version := snapshot.BaselineSchemaVersion
		if version != SnapshotSchemaVersion {
			var zero T
			return zero, "", snapshotSchemaError(version, options.UnsupportedSchema)
		}
		v := snapshot.Report
		if options.Repair != nil {
			v = options.Repair(v)
		}
		return v, strings.TrimSpace(snapshot.Key), nil
	}

	v, err := decodeLegacySnapshot(data, options.DecodeLegacy)
	if err != nil {
		var zero T
		return zero, "", err
	}
	if options.Repair != nil {
		v = options.Repair(v)
	}
	return v, "", nil
}

func LoadConfiguredSnapshot[T any](path string, store SnapshotStore[T]) (T, string, error) {
	return LoadSnapshotFile(path, snapshotDecodeOptions(store))
}

func DecodeConfiguredSnapshot[T any](data []byte, store SnapshotStore[T]) (T, string, error) {
	return DecodeSnapshot(data, snapshotDecodeOptions(store))
}

func LoadConfiguredStoreSnapshot[T any](dir, key string, maxBytes int64, store SnapshotStore[T]) (T, string, string, error) {
	decode := func(data []byte) (T, string, error) {
		return DecodeConfiguredSnapshot(data, store)
	}
	return LoadStoreSnapshot(dir, key, maxBytes, decode, store.ValidateKey)
}

func SaveConfiguredSnapshot[T any](dir, key string, now time.Time, report T, store SnapshotStore[T]) (string, error) {
	return SaveSnapshot(dir, key, now, report, store.ExistsErr, store.Normalize)
}

// SaveConfiguredSnapshotWithinRoot saves an immutable snapshot beneath an
// already-open write root.
func SaveConfiguredSnapshotWithinRoot[T any](root *safeio.WriteRoot, dir, displayDir, key string, now time.Time, report T, store SnapshotStore[T]) (string, error) {
	trimmedKey := strings.TrimSpace(key)
	if root == nil {
		return "", errors.New("baseline store root is required")
	}
	if trimmedKey == "" {
		return "", errors.New("baseline key is required")
	}
	cleanDir := filepath.Clean(strings.TrimSpace(dir))
	if filepath.IsAbs(cleanDir) || cleanDir == ".." || strings.HasPrefix(cleanDir, ".."+string(filepath.Separator)) {
		return "", errors.New("baseline store directory must be relative to repository root")
	}
	if cleanDir != "." {
		if err := root.EnsureDir(cleanDir, 0o750); err != nil {
			return "", err
		}
	}

	if err := rejectMatchingLegacySnapshotWithinRoot(root, cleanDir, displayDir, trimmedKey, store.ExistsErr); err != nil {
		return "", err
	}
	payload, err := json.MarshalIndent(NewConfiguredSnapshot(trimmedKey, report, now, store), "", "  ")
	if err != nil {
		return "", err
	}
	payload = append(payload, '\n')
	fileName := SnapshotFileName(trimmedKey)
	targetPath := filepath.Join(cleanDir, fileName)
	if err := root.WriteFileExclusiveCreatingParents(targetPath, payload, 0o600, 0o750); err != nil {
		if errors.Is(err, os.ErrExist) && store.ExistsErr != nil {
			return "", store.ExistsErr
		}
		return "", err
	}
	return filepath.Join(displayDir, fileName), nil
}

func rejectMatchingLegacySnapshotWithinRoot(root *safeio.WriteRoot, dir, displayDir, key string, existsErr error) error {
	legacyName := LegacySnapshotFileName(key)
	legacyPath := filepath.Join(dir, legacyName)
	info, err := root.Lstat(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	displayPath := filepath.Join(displayDir, legacyName)
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("baseline snapshot must not be a symlink: %s", displayPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("baseline snapshot must be a regular file: %s", displayPath)
	}
	if info.Size() > MaxSnapshotBytes {
		return fmt.Errorf("%w: %s", ErrSnapshotTooLarge, displayPath)
	}
	data, err := root.ReadFile(legacyPath)
	if err != nil {
		return err
	}
	return matchingLegacySnapshotError(data, key, existsErr, displayPath)
}

func NewConfiguredSnapshot[T any](key string, report T, now time.Time, store SnapshotStore[T]) Snapshot[T] {
	return NewSnapshot(key, report, now, store.Normalize)
}

func LoadStoreSnapshot[T any](dir, key string, maxBytes int64, decode func([]byte) (T, string, error), validateKey func(string, string) error) (T, string, string, error) {
	trimmedKey := strings.TrimSpace(key)
	path := ResolveSnapshotPath(dir, trimmedKey)
	data, err := ReadStoreEntry(dir, filepath.Base(path), maxBytes)
	if err != nil {
		var zero T
		return zero, "", path, err
	}
	v, loadedKey, err := decode(data)
	if err != nil {
		var zero T
		return zero, "", path, err
	}
	if validateKey != nil {
		if err := validateKey(trimmedKey, loadedKey); err != nil {
			var zero T
			return zero, loadedKey, path, err
		}
	}
	return v, loadedKey, path, nil
}

func ValidateSnapshotKey(requestedKey, storedKey string, mismatchErr error) error {
	requestedKey = strings.TrimSpace(requestedKey)
	storedKey = strings.TrimSpace(storedKey)
	if requestedKey == storedKey && requestedKey != "" {
		return nil
	}
	return fmt.Errorf("%w: requested %q, stored %q", mismatchErr, requestedKey, storedKey)
}

func snapshotDecodeOptions[T any](store SnapshotStore[T]) SnapshotDecodeOptions[T] {
	return SnapshotDecodeOptions[T]{
		Repair:            store.Repair,
		UnsupportedSchema: store.UnsupportedSchema,
	}
}

func decodeLegacySnapshot[T any](data []byte, decodeLegacy func([]byte) (T, error)) (T, error) {
	if decodeLegacy != nil {
		return decodeLegacy(data)
	}
	var v T
	return v, json.Unmarshal(data, &v)
}

func snapshotSchemaError(version string, unsupportedSchema func(string) error) error {
	if unsupportedSchema != nil {
		return unsupportedSchema(version)
	}
	return fmt.Errorf("unsupported baseline schema version: %s", version)
}
