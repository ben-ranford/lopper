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
	Name           string       `json:"name"`
	Path           string       `json:"path"`
	RepoURL        string       `json:"repo_url,omitempty"`
	Revision       RepoRevision `json:"revision,omitempty"`
	ResolvedCommit string       `json:"resolved_commit,omitempty"`
	Language       string       `json:"language,omitempty"`
}

type RepoRevision struct {
	Branch string `json:"branch,omitempty"`
	Tag    string `json:"tag,omitempty"`
	Commit string `json:"commit,omitempty"`
}

func (r *RepoRevision) IsZero() bool {
	if r == nil {
		return true
	}
	return strings.TrimSpace(r.Branch) == "" && strings.TrimSpace(r.Tag) == "" && strings.TrimSpace(r.Commit) == ""
}

func (r *RepoRevision) Kind() string {
	if r == nil {
		return ""
	}
	switch {
	case strings.TrimSpace(r.Branch) != "":
		return "branch"
	case strings.TrimSpace(r.Tag) != "":
		return "tag"
	case strings.TrimSpace(r.Commit) != "":
		return "commit"
	default:
		return ""
	}
}

func (r *RepoRevision) Value() string {
	if r == nil {
		return ""
	}
	switch {
	case strings.TrimSpace(r.Branch) != "":
		return strings.TrimSpace(r.Branch)
	case strings.TrimSpace(r.Tag) != "":
		return strings.TrimSpace(r.Tag)
	case strings.TrimSpace(r.Commit) != "":
		return strings.TrimSpace(r.Commit)
	default:
		return ""
	}
}

type RepoResult struct {
	Name                     string        `json:"name"`
	Path                     string        `json:"path"`
	RepoURL                  string        `json:"repo_url,omitempty"`
	Revision                 *RepoRevision `json:"revision,omitempty"`
	ResolvedCommit           string        `json:"resolved_commit,omitempty"`
	Language                 string        `json:"language,omitempty"`
	DependencyCount          int           `json:"dependency_count"`
	WasteCandidateCount      int           `json:"waste_candidate_count"`
	WasteCandidatePercent    float64       `json:"waste_candidate_percent"`
	TopRiskSeverity          string        `json:"top_risk_severity,omitempty"`
	CriticalCVEs             int           `json:"critical_cves"`
	VulnerabilityFindings    int           `json:"vulnerability_findings,omitempty"`
	ReachableVulnerabilities int           `json:"reachable_vulnerabilities,omitempty"`
	DeniedLicenseCount       int           `json:"denied_license_count"`
	RuntimeTraceData         bool          `json:"runtime_trace_data,omitempty"`
	RuntimeRegressionCount   int           `json:"runtime_regression_count,omitempty"`
	RuntimeImprovementCount  int           `json:"runtime_improvement_count,omitempty"`
	Warnings                 []string      `json:"warnings,omitempty"`
	Error                    string        `json:"error,omitempty"`
}

type CrossRepoDependency struct {
	Name         string   `json:"name"`
	Repositories []string `json:"repositories"`
	Count        int      `json:"count"`
}

type Summary struct {
	TotalRepos                  int `json:"total_repos"`
	TotalDeps                   int `json:"total_deps"`
	TotalWasteCandidates        int `json:"total_waste_candidates"`
	CrossRepoDuplicates         int `json:"cross_repo_duplicates"`
	CriticalCVEs                int `json:"critical_cves"`
	VulnerabilityFindings       int `json:"vulnerability_findings,omitempty"`
	ReachableVulnerabilities    int `json:"reachable_vulnerabilities,omitempty"`
	ReposWithRuntimeTraceData   int `json:"repos_with_runtime_trace_data"`
	ReposWithRuntimeRegressions int `json:"repos_with_runtime_regressions"`
}

type RemediationItem struct {
	ID              string   `json:"id"`
	Repo            string   `json:"repo"`
	RepoPath        string   `json:"repo_path,omitempty"`
	Dependency      string   `json:"dependency,omitempty"`
	Category        string   `json:"category"`
	Severity        string   `json:"severity,omitempty"`
	Priority        string   `json:"priority,omitempty"`
	Evidence        []string `json:"evidence,omitempty"`
	SuggestedAction string   `json:"suggested_action"`
	BaselineStatus  string   `json:"baseline_status,omitempty"`
}

type BaselineComparison struct {
	BaselineKey               string                 `json:"baseline_key"`
	CurrentKey                string                 `json:"current_key,omitempty"`
	SummaryDelta              SummaryDelta           `json:"summary_delta"`
	RepoDeltas                []RepoDelta            `json:"repo_deltas,omitempty"`
	Added                     []RepoDelta            `json:"added,omitempty"`
	Removed                   []RepoDelta            `json:"removed,omitempty"`
	Changed                   []RepoDelta            `json:"changed,omitempty"`
	RemediationItemDeltas     []RemediationItemDelta `json:"remediation_item_deltas,omitempty"`
	NewRemediationItems       []RemediationItemDelta `json:"new_remediation_items,omitempty"`
	RegressedRemediationItems []RemediationItemDelta `json:"regressed_remediation_items,omitempty"`
	ExistingRemediationItems  []RemediationItemDelta `json:"existing_remediation_items,omitempty"`
	RemovedRemediationItems   []RemediationItemDelta `json:"removed_remediation_items,omitempty"`
}

type SummaryDelta struct {
	TotalReposDelta                  int `json:"total_repos_delta"`
	TotalDepsDelta                   int `json:"total_deps_delta"`
	TotalWasteCandidatesDelta        int `json:"total_waste_candidates_delta"`
	CrossRepoDuplicatesDelta         int `json:"cross_repo_duplicates_delta"`
	CriticalCVEsDelta                int `json:"critical_cves_delta"`
	VulnerabilityFindingsDelta       int `json:"vulnerability_findings_delta,omitempty"`
	ReachableVulnerabilitiesDelta    int `json:"reachable_vulnerabilities_delta,omitempty"`
	ReposWithRuntimeTraceDataDelta   int `json:"repos_with_runtime_trace_data_delta"`
	ReposWithRuntimeRegressionsDelta int `json:"repos_with_runtime_regressions_delta"`
}

type RepoDeltaKind string

const (
	RepoDeltaAdded   RepoDeltaKind = "added"
	RepoDeltaRemoved RepoDeltaKind = "removed"
	RepoDeltaChanged RepoDeltaKind = "changed"
)

type RepoDelta struct {
	Kind                          RepoDeltaKind `json:"kind"`
	Name                          string        `json:"name"`
	Path                          string        `json:"path,omitempty"`
	DependencyCountDelta          int           `json:"dependency_count_delta"`
	WasteCandidateCountDelta      int           `json:"waste_candidate_count_delta"`
	WasteCandidatePercentDelta    float64       `json:"waste_candidate_percent_delta"`
	CriticalCVEsDelta             int           `json:"critical_cves_delta"`
	VulnerabilityFindingsDelta    int           `json:"vulnerability_findings_delta,omitempty"`
	ReachableVulnerabilitiesDelta int           `json:"reachable_vulnerabilities_delta,omitempty"`
	DeniedLicenseCountDelta       int           `json:"denied_license_count_delta"`
	RuntimeRegressionCountDelta   int           `json:"runtime_regression_count_delta"`
	RuntimeImprovementCountDelta  int           `json:"runtime_improvement_count_delta"`
	CurrentError                  string        `json:"current_error,omitempty"`
	BaselineError                 string        `json:"baseline_error,omitempty"`
}

type RemediationItemDeltaKind string

const (
	RemediationItemNew       RemediationItemDeltaKind = "new"
	RemediationItemRegressed RemediationItemDeltaKind = "regressed"
	RemediationItemExisting  RemediationItemDeltaKind = "existing"
	RemediationItemRemoved   RemediationItemDeltaKind = "removed"
)

type RemediationItemDelta struct {
	Kind             RemediationItemDeltaKind `json:"kind"`
	ID               string                   `json:"id"`
	Repo             string                   `json:"repo"`
	RepoPath         string                   `json:"repo_path,omitempty"`
	Dependency       string                   `json:"dependency,omitempty"`
	Category         string                   `json:"category"`
	Severity         string                   `json:"severity,omitempty"`
	Priority         string                   `json:"priority,omitempty"`
	BaselineSeverity string                   `json:"baseline_severity,omitempty"`
	BaselinePriority string                   `json:"baseline_priority,omitempty"`
	Evidence         []string                 `json:"evidence,omitempty"`
	SuggestedAction  string                   `json:"suggested_action"`
}

type Report struct {
	GeneratedAt        time.Time             `json:"generated_at"`
	Repos              []RepoResult          `json:"repos"`
	Summary            Summary               `json:"summary"`
	BaselineComparison *BaselineComparison   `json:"baseline_comparison,omitempty"`
	CrossRepoDeps      []CrossRepoDependency `json:"cross_repo_deps,omitempty"`
	RemediationItems   []RemediationItem     `json:"remediation_items,omitempty"`
	SourceWarnings     []string              `json:"warnings,omitempty"`
}
