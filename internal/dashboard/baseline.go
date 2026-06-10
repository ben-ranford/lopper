package dashboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	baselineutil "github.com/ben-ranford/lopper/internal/baseline"
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

func SaveSnapshot(dir string, key string, rep Report, now time.Time) (string, error) {
	return baselineutil.SaveJSON(dir, key, ErrBaselineAlreadyExists, func(trimmedKey string) BaselineSnapshot {
		return newBaselineSnapshot(trimmedKey, rep, now)
	})
}

func BaselineSnapshotPath(dir, key string) string {
	return baselineutil.SnapshotPath(dir, key)
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
