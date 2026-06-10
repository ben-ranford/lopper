package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/workspace"
)

const (
	toolAnalyseTop        = "lopper_analyse_top_dependencies"
	toolAnalyseDependency = "lopper_analyse_dependency"
	toolCompareBaseline   = "lopper_compare_baseline"
	toolListLanguages     = "lopper_list_languages"
)

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolCallResult struct {
	Content           []contentItem `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type analysisToolArguments struct {
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
	BaselinePath                      string   `json:"baselinePath,omitempty"`
	BaselineStorePath                 string   `json:"baselineStorePath,omitempty"`
	BaselineKey                       string   `json:"baselineKey,omitempty"`
	TimeoutMillis                     int      `json:"timeoutMillis,omitempty"`
}

type listLanguagesArguments struct {
	RepoPath        string   `json:"repoPath,omitempty"`
	ConfigPath      string   `json:"configPath,omitempty"`
	EnableFeatures  []string `json:"enableFeatures,omitempty"`
	DisableFeatures []string `json:"disableFeatures,omitempty"`
	TimeoutMillis   int      `json:"timeoutMillis,omitempty"`
}

type analysisPayload struct {
	SchemaVersion string        `json:"schemaVersion"`
	Summary       string        `json:"summary"`
	Report        report.Report `json:"report"`
}

type languageMetadata struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"`
}

type languagesPayload struct {
	Summary             string                         `json:"summary"`
	Languages           []languageMetadata             `json:"languages"`
	LanguageModes       []string                       `json:"languageModes"`
	RuntimeProfiles     []string                       `json:"runtimeProfiles"`
	DefaultScopeMode    string                         `json:"defaultScopeMode"`
	DefaultTopN         int                            `json:"defaultTopN"`
	ConfigPath          string                         `json:"configPath,omitempty"`
	EffectiveThresholds report.EffectiveThresholds     `json:"effectiveThresholds"`
	RemovalWeights      report.RemovalCandidateWeights `json:"removalCandidateWeights"`
	LicensePolicy       report.LicensePolicy           `json:"licensePolicy"`
	EnabledFeatures     []string                       `json:"enabledFeatures,omitempty"`
	PolicySources       []string                       `json:"policySources,omitempty"`
	PolicyTrace         []report.PolicyMergeTrace      `json:"policyTrace,omitempty"`
}

type resolvedToolRequest struct {
	analysisRequest analysis.Request
	repoPath        string
	thresholds      thresholds.Values
	policySources   []string
	policyTrace     []report.PolicyMergeTrace
	baselinePath    string
	baselineKey     string
	currentKey      string
}

func (s *Server) tools() []toolSpec {
	return []toolSpec{
		{
			Name:        toolAnalyseTop,
			Description: "Analyse and rank the top dependencies by unused surface area for a local repository.",
			InputSchema: analysisInputSchema(false, false, true, false),
		},
		{
			Name:        toolAnalyseDependency,
			Description: "Analyse one dependency in a local repository and return the report schema plus a concise summary.",
			InputSchema: analysisInputSchema(true, true, true, false),
		},
		{
			Name:        toolCompareBaseline,
			Description: "Run a read-only analysis and compare it with a baseline report or immutable baseline snapshot.",
			InputSchema: analysisInputSchema(false, true, true, true),
		},
		{
			Name:        toolListLanguages,
			Description: "List supported language adapters, modes, runtime profiles, and effective config metadata.",
			InputSchema: listLanguagesInputSchema(),
		},
	}
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (toolCallResult, *rpcError) {
	var call toolCallParams
	if err := decodeStrict(params, &call); err != nil {
		return toolCallResult{}, &rpcError{Code: codeInvalidParams, Message: "invalid tools/call params", Data: err.Error()}
	}
	if strings.TrimSpace(call.Name) == "" {
		return toolCallResult{}, &rpcError{Code: codeInvalidParams, Message: "tool name is required"}
	}
	args := call.Arguments
	if len(args) == 0 {
		args = []byte("{}")
	}

	switch call.Name {
	case toolAnalyseTop:
		return s.runAnalysisTool(ctx, args, analysisToolKindTop), nil
	case toolAnalyseDependency:
		return s.runAnalysisTool(ctx, args, analysisToolKindDependency), nil
	case toolCompareBaseline:
		return s.runAnalysisTool(ctx, args, analysisToolKindCompare), nil
	case toolListLanguages:
		return s.runListLanguagesTool(ctx, args), nil
	default:
		return toolCallResult{}, &rpcError{Code: codeInvalidParams, Message: fmt.Sprintf("unknown tool: %s", call.Name)}
	}
}

type analysisToolKind string

const (
	analysisToolKindTop        analysisToolKind = "top"
	analysisToolKindDependency analysisToolKind = "dependency"
	analysisToolKindCompare    analysisToolKind = "compare"
)

func (s *Server) runAnalysisTool(ctx context.Context, rawArgs json.RawMessage, kind analysisToolKind) toolCallResult {
	var args analysisToolArguments
	if err := decodeStrict(rawArgs, &args); err != nil {
		return toolError(errorCodeInvalidInput, err)
	}

	reqCtx, cancel, err := contextForTimeout(ctx, args.TimeoutMillis)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	defer cancel()

	resolved, err := s.resolveAnalysisRequest(reqCtx, args, kind)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}

	analyzer := s.analyzer
	if analyzer == nil {
		analyzer = analysis.NewService()
	}
	reportData, err := analyzer.Analyse(reqCtx, resolved.analysisRequest)
	if err != nil {
		return analysisErrorResult(err)
	}
	decorateReport(&reportData, resolved.thresholds, resolved.policySources, resolved.policyTrace)
	if resolved.baselinePath != "" {
		reportData, err = applyBaseline(reportData, resolved.baselinePath, resolved.baselineKey, resolved.currentKey)
		if err != nil {
			return analysisErrorResult(err)
		}
	}

	summary := summarizeReport(kind, reportData)
	payload := analysisPayload{
		SchemaVersion: report.SchemaVersion,
		Summary:       summary,
		Report:        reportData,
	}
	return toolSuccess(summary, payload)
}

func (s *Server) resolveAnalysisRequest(ctx context.Context, args analysisToolArguments, kind analysisToolKind) (resolvedToolRequest, error) {
	repoPath, err := validateRepoPath(args.RepoPath)
	if err != nil {
		return resolvedToolRequest{}, err
	}

	dependency, topN, err := resolveAnalysisTarget(args, kind)
	if err != nil {
		return resolvedToolRequest{}, err
	}

	scopeMode, err := parseScopeMode(args.ScopeMode)
	if err != nil {
		return resolvedToolRequest{}, err
	}
	loadResult, thresholdsValue, policySources, policyTrace, err := resolveThresholds(repoPath, args)
	if err != nil {
		return resolvedToolRequest{}, err
	}
	features, err := s.resolveFeatures(loadResult.Features, args.EnableFeatures, args.DisableFeatures)
	if err != nil {
		return resolvedToolRequest{}, err
	}
	analysisReq := newAnalysisRequest(args, analysisRequestContext{
		repoPath:   repoPath,
		dependency: dependency,
		topN:       topN,
		scopeMode:  scopeMode,
		loadResult: loadResult,
		thresholds: thresholdsValue,
		featureSet: features,
	})

	baselinePath, baselineKey, currentKey, err := resolveBaselineComparison(repoPath, args)
	if err != nil {
		return resolvedToolRequest{}, err
	}
	return resolvedToolRequest{
		analysisRequest: analysisReq,
		repoPath:        repoPath,
		thresholds:      thresholdsValue,
		policySources:   policySources,
		policyTrace:     policyTrace,
		baselinePath:    baselinePath,
		baselineKey:     baselineKey,
		currentKey:      currentKey,
	}, ctx.Err()
}

type analysisRequestContext struct {
	repoPath   string
	dependency string
	topN       int
	scopeMode  string
	loadResult thresholds.LoadResult
	thresholds thresholds.Values
	featureSet featureflags.Set
}

func resolveAnalysisTarget(args analysisToolArguments, kind analysisToolKind) (string, int, error) {
	dependency := strings.TrimSpace(args.Dependency)
	topN := topNOrDefault(args.TopN)
	switch kind {
	case analysisToolKindDependency:
		if dependency == "" {
			return "", 0, errors.New("dependency is required")
		}
		topN = 0
	case analysisToolKindTop:
		if dependency != "" {
			return "", 0, errors.New("dependency is not supported for top dependency analysis")
		}
	case analysisToolKindCompare:
		if !hasBaselineInput(args) {
			return "", 0, errors.New("baselinePath or baselineStorePath is required")
		}
		if dependency != "" {
			topN = 0
		}
	default:
		return "", 0, errors.New("unknown analysis tool kind")
	}
	if topN < 0 {
		return "", 0, errors.New("topN must be greater than zero")
	}
	return dependency, topN, nil
}

func hasBaselineInput(args analysisToolArguments) bool {
	return strings.TrimSpace(args.BaselinePath) != "" || strings.TrimSpace(args.BaselineStorePath) != ""
}

func newAnalysisRequest(args analysisToolArguments, req analysisRequestContext) analysis.Request {
	runtimeTracePath := strings.TrimSpace(args.RuntimeTracePath)
	lowConfidence := req.thresholds.LowConfidenceWarningPercent
	minUsage := req.thresholds.MinUsagePercentForRecommendations
	weights := thresholds.RemovalCandidateWeights(req.thresholds)
	return analysis.Request{
		RepoPath:                          req.repoPath,
		Dependency:                        req.dependency,
		TopN:                              req.topN,
		ScopeMode:                         req.scopeMode,
		Language:                          languageOrDefault(args.Language),
		ConfigPath:                        strings.TrimSpace(req.loadResult.ConfigPath),
		RuntimeProfile:                    runtimeProfileOrDefault(args.RuntimeProfile),
		RuntimeTracePath:                  runtimeTracePath,
		RuntimeTracePathExplicit:          runtimeTracePath != "",
		IncludePatterns:                   mergeStringOptions(req.loadResult.Scope.Include, args.Include),
		ExcludePatterns:                   mergeStringOptions(req.loadResult.Scope.Exclude, args.Exclude),
		Features:                          req.featureSet,
		LowConfidenceWarningPercent:       &lowConfidence,
		MinUsagePercentForRecommendations: &minUsage,
		RemovalCandidateWeights:           &weights,
		LicenseDenyList:                   append([]string{}, req.thresholds.LicenseDenyList...),
		IncludeRegistryProvenance:         req.thresholds.LicenseIncludeRegistryProvenance,
		Cache: &analysis.CacheOptions{
			Enabled:  cacheEnabled(args.CacheEnabled),
			Path:     strings.TrimSpace(args.CachePath),
			ReadOnly: args.CacheReadOnly,
		},
	}
}

func (s *Server) runListLanguagesTool(ctx context.Context, rawArgs json.RawMessage) toolCallResult {
	var args listLanguagesArguments
	if err := decodeStrict(rawArgs, &args); err != nil {
		return toolError(errorCodeInvalidInput, err)
	}

	reqCtx, cancel, err := contextForTimeout(ctx, args.TimeoutMillis)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	defer cancel()

	var loadResult thresholds.LoadResult
	thresholdValues := thresholds.Defaults()
	if strings.TrimSpace(args.RepoPath) != "" || strings.TrimSpace(args.ConfigPath) != "" {
		repoPath, err := validateRepoPath(args.RepoPath)
		if err != nil {
			return toolError(errorCodeInvalidInput, err)
		}
		loadResult, err = thresholds.LoadWithPolicy(repoPath, strings.TrimSpace(args.ConfigPath))
		if err != nil {
			return toolError(errorCodeInvalidInput, err)
		}
		thresholdValues = loadResult.Resolved
	}
	features, err := s.resolveFeatures(loadResult.Features, args.EnableFeatures, args.DisableFeatures)
	if err != nil {
		return toolError(errorCodeInvalidInput, err)
	}
	if err := reqCtx.Err(); err != nil {
		return analysisErrorResult(err)
	}

	languages := s.languageMetadata()
	summary := fmt.Sprintf("Lopper supports %d language adapters.", len(languages))
	payload := languagesPayload{
		Summary:          summary,
		Languages:        languages,
		LanguageModes:    []string{language.Auto, language.All},
		RuntimeProfiles:  []string{"node-import", "node-require", "browser-import", "browser-require"},
		DefaultScopeMode: defaultScopeMode,
		DefaultTopN:      defaultTopN,
		ConfigPath:       strings.TrimSpace(loadResult.ConfigPath),
		EffectiveThresholds: report.EffectiveThresholds{
			FailOnIncreasePercent:             thresholdValues.FailOnIncreasePercent,
			LowConfidenceWarningPercent:       thresholdValues.LowConfidenceWarningPercent,
			MinUsagePercentForRecommendations: thresholdValues.MinUsagePercentForRecommendations,
			MaxUncertainImportCount:           thresholdValues.MaxUncertainImportCount,
		},
		RemovalWeights:  thresholds.RemovalCandidateWeights(thresholdValues),
		LicensePolicy:   licensePolicy(thresholdValues),
		EnabledFeatures: features.EnabledCodes(),
		PolicySources:   append([]string{}, loadResult.PolicySources...),
		PolicyTrace:     append([]report.PolicyMergeTrace{}, loadResult.PolicyTrace...),
	}
	return toolSuccess(summary, payload)
}

func (s *Server) languageMetadata() []languageMetadata {
	registry := s.languageRegistry
	if registry == nil {
		if service, ok := s.analyzer.(*analysis.Service); ok {
			registry = service.Registry
		}
	}
	if registry == nil {
		registry = analysis.NewService().Registry
	}

	adapters := registry.Metadata()
	items := make([]languageMetadata, 0, len(adapters))
	for _, adapter := range adapters {
		items = append(items, languageMetadata{
			ID:      adapter.ID,
			Aliases: append([]string{}, adapter.Aliases...),
		})
	}
	return items
}

func (s *Server) resolveFeatures(config thresholds.FeatureConfig, enable []string, disable []string) (featureflags.Set, error) {
	registry := s.featureRegistry
	if registry == nil {
		if err := featureflags.ValidateDefaultRegistry(); err != nil {
			return featureflags.Set{}, err
		}
		registry = featureflags.DefaultRegistry()
	}
	info := currentVersion()
	channel, err := featureflags.NormalizeChannel(info.BuildChannel)
	if err != nil {
		return featureflags.Set{}, err
	}
	var lock *featureflags.ReleaseLock
	if channel == featureflags.ChannelRelease {
		lock, err = featureflags.DefaultReleaseLock(info.Version)
		if err != nil {
			return featureflags.Set{}, err
		}
	}

	enabled := append([]string{}, config.Enable...)
	enabled = append(enabled, enable...)
	disabled := append([]string{}, config.Disable...)
	disabled = append(disabled, disable...)
	return registry.Resolve(featureflags.ResolveOptions{
		Channel: channel,
		Lock:    lock,
		Enable:  enabled,
		Disable: disabled,
	})
}

func resolveThresholds(repoPath string, args analysisToolArguments) (thresholds.LoadResult, thresholds.Values, []string, []report.PolicyMergeTrace, error) {
	loadResult, err := thresholds.LoadWithPolicy(repoPath, strings.TrimSpace(args.ConfigPath))
	if err != nil {
		return thresholds.LoadResult{}, thresholds.Values{}, nil, nil, err
	}
	overrides := thresholds.Overrides{
		LowConfidenceWarningPercent:       args.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: args.MinUsagePercentForRecommendations,
		MaxUncertainImportCount:           args.MaxUncertainImportCount,
		RemovalCandidateWeightUsage:       args.ScoreWeightUsage,
		RemovalCandidateWeightImpact:      args.ScoreWeightImpact,
		RemovalCandidateWeightConfidence:  args.ScoreWeightConfidence,
		LicenseDenyList:                   append([]string{}, args.LicenseDeny...),
		LicenseFailOnDeny:                 args.LicenseFailOnDeny,
		LicenseIncludeRegistryProvenance:  args.LicenseProvenanceRegistry,
	}
	if err := overrides.Validate(); err != nil {
		return thresholds.LoadResult{}, thresholds.Values{}, nil, nil, err
	}
	resolved := overrides.Apply(loadResult.Resolved)
	if err := resolved.Validate(); err != nil {
		return thresholds.LoadResult{}, thresholds.Values{}, nil, nil, err
	}
	policySources := append([]string{}, loadResult.PolicySources...)
	policyTrace := append([]report.PolicyMergeTrace{}, loadResult.PolicyTrace...)
	if hasMCPPolicyOverrides(args) {
		policySources = prependPolicySource("mcp", policySources)
		policyTrace = mergeMCPPolicyTrace(policyTrace, args)
	}
	return loadResult, resolved, policySources, policyTrace, nil
}

func hasMCPPolicyOverrides(args analysisToolArguments) bool {
	return args.LowConfidenceWarningPercent != nil ||
		args.MinUsagePercentForRecommendations != nil ||
		args.MaxUncertainImportCount != nil ||
		args.ScoreWeightUsage != nil ||
		args.ScoreWeightImpact != nil ||
		args.ScoreWeightConfidence != nil ||
		len(args.LicenseDeny) > 0 ||
		args.LicenseFailOnDeny != nil ||
		args.LicenseProvenanceRegistry != nil
}

func prependPolicySource(source string, sources []string) []string {
	out := []string{source}
	seen := map[string]struct{}{source: {}}
	for _, item := range sources {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func mergeMCPPolicyTrace(trace []report.PolicyMergeTrace, args analysisToolArguments) []report.PolicyMergeTrace {
	updates := mcpPolicyTrace(args)
	if len(updates) == 0 {
		return append([]report.PolicyMergeTrace{}, trace...)
	}
	out := append([]report.PolicyMergeTrace{}, trace...)
	index := make(map[string]int, len(out))
	for i, item := range out {
		index[item.Field] = i
	}
	for _, item := range updates {
		if existing, ok := index[item.Field]; ok {
			out[existing].Source = item.Source
			continue
		}
		index[item.Field] = len(out)
		out = append(out, item)
	}
	return out
}

func mcpPolicyTrace(args analysisToolArguments) []report.PolicyMergeTrace {
	trace := make([]report.PolicyMergeTrace, 0, 9)
	if args.LowConfidenceWarningPercent != nil {
		trace = append(trace, report.PolicyMergeTrace{Field: "thresholds.low_confidence_warning_percent", Source: "mcp"})
	}
	if args.MinUsagePercentForRecommendations != nil {
		trace = append(trace, report.PolicyMergeTrace{Field: "thresholds.min_usage_percent_for_recommendations", Source: "mcp"})
	}
	if args.MaxUncertainImportCount != nil {
		trace = append(trace, report.PolicyMergeTrace{Field: "thresholds.max_uncertain_import_count", Source: "mcp"})
	}
	if args.ScoreWeightUsage != nil {
		trace = append(trace, report.PolicyMergeTrace{Field: "removal_candidate_weights.usage", Source: "mcp"})
	}
	if args.ScoreWeightImpact != nil {
		trace = append(trace, report.PolicyMergeTrace{Field: "removal_candidate_weights.impact", Source: "mcp"})
	}
	if args.ScoreWeightConfidence != nil {
		trace = append(trace, report.PolicyMergeTrace{Field: "removal_candidate_weights.confidence", Source: "mcp"})
	}
	if len(args.LicenseDeny) > 0 {
		trace = append(trace, report.PolicyMergeTrace{Field: "license.deny", Source: "mcp"})
	}
	if args.LicenseFailOnDeny != nil {
		trace = append(trace, report.PolicyMergeTrace{Field: "license.fail_on_deny", Source: "mcp"})
	}
	if args.LicenseProvenanceRegistry != nil {
		trace = append(trace, report.PolicyMergeTrace{Field: "license.include_registry_provenance", Source: "mcp"})
	}
	return trace
}

func resolveBaselineComparison(repoPath string, args analysisToolArguments) (string, string, string, error) {
	baselinePath := strings.TrimSpace(args.BaselinePath)
	baselineStorePath := strings.TrimSpace(args.BaselineStorePath)
	baselineKey := strings.TrimSpace(args.BaselineKey)
	if baselinePath != "" && baselineStorePath != "" {
		return "", "", "", errors.New("baselinePath and baselineStorePath cannot both be provided")
	}
	if baselineStorePath != "" {
		if baselineKey == "" {
			return "", "", "", errors.New("baselineKey is required with baselineStorePath")
		}
		baselinePath = report.BaselineSnapshotPath(baselineStorePath, baselineKey)
	}
	if baselinePath == "" {
		return "", "", "", nil
	}
	currentKey := "current"
	if sha, err := workspace.CurrentCommitSHA(repoPath); err == nil {
		currentKey = "commit:" + sha
	}
	return baselinePath, baselineKey, currentKey, nil
}

func applyBaseline(current report.Report, baselinePath, requestedKey, currentKey string) (report.Report, error) {
	baseline, loadedKey, err := report.LoadWithKey(baselinePath)
	if err != nil {
		return current, err
	}
	if strings.TrimSpace(requestedKey) == "" {
		requestedKey = loadedKey
	}
	return report.ApplyBaselineWithKeys(current, baseline, requestedKey, currentKey)
}

func decorateReport(reportData *report.Report, values thresholds.Values, policySources []string, policyTrace []report.PolicyMergeTrace) {
	if reportData == nil {
		return
	}
	effective := report.EffectiveThresholds{
		FailOnIncreasePercent:             values.FailOnIncreasePercent,
		LowConfidenceWarningPercent:       values.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: values.MinUsagePercentForRecommendations,
		MaxUncertainImportCount:           values.MaxUncertainImportCount,
	}
	reportData.EffectiveThresholds = &effective
	reportData.EffectivePolicy = &report.EffectivePolicy{
		Sources:                 append([]string{}, policySources...),
		MergeTrace:              append([]report.PolicyMergeTrace{}, policyTrace...),
		Thresholds:              effective,
		RemovalCandidateWeights: thresholds.RemovalCandidateWeights(values),
		License:                 licensePolicy(values),
	}
}

func licensePolicy(values thresholds.Values) report.LicensePolicy {
	return report.LicensePolicy{
		Deny:                      report.SortedDenyList(values.LicenseDenyList),
		FailOnDenied:              values.LicenseFailOnDeny,
		IncludeRegistryProvenance: values.LicenseIncludeRegistryProvenance,
	}
}

func decodeStrict(data json.RawMessage, target any) error {
	if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		data = []byte("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func contextForTimeout(ctx context.Context, timeoutMillis int) (context.Context, context.CancelFunc, error) {
	if timeoutMillis == defaultTimeoutMillis {
		return ctx, func() {
			// No timeout context was created, so cancellation has no work to do.
		}, nil
	}
	if timeoutMillis < 0 || timeoutMillis > maxTimeoutMillis {
		return nil, nil, fmt.Errorf("timeoutMillis must be between 1 and %d", maxTimeoutMillis)
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMillis)*time.Millisecond)
	return timeoutCtx, cancel, nil
}

func validateRepoPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("repoPath is required")
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(raw)), "://") {
		return "", errors.New("repoPath must be a local filesystem path")
	}
	repoPath, err := workspace.NormalizeRepoPath(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(repoPath)
	if err != nil {
		return "", fmt.Errorf("stat repoPath: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repoPath is not a directory: %s", repoPath)
	}
	return repoPath, nil
}

func topNOrDefault(topN *int) int {
	if topN == nil {
		return defaultTopN
	}
	if *topN <= 0 {
		return -1
	}
	return *topN
}

func parseScopeMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", analysis.ScopeModePackage:
		return analysis.ScopeModePackage, nil
	case analysis.ScopeModeRepo:
		return analysis.ScopeModeRepo, nil
	case analysis.ScopeModeChangedPackages:
		return analysis.ScopeModeChangedPackages, nil
	default:
		return "", fmt.Errorf("invalid scopeMode: %s", value)
	}
}

func languageOrDefault(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultLanguage
	}
	return value
}

func runtimeProfileOrDefault(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultRuntimeProfile
	}
	return value
}

func cacheEnabled(value *bool) bool {
	if value == nil {
		return true
	}
	return *value
}

func mergeStringOptions(configValues, argumentValues []string) []string {
	if len(argumentValues) > 0 {
		return append([]string{}, argumentValues...)
	}
	if len(configValues) > 0 {
		return append([]string{}, configValues...)
	}
	return nil
}

func toolSuccess(summary string, structured any) toolCallResult {
	return toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: summary,
		}},
		StructuredContent: structured,
	}
}

func analysisErrorResult(err error) toolCallResult {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return toolError(errorCodeTimeout, err)
	case errors.Is(err, context.Canceled):
		return toolError(errorCodeCancelled, err)
	default:
		return toolError(errorCodeToolFailed, err)
	}
}

func toolError(code string, err error) toolCallResult {
	message := err.Error()
	return toolCallResult{
		Content: []contentItem{{
			Type: "text",
			Text: message,
		}},
		StructuredContent: map[string]any{
			"error": map[string]any{
				"code":    code,
				"message": message,
			},
		},
		IsError: true,
	}
}

func summarizeReport(kind analysisToolKind, reportData report.Report) string {
	label := "Analysis"
	switch kind {
	case analysisToolKindTop:
		label = "Top dependency analysis"
	case analysisToolKindDependency:
		label = "Dependency analysis"
	case analysisToolKindCompare:
		label = "Baseline comparison"
	}
	if reportData.Summary == nil {
		return fmt.Sprintf("%s completed with no dependencies.", label)
	}
	waste, ok := report.WastePercent(reportData.Summary)
	if !ok {
		return fmt.Sprintf("%s completed: %d dependencies, %.1f%% used exports.", label, reportData.Summary.DependencyCount, reportData.Summary.UsedPercent)
	}
	summary := fmt.Sprintf("%s completed: %d dependencies, %.1f%% used exports, %.1f%% waste.", label, reportData.Summary.DependencyCount, reportData.Summary.UsedPercent, waste)
	if reportData.BaselineComparison != nil {
		summary += fmt.Sprintf(" Waste delta: %.1f%%.", reportData.BaselineComparison.SummaryDelta.WastePercentDelta)
	}
	return summary
}

func analysisInputSchema(requireDependency, allowDependency, allowTopN, requireBaseline bool) map[string]any {
	properties := commonAnalysisProperties()
	required := []string{"repoPath"}
	if allowDependency {
		properties["dependency"] = map[string]any{"type": "string"}
	}
	if requireDependency {
		required = append(required, "dependency")
	}
	if allowTopN {
		properties["topN"] = map[string]any{"type": "integer", "minimum": 1, "default": defaultTopN}
	}
	if requireBaseline {
		properties["baselinePath"] = map[string]any{"type": "string", "description": "Baseline report JSON path."}
		properties["baselineStorePath"] = map[string]any{"type": "string", "description": "Immutable baseline snapshot directory."}
		properties["baselineKey"] = map[string]any{"type": "string", "description": "Snapshot key to load from baselineStorePath."}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
	if requireBaseline {
		schema["anyOf"] = []map[string]any{
			{"required": []string{"baselinePath"}},
			{"required": []string{"baselineStorePath", "baselineKey"}},
		}
	}
	return schema
}

func commonAnalysisProperties() map[string]any {
	return map[string]any{
		"repoPath":                          map[string]any{"type": "string", "description": "Local repository path."},
		"language":                          map[string]any{"type": "string", "default": defaultLanguage},
		"scopeMode":                         map[string]any{"type": "string", "enum": []string{analysis.ScopeModeRepo, analysis.ScopeModePackage, analysis.ScopeModeChangedPackages}, "default": defaultScopeMode},
		"configPath":                        map[string]any{"type": "string"},
		"include":                           stringArraySchema(),
		"exclude":                           stringArraySchema(),
		"enableFeatures":                    stringArraySchema(),
		"disableFeatures":                   stringArraySchema(),
		"cacheEnabled":                      map[string]any{"type": "boolean", "default": true},
		"cachePath":                         map[string]any{"type": "string"},
		"cacheReadOnly":                     map[string]any{"type": "boolean", "default": false},
		"runtimeProfile":                    map[string]any{"type": "string", "enum": []string{"node-import", "node-require", "browser-import", "browser-require"}, "default": defaultRuntimeProfile},
		"runtimeTracePath":                  map[string]any{"type": "string"},
		"lowConfidenceWarningPercent":       percentageSchema(),
		"minUsagePercentForRecommendations": percentageSchema(),
		"maxUncertainImportCount":           map[string]any{"type": "integer", "minimum": -1},
		"scoreWeightUsage":                  map[string]any{"type": "number", "minimum": 0},
		"scoreWeightImpact":                 map[string]any{"type": "number", "minimum": 0},
		"scoreWeightConfidence":             map[string]any{"type": "number", "minimum": 0},
		"licenseDeny":                       stringArraySchema(),
		"licenseFailOnDeny":                 map[string]any{"type": "boolean"},
		"licenseProvenanceRegistry":         map[string]any{"type": "boolean"},
		"timeoutMillis":                     map[string]any{"type": "integer", "minimum": 1, "maximum": maxTimeoutMillis},
	}
}

func listLanguagesInputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"repoPath":        map[string]any{"type": "string", "description": "Optional local repository path used to resolve config metadata."},
			"configPath":      map[string]any{"type": "string"},
			"enableFeatures":  stringArraySchema(),
			"disableFeatures": stringArraySchema(),
			"timeoutMillis":   map[string]any{"type": "integer", "minimum": 1, "maximum": maxTimeoutMillis},
		},
		"additionalProperties": false,
	}
}

func stringArraySchema() map[string]any {
	return map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "string"},
	}
}

func percentageSchema() map[string]any {
	return map[string]any{
		"type":    "integer",
		"minimum": 0,
		"maximum": 100,
	}
}
