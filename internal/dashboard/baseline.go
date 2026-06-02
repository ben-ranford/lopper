package dashboard

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

var ErrBaselineAlreadyExists = errors.New("dashboard baseline snapshot already exists")

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
			return Report{}, "", fmt.Errorf("unsupported dashboard baseline schema version: %s", snapshot.BaselineSchemaVersion)
		}
		if snapshot.Report.Summary == (Summary{}) {
			snapshot.Report.Summary = computeSummary(snapshot.Report)
		}
		return snapshot.Report, strings.TrimSpace(snapshot.Key), nil
	}

	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return Report{}, "", err
	}
	if rep.Summary == (Summary{}) {
		rep.Summary = computeSummary(rep)
	}
	return rep, "", nil
}

func SaveSnapshot(dir string, key string, rep Report, now time.Time) (path string, err error) {
	trimmedDir, trimmedKey, err := validateBaselineSnapshotInputs(dir, key)
	if err != nil {
		return "", err
	}
	if err := rejectBaselineStoreDirSymlink(trimmedDir); err != nil {
		return "", err
	}

	root, file, snapshotPath, err := openBaselineSnapshotWriter(trimmedDir, trimmedKey)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	if err := encodeBaselineSnapshot(file, snapshotPath, newBaselineSnapshot(trimmedKey, rep, now)); err != nil {
		return "", err
	}

	return snapshotPath, nil
}

func BaselineSnapshotPath(dir, key string) string {
	return filepath.Join(strings.TrimSpace(dir), sanitizeBaselineKey(strings.TrimSpace(key))+".json")
}

func ApplyBaselineWithKeys(current, baseline Report, baselineKey, currentKey string) (Report, error) {
	comparison := ComputeBaselineComparison(current, baseline)
	comparison.BaselineKey = strings.TrimSpace(baselineKey)
	comparison.CurrentKey = strings.TrimSpace(currentKey)
	current.BaselineComparison = &comparison
	return current, nil
}

func ComputeBaselineComparison(current, baseline Report) BaselineComparison {
	if current.Summary == (Summary{}) {
		current.Summary = computeSummary(current)
	}
	if baseline.Summary == (Summary{}) {
		baseline.Summary = computeSummary(baseline)
	}

	currentByKey := make(map[string]RepoResult, len(current.Repos))
	for _, repo := range current.Repos {
		currentByKey[repoKey(repo)] = repo
	}
	baselineByKey := make(map[string]RepoResult, len(baseline.Repos))
	for _, repo := range baseline.Repos {
		baselineByKey[repoKey(repo)] = repo
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

	comparison := BaselineComparison{
		SummaryDelta: SummaryDelta{
			TotalReposDelta:           current.Summary.TotalRepos - baseline.Summary.TotalRepos,
			TotalDepsDelta:            current.Summary.TotalDeps - baseline.Summary.TotalDeps,
			TotalWasteCandidatesDelta: current.Summary.TotalWasteCandidates - baseline.Summary.TotalWasteCandidates,
			CrossRepoDuplicatesDelta:  current.Summary.CrossRepoDuplicates - baseline.Summary.CrossRepoDuplicates,
			CriticalCVEsDelta:         current.Summary.CriticalCVEs - baseline.Summary.CriticalCVEs,
		},
	}

	for _, key := range keys {
		curr, hasCurrent := currentByKey[key]
		base, hasBaseline := baselineByKey[key]
		delta, ok := computeRepoDelta(curr, hasCurrent, base, hasBaseline)
		if !ok {
			continue
		}
		comparison.RepoDeltas = append(comparison.RepoDeltas, delta)
		switch delta.Kind {
		case RepoDeltaAdded:
			comparison.Added = append(comparison.Added, delta)
		case RepoDeltaRemoved:
			comparison.Removed = append(comparison.Removed, delta)
		case RepoDeltaChanged:
			comparison.Changed = append(comparison.Changed, delta)
		}
	}

	return comparison
}

func computeRepoDelta(curr RepoResult, hasCurrent bool, base RepoResult, hasBaseline bool) (RepoDelta, bool) {
	name := curr.Name
	path := curr.Path
	if !hasCurrent {
		name = base.Name
		path = base.Path
	}

	delta := RepoDelta{
		Name: name,
		Path: path,
	}

	switch {
	case hasCurrent && !hasBaseline:
		delta.Kind = RepoDeltaAdded
		delta.DependencyCountDelta = curr.DependencyCount
		delta.WasteCandidateCountDelta = curr.WasteCandidateCount
		delta.WasteCandidatePercentDelta = curr.WasteCandidatePercent
		delta.CriticalCVEsDelta = curr.CriticalCVEs
		delta.DeniedLicenseCountDelta = curr.DeniedLicenseCount
		delta.CurrentError = curr.Error
		return delta, true
	case !hasCurrent && hasBaseline:
		delta.Kind = RepoDeltaRemoved
		delta.DependencyCountDelta = -base.DependencyCount
		delta.WasteCandidateCountDelta = -base.WasteCandidateCount
		delta.WasteCandidatePercentDelta = -base.WasteCandidatePercent
		delta.CriticalCVEsDelta = -base.CriticalCVEs
		delta.DeniedLicenseCountDelta = -base.DeniedLicenseCount
		delta.BaselineError = base.Error
		return delta, true
	default:
		delta.Kind = RepoDeltaChanged
		delta.DependencyCountDelta = curr.DependencyCount - base.DependencyCount
		delta.WasteCandidateCountDelta = curr.WasteCandidateCount - base.WasteCandidateCount
		delta.WasteCandidatePercentDelta = curr.WasteCandidatePercent - base.WasteCandidatePercent
		delta.CriticalCVEsDelta = curr.CriticalCVEs - base.CriticalCVEs
		delta.DeniedLicenseCountDelta = curr.DeniedLicenseCount - base.DeniedLicenseCount
		delta.CurrentError = curr.Error
		delta.BaselineError = base.Error
		if delta.DependencyCountDelta == 0 &&
			delta.WasteCandidateCountDelta == 0 &&
			delta.WasteCandidatePercentDelta == 0 &&
			delta.CriticalCVEsDelta == 0 &&
			delta.DeniedLicenseCountDelta == 0 &&
			strings.TrimSpace(curr.Error) == strings.TrimSpace(base.Error) {
			return RepoDelta{}, false
		}
		return delta, true
	}
}

func repoKey(repo RepoResult) string {
	return repo.Name + "\x00" + repo.Path
}

func computeSummary(rep Report) Summary {
	return Summary{
		TotalRepos:           len(rep.Repos),
		TotalDeps:            sumRepoField(rep.Repos, func(repo RepoResult) int { return repo.DependencyCount }),
		TotalWasteCandidates: sumRepoField(rep.Repos, func(repo RepoResult) int { return repo.WasteCandidateCount }),
		CrossRepoDuplicates:  len(rep.CrossRepoDeps),
		CriticalCVEs:         sumRepoField(rep.Repos, func(repo RepoResult) int { return repo.CriticalCVEs }),
	}
}

func sumRepoField(repos []RepoResult, selector func(RepoResult) int) int {
	total := 0
	for _, repo := range repos {
		total += selector(repo)
	}
	return total
}

func validateBaselineSnapshotInputs(dir, key string) (string, string, error) {
	trimmedDir := strings.TrimSpace(dir)
	if trimmedDir == "" {
		return "", "", fmt.Errorf("baseline store directory is required")
	}

	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return "", "", fmt.Errorf("baseline key is required")
	}

	return trimmedDir, trimmedKey, nil
}

func rejectBaselineStoreDirSymlink(dir string) error {
	if info, err := os.Lstat(dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("baseline store directory must not be a symlink: %s", dir)
	}
	return nil
}

func openBaselineSnapshotWriter(dir, key string) (root *os.Root, file *os.File, snapshotPath string, err error) {
	if err = os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, "", err
	}

	sanitizedFileName := sanitizeBaselineKey(key) + ".json"
	snapshotPath = filepath.Join(dir, sanitizedFileName)

	root, err = os.OpenRoot(dir)
	if err != nil {
		return nil, nil, "", err
	}

	file, err = root.OpenFile(sanitizedFileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		if errors.Is(err, os.ErrExist) {
			err = fmt.Errorf("%w: key %q (%s)", ErrBaselineAlreadyExists, key, snapshotPath)
		}
		return nil, nil, "", err
	}

	return root, file, snapshotPath, nil
}

func newBaselineSnapshot(key string, rep Report, now time.Time) BaselineSnapshot {
	return BaselineSnapshot{
		BaselineSchemaVersion: BaselineSnapshotSchemaVersion,
		Key:                   key,
		SavedAt:               now.UTC(),
		Report:                normalizeSnapshotReport(rep),
	}
}

func encodeBaselineSnapshot(file *os.File, snapshotPath string, snapshot BaselineSnapshot) (err error) {
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

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(snapshot)
}

func normalizeSnapshotReport(rep Report) Report {
	normalized := rep
	normalized.Repos = append([]RepoResult(nil), rep.Repos...)
	sort.Slice(normalized.Repos, func(i, j int) bool {
		if normalized.Repos[i].Name != normalized.Repos[j].Name {
			return normalized.Repos[i].Name < normalized.Repos[j].Name
		}
		return normalized.Repos[i].Path < normalized.Repos[j].Path
	})
	if normalized.Summary == (Summary{}) {
		normalized.Summary = computeSummary(normalized)
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
