package report

import (
	"errors"
	"fmt"
	"strings"
	"time"

	baselineutil "github.com/ben-ranford/lopper/internal/baseline"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const BaselineSnapshotSchemaVersion = baselineutil.SnapshotSchemaVersion

var ErrBaselineAlreadyExists = errors.New("baseline snapshot already exists")

var ErrBaselineKeyMismatch = errors.New("baseline snapshot key does not match requested key")

type BaselineSnapshot = baselineutil.Snapshot[Report]

var baselineSnapshots = baselineutil.SnapshotStore[Report]{
	Repair:            repairSnapshotReport,
	Normalize:         normalizeSnapshotReport,
	UnsupportedSchema: unsupportedBaselineSnapshotSchemaError,
	ValidateKey:       ValidateBaselineSnapshotKey,
	ExistsErr:         ErrBaselineAlreadyExists,
}

func Load(path string) (Report, error) {
	rep, _, err := LoadWithKey(path)
	if err != nil {
		return Report{}, err
	}
	return rep, nil
}

func LoadWithKey(path string) (Report, string, error) {
	return baselineutil.LoadConfiguredSnapshot(path, baselineSnapshots)
}

func LoadSnapshot(dir, key string) (Report, string, string, error) {
	return baselineutil.LoadConfiguredStoreSnapshot(dir, key, baselineutil.MaxSnapshotBytes, baselineSnapshots)
}

func ValidateBaselineSnapshotKey(requestedKey, storedKey string) error {
	return baselineutil.ValidateSnapshotKey(requestedKey, storedKey, ErrBaselineKeyMismatch)
}

func SaveSnapshot(dir string, key string, rep Report, now time.Time) (string, error) {
	return baselineutil.SaveConfiguredSnapshot(dir, key, now, rep, baselineSnapshots)
}

func SaveSnapshotWithinRoot(root *safeio.WriteRoot, dir, displayDir, key string, rep Report, now time.Time) (string, error) {
	return baselineutil.SaveConfiguredSnapshotWithinRoot(root, dir, displayDir, key, now, rep, baselineSnapshots)
}

func BaselineSnapshotPath(dir, key string) string {
	return baselineutil.SnapshotPath(dir, key)
}

func ResolveBaselineSnapshotPath(dir, key string) string {
	return baselineutil.ResolveSnapshotPath(dir, key)
}

func newBaselineSnapshot(key string, rep Report, now time.Time) BaselineSnapshot {
	return baselineutil.NewConfiguredSnapshot(key, rep, now, baselineSnapshots)
}

func normalizeSnapshotReport(rep Report) Report {
	normalized := rep
	normalized.Dependencies = baselineutil.SortedCopyByStrings(rep.Dependencies, func(dependency DependencyReport) string { return dependency.Language }, func(dependency DependencyReport) string { return dependency.Name })
	normalized = repairSnapshotReport(normalized)
	if strings.TrimSpace(normalized.SchemaVersion) == "" {
		normalized.SchemaVersion = SchemaVersion
	}
	return normalized
}

func repairSnapshotReport(rep Report) Report {
	repaired := rep
	if repaired.Summary == nil {
		repaired.Summary = ComputeSummary(repaired.Dependencies)
	}
	if len(repaired.LanguageBreakdown) == 0 {
		repaired.LanguageBreakdown = ComputeLanguageBreakdown(repaired.Dependencies)
	}
	return repaired
}

func unsupportedBaselineSnapshotSchemaError(version string) error {
	return fmt.Errorf("unsupported baseline schema version: %s", version)
}
