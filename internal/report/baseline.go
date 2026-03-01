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

var (
	ErrBaselineMissing       = errors.New("baseline report is missing summary data")
	ErrBaselineAlreadyExists = errors.New("baseline snapshot already exists")
)

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

func SaveSnapshot(dir string, key string, rep Report, now time.Time) (string, error) {
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
	path := filepath.Join(trimmedDir, sanitizedFileName)
	root, err := os.OpenRoot(trimmedDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.OpenFile(sanitizedFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: key %q (%s)", ErrBaselineAlreadyExists, trimmedKey, path)
		}
		return "", err
	}
	defer file.Close()

	snapshot := BaselineSnapshot{
		BaselineSchemaVersion: BaselineSnapshotSchemaVersion,
		Key:                   trimmedKey,
		SavedAt:               now.UTC(),
		Report:                normalizeSnapshotReport(rep),
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		return "", err
	}

	return path, nil
}

func BaselineSnapshotPath(dir string, key string) string {
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

func ComputeSummary(dependencies []DependencyReport) *Summary {
	if len(dependencies) == 0 {
		return nil
	}

	summary := Summary{DependencyCount: len(dependencies)}
	for _, dep := range dependencies {
		summary.UsedExportsCount += dep.UsedExportsCount
		summary.TotalExportsCount += dep.TotalExportsCount
	}
	if summary.TotalExportsCount > 0 {
		summary.UsedPercent = (float64(summary.UsedExportsCount) / float64(summary.TotalExportsCount)) * 100
	}

	return &summary
}

func ComputeLanguageBreakdown(dependencies []DependencyReport) []LanguageSummary {
	if len(dependencies) == 0 {
		return nil
	}

	byLanguage := make(map[string]*LanguageSummary)
	for _, dep := range dependencies {
		languageID := dep.Language
		if languageID == "" {
			continue
		}
		current, ok := byLanguage[languageID]
		if !ok {
			current = &LanguageSummary{Language: languageID}
			byLanguage[languageID] = current
		}
		current.DependencyCount++
		current.UsedExportsCount += dep.UsedExportsCount
		current.TotalExportsCount += dep.TotalExportsCount
	}

	breakdown := make([]LanguageSummary, 0, len(byLanguage))
	if len(byLanguage) == 0 {
		return nil
	}
	for _, item := range byLanguage {
		if item.TotalExportsCount > 0 {
			item.UsedPercent = (float64(item.UsedExportsCount) / float64(item.TotalExportsCount)) * 100
		}
		breakdown = append(breakdown, *item)
	}
	sort.Slice(breakdown, func(i, j int) bool {
		return breakdown[i].Language < breakdown[j].Language
	})
	return breakdown
}

func ApplyBaseline(current Report, baseline Report) (Report, error) {
	return ApplyBaselineWithKeys(current, baseline, "", "")
}

func ApplyBaselineWithKeys(current Report, baseline Report, baselineKey string, currentKey string) (Report, error) {
	currentSummary := current.Summary
	if currentSummary == nil {
		currentSummary = ComputeSummary(current.Dependencies)
		current.Summary = currentSummary
	}

	baselineSummary := baseline.Summary
	if baselineSummary == nil {
		baselineSummary = ComputeSummary(baseline.Dependencies)
	}
	if baselineSummary == nil {
		return current, ErrBaselineMissing
	}
	if baselineSummary.TotalExportsCount == 0 {
		return current, fmt.Errorf("baseline total exports count is zero")
	}

	currentWaste, ok := WastePercent(currentSummary)
	if !ok {
		return current, fmt.Errorf("current report has no export totals")
	}
	baselineWaste, _ := WastePercent(baselineSummary)
	delta := currentWaste - baselineWaste
	current.WasteIncreasePercent = &delta

	comparison := ComputeBaselineComparison(current, baseline)
	comparison.BaselineKey = strings.TrimSpace(baselineKey)
	comparison.CurrentKey = strings.TrimSpace(currentKey)
	current.BaselineComparison = &comparison

	return current, nil
}

func ComputeBaselineComparison(current, baseline Report) BaselineComparison {
	currentSummary := current.Summary
	if currentSummary == nil {
		currentSummary = ComputeSummary(current.Dependencies)
	}
	baselineSummary := baseline.Summary
	if baselineSummary == nil {
		baselineSummary = ComputeSummary(baseline.Dependencies)
	}

	currentUnused := sumEstimatedUnusedBytes(current.Dependencies)
	baselineUnused := sumEstimatedUnusedBytes(baseline.Dependencies)

	comparison := BaselineComparison{
		SummaryDelta: SummaryDelta{
			DependencyCountDelta:   safeSummaryField(currentSummary, func(s *Summary) int { return s.DependencyCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.DependencyCount }),
			UsedExportsCountDelta:  safeSummaryField(currentSummary, func(s *Summary) int { return s.UsedExportsCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.UsedExportsCount }),
			TotalExportsCountDelta: safeSummaryField(currentSummary, func(s *Summary) int { return s.TotalExportsCount }) - safeSummaryField(baselineSummary, func(s *Summary) int { return s.TotalExportsCount }),
			UsedPercentDelta:       safeSummaryFloat(currentSummary, func(s *Summary) float64 { return s.UsedPercent }) - safeSummaryFloat(baselineSummary, func(s *Summary) float64 { return s.UsedPercent }),
			WastePercentDelta:      wasteFromSummary(currentSummary) - wasteFromSummary(baselineSummary),
			UnusedBytesDelta:       currentUnused - baselineUnused,
		},
	}

	currentByKey := make(map[string]DependencyReport, len(current.Dependencies))
	for _, dep := range current.Dependencies {
		currentByKey[dependencyKey(dep)] = dep
	}
	baselineByKey := make(map[string]DependencyReport, len(baseline.Dependencies))
	for _, dep := range baseline.Dependencies {
		baselineByKey[dependencyKey(dep)] = dep
	}

	keys := make([]string, 0, len(currentByKey)+len(baselineByKey))
	seen := make(map[string]struct{}, len(currentByKey)+len(baselineByKey))
	for key := range currentByKey {
		keys = append(keys, key)
		seen[key] = struct{}{}
	}
	for key := range baselineByKey {
		if _, ok := seen[key]; ok {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		curr, hasCurrent := currentByKey[key]
		base, hasBaseline := baselineByKey[key]

		delta, ok := dependencyDelta(curr, hasCurrent, base, hasBaseline)
		if !ok {
			comparison.UnchangedRows++
			continue
		}
		comparison.Dependencies = append(comparison.Dependencies, delta)
		switch delta.Kind {
		case DependencyDeltaAdded:
			comparison.Added = append(comparison.Added, delta)
		case DependencyDeltaRemoved:
			comparison.Removed = append(comparison.Removed, delta)
		}
		if delta.WastePercentDelta > 0 {
			comparison.Regressions = append(comparison.Regressions, delta)
		} else if delta.WastePercentDelta < 0 {
			comparison.Progressions = append(comparison.Progressions, delta)
		}
	}

	return comparison
}

func sumEstimatedUnusedBytes(dependencies []DependencyReport) int64 {
	total := int64(0)
	for _, dep := range dependencies {
		total += dep.EstimatedUnusedBytes
	}
	return total
}

func safeSummaryField(summary *Summary, selector func(*Summary) int) int {
	if summary == nil {
		return 0
	}
	return selector(summary)
}

func safeSummaryFloat(summary *Summary, selector func(*Summary) float64) float64 {
	if summary == nil {
		return 0
	}
	return selector(summary)
}

func wasteFromSummary(summary *Summary) float64 {
	if summary == nil || summary.TotalExportsCount == 0 {
		return 0
	}
	return 100 - summary.UsedPercent
}

func dependencyKey(dep DependencyReport) string {
	return dep.Language + "\x00" + dep.Name
}

func dependencyDelta(curr DependencyReport, hasCurrent bool, base DependencyReport, hasBaseline bool) (DependencyDelta, bool) {
	name := curr.Name
	language := curr.Language
	if !hasCurrent {
		name = base.Name
		language = base.Language
	}

	delta := DependencyDelta{
		Language: language,
		Name:     name,
	}

	switch {
	case hasCurrent && !hasBaseline:
		delta.Kind = DependencyDeltaAdded
		delta.UsedExportsCountDelta = curr.UsedExportsCount
		delta.TotalExportsCountDelta = curr.TotalExportsCount
		delta.UsedPercentDelta = curr.UsedPercent
		delta.EstimatedUnusedBytesDelta = curr.EstimatedUnusedBytes
		delta.WastePercentDelta = wasteFromDependency(curr)
		return delta, true
	case !hasCurrent && hasBaseline:
		delta.Kind = DependencyDeltaRemoved
		delta.UsedExportsCountDelta = -base.UsedExportsCount
		delta.TotalExportsCountDelta = -base.TotalExportsCount
		delta.UsedPercentDelta = -base.UsedPercent
		delta.EstimatedUnusedBytesDelta = -base.EstimatedUnusedBytes
		delta.WastePercentDelta = -wasteFromDependency(base)
		return delta, true
	default:
		delta.Kind = DependencyDeltaChanged
		delta.UsedExportsCountDelta = curr.UsedExportsCount - base.UsedExportsCount
		delta.TotalExportsCountDelta = curr.TotalExportsCount - base.TotalExportsCount
		delta.UsedPercentDelta = curr.UsedPercent - base.UsedPercent
		delta.EstimatedUnusedBytesDelta = curr.EstimatedUnusedBytes - base.EstimatedUnusedBytes
		delta.WastePercentDelta = wasteFromDependency(curr) - wasteFromDependency(base)
		if delta.UsedExportsCountDelta == 0 &&
			delta.TotalExportsCountDelta == 0 &&
			delta.UsedPercentDelta == 0 &&
			delta.EstimatedUnusedBytesDelta == 0 {
			return DependencyDelta{}, false
		}
		return delta, true
	}
}

func wasteFromDependency(dep DependencyReport) float64 {
	if dep.TotalExportsCount == 0 {
		return 0
	}
	usedPercent := dep.UsedPercent
	if usedPercent == 0 && dep.UsedExportsCount > 0 {
		usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
	}
	return 100 - usedPercent
}

func WastePercent(summary *Summary) (float64, bool) {
	if summary == nil {
		return 0, false
	}
	if summary.TotalExportsCount == 0 {
		return 0, false
	}
	return 100 - summary.UsedPercent, true
}
