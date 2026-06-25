package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/version"
)

type failWriteCloser struct{}

func (*failWriteCloser) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

type unsupportedResult struct {
	Bad chan int `json:"bad"`
}

func TestServeErrorBranches(t *testing.T) {
	server := NewServer(Options{})
	if err := Serve(context.Background(), nil, &bytes.Buffer{}, Options{}); err == nil || !strings.Contains(err.Error(), "input") {
		t.Fatalf("expected nil input error, got %v", err)
	}
	if err := server.Serve(context.Background(), strings.NewReader(""), nil); err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatalf("expected nil output error, got %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Serve(ctx, strings.NewReader(""), &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled context, got %v", err)
	}

	var parseOutput bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader("bad header\r\n\r\n{}"), &parseOutput); err != nil {
		t.Fatalf("serve parse response: %v", err)
	}
	parseFrame, err := readFrame(bufio.NewReader(&parseOutput))
	if err != nil {
		t.Fatalf("read parse error frame: %v", err)
	}
	var parseResponse rpcResponse
	if err := json.Unmarshal(parseFrame, &parseResponse); err != nil {
		t.Fatalf("unmarshal parse response: %v", err)
	}
	if parseResponse.Error == nil || parseResponse.Error.Code != codeParseError {
		t.Fatalf("expected parse error response, got %#v", parseResponse)
	}

	var input bytes.Buffer
	writeTestFrame(t, &input, mustJSON(t, rpcRequest{JSONRPC: jsonrpcVersion, ID: json.RawMessage(`1`), Method: methodInitialize}))
	if err := server.Serve(context.Background(), &input, &failWriteCloser{}); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("expected write failure, got %v", err)
	}

	var notificationInput bytes.Buffer
	writeTestFrame(t, &notificationInput, mustJSON(t, rpcRequest{JSONRPC: jsonrpcVersion, Method: methodCancelled}))
	var notificationOutput bytes.Buffer
	if err := server.Serve(context.Background(), &notificationInput, &notificationOutput); err != nil {
		t.Fatalf("serve notification: %v", err)
	}
	if notificationOutput.Len() != 0 {
		t.Fatalf("expected notification to produce no response, got %q", notificationOutput.String())
	}
}

func TestFramingErrorBranches(t *testing.T) {
	if _, err := readFrame(bufio.NewReader(strings.NewReader("Content-Length: 1"))); err == nil {
		t.Fatalf("expected partial header EOF error")
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader("Content-Length: nope\r\n\r\n"))); err == nil || !strings.Contains(err.Error(), "invalid Content-Length") {
		t.Fatalf("expected invalid content length, got %v", err)
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader("Other: 1\r\n\r\n"))); err == nil || !strings.Contains(err.Error(), "missing Content-Length") {
		t.Fatalf("expected missing content length, got %v", err)
	}
	tooLarge := "Content-Length: 16777217\r\n\r\n"
	if _, err := readFrame(bufio.NewReader(strings.NewReader(tooLarge))); err == nil || !strings.Contains(err.Error(), "frame exceeds") {
		t.Fatalf("expected frame size error, got %v", err)
	}
	if _, err := readFrame(bufio.NewReader(strings.NewReader("Content-Length: 5\r\n\r\nabc"))); err == nil {
		t.Fatalf("expected short body error")
	}
	if err := frameReadError(io.ErrUnexpectedEOF, true); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected non-empty partial header error to pass through, got %v", err)
	}
	if err := writeFrame(&failWriteCloser{}, []byte("{}")); err == nil {
		t.Fatalf("expected writeFrame header write error")
	}
}

func TestHandlePayloadBranches(t *testing.T) {
	server := NewServer(Options{Analyzer: &fakeAnalyser{report: sampleReport(t.TempDir())}})

	parse := server.handlePayload(context.Background(), []byte("{"))
	if parse.Error == nil || parse.Error.Code != codeParseError {
		t.Fatalf("expected parse error, got %#v", parse)
	}
	invalid := server.handlePayload(context.Background(), mustJSON(t, map[string]any{"jsonrpc": jsonrpcVersion}))
	if invalid.Error == nil || invalid.Error.Code != codeInvalidRequest {
		t.Fatalf("expected invalid request, got %#v", invalid)
	}
	if response := server.handlePayload(context.Background(), mustJSON(t, rpcRequest{JSONRPC: jsonrpcVersion, Method: methodInitialized})); response != nil {
		t.Fatalf("expected initialized notification to be ignored, got %#v", response)
	}
	if response := server.handlePayload(context.Background(), mustJSON(t, rpcRequest{JSONRPC: jsonrpcVersion, Method: "notifications/unknown"})); response != nil {
		t.Fatalf("expected unknown notification to be ignored, got %#v", response)
	}

	call := server.handlePayload(context.Background(), mustJSON(t, rpcRequest{
		JSONRPC: jsonrpcVersion,
		ID:      json.RawMessage(`2`),
		Method:  methodToolsCall,
		Params:  mustJSON(t, toolCallParams{Name: toolListLanguages}),
	}))
	if call.Error != nil {
		t.Fatalf("expected tools/call success, got %#v", call.Error)
	}

	badCall := server.handlePayload(context.Background(), mustJSON(t, rpcRequest{
		JSONRPC: jsonrpcVersion,
		ID:      json.RawMessage(`3`),
		Method:  methodToolsCall,
		Params:  json.RawMessage(`{"name":""}`),
	}))
	if badCall.Error == nil || badCall.Error.Code != codeInvalidParams {
		t.Fatalf("expected invalid params error, got %#v", badCall)
	}
}

func TestWriteResponseBranches(t *testing.T) {
	server := NewServer(Options{})
	if err := server.writeResponse(&bytes.Buffer{}, newResultResponse(nil, unsupportedResult{Bad: make(chan int)})); err != nil {
		t.Fatalf("expected fallback error response to marshal, got %v", err)
	}
	if err := server.writeResponse(&bytes.Buffer{}, &rpcResponse{JSONRPC: jsonrpcVersion, ID: json.RawMessage(`{`), Result: unsupportedResult{Bad: make(chan int)}}); err == nil {
		t.Fatalf("expected fallback marshal failure")
	}

	id := responseID(nil)
	if string(id) != "null" {
		t.Fatalf("expected null response id, got %s", id)
	}

	custom := NewServer(Options{ServerName: "custom", ServerVersion: "1.2.3"})
	if custom.serverName != "custom" || custom.serverVersion != "1.2.3" {
		t.Fatalf("expected custom server metadata, got %q %q", custom.serverName, custom.serverVersion)
	}

	withCurrentVersion(t, version.Info{})
	fallback := NewServer(Options{})
	if fallback.serverVersion != defaultServerVersion {
		t.Fatalf("expected fallback version, got %q", fallback.serverVersion)
	}
}

func TestCallToolValidationBranches(t *testing.T) {
	server := NewServer(Options{})
	if _, rpcErr := server.callTool(context.Background(), json.RawMessage(`{"name":1}`)); rpcErr == nil || rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected invalid params rpc error, got %#v", rpcErr)
	}
	if _, rpcErr := server.callTool(context.Background(), json.RawMessage(`{"name":""}`)); rpcErr == nil || rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected missing tool name rpc error, got %#v", rpcErr)
	}
	if _, rpcErr := server.callTool(context.Background(), json.RawMessage(`{"name":"missing"}`)); rpcErr == nil || rpcErr.Code != codeInvalidParams {
		t.Fatalf("expected unknown tool rpc error, got %#v", rpcErr)
	}
}

func TestRunAnalysisToolErrorBranches(t *testing.T) {
	repo := t.TempDir()
	server := NewServer(Options{Analyzer: &fakeAnalyser{report: sampleReport(repo)}})
	if result := server.runAnalysisTool(context.Background(), json.RawMessage(`{"repoPath":1}`), analysisToolKindTop); !result.IsError {
		t.Fatalf("expected decode error")
	}
	if result := server.runAnalysisTool(context.Background(), mustJSON(t, analysisToolArguments{RepoPath: repo, TimeoutMillis: -1}), analysisToolKindTop); !result.IsError {
		t.Fatalf("expected timeout validation error")
	}
	errServer := NewServer(Options{Analyzer: &fakeAnalyser{err: errors.New("analyse failed")}})
	if result := errServer.runAnalysisTool(context.Background(), mustJSON(t, analysisToolArguments{RepoPath: repo}), analysisToolKindTop); !result.IsError {
		t.Fatalf("expected analyser error")
	}

	badBaseline := server.runAnalysisTool(context.Background(), mustJSON(t, analysisToolArguments{RepoPath: repo, BaselinePath: filepath.Join(repo, "missing.json")}), analysisToolKindCompare)
	if !badBaseline.IsError {
		t.Fatalf("expected missing baseline error")
	}

	nilAnalyzer := NewServer(Options{})
	if result := nilAnalyzer.runAnalysisTool(context.Background(), mustJSON(t, analysisToolArguments{RepoPath: repo, Language: "unknown"}), analysisToolKindTop); !result.IsError {
		t.Fatalf("expected default analyser error for unknown language")
	}
}

func TestRunAnalysisToolAppliesLocalAdvisories(t *testing.T) {
	repo := t.TempDir()
	advisoryPath := filepath.Join(repo, "advisories.yml")
	if err := os.WriteFile(advisoryPath, []byte("advisories:\n  - id: GHSA-mcp\n    package: lodash\n    ecosystem: npm\n    severity: high\n"), 0o600); err != nil {
		t.Fatalf("write advisory source: %v", err)
	}
	reportData := sampleReport(repo)
	reportData.Dependencies[0].UsedImports = []report.ImportUse{{Name: "default", Module: "lodash", Locations: []report.Location{{File: "app.ts", Line: 1}}}}
	server := NewServer(Options{Analyzer: &fakeAnalyser{report: reportData}})

	args := mustJSON(t, analysisToolArguments{
		RepoPath:                       repo,
		AdvisorySourcePath:             advisoryPath,
		RuntimeTracePath:               "trace.ndjson",
		Include:                        []string{"src/**/*.ts"},
		Exclude:                        []string{"src/**/*.test.ts"},
		ReachableVulnerabilityPriority: stringPtr(report.VulnerabilityPriorityHigh),
		EnableFeatures:                 []string{report.ReachabilityVulnerabilityPrioritizationPreviewFeature},
	})
	result := server.runAnalysisTool(context.Background(), args, analysisToolKindTop)
	if result.IsError {
		t.Fatalf("expected advisory analysis success, got %#v", result)
	}
	payload, ok := result.StructuredContent.(analysisPayload)
	if !ok {
		t.Fatalf("expected analysis payload, got %#v", result.StructuredContent)
	}
	if len(payload.Report.Dependencies) != 1 || len(payload.Report.Dependencies[0].Vulnerabilities) != 1 {
		t.Fatalf("expected annotated vulnerability finding, got %#v", payload.Report.Dependencies)
	}
	if payload.Report.EffectivePolicy == nil || payload.Report.EffectivePolicy.Vulnerabilities.AdvisorySourcePath != advisoryPath {
		t.Fatalf("expected advisory policy metadata, got %#v", payload.Report.EffectivePolicy)
	}
}

func TestRunAnalysisToolRequiresVulnerabilityPreviewFeature(t *testing.T) {
	withCurrentVersion(t, version.Info{BuildChannel: "dev"})

	repo := t.TempDir()
	server := NewServer(Options{Analyzer: &fakeAnalyser{report: sampleReport(repo)}})

	args := mustJSON(t, analysisToolArguments{
		RepoPath:                       repo,
		AdvisorySourcePath:             "advisories.yml",
		ReachableVulnerabilityPriority: stringPtr(report.VulnerabilityPriorityHigh),
	})
	result := server.runAnalysisTool(context.Background(), args, analysisToolKindTop)
	if !result.IsError || !strings.Contains(result.Content[0].Text, report.ReachabilityVulnerabilityPrioritizationPreviewFeature) {
		t.Fatalf("expected vulnerability preview feature error, got %#v", result)
	}
}

func TestResolveAnalysisRequestValidationBranches(t *testing.T) {
	repo := t.TempDir()
	server := NewServer(Options{})
	cases := []struct {
		name string
		args analysisToolArguments
		kind analysisToolKind
		want string
	}{
		{"missing dependency", analysisToolArguments{RepoPath: repo}, analysisToolKindDependency, "dependency is required"},
		{"top rejects dependency", analysisToolArguments{RepoPath: repo, Dependency: "dep"}, analysisToolKindTop, "dependency is not supported"},
		{"compare requires baseline", analysisToolArguments{RepoPath: repo}, analysisToolKindCompare, "baselinePath or baselineStorePath"},
		{"invalid topN", analysisToolArguments{RepoPath: repo, TopN: intPtr(0)}, analysisToolKindTop, "topN"},
		{"invalid scope", analysisToolArguments{RepoPath: repo, ScopeMode: "bad"}, analysisToolKindTop, "scopeMode"},
		{"unknown kind", analysisToolArguments{RepoPath: repo}, analysisToolKind("bad"), "unknown analysis tool kind"},
		{"baseline conflict", analysisToolArguments{RepoPath: repo, BaselinePath: "a.json", BaselineStorePath: "store", BaselineKey: "key"}, analysisToolKindCompare, "baselinePath and baselineStorePath"},
		{"store missing key", analysisToolArguments{RepoPath: repo, BaselineStorePath: "store"}, analysisToolKindCompare, "baselineKey"},
		{"feature error", analysisToolArguments{RepoPath: repo, EnableFeatures: []string{"missing"}}, analysisToolKindTop, "unknown feature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.resolveAnalysisRequest(context.Background(), tc.args, tc.kind)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}

	configPath := filepath.Join(repo, ".lopper.yml")
	if err := os.WriteFile(configPath, []byte("thresholds:\n  low_confidence_warning_percent: 101\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := server.resolveAnalysisRequest(context.Background(), analysisToolArguments{RepoPath: repo}, analysisToolKindTop); err == nil || !strings.Contains(err.Error(), "low_confidence") {
		t.Fatalf("expected config validation error, got %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cleanRepo := t.TempDir()
	_, err := server.resolveAnalysisRequest(cancelled, analysisToolArguments{RepoPath: cleanRepo}, analysisToolKindTop)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestListLanguagesBranches(t *testing.T) {
	repo := t.TempDir()
	configPath := filepath.Join(repo, "lopper.json")
	if err := os.WriteFile(configPath, []byte(`{"thresholds":{"low_confidence_warning_percent":35}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	server := NewServer(Options{})
	if result := server.runListLanguagesTool(context.Background(), json.RawMessage(`{"repoPath":1}`)); !result.IsError {
		t.Fatalf("expected decode error")
	}
	if result := server.runListLanguagesTool(context.Background(), mustJSON(t, listLanguagesArguments{ConfigPath: configPath})); !result.IsError {
		t.Fatalf("expected repoPath required with config")
	}
	if result := server.runListLanguagesTool(context.Background(), mustJSON(t, listLanguagesArguments{RepoPath: repo, ConfigPath: configPath})); result.IsError {
		t.Fatalf("expected config metadata success, got %#v", result)
	}
	if result := server.runListLanguagesTool(context.Background(), mustJSON(t, listLanguagesArguments{RepoPath: repo, ConfigPath: "missing.yml"})); !result.IsError {
		t.Fatalf("expected missing config error")
	}
	if result := server.runListLanguagesTool(context.Background(), mustJSON(t, listLanguagesArguments{TimeoutMillis: -1})); !result.IsError {
		t.Fatalf("expected timeout validation error")
	}
	if result := server.runListLanguagesTool(context.Background(), mustJSON(t, listLanguagesArguments{EnableFeatures: []string{"missing"}})); !result.IsError {
		t.Fatalf("expected feature resolution error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := server.runListLanguagesTool(ctx, mustJSON(t, listLanguagesArguments{})); !result.IsError {
		t.Fatalf("expected context error")
	}
}

func TestLanguageMetadataBranches(t *testing.T) {
	service := analysis.NewService()
	serviceServer := NewServer(Options{Analyzer: service})
	if len(serviceServer.languageMetadata()) == 0 {
		t.Fatalf("expected metadata from analyser service registry")
	}
	defaultServer := NewServer(Options{})
	if len(defaultServer.languageMetadata()) == 0 {
		t.Fatalf("expected metadata from default registry")
	}
}

func TestResolveFeaturesBranches(t *testing.T) {
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0001",
		Name:      "preview-one",
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	server := NewServer(Options{FeatureRegistry: registry})
	if _, err := server.resolveFeatures(emptyFeatureConfig(), []string{"preview-one"}, []string{"preview-one"}); err == nil {
		t.Fatalf("expected conflicting feature override error")
	}
	resolved, err := server.resolveFeatures(emptyFeatureConfig(), []string{"preview-one"}, nil)
	if err != nil {
		t.Fatalf("resolve feature: %v", err)
	}
	if !resolved.Enabled("preview-one") {
		t.Fatalf("expected preview feature enabled")
	}

	withCurrentVersion(t, version.Info{Version: "1.2.3", BuildChannel: "release"})
	if _, err := server.resolveFeatures(emptyFeatureConfig(), nil, nil); err != nil {
		t.Fatalf("expected release-channel feature resolution, got %v", err)
	}
	withCurrentVersion(t, version.Info{BuildChannel: "bad"})
	if _, err := server.resolveFeatures(emptyFeatureConfig(), nil, nil); err == nil {
		t.Fatalf("expected invalid build channel error")
	}
}

func TestDefaultServerFeatureBranches(t *testing.T) {
	t.Run("invalid channel returns empty defaults", func(t *testing.T) {
		withCurrentVersion(t, version.Info{BuildChannel: "bad"})
		features := resolveDefaultServerFeatures(featureflags.DefaultRegistry())
		if features.Snapshot() != nil {
			t.Fatalf("expected empty feature set for invalid channel, got %#v", features.Snapshot())
		}
	})

	t.Run("release channel resolves defaults", func(t *testing.T) {
		withCurrentVersion(t, version.Info{Version: "v1.6.0", BuildChannel: "release"})
		features := resolveDefaultServerFeatures(featureflags.DefaultRegistry())
		if features.Snapshot() == nil {
			t.Fatalf("expected release feature defaults")
		}
	})

	t.Run("nil registry fallback", func(t *testing.T) {
		withCurrentVersion(t, version.Info{BuildChannel: "dev"})
		server := &Server{}
		features, err := server.resolveFeatures(emptyFeatureConfig(), nil, nil)
		if err != nil {
			t.Fatalf("resolve fallback features: %v", err)
		}
		if features.Snapshot() == nil {
			t.Fatalf("expected fallback default registry features")
		}
	})
}

func TestThresholdAndPolicyHelpers(t *testing.T) {
	repo := t.TempDir()
	bad := analysisToolArguments{LowConfidenceWarningPercent: intPtr(101)}
	if _, _, _, _, err := resolveThresholds(repo, bad); err == nil {
		t.Fatalf("expected threshold override validation error")
	}
	if _, _, _, _, err := resolveThresholds(repo, analysisToolArguments{ConfigPath: "missing.yml"}); err == nil {
		t.Fatalf("expected missing config error")
	}

	args := analysisToolArguments{
		LowConfidenceWarningPercent:       intPtr(35),
		MinUsagePercentForRecommendations: intPtr(45),
		MaxUncertainImportCount:           intPtr(3),
		ScoreWeightUsage:                  floatPtr(0.5),
		ScoreWeightImpact:                 floatPtr(0.3),
		ScoreWeightConfidence:             floatPtr(0.2),
		LicenseDeny:                       []string{"MIT"},
		LicenseFailOnDeny:                 boolPtr(true),
		LicenseProvenanceRegistry:         boolPtr(true),
		ReachableVulnerabilityPriority:    stringPtr(report.VulnerabilityPriorityHigh),
		AdvisorySourcePath:                "advisories.yml",
	}
	_, values, sources, trace, err := resolveThresholds(repo, args)
	if err != nil {
		t.Fatalf("resolve thresholds: %v", err)
	}
	if sources[0] != "mcp" || policyTraceSource(trace, "license.fail_on_deny") != "mcp" || policyTraceSource(trace, "advisories.source") != "mcp" {
		t.Fatalf("expected mcp policy metadata, sources=%#v trace=%#v", sources, trace)
	}
	if !values.LicenseFailOnDeny || !values.LicenseIncludeRegistryProvenance || values.ReachableVulnerabilityPriority != report.VulnerabilityPriorityHigh {
		t.Fatalf("expected license policy values, got %#v", values)
	}
	if got := resolveAdvisorySourcePath(thresholds.LoadResult{AdvisorySourcePath: "config.yml"}, analysisToolArguments{}); got != "config.yml" {
		t.Fatalf("expected config advisory source, got %q", got)
	}
	if got := resolveAdvisorySourcePath(thresholds.LoadResult{AdvisorySourcePath: "config.yml"}, analysisToolArguments{AdvisorySourcePath: "cli.yml"}); got != "cli.yml" {
		t.Fatalf("expected argument advisory source, got %q", got)
	}
	if got := prependPolicySource("mcp", []string{"mcp", "defaults"}); !slicesEqual(got, []string{"mcp", "defaults"}) {
		t.Fatalf("unexpected deduped sources: %#v", got)
	}
	if got := mergeMCPPolicyTrace([]report.PolicyMergeTrace{{Field: "existing", Source: "defaults"}}, analysisToolArguments{}); len(got) != 1 || got[0].Source != "defaults" {
		t.Fatalf("unexpected no-op trace merge: %#v", got)
	}
	if got := mergeMCPPolicyTrace(nil, analysisToolArguments{LowConfidenceWarningPercent: intPtr(35)}); len(got) != 1 || got[0].Source != "mcp" {
		t.Fatalf("unexpected appended trace merge: %#v", got)
	}
}

func TestBaselineHelpers(t *testing.T) {
	repo := t.TempDir()
	if _, _, _, err := resolveBaselineComparison(repo, analysisToolArguments{BaselinePath: "a", BaselineStorePath: "b", BaselineKey: "k"}); err == nil {
		t.Fatalf("expected baseline conflict")
	}
	if _, _, _, err := resolveBaselineComparison(repo, analysisToolArguments{BaselineStorePath: "store"}); err == nil {
		t.Fatalf("expected missing baseline key")
	}
	directPath, directKey, directCurrent, err := resolveBaselineComparison(repo, analysisToolArguments{BaselinePath: "baseline.json", BaselineKey: "ignored"})
	if err != nil {
		t.Fatalf("resolve direct baseline: %v", err)
	}
	if directPath != "baseline.json" || directKey != "ignored" || directCurrent == "" {
		t.Fatalf("unexpected direct baseline resolution: path=%q key=%q current=%q", directPath, directKey, directCurrent)
	}
	path, key, current, err := resolveBaselineComparison(repo, analysisToolArguments{BaselineStorePath: "store", BaselineKey: "release/1"})
	if err != nil {
		t.Fatalf("resolve baseline store: %v", err)
	}
	if !strings.Contains(path, "release_1") || key != "release/1" || current == "" {
		t.Fatalf("unexpected baseline resolution: path=%q key=%q current=%q", path, key, current)
	}

	baselinePath := filepath.Join(repo, "baseline.json")
	writeJSONFile(t, baselinePath, report.Report{SchemaVersion: report.SchemaVersion, Dependencies: []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}}})
	currentReport := report.Report{Dependencies: []report.DependencyReport{{Name: "dep", UsedExportsCount: 2, TotalExportsCount: 2, UsedPercent: 100}}}
	updated, err := applyBaseline(currentReport, baselinePath, "", "current")
	if err != nil {
		t.Fatalf("apply baseline: %v", err)
	}
	if updated.BaselineComparison == nil {
		t.Fatalf("expected baseline comparison")
	}
	if _, err := applyBaseline(currentReport, filepath.Join(repo, "missing.json"), "", "current"); err == nil {
		t.Fatalf("expected missing baseline error")
	}
}

func TestSmallHelperBranches(t *testing.T) {
	if _, _, err := contextForTimeout(context.Background(), maxTimeoutMillis+1); err == nil {
		t.Fatalf("expected invalid timeout")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	for _, path := range []string{"", "https://example.com/repo", "\x00", filepath.Join(t.TempDir(), "missing"), file} {
		if _, err := validateRepoPath(path); err == nil {
			t.Fatalf("expected validateRepoPath(%q) error", path)
		}
	}
	for _, mode := range []string{"", analysis.ScopeModePackage, analysis.ScopeModeRepo, analysis.ScopeModeChangedPackages} {
		if _, err := parseScopeMode(mode); err != nil {
			t.Fatalf("parse scope %q: %v", mode, err)
		}
	}
	if _, err := parseScopeMode("bad"); err == nil {
		t.Fatalf("expected bad scope error")
	}
	if !cacheEnabled(nil) || cacheEnabled(boolPtr(false)) {
		t.Fatalf("unexpected cache enabled defaults")
	}
	if got := mergeStringOptions([]string{"config"}, nil); !slicesEqual(got, []string{"config"}) {
		t.Fatalf("expected config patterns, got %#v", got)
	}
	if got := mergeStringOptions([]string{"config"}, []string{"arg"}); !slicesEqual(got, []string{"arg"}) {
		t.Fatalf("expected arg patterns, got %#v", got)
	}
	if got := mergeStringOptions(nil, nil); len(got) != 0 {
		t.Fatalf("expected nil patterns, got %#v", got)
	}
	decorateReport(nil, defaultThresholdValues(), nil, nil, "")
}

func TestDecodeStrictBranches(t *testing.T) {
	var target analysisToolArguments
	if err := decodeStrict(nil, &target); err != nil {
		t.Fatalf("expected empty args decode success, got %v", err)
	}
	if err := decodeStrict(json.RawMessage(` null `), &target); err != nil {
		t.Fatalf("expected null args decode success, got %v", err)
	}
	if err := decodeStrict(json.RawMessage(`{} {}`), &target); err == nil {
		t.Fatalf("expected trailing JSON error")
	}
}

func TestSummarizeReportBranches(t *testing.T) {
	if got := summarizeReport(analysisToolKindTop, report.Report{}); !strings.Contains(got, "no dependencies") {
		t.Fatalf("unexpected empty summary: %q", got)
	}
	noTotals := report.Report{Summary: &report.Summary{DependencyCount: 2, UsedPercent: 25}}
	if got := summarizeReport(analysisToolKindTop, noTotals); !strings.Contains(got, "used exports") {
		t.Fatalf("unexpected no-total summary: %q", got)
	}
	withBaseline := sampleReport(".")
	withBaseline.BaselineComparison = &report.BaselineComparison{SummaryDelta: report.SummaryDelta{WastePercentDelta: 2.5}}
	if got := summarizeReport(analysisToolKindCompare, withBaseline); !strings.Contains(got, "Waste delta") {
		t.Fatalf("unexpected baseline summary: %q", got)
	}
}

func TestReadFrameEOF(t *testing.T) {
	if _, err := readFrame(bufio.NewReader(strings.NewReader(""))); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func emptyFeatureConfig() thresholds.FeatureConfig {
	return thresholds.FeatureConfig{}
}

func defaultThresholdValues() thresholds.Values {
	return thresholds.Defaults()
}

func intPtr(value int) *int {
	return &value
}

func floatPtr(value float64) *float64 {
	return &value
}

func stringPtr(value string) *string {
	return &value
}

func boolPtr(value bool) *bool {
	return &value
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func withCurrentVersion(t *testing.T, info version.Info) {
	t.Helper()
	previous := currentVersion
	currentVersion = func() version.Info {
		return info
	}
	t.Cleanup(func() {
		currentVersion = previous
	})
}
