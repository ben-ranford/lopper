package baseline

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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
	Normalize         func(T) T
	UnsupportedSchema func(string) error
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
		version := strings.TrimSpace(snapshot.BaselineSchemaVersion)
		if version != SnapshotSchemaVersion {
			var zero T
			return zero, "", snapshotSchemaError(version, options.UnsupportedSchema)
		}
		v := snapshot.Report
		if options.Normalize != nil {
			v = options.Normalize(v)
		}
		return v, strings.TrimSpace(snapshot.Key), nil
	}

	v, err := decodeLegacySnapshot(data, options.DecodeLegacy)
	if err != nil {
		var zero T
		return zero, "", err
	}
	if options.Normalize != nil {
		v = options.Normalize(v)
	}
	return v, "", nil
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
