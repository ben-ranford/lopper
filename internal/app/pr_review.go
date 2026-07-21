package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/advisory"
	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/gitexec"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/workspace"
)

const (
	prReviewFormatMarkdown = "markdown"
	prReviewFormatJSON     = "json"

	prReviewSchemaVersion = "lopper.pr-review.v1"
	prReviewGitRevParse   = "rev-parse"

	prReviewCategoryAdded              = "added"
	prReviewCategoryRemoved            = "removed"
	prReviewCategoryUpgraded           = "upgraded"
	prReviewCategoryDowngraded         = "downgraded"
	prReviewCategoryVersionChanged     = "version-changed"
	prReviewCategoryPolicyChanged      = "policy-changed"
	prReviewCategoryNewlyReachable     = "newly-reachable"
	prReviewCategoryMateriallyWorsened = "materially-worsened"
)

var (
	fullCommitSHARe               = regexp.MustCompile(`\A[0-9a-fA-F]{40}([0-9a-fA-F]{24})?\z`)
	execPRReviewGitCommandFn      = gitexec.CommandContext
	resolvePRReviewGitPathFn      = gitexec.ResolveBinaryPath
	resolvePRReviewChangedFilesFn = workspace.ChangedFilesBetween
	createPRReviewTempRootFn      = os.MkdirTemp
	removePRReviewTempRootFn      = os.RemoveAll
	prReviewNow                   = time.Now
)

type prReviewArtifact struct {
	SchemaVersion string            `json:"schemaVersion"`
	GeneratedAt   time.Time         `json:"generatedAt"`
	RepoPath      string            `json:"repoPath"`
	BaseSHA       string            `json:"baseSha"`
	HeadSHA       string            `json:"headSha"`
	MergeBaseMode string            `json:"mergeBaseMode"`
	AnalysisMode  string            `json:"analysisMode"`
	FullArtifact  string            `json:"fullArtifactHint,omitempty"`
	Summary       prReviewSummary   `json:"summary"`
	Sections      []prReviewSection `json:"sections"`
	Warnings      []string          `json:"warnings,omitempty"`
}

type prReviewSummary struct {
	Added                int `json:"added"`
	Removed              int `json:"removed"`
	Upgraded             int `json:"upgraded"`
	Downgraded           int `json:"downgraded"`
	VersionChanged       int `json:"versionChanged"`
	PolicyChanged        int `json:"policyChanged"`
	NewlyReachable       int `json:"newlyReachable"`
	MateriallyWorsened   int `json:"materiallyWorsened"`
	RegressionCount      int `json:"regressionCount"`
	MarkdownOverflowRows int `json:"markdownOverflowRows,omitempty"`
}

type prReviewSection struct {
	ID    string        `json:"id"`
	Title string        `json:"title"`
	Rows  []prReviewRow `json:"rows"`
}

type prReviewRow struct {
	Category           string   `json:"category"`
	Dependency         string   `json:"dependency"`
	Language           string   `json:"language,omitempty"`
	Ecosystem          string   `json:"ecosystem,omitempty"`
	BaseVersion        string   `json:"baseVersion,omitempty"`
	HeadVersion        string   `json:"headVersion,omitempty"`
	VersionChange      string   `json:"versionChange,omitempty"`
	PURL               string   `json:"purl,omitempty"`
	IdentityConfidence string   `json:"identityConfidence"`
	EvidenceConfidence string   `json:"evidenceConfidence"`
	WasteDeltaBytes    int64    `json:"wasteDeltaBytes,omitempty"`
	UsedPercentDelta   float64  `json:"usedPercentDelta,omitempty"`
	PolicyChange       string   `json:"policyChange,omitempty"`
	AdvisoryID         string   `json:"advisoryId,omitempty"`
	Severity           string   `json:"severity,omitempty"`
	Priority           string   `json:"priority,omitempty"`
	Reachable          bool     `json:"reachable,omitempty"`
	Regression         bool     `json:"regression,omitempty"`
	Evidence           []string `json:"evidence,omitempty"`
}

type prReviewArtifactInput struct {
	repoPath   string
	baseSHA    string
	headSHA    string
	baseReport report.Report
	headReport report.Report
	req        PRReviewRequest
	now        time.Time
	warnings   []string
}

func (a *App) executePRReview(ctx context.Context, req Request) (string, error) {
	if !req.PRReview.Features.Enabled(report.DependencySurfacePRReviewPreviewFeature) {
		return "", fmt.Errorf("pr-review requires --enable-feature %s", report.DependencySurfacePRReviewPreviewFeature)
	}
	if err := validatePRReviewFeatures(req.PRReview); err != nil {
		return "", err
	}
	format, err := parsePRReviewFormat(req.PRReview.Format)
	if err != nil {
		return "", err
	}
	baseSHA, headSHA, err := validatePRReviewSHAs(req.PRReview.BaseSHA, req.PRReview.HeadSHA)
	if err != nil {
		return "", err
	}
	repoPath, err := filepath.Abs(strings.TrimSpace(req.RepoPath))
	if err != nil {
		return "", err
	}
	if a.Analyzer == nil {
		return "", fmt.Errorf("pr-review analyzer is not configured")
	}

	baseReport, headReport, warnings, err := a.analysePRReviewRevisions(ctx, repoPath, req.PRReview, baseSHA, headSHA)
	if err != nil {
		return "", err
	}
	artifact := buildPRReviewArtifact(prReviewArtifactInput{
		repoPath:   repoPath,
		baseSHA:    baseSHA,
		headSHA:    headSHA,
		baseReport: baseReport,
		headReport: headReport,
		req:        req.PRReview,
		now:        prReviewNow().UTC(),
		warnings:   warnings,
	})
	formatted, err := formatPRReviewArtifact(artifact, format, req.PRReview.MaxRows)
	if err != nil {
		return "", err
	}
	output, err := persistCommandOutput(formatted, req.PRReview.OutputPath, "pr review report")
	if err != nil {
		return "", err
	}
	if req.PRReview.FailOnRegression && artifact.Summary.RegressionCount > 0 {
		return output, ErrPRReviewRegressions
	}
	return output, nil
}

func parsePRReviewFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", prReviewFormatMarkdown, "md", "pr-comment":
		return prReviewFormatMarkdown, nil
	case prReviewFormatJSON:
		return prReviewFormatJSON, nil
	default:
		return "", fmt.Errorf("unknown pr-review format: %s", value)
	}
}

func validatePRReviewSHAs(base, head string) (string, string, error) {
	base = strings.TrimSpace(base)
	head = strings.TrimSpace(head)
	if !fullCommitSHARe.MatchString(base) {
		return "", "", fmt.Errorf("--base must be a full immutable commit SHA")
	}
	if !fullCommitSHARe.MatchString(head) {
		return "", "", fmt.Errorf("--head must be a full immutable commit SHA")
	}
	if strings.EqualFold(base, head) {
		return "", "", fmt.Errorf("--base and --head must be different commits")
	}
	return strings.ToLower(base), strings.ToLower(head), nil
}

func (a *App) analysePRReviewRevisions(ctx context.Context, repoPath string, req PRReviewRequest, baseSHA, headSHA string) (baseReport report.Report, headReport report.Report, warnings []string, err error) {
	gitRepoPath, repoPrefix, err := resolvePRReviewRepositoryScope(ctx, repoPath)
	if err != nil {
		return report.Report{}, report.Report{}, nil, err
	}
	if err := verifyPRReviewCommits(ctx, gitRepoPath, baseSHA, headSHA); err != nil {
		return report.Report{}, report.Report{}, nil, err
	}
	changedFiles, err := resolvePRReviewChangedFilesFn(gitRepoPath, baseSHA, headSHA)
	if err != nil {
		return report.Report{}, report.Report{}, nil, fmt.Errorf("resolve explicit pr-review changed files: %w", err)
	}
	req.ChangedFiles = scopePRReviewChangedFiles(changedFiles, repoPrefix)
	req.ChangedFilesExplicit = true

	tempRoot, err := createPRReviewTempRootFn("", "lopper-pr-review-*")
	if err != nil {
		return report.Report{}, report.Report{}, nil, fmt.Errorf("create pr-review workspace: %w", err)
	}
	defer cleanupPRReviewTempRootOnReturn(tempRoot, &err)

	basePath := filepath.Join(tempRoot, "base")
	headPath := filepath.Join(tempRoot, "head")
	if err := addPRReviewWorktree(ctx, gitRepoPath, basePath, baseSHA); err != nil {
		return report.Report{}, report.Report{}, nil, fmt.Errorf("create base worktree: %w", err)
	}
	defer cleanupPRReviewWorktreeOnReturn(ctx, gitRepoPath, basePath, "remove base worktree", &err)
	if err := addPRReviewWorktree(ctx, gitRepoPath, headPath, headSHA); err != nil {
		return report.Report{}, report.Report{}, nil, fmt.Errorf("create head worktree: %w", err)
	}
	defer cleanupPRReviewWorktreeOnReturn(ctx, gitRepoPath, headPath, "remove head worktree", &err)

	baseReport, err = a.analysePRReviewScope(ctx, filepath.Join(basePath, filepath.FromSlash(repoPrefix)), repoPath, req)
	if err != nil {
		return report.Report{}, report.Report{}, nil, fmt.Errorf("analyse base commit: %w", err)
	}
	headReport, err = a.analysePRReviewScope(ctx, filepath.Join(headPath, filepath.FromSlash(repoPrefix)), repoPath, req)
	if err != nil {
		return report.Report{}, report.Report{}, nil, fmt.Errorf("analyse head commit: %w", err)
	}
	warnings = []string{
		"pr-review uses explicit base/head SHAs; merge-base inference is intentionally disabled",
		"pr-review disables git hooks for temporary worktree checkouts and does not run package-manager or runtime test commands",
	}
	return baseReport, headReport, warnings, nil
}

func (a *App) analysePRReviewScope(ctx context.Context, repoPath, callerRepoPath string, req PRReviewRequest) (report.Report, error) {
	info, err := os.Stat(repoPath)
	if errors.Is(err, os.ErrNotExist) {
		return report.Report{RepoPath: callerRepoPath}, nil
	}
	if err != nil {
		return report.Report{}, fmt.Errorf("inspect pr-review repository scope: %w", err)
	}
	if !info.IsDir() {
		return report.Report{RepoPath: callerRepoPath}, nil
	}
	return a.analysePRReviewWorktree(ctx, repoPath, callerRepoPath, req)
}

func cleanupPRReviewTempRootOnReturn(tempRoot string, resultErr *error) {
	recordPRReviewCleanupError(resultErr, removePRReviewTempRootFn(tempRoot), "remove pr-review workspace")
}

func cleanupPRReviewWorktreeOnReturn(ctx context.Context, repoPath, worktreePath, operation string, resultErr *error) {
	recordPRReviewCleanupError(resultErr, removePRReviewWorktree(ctx, repoPath, worktreePath), operation)
}

func recordPRReviewCleanupError(resultErr *error, cleanupErr error, operation string) {
	if cleanupErr == nil {
		return
	}
	wrappedCleanupErr := fmt.Errorf("%s: %w", operation, cleanupErr)
	if *resultErr == nil {
		*resultErr = wrappedCleanupErr
		return
	}
	*resultErr = errors.Join(*resultErr, wrappedCleanupErr)
}

func verifyPRReviewCommits(ctx context.Context, repoPath, baseSHA, headSHA string) error {
	if _, err := verifyPRReviewCommit(ctx, repoPath, baseSHA); err != nil {
		return fmt.Errorf("verify base commit: %w", err)
	}
	if _, err := verifyPRReviewCommit(ctx, repoPath, headSHA); err != nil {
		return fmt.Errorf("verify head commit: %w", err)
	}
	return nil
}

func resolvePRReviewRepositoryScope(ctx context.Context, repoPath string) (string, string, error) {
	requestedPath := filepath.Clean(repoPath)
	candidate := requestedPath
	missingSuffix := ""
	for {
		root, err := runPRReviewGit(ctx, candidate, prReviewGitRevParse, "--show-toplevel")
		if err == nil {
			prefix, prefixErr := runPRReviewGit(ctx, candidate, prReviewGitRevParse, "--show-prefix")
			if prefixErr != nil {
				return "", "", fmt.Errorf("resolve pr-review repository scope: %w", prefixErr)
			}
			parts := make([]string, 0, 2)
			if prefix = strings.Trim(filepath.ToSlash(prefix), "/"); prefix != "" {
				parts = append(parts, prefix)
			}
			if missingSuffix != "" {
				parts = append(parts, filepath.ToSlash(missingSuffix))
			}
			return filepath.Clean(root), strings.Join(parts, "/"), nil
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", "", fmt.Errorf("resolve pr-review repository scope: %w", err)
		}
		missingSuffix = filepath.Join(filepath.Base(candidate), missingSuffix)
		candidate = parent
	}
}

func scopePRReviewChangedFiles(changedFiles []string, repoPrefix string) []string {
	prefix := strings.Trim(filepath.ToSlash(repoPrefix), "/")
	if prefix == "" {
		return changedFiles
	}
	prefix += "/"
	scoped := make([]string, 0, len(changedFiles))
	for _, changedFile := range changedFiles {
		if relative, ok := strings.CutPrefix(filepath.ToSlash(changedFile), prefix); ok && relative != "" {
			scoped = append(scoped, relative)
		}
	}
	return scoped
}

func verifyPRReviewCommit(ctx context.Context, repoPath, sha string) (string, error) {
	out, err := runPRReviewGit(ctx, repoPath, prReviewGitRevParse, "--verify", sha+"^{commit}")
	if err != nil {
		return "", err
	}
	resolved := strings.ToLower(strings.TrimSpace(out))
	if !strings.EqualFold(resolved, sha) {
		return "", fmt.Errorf("resolved %s to %s", sha, resolved)
	}
	return resolved, nil
}

func addPRReviewWorktree(ctx context.Context, repoPath, worktreePath, sha string) error {
	_, err := runPRReviewGit(ctx, repoPath, "worktree", "add", "--detach", "--force", worktreePath, sha)
	return err
}

func removePRReviewWorktree(ctx context.Context, repoPath, worktreePath string) error {
	_, err := runPRReviewGit(ctx, repoPath, "worktree", "remove", "--force", worktreePath)
	return err
}

func runPRReviewGit(ctx context.Context, repoPath string, args ...string) (string, error) {
	gitPath, err := resolvePRReviewGitPathFn()
	if err != nil {
		return "", err
	}
	commandArgs := append([]string{"-C", repoPath, "-c", "core.hooksPath=/dev/null"}, args...)
	command, err := execPRReviewGitCommandFn(ctx, gitPath, commandArgs...)
	if err != nil {
		return "", fmt.Errorf("construct git command: %w", err)
	}
	command.Env = gitexec.SanitizedEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func (a *App) analysePRReviewWorktree(ctx context.Context, repoPath, callerRepoPath string, req PRReviewRequest) (report.Report, error) {
	baseRequest := analysis.Request{
		RepoPath:             repoPath,
		ChangedFiles:         append([]string{}, req.ChangedFiles...),
		ChangedFilesExplicit: req.ChangedFilesExplicit,
		TopN:                 req.TopN,
		ScopeMode:            req.ScopeMode,
		Language:             req.Language,
		ConfigPath:           req.ConfigPath,
		IncludePatterns:      append([]string{}, req.IncludePatterns...),
		ExcludePatterns:      append([]string{}, req.ExcludePatterns...),
		Features:             req.Features,
		Cache: &analysis.CacheOptions{
			Enabled:  false,
			ReadOnly: true,
		},
	}
	policy := analysisRequestPolicy{
		thresholds:              req.Thresholds,
		advisorySourcePath:      req.AdvisorySourcePath,
		vulnerabilityExceptions: req.VulnerabilityExceptions,
		policySources:           req.PolicySources,
		policyTrace:             req.PolicyTrace,
	}
	preparedPolicy := prepareAnalysisPolicy(baseRequest, policy)
	reportData, err := a.Analyzer.Analyse(ctx, preparedPolicy.request)
	if err != nil {
		return report.Report{}, err
	}
	if resolvedCallerRepoPath := strings.TrimSpace(callerRepoPath); resolvedCallerRepoPath != "" {
		reportData.RepoPath = resolvedCallerRepoPath
	}
	if strings.TrimSpace(req.AdvisorySourcePath) != "" {
		advisories, err := advisory.Load(req.AdvisorySourcePath)
		if err != nil {
			return report.Report{}, err
		}
		report.AnnotateVulnerabilities(&reportData, advisories)
		reportData.Summary = report.ComputeSummary(reportData.Dependencies)
	}
	return applyVulnerabilityExceptionsToReport(reportData, req.VulnerabilityExceptions, prReviewNow().UTC()), nil
}

func validatePRReviewFeatures(req PRReviewRequest) error {
	return validateAnalysisPolicyFeatures(req.Features, req.AdvisorySourcePath, req.Thresholds, req.VulnerabilityExceptions)
}

func buildPRReviewArtifact(input prReviewArtifactInput) prReviewArtifact {
	comparison := report.ComputeBaselineComparison(input.headReport, input.baseReport)
	comparison.BaselineKey = "commit:" + input.baseSHA
	comparison.CurrentKey = "commit:" + input.headSHA
	input.headReport.BaselineComparison = &comparison

	sections := []prReviewSection{
		{ID: prReviewCategoryAdded, Title: "Added Dependencies", Rows: prReviewAddedRows(input.baseReport, input.headReport)},
		{ID: prReviewCategoryRemoved, Title: "Removed Dependencies", Rows: prReviewRemovedRows(input.baseReport, input.headReport)},
		{ID: prReviewCategoryUpgraded, Title: "Upgraded Dependencies", Rows: prReviewVersionRows(input.baseReport, input.headReport, prReviewCategoryUpgraded)},
		{ID: prReviewCategoryDowngraded, Title: "Downgraded Dependencies", Rows: prReviewVersionRows(input.baseReport, input.headReport, prReviewCategoryDowngraded)},
		{ID: prReviewCategoryVersionChanged, Title: "Version Changed Dependencies", Rows: prReviewVersionRows(input.baseReport, input.headReport, prReviewCategoryVersionChanged)},
		{ID: prReviewCategoryPolicyChanged, Title: "Policy Changed Dependencies", Rows: prReviewPolicyRows(input.baseReport, input.headReport)},
		{ID: prReviewCategoryNewlyReachable, Title: "Newly Reachable Vulnerabilities", Rows: prReviewNewlyReachableRows(input.headReport, comparison.NewReachableVulnerabilities, input.req.Thresholds.ReachableVulnerabilityPriority)},
		{ID: prReviewCategoryMateriallyWorsened, Title: "Materially Worsened Dependencies", Rows: prReviewMaterialRows(input.baseReport, input.headReport, input.req.MaterialWasteBytes)},
	}
	for i := range sections {
		sortPRReviewRows(sections[i].Rows)
	}

	artifact := prReviewArtifact{
		SchemaVersion: prReviewSchemaVersion,
		GeneratedAt:   input.now,
		RepoPath:      input.repoPath,
		BaseSHA:       input.baseSHA,
		HeadSHA:       input.headSHA,
		MergeBaseMode: "none; explicit base/head SHAs only",
		AnalysisMode:  "static dependency-surface analysis; no package-manager or runtime test command execution",
		FullArtifact:  "Run pr-review with --format json --output lopper-pr-review.json for the complete machine-readable artifact.",
		Sections:      sections,
		Warnings:      collectPRReviewWarnings(input),
	}
	artifact.Summary = summarizePRReviewSections(sections)
	return artifact
}

func collectPRReviewWarnings(input prReviewArtifactInput) []string {
	warnings := append([]string{}, input.warnings...)
	for _, warning := range input.baseReport.Warnings {
		if trimmed := strings.TrimSpace(warning); trimmed != "" {
			warnings = append(warnings, fmt.Sprintf("base %s: %s", shortPRReviewRevision(input.baseSHA), trimmed))
		}
	}
	for _, warning := range input.headReport.Warnings {
		if trimmed := strings.TrimSpace(warning); trimmed != "" {
			warnings = append(warnings, fmt.Sprintf("head %s: %s", shortPRReviewRevision(input.headSHA), trimmed))
		}
	}
	return uniqueSortedStrings(warnings)
}

func shortPRReviewRevision(sha string) string {
	sha = strings.TrimSpace(sha)
	if len(sha) <= 12 {
		return sha
	}
	return sha[:12]
}

func prReviewAddedRows(baseReport, headReport report.Report) []prReviewRow {
	rows := make([]prReviewRow, 0)
	for _, pair := range prReviewDependencyPairs(baseReport.Dependencies, headReport.Dependencies) {
		if pair.hasBase || !pair.hasHead {
			continue
		}
		dep := pair.head
		row := prReviewRowForDependency(prReviewCategoryAdded, dep)
		row.Evidence = append(row.Evidence, "dependency present in head only")
		if dep.License != nil && dep.License.Denied {
			row.Regression = true
			row.PolicyChange = "denied license introduced"
			row.Evidence = append(row.Evidence, "dependency is denied by license policy")
		}
		rows = append(rows, row)
	}
	return rows
}

func prReviewRemovedRows(baseReport, headReport report.Report) []prReviewRow {
	rows := make([]prReviewRow, 0)
	for _, pair := range prReviewDependencyPairs(baseReport.Dependencies, headReport.Dependencies) {
		if !pair.hasBase || pair.hasHead {
			continue
		}
		dep := pair.base
		row := prReviewRowForDependency(prReviewCategoryRemoved, dep)
		row.BaseVersion = row.HeadVersion
		row.HeadVersion = ""
		row.Evidence = append(row.Evidence, "dependency present in base only")
		rows = append(rows, row)
	}
	return rows
}

func prReviewVersionRows(baseReport, headReport report.Report, category string) []prReviewRow {
	rows := make([]prReviewRow, 0)
	for _, pair := range prReviewDependencyPairs(baseReport.Dependencies, headReport.Dependencies) {
		if !pair.hasBase || !pair.hasHead {
			continue
		}
		baseDep := pair.base
		headDep := pair.head
		baseVersion := dependencyIdentityVersion(baseDep)
		headVersion := dependencyIdentityVersion(headDep)
		if baseVersion == "" || headVersion == "" || baseVersion == headVersion {
			continue
		}
		versionCategory := prReviewVersionCategory(baseVersion, headVersion)
		if versionCategory != category {
			continue
		}
		row := prReviewRowForPair(category, baseDep, headDep)
		row.VersionChange = versionCategory
		row.Regression = versionCategory == prReviewCategoryDowngraded
		row.Evidence = append(row.Evidence, "identity versions differ")
		row.Evidence = appendVersionOrderingEvidence(row.Evidence, versionCategory)
		rows = append(rows, row)
	}
	return rows
}

func prReviewVersionCategory(baseVersion, headVersion string) string {
	cmp, comparableVersion := report.CompareSemanticVersions(baseVersion, headVersion)
	if !comparableVersion {
		return prReviewCategoryVersionChanged
	}
	switch {
	case cmp < 0:
		return prReviewCategoryUpgraded
	case cmp > 0:
		return prReviewCategoryDowngraded
	default:
		return prReviewCategoryVersionChanged
	}
}

func appendVersionOrderingEvidence(evidence []string, category string) []string {
	if category == prReviewCategoryVersionChanged {
		return append(evidence, "version ordering was not inferred")
	}
	return evidence
}

func prReviewPolicyRows(baseReport, headReport report.Report) []prReviewRow {
	rows := make([]prReviewRow, 0)
	for _, pair := range prReviewDependencyPairs(baseReport.Dependencies, headReport.Dependencies) {
		if !pair.hasBase || !pair.hasHead {
			continue
		}
		baseDep := pair.base
		headDep := pair.head
		change, regression := dependencyPolicyChange(baseDep, headDep)
		if change == "" {
			continue
		}
		row := prReviewRowForPair(prReviewCategoryPolicyChanged, baseDep, headDep)
		row.PolicyChange = change
		row.Regression = regression
		row.Evidence = append(row.Evidence, "policy metadata changed between base and head")
		rows = append(rows, row)
	}
	return rows
}

func prReviewNewlyReachableRows(headReport report.Report, findings []report.VulnerabilityDelta, threshold string) []prReviewRow {
	rows := make([]prReviewRow, 0, len(findings))
	for _, finding := range findings {
		dep := findPRReviewDependency(headReport.Dependencies, finding)
		row := prReviewRowForDependency(prReviewCategoryNewlyReachable, dep)
		row.Dependency = firstNonBlankString(finding.Name, row.Dependency)
		row.Language = firstNonBlankString(finding.Language, row.Language)
		row.AdvisoryID = finding.AdvisoryID
		row.Severity = finding.Severity
		row.Priority = finding.Priority
		row.Reachable = true
		row.Regression = report.VulnerabilityPriorityMeetsThreshold(finding.Priority, threshold)
		row.Evidence = compactPRReviewEvidence(append([]string{"new reachable vulnerability introduced"}, finding.Evidence...))
		rows = append(rows, row)
	}
	return rows
}

func prReviewMaterialRows(baseReport, headReport report.Report, threshold int64) []prReviewRow {
	rows := make([]prReviewRow, 0)
	for _, pair := range prReviewDependencyPairs(baseReport.Dependencies, headReport.Dependencies) {
		if !pair.hasHead {
			continue
		}
		baseDep := pair.base
		headDep := pair.head
		wasteDelta := headDep.EstimatedUnusedBytes - baseDep.EstimatedUnusedBytes
		usedPercentDelta := headDep.UsedPercent - baseDep.UsedPercent
		if wasteDelta <= 0 || wasteDelta < threshold {
			continue
		}
		row := prReviewRowForPair(prReviewCategoryMateriallyWorsened, baseDep, headDep)
		row.WasteDeltaBytes = wasteDelta
		row.UsedPercentDelta = usedPercentDelta
		row.Regression = true
		row.Evidence = append(row.Evidence, fmt.Sprintf("estimated unused bytes increased by %d", wasteDelta))
		if !pair.hasBase {
			row.UsedPercentDelta = 0
			row.Evidence = append(row.Evidence, "dependency present in head only")
		}
		rows = append(rows, row)
	}
	return rows
}

func prReviewRowForDependency(category string, dep report.DependencyReport) prReviewRow {
	return prReviewRow{
		Category:           category,
		Dependency:         dep.Name,
		Language:           dep.Language,
		Ecosystem:          dependencyIdentityEcosystem(dep),
		HeadVersion:        dependencyIdentityVersion(dep),
		PURL:               dependencyIdentityPURL(dep),
		IdentityConfidence: dependencyIdentityConfidence(dep),
		EvidenceConfidence: dependencyEvidenceConfidence(dep),
		Evidence:           dependencyIdentityEvidence(dep),
	}
}

func prReviewRowForPair(category string, baseDep, headDep report.DependencyReport) prReviewRow {
	row := prReviewRowForDependency(category, headDep)
	row.BaseVersion = dependencyIdentityVersion(baseDep)
	row.HeadVersion = dependencyIdentityVersion(headDep)
	row.WasteDeltaBytes = headDep.EstimatedUnusedBytes - baseDep.EstimatedUnusedBytes
	row.UsedPercentDelta = headDep.UsedPercent - baseDep.UsedPercent
	return row
}

func summarizePRReviewSections(sections []prReviewSection) prReviewSummary {
	summary := prReviewSummary{}
	for _, section := range sections {
		regressions := 0
		for _, row := range section.Rows {
			if row.Regression {
				regressions++
			}
		}
		summary.RegressionCount += regressions
		switch section.ID {
		case prReviewCategoryAdded:
			summary.Added = len(section.Rows)
		case prReviewCategoryRemoved:
			summary.Removed = len(section.Rows)
		case prReviewCategoryUpgraded:
			summary.Upgraded = len(section.Rows)
		case prReviewCategoryDowngraded:
			summary.Downgraded = len(section.Rows)
		case prReviewCategoryVersionChanged:
			summary.VersionChanged = len(section.Rows)
		case prReviewCategoryPolicyChanged:
			summary.PolicyChanged = len(section.Rows)
		case prReviewCategoryNewlyReachable:
			summary.NewlyReachable = len(section.Rows)
		case prReviewCategoryMateriallyWorsened:
			summary.MateriallyWorsened = len(section.Rows)
		}
	}
	return summary
}

func formatPRReviewArtifact(artifact prReviewArtifact, format string, maxRows int) (string, error) {
	switch format {
	case prReviewFormatJSON:
		payload, err := json.MarshalIndent(artifact, "", "  ")
		if err != nil {
			return "", err
		}
		return string(payload) + "\n", nil
	case prReviewFormatMarkdown:
		return formatPRReviewMarkdown(artifact, maxRows), nil
	default:
		return "", fmt.Errorf("unknown pr-review format: %s", format)
	}
}

func formatPRReviewMarkdown(artifact prReviewArtifact, maxRows int) string {
	var buffer strings.Builder
	fmt.Fprintf(&buffer, "## Lopper PR Review\n\n")
	fmt.Fprintf(&buffer, "Base `%s` -> Head `%s`\n\n", artifact.BaseSHA, artifact.HeadSHA)
	fmt.Fprintf(&buffer, "_%s. Merge-base mode: %s._\n\n", artifact.AnalysisMode, artifact.MergeBaseMode)
	buffer.WriteString("| Section | Count |\n| --- | ---: |\n")
	fmt.Fprintf(&buffer, "| Added | %d |\n", artifact.Summary.Added)
	fmt.Fprintf(&buffer, "| Removed | %d |\n", artifact.Summary.Removed)
	fmt.Fprintf(&buffer, "| Upgraded | %d |\n", artifact.Summary.Upgraded)
	fmt.Fprintf(&buffer, "| Downgraded | %d |\n", artifact.Summary.Downgraded)
	fmt.Fprintf(&buffer, "| Version changed | %d |\n", artifact.Summary.VersionChanged)
	fmt.Fprintf(&buffer, "| Policy changed | %d |\n", artifact.Summary.PolicyChanged)
	fmt.Fprintf(&buffer, "| Newly reachable | %d |\n", artifact.Summary.NewlyReachable)
	fmt.Fprintf(&buffer, "| Materially worsened | %d |\n", artifact.Summary.MateriallyWorsened)
	fmt.Fprintf(&buffer, "| Regression rows | %d |\n", artifact.Summary.RegressionCount)

	overflowRows := 0
	for _, section := range artifact.Sections {
		if len(section.Rows) == 0 {
			continue
		}
		buffer.WriteString("\n### ")
		buffer.WriteString(section.Title)
		buffer.WriteString("\n\n")
		buffer.WriteString("| Dependency | Language | Base | Head | Waste Δ | Used % Δ | Confidence | Advisory | Priority | Evidence |\n")
		buffer.WriteString("| --- | --- | --- | --- | ---: | ---: | --- | --- | --- | --- |\n")
		rows := section.Rows
		if len(rows) > maxRows {
			rows = rows[:maxRows]
			overflowRows += len(section.Rows) - maxRows
		}
		for _, row := range rows {
			cells := []string{
				escapePRReviewMarkdown(row.Dependency),
				escapePRReviewMarkdown(row.Language),
				escapePRReviewMarkdown(emptyPRReviewValue(row.BaseVersion)),
				escapePRReviewMarkdown(emptyPRReviewValue(row.HeadVersion)),
				formatSignedInt64(row.WasteDeltaBytes),
				formatSignedFloat(row.UsedPercentDelta),
				escapePRReviewMarkdown(row.EvidenceConfidence),
				escapePRReviewMarkdown(emptyPRReviewValue(row.AdvisoryID)),
				escapePRReviewMarkdown(emptyPRReviewValue(row.Priority)),
				escapePRReviewMarkdown(strings.Join(row.Evidence, "; ")),
			}
			buffer.WriteString("| `" + cells[0] + "` | " + strings.Join(cells[1:], " | ") + " |\n")
		}
		if len(section.Rows) > maxRows {
			fmt.Fprintf(&buffer, "\n_%d additional rows omitted from this section._\n", len(section.Rows)-maxRows)
		}
	}
	if overflowRows > 0 {
		fmt.Fprintf(&buffer, "\n%d rows were omitted from Markdown output. %s\n", overflowRows, artifact.FullArtifact)
	}
	if len(artifact.Warnings) > 0 {
		buffer.WriteString("\n### Notes\n\n")
		for _, warning := range artifact.Warnings {
			fmt.Fprintf(&buffer, "- %s\n", escapePRReviewMarkdown(warning))
		}
	}
	return buffer.String()
}

type prReviewDependencyPair struct {
	base    report.DependencyReport
	hasBase bool
	head    report.DependencyReport
	hasHead bool
}

func prReviewDependencyPairs(baseDependencies, headDependencies []report.DependencyReport) []prReviewDependencyPair {
	reportPairs := report.PairDependencyInstances(headDependencies, baseDependencies)
	pairs := make([]prReviewDependencyPair, 0, len(reportPairs))
	for _, reportPair := range reportPairs {
		pairs = append(pairs, prReviewDependencyPair{
			base:    reportPair.Baseline,
			hasBase: reportPair.HasBaseline,
			head:    reportPair.Current,
			hasHead: reportPair.HasCurrent,
		})
	}
	return pairs
}

func findPRReviewDependency(dependencies []report.DependencyReport, finding report.VulnerabilityDelta) report.DependencyReport {
	if dep, ok := findPRReviewDependencyByOrdinal(dependencies, finding.DependencyKey, finding.CurrentOrdinal); ok {
		return dep
	}
	name := strings.TrimSpace(finding.Name)
	language := strings.TrimSpace(finding.Language)
	for _, dep := range dependencies {
		if strings.TrimSpace(dep.Name) == name && strings.TrimSpace(dep.Language) == language {
			return dep
		}
	}
	for _, dep := range dependencies {
		if strings.TrimSpace(dep.Name) == name {
			return dep
		}
	}
	return report.DependencyReport{Name: name, Language: language}
}

func findPRReviewDependencyByOrdinal(dependencies []report.DependencyReport, key string, ordinal int) (report.DependencyReport, bool) {
	if strings.TrimSpace(key) == "" || ordinal < 0 {
		return report.DependencyReport{}, false
	}
	grouped := make([]report.DependencyReport, 0)
	for _, dep := range dependencies {
		if report.DependencyVersionlessKey(dep) == key {
			grouped = append(grouped, dep)
		}
	}
	if ordinal >= len(grouped) {
		return report.DependencyReport{}, false
	}
	sort.Slice(grouped, func(i, j int) bool {
		return report.DependencyPairingOrderKey(grouped[i]) < report.DependencyPairingOrderKey(grouped[j])
	})
	return grouped[ordinal], true
}

func dependencyPolicyChange(baseDep, headDep report.DependencyReport) (string, bool) {
	baseDenied := baseDep.License != nil && baseDep.License.Denied
	headDenied := headDep.License != nil && headDep.License.Denied
	if baseDenied != headDenied {
		return fmt.Sprintf("license denied %t -> %t", baseDenied, headDenied), headDenied
	}
	baseLicense := dependencyLicenseLabel(baseDep.License)
	headLicense := dependencyLicenseLabel(headDep.License)
	if baseLicense != "" && headLicense != "" && baseLicense != headLicense {
		return fmt.Sprintf("license %s -> %s", baseLicense, headLicense), false
	}
	return "", false
}

func dependencyLicenseLabel(license *report.DependencyLicense) string {
	if license == nil {
		return ""
	}
	return firstNonBlankString(license.SPDX, license.Raw)
}

func dependencyIdentityVersion(dep report.DependencyReport) string {
	if dep.Identity == nil {
		return ""
	}
	return strings.TrimSpace(dep.Identity.Version)
}

func dependencyIdentityEcosystem(dep report.DependencyReport) string {
	if dep.Identity == nil {
		return ""
	}
	return strings.TrimSpace(dep.Identity.Ecosystem)
}

func dependencyIdentityPURL(dep report.DependencyReport) string {
	if dep.Identity == nil {
		return ""
	}
	return strings.TrimSpace(dep.Identity.PURL)
}

func dependencyIdentityConfidence(dep report.DependencyReport) string {
	if dep.Identity == nil || strings.TrimSpace(dep.Identity.Confidence) == "" {
		return "unknown"
	}
	return strings.TrimSpace(dep.Identity.Confidence)
}

func dependencyEvidenceConfidence(dep report.DependencyReport) string {
	if dep.ReachabilityConfidence != nil {
		return fmt.Sprintf("%.2f", dep.ReachabilityConfidence.Score)
	}
	return dependencyIdentityConfidence(dep)
}

func dependencyIdentityEvidence(dep report.DependencyReport) []string {
	if dep.Identity == nil {
		return []string{"dependency identity unknown"}
	}
	evidence := append([]string{}, dep.Identity.Evidence...)
	if dep.Identity.VersionStatus != "" {
		evidence = append(evidence, "version status: "+strings.TrimSpace(dep.Identity.VersionStatus))
	}
	if dep.Identity.PURLStatus != "" {
		evidence = append(evidence, "purl status: "+strings.TrimSpace(dep.Identity.PURLStatus))
	}
	if len(evidence) == 0 {
		evidence = append(evidence, "dependency identity evidence unavailable")
	}
	return compactPRReviewEvidence(evidence)
}

func compactPRReviewEvidence(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func sortPRReviewRows(rows []prReviewRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Regression != rows[j].Regression {
			return rows[i].Regression
		}
		if rows[i].Priority != rows[j].Priority {
			return vulnerabilityPriorityRank(rows[i].Priority) > vulnerabilityPriorityRank(rows[j].Priority)
		}
		if rows[i].WasteDeltaBytes != rows[j].WasteDeltaBytes {
			return rows[i].WasteDeltaBytes > rows[j].WasteDeltaBytes
		}
		if rows[i].Language != rows[j].Language {
			return rows[i].Language < rows[j].Language
		}
		return rows[i].Dependency < rows[j].Dependency
	})
}

func vulnerabilityPriorityRank(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical":
		return 5
	case "high":
		return 4
	case "medium":
		return 3
	case "low":
		return 2
	default:
		return 1
	}
}

func firstNonBlankString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func emptyPRReviewValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func formatSignedInt64(value int64) string {
	if value > 0 {
		return fmt.Sprintf("+%d", value)
	}
	return fmt.Sprintf("%d", value)
}

func formatSignedFloat(value float64) string {
	if value > 0 {
		return fmt.Sprintf("+%.1f", value)
	}
	return fmt.Sprintf("%.1f", value)
}

func escapePRReviewMarkdown(value string) string {
	return strings.NewReplacer("|", "\\|", "`", "'", "\n", " ", "\r", " ").Replace(value)
}
