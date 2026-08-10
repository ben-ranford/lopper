package baseline

import (
	"encoding/json"
	"fmt"
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
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err == nil {
		version, typed, err := typedSnapshotVersion(fields)
		if err != nil {
			var zero T
			return zero, "", err
		}
		if typed {
			return decodeTypedSnapshot(data, version, options)
		}
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

func typedSnapshotVersion(fields map[string]json.RawMessage) (string, bool, error) {
	rawVersion, ok := fields["baselineSchemaVersion"]
	if !ok {
		return "", false, nil
	}

	var version string
	if err := json.Unmarshal(rawVersion, &version); err != nil {
		return "", false, fmt.Errorf("invalid baseline schema version discriminator: %w", err)
	}
	if strings.TrimSpace(string(rawVersion)) == "null" {
		return "", false, fmt.Errorf("invalid baseline schema version discriminator: must be a non-empty string")
	}
	if strings.TrimSpace(version) == "" {
		if hasTypedSnapshotFields(fields) {
			return "", false, fmt.Errorf("invalid baseline schema version discriminator: must be a non-empty string")
		}
		return "", false, nil
	}
	return version, true, nil
}

func decodeTypedSnapshot[T any](data []byte, version string, options SnapshotDecodeOptions[T]) (T, string, error) {
	var snapshot Snapshot[T]
	if err := json.Unmarshal(data, &snapshot); err != nil {
		var zero T
		return zero, "", fmt.Errorf("decode typed baseline snapshot: %w", err)
	}
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

func hasTypedSnapshotFields(fields map[string]json.RawMessage) bool {
	for _, field := range []string{"key", "savedAt", "report"} {
		if _, ok := fields[field]; ok {
			return true
		}
	}
	return false
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
