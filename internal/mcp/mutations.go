package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

// MutationToolsFeature gates explicit MCP mutation tools. Read-only MCP tools
// stay available under ServerPreviewFeature and do not use this flag.
const MutationToolsFeature = "mcp-mutation-tools-preview"

type MutationRunner interface {
	ApplyCodemod(context.Context, AnalysisMutationRequest) (report.Report, error)
	SaveBaseline(context.Context, AnalysisMutationRequest) (report.Report, string, error)
	SaveDashboardBaseline(context.Context, DashboardMutationRequest) (dashboard.Report, string, error)
}

type AnalysisMutationRequest struct {
	RepoPath          string
	Dependency        string
	TopN              int
	ScopeMode         string
	Language          string
	ConfigPath        string
	IncludePatterns   []string
	ExcludePatterns   []string
	CacheEnabled      bool
	CachePath         string
	CacheReadOnly     bool
	RuntimeProfile    string
	RuntimeTracePath  string
	Features          featureflags.Set
	Thresholds        thresholds.Values
	PolicySources     []string
	PolicyTrace       []report.PolicyMergeTrace
	AllowDirty        bool
	BaselineStorePath string
	BaselineKey       string
	BaselineLabel     string
}

type DashboardMutationRequest struct {
	RepoPath          string
	Repos             []DashboardRepoInput
	ConfigPath        string
	TopN              int
	DefaultLanguage   string
	BaselineStorePath string
	BaselineKey       string
	BaselineLabel     string
	Features          featureflags.Set
}

type DashboardRepoInput struct {
	Name     string `json:"name,omitempty"`
	Path     string `json:"path"`
	Language string `json:"language,omitempty"`
}

type mutationAnalysisArguments struct {
	RepoPath                          string   `json:"repoPath"`
	Dependency                        string   `json:"dependency,omitempty"`
	TopN                              *int     `json:"topN,omitempty"`
	Language                          string   `json:"language,omitempty"`
	ScopeMode                         string   `json:"scopeMode,omitempty"`
	ConfigPath                        string   `json:"configPath,omitempty"`
	Include                           []string `json:"include,omitempty"`
	Exclude                           []string `json:"exclude,omitempty"`
	EnableFeatures                    []string `json:"enableFeatures,omitempty"`
	DisableFeatures                   []string `json:"disableFeatures,omitempty"`
	CacheEnabled                      *bool    `json:"cacheEnabled,omitempty"`
	CachePath                         string   `json:"cachePath,omitempty"`
	CacheReadOnly                     bool     `json:"cacheReadOnly,omitempty"`
	RuntimeProfile                    string   `json:"runtimeProfile,omitempty"`
	RuntimeTracePath                  string   `json:"runtimeTracePath,omitempty"`
	LowConfidenceWarningPercent       *int     `json:"lowConfidenceWarningPercent,omitempty"`
	MinUsagePercentForRecommendations *int     `json:"minUsagePercentForRecommendations,omitempty"`
	MaxUncertainImportCount           *int     `json:"maxUncertainImportCount,omitempty"`
	ScoreWeightUsage                  *float64 `json:"scoreWeightUsage,omitempty"`
	ScoreWeightImpact                 *float64 `json:"scoreWeightImpact,omitempty"`
	ScoreWeightConfidence             *float64 `json:"scoreWeightConfidence,omitempty"`
	LicenseDeny                       []string `json:"licenseDeny,omitempty"`
	LicenseFailOnDeny                 *bool    `json:"licenseFailOnDeny,omitempty"`
	LicenseProvenanceRegistry         *bool    `json:"licenseProvenanceRegistry,omitempty"`
	TimeoutMillis                     int      `json:"timeoutMillis,omitempty"`
}

type codemodApplyArguments struct {
	mutationAnalysisArguments
	ConfirmApply bool `json:"confirmApply"`
	AllowDirty   bool `json:"allowDirty,omitempty"`
}

type baselineSaveArguments struct {
	mutationAnalysisArguments
	BaselineStorePath string `json:"baselineStorePath"`
	BaselineKey       string `json:"baselineKey,omitempty"`
	BaselineLabel     string `json:"baselineLabel,omitempty"`
	ConfirmSave       bool   `json:"confirmSave"`
}

type dashboardBaselineSaveArguments struct {
	RepoPath          string               `json:"repoPath"`
	Repos             []DashboardRepoInput `json:"repos,omitempty"`
	ConfigPath        string               `json:"configPath,omitempty"`
	TopN              *int                 `json:"topN,omitempty"`
	DefaultLanguage   string               `json:"defaultLanguage,omitempty"`
	BaselineStorePath string               `json:"baselineStorePath"`
	BaselineKey       string               `json:"baselineKey,omitempty"`
	BaselineLabel     string               `json:"baselineLabel,omitempty"`
	ConfirmSave       bool                 `json:"confirmSave"`
	EnableFeatures    []string             `json:"enableFeatures,omitempty"`
	DisableFeatures   []string             `json:"disableFeatures,omitempty"`
	TimeoutMillis     int                  `json:"timeoutMillis,omitempty"`
}

type structuredToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type codemodApplyPayload struct {
	SchemaVersion  string                      `json:"schemaVersion"`
	Summary        string                      `json:"summary"`
	RepoPath       string                      `json:"repoPath"`
	Dependency     string                      `json:"dependency"`
	AppliedFiles   int                         `json:"appliedFiles"`
	AppliedPatches int                         `json:"appliedPatches"`
	SkippedFiles   int                         `json:"skippedFiles"`
	SkippedPatches int                         `json:"skippedPatches"`
	FailedFiles    int                         `json:"failedFiles"`
	FailedPatches  int                         `json:"failedPatches"`
	BackupPath     string                      `json:"backupPath,omitempty"`
	Results        []report.CodemodApplyResult `json:"results,omitempty"`
	Report         report.Report               `json:"report"`
	Error          *structuredToolError        `json:"error,omitempty"`
}

type baselineSavePayload struct {
	SchemaVersion     string               `json:"schemaVersion"`
	Summary           string               `json:"summary"`
	RepoPath          string               `json:"repoPath"`
	BaselineStorePath string               `json:"baselineStorePath"`
	BaselineKey       string               `json:"baselineKey"`
	SnapshotPath      string               `json:"snapshotPath"`
	ReportSummary     *report.Summary      `json:"reportSummary,omitempty"`
	Report            report.Report        `json:"report"`
	Error             *structuredToolError `json:"error,omitempty"`
}

type dashboardBaselineSavePayload struct {
	SchemaVersion     string               `json:"schemaVersion"`
	Summary           string               `json:"summary"`
	RepoPath          string               `json:"repoPath"`
	BaselineStorePath string               `json:"baselineStorePath"`
	BaselineKey       string               `json:"baselineKey"`
	SnapshotPath      string               `json:"snapshotPath"`
	DashboardSummary  dashboard.Summary    `json:"dashboardSummary"`
	Report            dashboard.Report     `json:"report"`
	Error             *structuredToolError `json:"error,omitempty"`
}

func (s *Server) mutationToolsEnabled() bool {
	return s.features.Enabled(MutationToolsFeature)
}

func (s *Server) runCodemodApplyTool(ctx context.Context, rawArgs json.RawMessage) toolCallResult {
	if err := s.validateMutationToolCall(); err != nil {
		return toolError(errorCodeInvalidInput, err)
	}

	var args codemodApplyArguments
	if err := decodeStrict(rawArgs, &args); err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	if !args.ConfirmApply {
		return toolError(errorCodeInvalidInput, errors.New("confirmApply must be true to apply codemod mutations"))
	}
	if strings.TrimSpace(args.Dependency) == "" {
		return toolError(errorCodeInvalidInput, errors.New("dependency is required for codemod apply"))
	}
	if args.TopN != nil {
		return toolError(errorCodeInvalidInput, errors.New("topN is not supported for codemod apply"))
	}

	reqCtx, cancel, err := contextForTimeout(ctx, args.TimeoutMillis)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	defer cancel()

	request, err := s.resolveAnalysisMutationRequest(reqCtx, args.mutationAnalysisArguments, mutationAnalysisKindDependency)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	request.AllowDirty = args.AllowDirty

	reportData, runErr := s.mutationRunner.ApplyCodemod(reqCtx, request)
	payload := shapeCodemodApplyPayload(request, reportData, runErr)
	if runErr != nil {
		return mutationToolError(runErr, payload)
	}
	return toolSuccess(payload.Summary, payload)
}

func (s *Server) runBaselineSaveTool(ctx context.Context, rawArgs json.RawMessage) toolCallResult {
	if err := s.validateMutationToolCall(); err != nil {
		return toolError(errorCodeInvalidInput, err)
	}

	var args baselineSaveArguments
	if err := decodeStrict(rawArgs, &args); err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	if !args.ConfirmSave {
		return toolError(errorCodeInvalidInput, errors.New("confirmSave must be true to save baseline snapshots"))
	}

	reqCtx, cancel, err := contextForTimeout(ctx, args.TimeoutMillis)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	defer cancel()

	request, err := s.resolveAnalysisMutationRequest(reqCtx, args.mutationAnalysisArguments, mutationAnalysisKindTopOrDependency)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	storePath, key, err := resolveBaselineMutationTarget(request.RepoPath, args.BaselineStorePath, args.BaselineKey, args.BaselineLabel, "baseline")
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	request.BaselineStorePath = storePath
	request.BaselineKey = key
	request.BaselineLabel = strings.TrimSpace(args.BaselineLabel)

	reportData, savedPath, runErr := s.mutationRunner.SaveBaseline(reqCtx, request)
	if savedPath == "" {
		savedPath = report.BaselineSnapshotPath(storePath, key)
	}
	payload := shapeBaselineSavePayload(request, reportData, savedPath, runErr)
	if runErr != nil {
		return mutationToolError(runErr, payload)
	}
	return toolSuccess(payload.Summary, payload)
}

func (s *Server) runDashboardBaselineSaveTool(ctx context.Context, rawArgs json.RawMessage) toolCallResult {
	if err := s.validateMutationToolCall(); err != nil {
		return toolError(errorCodeInvalidInput, err)
	}

	var args dashboardBaselineSaveArguments
	if err := decodeStrict(rawArgs, &args); err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	if !args.ConfirmSave {
		return toolError(errorCodeInvalidInput, errors.New("confirmSave must be true to save dashboard baseline snapshots"))
	}

	reqCtx, cancel, err := contextForTimeout(ctx, args.TimeoutMillis)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	defer cancel()

	request, err := s.resolveDashboardMutationRequest(reqCtx, args)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	reportData, savedPath, runErr := s.mutationRunner.SaveDashboardBaseline(reqCtx, request)
	if savedPath == "" {
		savedPath = dashboard.BaselineSnapshotPath(request.BaselineStorePath, request.BaselineKey)
	}
	payload := shapeDashboardBaselineSavePayload(request, reportData, savedPath, runErr)
	if runErr != nil {
		return mutationToolError(runErr, payload)
	}
	return toolSuccess(payload.Summary, payload)
}

func (s *Server) validateMutationToolCall() error {
	if !s.mutationToolsEnabled() {
		return fmt.Errorf("%s must be enabled at MCP server startup to use mutation tools", MutationToolsFeature)
	}
	if s.mutationRunner == nil {
		return errors.New("mcp mutation runner is not configured")
	}
	return nil
}

type mutationAnalysisKind string

const (
	mutationAnalysisKindDependency      mutationAnalysisKind = "dependency"
	mutationAnalysisKindTopOrDependency mutationAnalysisKind = "top-or-dependency"
)

func (s *Server) resolveAnalysisMutationRequest(ctx context.Context, args mutationAnalysisArguments, kind mutationAnalysisKind) (AnalysisMutationRequest, error) {
	repoPath, err := validateRepoPath(args.RepoPath)
	if err != nil {
		return AnalysisMutationRequest{}, err
	}
	dependency, topN, err := resolveMutationAnalysisTarget(args, kind)
	if err != nil {
		return AnalysisMutationRequest{}, err
	}
	scopeMode, err := parseScopeMode(args.ScopeMode)
	if err != nil {
		return AnalysisMutationRequest{}, err
	}
	analysisArgs := analysisArgsFromMutation(args)
	loadResult, thresholdsValue, policySources, policyTrace, err := resolveThresholds(repoPath, analysisArgs)
	if err != nil {
		return AnalysisMutationRequest{}, err
	}
	features, err := s.resolveFeatures(loadResult.Features, args.EnableFeatures, args.DisableFeatures)
	if err != nil {
		return AnalysisMutationRequest{}, err
	}
	if err := ctx.Err(); err != nil {
		return AnalysisMutationRequest{}, err
	}

	return AnalysisMutationRequest{
		RepoPath:         repoPath,
		Dependency:       dependency,
		TopN:             topN,
		ScopeMode:        scopeMode,
		Language:         languageOrDefault(args.Language),
		ConfigPath:       strings.TrimSpace(loadResult.ConfigPath),
		IncludePatterns:  mergeStringOptions(loadResult.Scope.Include, args.Include),
		ExcludePatterns:  mergeStringOptions(loadResult.Scope.Exclude, args.Exclude),
		CacheEnabled:     cacheEnabled(args.CacheEnabled),
		CachePath:        strings.TrimSpace(args.CachePath),
		CacheReadOnly:    args.CacheReadOnly,
		RuntimeProfile:   runtimeProfileOrDefault(args.RuntimeProfile),
		RuntimeTracePath: strings.TrimSpace(args.RuntimeTracePath),
		Features:         features,
		Thresholds:       thresholdsValue,
		PolicySources:    policySources,
		PolicyTrace:      policyTrace,
	}, nil
}

func resolveMutationAnalysisTarget(args mutationAnalysisArguments, kind mutationAnalysisKind) (string, int, error) {
	dependency := strings.TrimSpace(args.Dependency)
	topN := topNOrDefault(args.TopN)
	switch kind {
	case mutationAnalysisKindDependency:
		if dependency == "" {
			return "", 0, errors.New("dependency is required")
		}
		return dependency, 0, nil
	case mutationAnalysisKindTopOrDependency:
		if dependency != "" {
			if args.TopN != nil {
				return "", 0, errors.New("topN cannot be combined with dependency")
			}
			return dependency, 0, nil
		}
		if topN < 0 {
			return "", 0, errors.New("topN must be greater than zero")
		}
		return "", topN, nil
	default:
		return "", 0, errors.New("unknown mutation analysis kind")
	}
}

func analysisArgsFromMutation(args mutationAnalysisArguments) analysisToolArguments {
	return analysisToolArguments{
		RepoPath:                          args.RepoPath,
		Dependency:                        args.Dependency,
		TopN:                              args.TopN,
		Language:                          args.Language,
		ScopeMode:                         args.ScopeMode,
		ConfigPath:                        args.ConfigPath,
		Include:                           append([]string{}, args.Include...),
		Exclude:                           append([]string{}, args.Exclude...),
		EnableFeatures:                    append([]string{}, args.EnableFeatures...),
		DisableFeatures:                   append([]string{}, args.DisableFeatures...),
		CacheEnabled:                      args.CacheEnabled,
		CachePath:                         args.CachePath,
		CacheReadOnly:                     args.CacheReadOnly,
		RuntimeProfile:                    args.RuntimeProfile,
		RuntimeTracePath:                  args.RuntimeTracePath,
		LowConfidenceWarningPercent:       args.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: args.MinUsagePercentForRecommendations,
		MaxUncertainImportCount:           args.MaxUncertainImportCount,
		ScoreWeightUsage:                  args.ScoreWeightUsage,
		ScoreWeightImpact:                 args.ScoreWeightImpact,
		ScoreWeightConfidence:             args.ScoreWeightConfidence,
		LicenseDeny:                       append([]string{}, args.LicenseDeny...),
		LicenseFailOnDeny:                 args.LicenseFailOnDeny,
		LicenseProvenanceRegistry:         args.LicenseProvenanceRegistry,
		TimeoutMillis:                     args.TimeoutMillis,
	}
}

func (s *Server) resolveDashboardMutationRequest(ctx context.Context, args dashboardBaselineSaveArguments) (DashboardMutationRequest, error) {
	repoPath, err := validateRepoPath(args.RepoPath)
	if err != nil {
		return DashboardMutationRequest{}, err
	}
	if len(args.Repos) == 0 && strings.TrimSpace(args.ConfigPath) == "" {
		return DashboardMutationRequest{}, errors.New("repos or configPath is required for dashboard baseline save")
	}
	if err := validateDashboardMutationRepos(args.Repos); err != nil {
		return DashboardMutationRequest{}, err
	}
	topN := topNOrDefault(args.TopN)
	if topN < 0 {
		return DashboardMutationRequest{}, errors.New("topN must be greater than zero")
	}
	storePath, key, err := resolveBaselineMutationTarget(repoPath, args.BaselineStorePath, args.BaselineKey, args.BaselineLabel, "dashboard baseline")
	if err != nil {
		return DashboardMutationRequest{}, err
	}
	features, err := s.resolveFeatures(thresholds.FeatureConfig{}, args.EnableFeatures, args.DisableFeatures)
	if err != nil {
		return DashboardMutationRequest{}, err
	}
	if err := ctx.Err(); err != nil {
		return DashboardMutationRequest{}, err
	}
	return DashboardMutationRequest{
		RepoPath:          repoPath,
		Repos:             append([]DashboardRepoInput{}, args.Repos...),
		ConfigPath:        strings.TrimSpace(args.ConfigPath),
		TopN:              topN,
		DefaultLanguage:   languageOrDefault(args.DefaultLanguage),
		BaselineStorePath: storePath,
		BaselineKey:       key,
		BaselineLabel:     strings.TrimSpace(args.BaselineLabel),
		Features:          features,
	}, nil
}

func validateDashboardMutationRepos(repos []DashboardRepoInput) error {
	for _, repo := range repos {
		path := strings.TrimSpace(repo.Path)
		if path == "" {
			return errors.New("dashboard repo path is required")
		}
		if strings.Contains(strings.ToLower(path), "://") {
			return fmt.Errorf("dashboard repo path must be a local filesystem path: %s", path)
		}
		if strings.ContainsRune(path, '\x00') {
			return errors.New("dashboard repo path contains an invalid NUL byte")
		}
	}
	return nil
}

func resolveBaselineMutationTarget(repoPath, storePath, key, label, keyName string) (string, string, error) {
	resolvedStorePath, err := resolveLocalMutationPath(repoPath, storePath, "baselineStorePath")
	if err != nil {
		return "", "", err
	}
	trimmedKey := strings.TrimSpace(key)
	trimmedLabel := strings.TrimSpace(label)
	if trimmedKey != "" && trimmedLabel != "" {
		return "", "", errors.New("baselineKey and baselineLabel cannot both be provided")
	}
	switch {
	case trimmedLabel != "":
		return resolvedStorePath, "label:" + trimmedLabel, nil
	case trimmedKey != "":
		return resolvedStorePath, trimmedKey, nil
	default:
		currentKey := resolveMutationCurrentBaselineKey(repoPath)
		if currentKey == "" {
			return "", "", fmt.Errorf("unable to resolve git commit for %s key; pass baselineLabel or baselineKey", keyName)
		}
		return resolvedStorePath, currentKey, nil
	}
}

func resolveLocalMutationPath(repoPath, rawPath, field string) (string, error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return "", fmt.Errorf("%s is required", field)
	}
	if strings.Contains(strings.ToLower(trimmed), "://") {
		return "", fmt.Errorf("%s must be a local filesystem path", field)
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", fmt.Errorf("%s contains an invalid NUL byte", field)
	}
	if filepath.IsAbs(trimmed) {
		return filepath.Clean(trimmed), nil
	}
	return filepath.Join(repoPath, trimmed), nil
}

func resolveMutationCurrentBaselineKey(repoPath string) string {
	sha, err := workspace.CurrentCommitSHA(repoPath)
	if err != nil || strings.TrimSpace(sha) == "" {
		return ""
	}
	return "commit:" + sha
}

func shapeCodemodApplyPayload(req AnalysisMutationRequest, reportData report.Report, err error) codemodApplyPayload {
	apply := findCodemodApplyReport(reportData, req.Dependency)
	payload := codemodApplyPayload{
		SchemaVersion: report.SchemaVersion,
		RepoPath:      req.RepoPath,
		Dependency:    req.Dependency,
		Report:        reportData,
	}
	if apply != nil {
		payload.AppliedFiles = apply.AppliedFiles
		payload.AppliedPatches = apply.AppliedPatches
		payload.SkippedFiles = apply.SkippedFiles
		payload.SkippedPatches = apply.SkippedPatches
		payload.FailedFiles = apply.FailedFiles
		payload.FailedPatches = apply.FailedPatches
		payload.BackupPath = apply.BackupPath
		payload.Results = append([]report.CodemodApplyResult{}, apply.Results...)
	}
	payload.Summary = summarizeCodemodApply(req.Dependency, apply, err)
	if err != nil {
		payload.Error = &structuredToolError{Code: errorCodeToolFailed, Message: err.Error()}
	}
	return payload
}

func findCodemodApplyReport(reportData report.Report, dependency string) *report.CodemodApplyReport {
	for _, item := range reportData.Dependencies {
		if strings.TrimSpace(dependency) != "" && item.Name != dependency {
			continue
		}
		if item.Codemod != nil && item.Codemod.Apply != nil {
			return item.Codemod.Apply
		}
	}
	return nil
}

func summarizeCodemodApply(dependency string, apply *report.CodemodApplyReport, err error) string {
	prefix := fmt.Sprintf("Codemod apply completed for %s", dependency)
	if err != nil {
		prefix = fmt.Sprintf("Codemod apply failed for %s", dependency)
	}
	if apply == nil {
		return prefix + ": no codemod changes were produced."
	}
	return fmt.Sprintf("%s: %d files changed, %d patches applied, %d files skipped, %d files failed.", prefix, apply.AppliedFiles, apply.AppliedPatches, apply.SkippedFiles, apply.FailedFiles)
}

func shapeBaselineSavePayload(req AnalysisMutationRequest, reportData report.Report, savedPath string, err error) baselineSavePayload {
	payload := baselineSavePayload{
		SchemaVersion:     report.SchemaVersion,
		Summary:           summarizeSnapshotSave("baseline", req.BaselineKey, savedPath, err),
		RepoPath:          req.RepoPath,
		BaselineStorePath: req.BaselineStorePath,
		BaselineKey:       req.BaselineKey,
		SnapshotPath:      savedPath,
		ReportSummary:     reportData.Summary,
		Report:            reportData,
	}
	payload.Error = structuredError(err)
	return payload
}

func shapeDashboardBaselineSavePayload(req DashboardMutationRequest, reportData dashboard.Report, savedPath string, err error) dashboardBaselineSavePayload {
	payload := dashboardBaselineSavePayload{
		SchemaVersion:     dashboard.BaselineSnapshotSchemaVersion,
		Summary:           summarizeSnapshotSave("dashboard baseline", req.BaselineKey, savedPath, err),
		RepoPath:          req.RepoPath,
		BaselineStorePath: req.BaselineStorePath,
		BaselineKey:       req.BaselineKey,
		SnapshotPath:      savedPath,
		DashboardSummary:  reportData.Summary,
		Report:            reportData,
	}
	payload.Error = structuredError(err)
	return payload
}

func summarizeSnapshotSave(kind, key, savedPath string, err error) string {
	if err != nil {
		return fmt.Sprintf("%s snapshot save failed for %s.", titleCaseFirst(kind), key)
	}
	return fmt.Sprintf("Saved %s snapshot %s to %s.", kind, key, savedPath)
}

func structuredError(err error) *structuredToolError {
	if err == nil {
		return nil
	}
	return &structuredToolError{Code: errorCodeToolFailed, Message: err.Error()}
}

func titleCaseFirst(value string) string {
	if value == "" {
		return ""
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func mutationToolError(err error, structured any) toolCallResult {
	message := err.Error()
	return toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: message,
		}},
		StructuredContent: structured,
		IsError:           true,
	}
}

func codemodApplyInputSchema() map[string]any {
	properties := mutationAnalysisProperties()
	properties["dependency"] = map[string]any{"type": "string", "description": "Dependency name to analyse and apply codemods for."}
	properties["confirmApply"] = map[string]any{"type": "boolean", "description": "Must be true to confirm source-file mutation."}
	properties["allowDirty"] = map[string]any{"type": "boolean", "default": false, "description": "Allow codemod apply when the git worktree has uncommitted changes."}
	return mutationObjectSchema(properties, []string{"repoPath", "dependency", "confirmApply"})
}

func baselineSaveInputSchema() map[string]any {
	properties := mutationAnalysisProperties()
	properties["dependency"] = map[string]any{"type": "string", "description": "Optional dependency to analyse before saving."}
	properties["topN"] = map[string]any{"type": "integer", "minimum": 1, "default": defaultTopN}
	addBaselineSaveProperties(properties, "save the baseline snapshot")
	return mutationObjectSchema(properties, []string{"repoPath", "baselineStorePath", "confirmSave"})
}

func dashboardBaselineSaveInputSchema() map[string]any {
	properties := map[string]any{
		"repoPath":        map[string]any{"type": "string", "description": "Local repository path used for default commit-key resolution."},
		"repos":           dashboardReposSchema(),
		"configPath":      map[string]any{"type": "string"},
		"topN":            map[string]any{"type": "integer", "minimum": 1, "default": defaultTopN},
		"defaultLanguage": map[string]any{"type": "string", "default": defaultLanguage},
		"enableFeatures":  stringArraySchema(),
		"disableFeatures": stringArraySchema(),
		"timeoutMillis":   map[string]any{"type": "integer", "minimum": 1, "maximum": maxTimeoutMillis},
	}
	addBaselineSaveProperties(properties, "save the dashboard baseline snapshot")
	schema := mutationObjectSchema(properties, []string{"repoPath", "baselineStorePath", "confirmSave"})
	schema["anyOf"] = []map[string]any{
		{"required": []string{"repos"}},
		{"required": []string{"configPath"}},
	}
	return schema
}

func mutationAnalysisProperties() map[string]any {
	properties := commonAnalysisProperties()
	delete(properties, "runtimeTestCommand")
	return properties
}

func addBaselineSaveProperties(properties map[string]any, confirmationDescription string) {
	properties["baselineStorePath"] = map[string]any{"type": "string", "description": "Local directory where the immutable snapshot will be written. Relative paths resolve under repoPath."}
	properties["baselineKey"] = map[string]any{"type": "string", "description": "Explicit snapshot key. Mutually exclusive with baselineLabel."}
	properties["baselineLabel"] = map[string]any{"type": "string", "description": "Label used to create a label:<value> snapshot key. Mutually exclusive with baselineKey."}
	properties["confirmSave"] = map[string]any{"type": "boolean", "description": "Must be true to " + confirmationDescription + "."}
}

func mutationObjectSchema(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func dashboardReposSchema() map[string]any {
	return map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":     map[string]any{"type": "string"},
				"path":     map[string]any{"type": "string"},
				"language": map[string]any{"type": "string"},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
	}
}
