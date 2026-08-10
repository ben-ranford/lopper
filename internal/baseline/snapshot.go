package baseline

import (
	"bytes"
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
	version, typed, err := inspectSnapshotEnvelope(data)
	if err != nil {
		var zero T
		return zero, "", err
	}
	if typed {
		if version != SnapshotSchemaVersion {
			var zero T
			return zero, "", snapshotSchemaError(version, options.UnsupportedSchema)
		}
		return decodeTypedSnapshot(data, options)
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

func decodeTypedSnapshot[T any](data []byte, options SnapshotDecodeOptions[T]) (T, string, error) {
	var snapshot Snapshot[T]
	if err := json.Unmarshal(data, &snapshot); err != nil {
		var zero T
		return zero, "", fmt.Errorf("decode typed baseline snapshot: %w", err)
	}
	v := snapshot.Report
	if options.Repair != nil {
		v = options.Repair(v)
	}
	return v, strings.TrimSpace(snapshot.Key), nil
}

func inspectSnapshotEnvelope(data []byte) (version string, typed bool, returnErr error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return "", false, nil
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return "", false, nil
	}

	var hasVersion, hasTypedFields bool
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return "", false, nil
		}
		field, ok := fieldToken.(string)
		if !ok {
			return "", false, nil
		}
		switch {
		case strings.EqualFold(field, "baselineSchemaVersion"):
			if hasVersion {
				return "", false, fmt.Errorf("invalid baseline schema version discriminator: duplicate case-folded field")
			}
			hasVersion = true
			version, err = decodeSnapshotVersionToken(decoder)
			if err != nil {
				return "", false, err
			}
		case isTypedSnapshotField(field):
			hasTypedFields = true
			if err := skipJSONValue(decoder); err != nil {
				return "", false, nil
			}
		default:
			if err := skipJSONValue(decoder); err != nil {
				return "", false, nil
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return "", false, nil
	}
	if !hasVersion {
		if hasTypedFields {
			return "", false, fmt.Errorf("invalid baseline schema version discriminator: typed envelope requires a version")
		}
		return "", false, nil
	}
	if strings.TrimSpace(version) == "" {
		if hasTypedFields {
			return "", false, fmt.Errorf("invalid baseline schema version discriminator: must be a non-empty string")
		}
		return "", false, nil
	}
	return version, true, nil
}

func decodeSnapshotVersionToken(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	if delimiter, ok := token.(json.Delim); ok {
		if err := skipJSONDelimitedValue(decoder, delimiter); err != nil {
			return "", err
		}
		return "", fmt.Errorf("invalid baseline schema version discriminator: must be a string")
	}
	if token == nil {
		return "", fmt.Errorf("invalid baseline schema version discriminator: must be a non-empty string")
	}
	if _, ok := token.(float64); ok {
		return "", fmt.Errorf("invalid baseline schema version discriminator: json: cannot unmarshal number into Go value of type string")
	}
	version, ok := token.(string)
	if !ok {
		return "", fmt.Errorf("invalid baseline schema version discriminator: must be a string")
	}
	return version, nil
}

func isTypedSnapshotField(field string) bool {
	return strings.EqualFold(field, "key") || strings.EqualFold(field, "savedAt") || strings.EqualFold(field, "report")
}

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		return skipJSONDelimitedValue(decoder, delimiter)
	}
	return nil
}

func skipJSONDelimitedValue(decoder *json.Decoder, delimiter json.Delim) error {
	for decoder.More() {
		if delimiter == '{' {
			if _, err := decoder.Token(); err != nil {
				return err
			}
		}
		if err := skipJSONValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
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
