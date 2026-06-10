package dashboard

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type Format string

const (
	FormatJSON Format = "json"
	FormatCSV  Format = "csv"
	FormatHTML Format = "html"
)

var ErrUnknownFormat = errors.New("unknown dashboard format")

func ParseFormat(value string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(FormatJSON):
		return FormatJSON, nil
	case string(FormatCSV):
		return FormatCSV, nil
	case string(FormatHTML):
		return FormatHTML, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnknownFormat, value)
	}
}

type RepoInput struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

type RepoResult struct {
	Name                  string   `json:"name"`
	Path                  string   `json:"path"`
	Language              string   `json:"language,omitempty"`
	DependencyCount       int      `json:"dependency_count"`
	WasteCandidateCount   int      `json:"waste_candidate_count"`
	WasteCandidatePercent float64  `json:"waste_candidate_percent"`
	TopRiskSeverity       string   `json:"top_risk_severity,omitempty"`
	CriticalCVEs          int      `json:"critical_cves"`
	DeniedLicenseCount    int      `json:"denied_license_count"`
	Warnings              []string `json:"warnings,omitempty"`
	Error                 string   `json:"error,omitempty"`
}

type CrossRepoDependency struct {
	Name         string   `json:"name"`
	Repositories []string `json:"repositories"`
	Count        int      `json:"count"`
}

type Summary struct {
	TotalRepos           int `json:"total_repos"`
	TotalDeps            int `json:"total_deps"`
	TotalWasteCandidates int `json:"total_waste_candidates"`
	CrossRepoDuplicates  int `json:"cross_repo_duplicates"`
	CriticalCVEs         int `json:"critical_cves"`
}

type BaselineComparison struct {
	BaselineKey  string       `json:"baseline_key"`
	CurrentKey   string       `json:"current_key,omitempty"`
	SummaryDelta SummaryDelta `json:"summary_delta"`
	RepoDeltas   []RepoDelta  `json:"repo_deltas,omitempty"`
	Added        []RepoDelta  `json:"added,omitempty"`
	Removed      []RepoDelta  `json:"removed,omitempty"`
	Changed      []RepoDelta  `json:"changed,omitempty"`
}

type SummaryDelta struct {
	TotalReposDelta           int `json:"total_repos_delta"`
	TotalDepsDelta            int `json:"total_deps_delta"`
	TotalWasteCandidatesDelta int `json:"total_waste_candidates_delta"`
	CrossRepoDuplicatesDelta  int `json:"cross_repo_duplicates_delta"`
	CriticalCVEsDelta         int `json:"critical_cves_delta"`
}

type RepoDeltaKind string

const (
	RepoDeltaAdded   RepoDeltaKind = "added"
	RepoDeltaRemoved RepoDeltaKind = "removed"
	RepoDeltaChanged RepoDeltaKind = "changed"
)

type RepoDelta struct {
	Kind                       RepoDeltaKind `json:"kind"`
	Name                       string        `json:"name"`
	Path                       string        `json:"path,omitempty"`
	DependencyCountDelta       int           `json:"dependency_count_delta"`
	WasteCandidateCountDelta   int           `json:"waste_candidate_count_delta"`
	WasteCandidatePercentDelta float64       `json:"waste_candidate_percent_delta"`
	CriticalCVEsDelta          int           `json:"critical_cves_delta"`
	DeniedLicenseCountDelta    int           `json:"denied_license_count_delta"`
	CurrentError               string        `json:"current_error,omitempty"`
	BaselineError              string        `json:"baseline_error,omitempty"`
}

type Report struct {
	GeneratedAt        time.Time             `json:"generated_at"`
	Repos              []RepoResult          `json:"repos"`
	Summary            Summary               `json:"summary"`
	BaselineComparison *BaselineComparison   `json:"baseline_comparison,omitempty"`
	CrossRepoDeps      []CrossRepoDependency `json:"cross_repo_deps,omitempty"`
	SourceWarnings     []string              `json:"warnings,omitempty"`
}
