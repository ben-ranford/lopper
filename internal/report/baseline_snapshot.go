package report

import (
	"errors"
	"fmt"
	"strings"
	"time"

	baselineutil "github.com/ben-ranford/lopper/internal/baseline"
)

const BaselineSnapshotSchemaVersion = baselineutil.SnapshotSchemaVersion

var ErrBaselineAlreadyExists = errors.New("baseline snapshot already exists")

var ErrBaselineKeyMismatch = errors.New("baseline snapshot key does not match requested key")

type BaselineSnapshot = baselineutil.Snapshot[Report]

func Load(path string) (Report, error) {
	rep, _, err := LoadWithKey(path)
	if err != nil {
		return Report{}, err
	}
	return rep, nil
}

func LoadWithKey(path string) (Report, string, error) {
	return baselineutil.LoadSnapshotFile(path, baselineutil.SnapshotDecodeOptions[Report]{
		Normalize:         normalizeSnapshotReport,
		UnsupportedSchema: unsupportedBaselineSnapshotSchemaError,
	})
}

func decodeBaselineSnapshot(data []byte) (Report, string, error) {
	return baselineutil.DecodeSnapshot(data, baselineutil.SnapshotDecodeOptions[Report]{
		Normalize:         normalizeSnapshotReport,
		UnsupportedSchema: unsupportedBaselineSnapshotSchemaError,
	})
}

func LoadSnapshot(dir, key string) (Report, string, string, error) {
	return baselineutil.LoadStoreSnapshot(dir, key, baselineutil.MaxSnapshotBytes, decodeBaselineSnapshot, ValidateBaselineSnapshotKey)
}

func ValidateBaselineSnapshotKey(requestedKey, storedKey string) error {
	return baselineutil.ValidateSnapshotKey(requestedKey, storedKey, ErrBaselineKeyMismatch)
}

func SaveSnapshot(dir string, key string, rep Report, now time.Time) (string, error) {
	return baselineutil.SaveSnapshot(dir, key, now, rep, ErrBaselineAlreadyExists, normalizeSnapshotReport)
}

func BaselineSnapshotPath(dir, key string) string {
	return baselineutil.SnapshotPath(dir, key)
}

func ResolveBaselineSnapshotPath(dir, key string) string {
	return baselineutil.ResolveSnapshotPath(dir, key)
}

func newBaselineSnapshot(key string, rep Report, now time.Time) BaselineSnapshot {
	return baselineutil.NewSnapshot(key, rep, now, normalizeSnapshotReport)
}

func normalizeSnapshotReport(rep Report) Report {
	normalized := rep
	normalized.Dependencies = baselineutil.SortedCopyByStrings(rep.Dependencies, func(dependency DependencyReport) string { return dependency.Language }, func(dependency DependencyReport) string { return dependency.Name })
	if normalized.Summary == nil {
		normalized.Summary = ComputeSummary(normalized.Dependencies)
	}
	if len(normalized.LanguageBreakdown) == 0 {
		normalized.LanguageBreakdown = ComputeLanguageBreakdown(normalized.Dependencies)
	}
	if strings.TrimSpace(normalized.SchemaVersion) == "" {
		normalized.SchemaVersion = SchemaVersion
	}
	return normalized
}

func unsupportedBaselineSnapshotSchemaError(version string) error {
	return fmt.Errorf("unsupported baseline schema version: %s", version)
}
