package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	baselineutil "github.com/ben-ranford/lopper/internal/baseline"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const BaselineSnapshotSchemaVersion = "1.0.0"

var ErrBaselineAlreadyExists = errors.New("baseline snapshot already exists")

var ErrBaselineKeyMismatch = errors.New("baseline snapshot key does not match requested key")

type BaselineSnapshot struct {
	BaselineSchemaVersion string    `json:"baselineSchemaVersion"`
	Key                   string    `json:"key"`
	SavedAt               time.Time `json:"savedAt"`
	Report                Report    `json:"report"`
}

func Load(path string) (Report, error) {
	rep, _, err := LoadWithKey(path)
	if err != nil {
		return Report{}, err
	}
	return rep, nil
}

func LoadWithKey(path string) (Report, string, error) {
	data, err := safeio.ReadFile(path)
	if err != nil {
		return Report{}, "", err
	}
	return decodeBaselineSnapshot(data)
}

func decodeBaselineSnapshot(data []byte) (Report, string, error) {
	var snapshot BaselineSnapshot
	if err := json.Unmarshal(data, &snapshot); err == nil && strings.TrimSpace(snapshot.BaselineSchemaVersion) != "" {
		if snapshot.BaselineSchemaVersion != BaselineSnapshotSchemaVersion {
			return Report{}, "", fmt.Errorf("unsupported baseline schema version: %s", snapshot.BaselineSchemaVersion)
		}
		if snapshot.Report.Summary == nil {
			snapshot.Report.Summary = ComputeSummary(snapshot.Report.Dependencies)
		}
		if len(snapshot.Report.LanguageBreakdown) == 0 {
			snapshot.Report.LanguageBreakdown = ComputeLanguageBreakdown(snapshot.Report.Dependencies)
		}
		return snapshot.Report, strings.TrimSpace(snapshot.Key), nil
	}

	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return Report{}, "", err
	}
	if rep.Summary == nil {
		rep.Summary = ComputeSummary(rep.Dependencies)
	}
	if len(rep.LanguageBreakdown) == 0 {
		rep.LanguageBreakdown = ComputeLanguageBreakdown(rep.Dependencies)
	}
	return rep, "", nil
}

func LoadSnapshot(dir, key string) (Report, string, string, error) {
	trimmedKey := strings.TrimSpace(key)
	path := ResolveBaselineSnapshotPath(dir, trimmedKey)
	data, err := baselineutil.ReadStoreEntry(dir, filepath.Base(path), baselineutil.MaxSnapshotBytes)
	if err != nil {
		return Report{}, "", path, err
	}
	rep, loadedKey, err := decodeBaselineSnapshot(data)
	if err != nil {
		return Report{}, "", path, err
	}
	if err := ValidateBaselineSnapshotKey(trimmedKey, loadedKey); err != nil {
		return Report{}, loadedKey, path, err
	}
	return rep, loadedKey, path, nil
}

func ValidateBaselineSnapshotKey(requestedKey, storedKey string) error {
	requestedKey = strings.TrimSpace(requestedKey)
	storedKey = strings.TrimSpace(storedKey)
	if requestedKey == storedKey && requestedKey != "" {
		return nil
	}
	return fmt.Errorf("%w: requested %q, stored %q", ErrBaselineKeyMismatch, requestedKey, storedKey)
}

func SaveSnapshot(dir string, key string, rep Report, now time.Time) (string, error) {
	return baselineutil.SaveJSON(dir, key, ErrBaselineAlreadyExists, func(trimmedKey string) BaselineSnapshot {
		return newBaselineSnapshot(trimmedKey, rep, now)
	})
}

func BaselineSnapshotPath(dir, key string) string {
	return baselineutil.SnapshotPath(dir, key)
}

func ResolveBaselineSnapshotPath(dir, key string) string {
	return baselineutil.ResolveSnapshotPath(dir, key)
}

func newBaselineSnapshot(key string, rep Report, now time.Time) BaselineSnapshot {
	return BaselineSnapshot{
		BaselineSchemaVersion: BaselineSnapshotSchemaVersion,
		Key:                   key,
		SavedAt:               now.UTC(),
		Report:                normalizeSnapshotReport(rep),
	}
}

func normalizeSnapshotReport(rep Report) Report {
	normalized := rep
	normalized.Dependencies = append([]DependencyReport(nil), rep.Dependencies...)
	sort.Slice(normalized.Dependencies, func(i, j int) bool {
		if normalized.Dependencies[i].Language != normalized.Dependencies[j].Language {
			return normalized.Dependencies[i].Language < normalized.Dependencies[j].Language
		}
		return normalized.Dependencies[i].Name < normalized.Dependencies[j].Name
	})
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
