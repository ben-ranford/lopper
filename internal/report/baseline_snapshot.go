package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/safeio"
)

const BaselineSnapshotSchemaVersion = "1.0.0"

var ErrBaselineAlreadyExists = errors.New("baseline snapshot already exists")

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

func SaveSnapshot(dir string, key string, rep Report, now time.Time) (path string, err error) {
	trimmedDir := strings.TrimSpace(dir)
	trimmedKey := strings.TrimSpace(key)
	if trimmedDir == "" {
		return "", fmt.Errorf("baseline store directory is required")
	}
	if trimmedKey == "" {
		return "", fmt.Errorf("baseline key is required")
	}

	if err := os.MkdirAll(trimmedDir, 0o750); err != nil {
		return "", err
	}

	sanitizedFileName := sanitizeBaselineKey(trimmedKey) + ".json"
	snapshotPath := filepath.Join(trimmedDir, sanitizedFileName)
	root, err := os.OpenRoot(trimmedDir)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	file, err := root.OpenFile(sanitizedFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: key %q (%s)", ErrBaselineAlreadyExists, trimmedKey, snapshotPath)
		}
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if err != nil {
			if removeErr := os.Remove(snapshotPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
	}()

	snapshot := BaselineSnapshot{
		BaselineSchemaVersion: BaselineSnapshotSchemaVersion,
		Key:                   trimmedKey,
		SavedAt:               now.UTC(),
		Report:                normalizeSnapshotReport(rep),
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(snapshot); err != nil {
		return "", err
	}

	return snapshotPath, nil
}

func BaselineSnapshotPath(dir, key string) string {
	return filepath.Join(strings.TrimSpace(dir), sanitizeBaselineKey(strings.TrimSpace(key))+".json")
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

func sanitizeBaselineKey(key string) string {
	if key == "" {
		return "baseline"
	}
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	sanitized := strings.Trim(b.String(), "._-")
	if sanitized == "" {
		return "baseline"
	}
	return sanitized
}
