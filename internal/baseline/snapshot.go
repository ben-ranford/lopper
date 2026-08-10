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
	index := skipSnapshotJSONWhitespace(data, 0)
	if index == len(data) || data[index] != '{' {
		return "", false, nil
	}
	state := snapshotEnvelopeState{}
	index++
	for {
		next, done, err := inspectSnapshotEnvelopeField(data, index, &state)
		if err != nil {
			if strings.HasPrefix(err.Error(), "invalid baseline schema version discriminator:") {
				return "", false, err
			}
			return snapshotEnvelopeInspectionError(state.hasVersion || state.hasTypedFields, err)
		}
		index = next
		if done {
			break
		}
	}
	if skipSnapshotJSONWhitespace(data, index) != len(data) {
		return snapshotEnvelopeInspectionError(state.hasVersion || state.hasTypedFields, fmt.Errorf("unexpected trailing JSON data"))
	}
	return snapshotEnvelopeResult(state)
}

func snapshotEnvelopeResult(state snapshotEnvelopeState) (string, bool, error) {
	if !state.hasVersion {
		if state.hasTypedFields {
			return "", false, fmt.Errorf("invalid baseline schema version discriminator: typed envelope requires a version")
		}
		return "", false, nil
	}
	if strings.TrimSpace(state.version) == "" {
		if state.hasTypedFields {
			return "", false, fmt.Errorf("invalid baseline schema version discriminator: must be a non-empty string")
		}
		return "", false, nil
	}
	return state.version, true, nil
}

type snapshotEnvelopeState struct {
	version        string
	hasVersion     bool
	hasTypedFields bool
}

func inspectSnapshotEnvelopeField(data []byte, index int, state *snapshotEnvelopeState) (int, bool, error) {
	index = skipSnapshotJSONWhitespace(data, index)
	if index >= len(data) {
		return index, false, fmt.Errorf("unexpected end of JSON object")
	}
	if data[index] == '}' {
		return index + 1, true, nil
	}
	field, next, err := decodeSnapshotJSONString(data, index)
	if err != nil {
		return index, false, err
	}
	index = skipSnapshotJSONWhitespace(data, next)
	if index >= len(data) || data[index] != ':' {
		return index, false, fmt.Errorf("expected colon after field %q", field)
	}
	index++
	if strings.EqualFold(field, "baselineSchemaVersion") {
		if state.hasVersion {
			return index, false, fmt.Errorf("invalid baseline schema version discriminator: duplicate case-folded field")
		}
		state.hasVersion = true
		state.version, index, err = decodeSnapshotVersion(data, index)
	} else {
		state.hasTypedFields = state.hasTypedFields || isTypedSnapshotField(field)
		index, err = skipSnapshotJSONValue(data, index)
	}
	if err != nil {
		return index, false, err
	}
	return finishSnapshotEnvelopeField(data, index)
}

func finishSnapshotEnvelopeField(data []byte, index int) (int, bool, error) {
	index = skipSnapshotJSONWhitespace(data, index)
	if index >= len(data) {
		return index, false, fmt.Errorf("unexpected end of JSON object")
	}
	if data[index] == ',' {
		return index + 1, false, nil
	}
	if data[index] == '}' {
		return index + 1, true, nil
	}
	return index, false, fmt.Errorf("expected comma or closing brace")
}

func snapshotEnvelopeInspectionError(typed bool, err error) (string, bool, error) {
	if typed {
		return "", false, fmt.Errorf("inspect typed baseline snapshot envelope: %w", err)
	}
	return "", false, nil
}

func decodeSnapshotVersion(data []byte, index int) (string, int, error) {
	index = skipSnapshotJSONWhitespace(data, index)
	if index >= len(data) {
		return "", index, fmt.Errorf("invalid baseline schema version discriminator: unexpected end of JSON")
	}
	if len(data)-index >= len("null") && string(data[index:index+len("null")]) == "null" {
		return "", index + len("null"), fmt.Errorf("invalid baseline schema version discriminator: must be a non-empty string")
	}
	if data[index] != '"' {
		next, err := skipSnapshotJSONValue(data, index)
		if err != nil {
			return "", index, fmt.Errorf("invalid baseline schema version discriminator: %w", err)
		}
		if data[index] == '-' || (data[index] >= '0' && data[index] <= '9') {
			return "", next, fmt.Errorf("invalid baseline schema version discriminator: json: cannot unmarshal number into Go value of type string")
		}
		return "", next, fmt.Errorf("invalid baseline schema version discriminator: must be a string")
	}
	version, next, err := decodeSnapshotJSONString(data, index)
	if err != nil {
		return "", index, fmt.Errorf("invalid baseline schema version discriminator: %w", err)
	}
	return version, next, nil
}

func skipSnapshotJSONWhitespace(data []byte, index int) int {
	for index < len(data) && (data[index] == ' ' || data[index] == '\n' || data[index] == '\r' || data[index] == '\t') {
		index++
	}
	return index
}

func decodeSnapshotJSONString(data []byte, index int) (string, int, error) {
	next, err := skipSnapshotJSONString(data, index)
	if err != nil {
		return "", index, err
	}
	var value string
	if err := json.Unmarshal(data[index:next], &value); err != nil {
		return "", index, err
	}
	return value, next, nil
}

func skipSnapshotJSONString(data []byte, index int) (int, error) {
	if index >= len(data) || data[index] != '"' {
		return index, fmt.Errorf("expected JSON string")
	}
	for index++; index < len(data); index++ {
		switch data[index] {
		case '"':
			return index + 1, nil
		case '\\':
			index++
			if index >= len(data) {
				return index, fmt.Errorf("unterminated JSON string escape")
			}
		case '\n', '\r':
			return index, fmt.Errorf("invalid control character in JSON string")
		}
	}
	return index, fmt.Errorf("unterminated JSON string")
}

func skipSnapshotJSONValue(data []byte, index int) (int, error) {
	index = skipSnapshotJSONWhitespace(data, index)
	if index >= len(data) {
		return index, fmt.Errorf("unexpected end of JSON value")
	}
	depth := 0
	for current := index; current < len(data); current++ {
		if data[current] == '"' {
			next, err := skipSnapshotJSONString(data, current)
			if err != nil {
				return next, err
			}
			current = next - 1
			continue
		}
		next, done := skipSnapshotJSONValueDelimiter(data[current], current, &depth)
		if done {
			return next, nil
		}
	}
	if depth != 0 {
		return len(data), fmt.Errorf("unexpected end of JSON value")
	}
	return len(data), nil
}

func skipSnapshotJSONValueDelimiter(value byte, index int, depth *int) (int, bool) {
	switch value {
	case '{', '[':
		*depth++
	case '}', ']':
		if *depth == 0 {
			return index, true
		}
		*depth--
		if *depth == 0 {
			return index + 1, true
		}
	case ',', ' ', '\n', '\r', '\t':
		if *depth == 0 {
			return index, true
		}
	}
	return 0, false
}

func isTypedSnapshotField(field string) bool {
	return strings.EqualFold(field, "key") || strings.EqualFold(field, "savedAt") || strings.EqualFold(field, "report")
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
