package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

type fakeAnalyser struct {
	report  report.Report
	err     error
	wait    bool
	called  bool
	lastReq analysis.Request
}

func (f *fakeAnalyser) Analyse(ctx context.Context, req analysis.Request) (report.Report, error) {
	f.called = true
	f.lastReq = req
	if f.wait {
		<-ctx.Done()
		return report.Report{}, ctx.Err()
	}
	return f.report, f.err
}

type testAdapter struct {
	language.AdapterContract
}

func newTestAdapter(id string, aliases ...string) *testAdapter {
	return &testAdapter{AdapterContract: language.NewAdapterContract(id, aliases...)}
}

func (a *testAdapter) Detect(context.Context, string) (bool, error) {
	return true, nil
}

func (a *testAdapter) Analyse(context.Context, language.Request) (report.Result, error) {
	return report.Report{}, nil
}

func TestHandleToolsListRegistersExpectedTools(t *testing.T) {
	server := NewServer(Options{})
	response := server.handlePayload(context.Background(), mustJSON(t, rpcRequest{
		JSONRPC: jsonrpcVersion,
		ID:      json.RawMessage(`1`),
		Method:  methodToolsList,
	}))
	if response == nil || response.Error != nil {
		t.Fatalf("expected tools/list response, got %#v", response)
	}

	result := response.Result.(map[string]any)
	tools := result["tools"].([]toolSpec)
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
		if tool.InputSchema["additionalProperties"] != false {
			t.Fatalf("expected strict input schema for %s", tool.Name)
		}
		if tool.Name == toolAnalyseTop {
			properties := tool.InputSchema["properties"].(map[string]any)
			if _, ok := properties["dependency"]; ok {
				t.Fatalf("top dependency schema should not advertise dependency input")
			}
		}
	}
	want := []string{toolAnalyseTop, toolAnalyseDependency, toolCompareBaseline, toolListLanguages}
	if !slices.Equal(names, want) {
		t.Fatalf("unexpected tools: %#v", names)
	}
}

func TestCallAnalyseDependencyMapsRequestAndReturnsStructuredReport(t *testing.T) {
	repo := t.TempDir()
	cacheEnabled := false
	topN := 10
	lowConfidence := 33
	minUsage := 44
	weightUsage := 0.6
	weightImpact := 0.2
	weightConfidence := 0.2
	fake := &fakeAnalyser{report: sampleReport(repo)}
	server := NewServer(Options{Analyzer: fake})

	result, rpcErr := server.callTool(context.Background(), mustJSON(t, toolCallParams{
		Name: toolAnalyseDependency,
		Arguments: mustJSON(t, analysisToolArguments{
			RepoPath:                          repo,
			Dependency:                        "lodash",
			TopN:                              &topN,
			Language:                          "js-ts",
			ScopeMode:                         analysis.ScopeModeRepo,
			Include:                           []string{"src/**"},
			Exclude:                           []string{"vendor/**"},
			CacheEnabled:                      &cacheEnabled,
			CachePath:                         ".cache/lopper",
			CacheReadOnly:                     true,
			RuntimeProfile:                    "browser-import",
			RuntimeTracePath:                  "trace.ndjson",
			LowConfidenceWarningPercent:       &lowConfidence,
			MinUsagePercentForRecommendations: &minUsage,
			ScoreWeightUsage:                  &weightUsage,
			ScoreWeightImpact:                 &weightImpact,
			ScoreWeightConfidence:             &weightConfidence,
			LicenseDeny:                       []string{"GPL-3.0-only"},
		}),
	}))
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %#v", rpcErr)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %#v", result)
	}
	if !fake.called {
		t.Fatalf("expected analyser to be called")
	}
	if fake.lastReq.RepoPath != repo {
		t.Fatalf("expected normalized repo path %q, got %q", repo, fake.lastReq.RepoPath)
	}
	assertAnalysisRequest(t, fake.lastReq)

	payload, ok := result.StructuredContent.(analysisPayload)
	if !ok {
		t.Fatalf("expected analysis payload, got %#v", result.StructuredContent)
	}
	if payload.Report.EffectiveThresholds == nil {
		t.Fatalf("expected effective thresholds in report")
	}
	if payload.Report.EffectivePolicy == nil {
		t.Fatalf("expected effective policy in report")
	}
	if payload.Report.EffectivePolicy.Sources[0] != "mcp" {
		t.Fatalf("expected mcp policy source, got %#v", payload.Report.EffectivePolicy.Sources)
	}
	if source := policyTraceSource(payload.Report.EffectivePolicy.MergeTrace, "thresholds.low_confidence_warning_percent"); source != "mcp" {
		t.Fatalf("expected mcp policy trace source, got %q", source)
	}
	if payload.Report.EffectivePolicy.License.Deny[0] != "GPL-3.0-ONLY" {
		t.Fatalf("expected license deny list to be preserved, got %#v", payload.Report.EffectivePolicy.License.Deny)
	}
	if !strings.Contains(result.Content[0].Text, "Dependency analysis completed") {
		t.Fatalf("expected concise text summary, got %#v", result.Content)
	}
}

func TestCallAnalyseTopValidatesInputs(t *testing.T) {
	repo := t.TempDir()
	server := NewServer(Options{Analyzer: &fakeAnalyser{report: sampleReport(repo)}})

	missingRepo := callToolResult(t, server, toolAnalyseTop, map[string]any{"topN": 5})
	if !missingRepo.IsError || !strings.Contains(missingRepo.Content[0].Text, "repoPath is required") {
		t.Fatalf("expected missing repoPath validation error, got %#v", missingRepo)
	}

	mutationArg := callToolResult(t, server, toolAnalyseTop, map[string]any{
		"repoPath":           repo,
		"runtimeTestCommand": "npm test",
	})
	if !mutationArg.IsError || !strings.Contains(mutationArg.Content[0].Text, "unknown field") {
		t.Fatalf("expected unsupported argument validation error, got %#v", mutationArg)
	}

	badTopN := callToolResult(t, server, toolAnalyseTop, map[string]any{
		"repoPath": repo,
		"topN":     0,
	})
	if !badTopN.IsError || !strings.Contains(badTopN.Content[0].Text, "topN") {
		t.Fatalf("expected topN validation error, got %#v", badTopN)
	}
}

func TestCallCompareBaselineAppliesBaselineDiff(t *testing.T) {
	repo := t.TempDir()
	baselinePath := filepath.Join(repo, "baseline.json")
	baseline := report.Report{
		SchemaVersion: report.SchemaVersion,
		RepoPath:      repo,
		Dependencies: []report.DependencyReport{
			{Name: "lodash", Language: "js-ts", UsedExportsCount: 8, TotalExportsCount: 10, UsedPercent: 80},
		},
	}
	writeJSONFile(t, baselinePath, baseline)

	fake := &fakeAnalyser{report: sampleReport(repo)}
	server := NewServer(Options{Analyzer: fake})
	result := callToolResult(t, server, toolCompareBaseline, map[string]any{
		"repoPath":      repo,
		"dependency":    "lodash",
		"baselinePath":  baselinePath,
		"cacheEnabled":  false,
		"timeoutMillis": 1000,
	})
	if result.IsError {
		t.Fatalf("unexpected compare error: %#v", result)
	}

	payload := result.StructuredContent.(analysisPayload)
	if payload.Report.BaselineComparison == nil {
		t.Fatalf("expected baseline comparison in report")
	}
	if payload.Report.WasteIncreasePercent == nil {
		t.Fatalf("expected waste increase percent in report")
	}
	if !strings.Contains(payload.Summary, "Waste delta") {
		t.Fatalf("expected baseline summary with delta, got %q", payload.Summary)
	}
}

func TestCallToolTimeoutCancelsAnalysis(t *testing.T) {
	repo := t.TempDir()
	fake := &fakeAnalyser{wait: true}
	server := NewServer(Options{Analyzer: fake})

	result := callToolResult(t, server, toolAnalyseTop, map[string]any{
		"repoPath":      repo,
		"topN":          5,
		"timeoutMillis": 1,
		"cacheEnabled":  false,
	})
	if !fake.called {
		t.Fatalf("expected analyser to be called")
	}
	if !result.IsError {
		t.Fatalf("expected timeout tool error, got %#v", result)
	}
	payload := result.StructuredContent.(map[string]any)
	errPayload := payload["error"].(map[string]any)
	if errPayload["code"] != errorCodeTimeout {
		t.Fatalf("expected timeout code, got %#v", errPayload)
	}
}

func TestListLanguagesReturnsAdapterAndConfigMetadata(t *testing.T) {
	registry := language.NewRegistry()
	if err := registry.Register(newTestAdapter("js-ts", "javascript", "typescript")); err != nil {
		t.Fatalf("register adapter: %v", err)
	}
	server := NewServer(Options{LanguageRegistry: registry})

	result := callToolResult(t, server, toolListLanguages, map[string]any{})
	if result.IsError {
		t.Fatalf("unexpected language metadata error: %#v", result)
	}

	payload := result.StructuredContent.(languagesPayload)
	if len(payload.Languages) != 1 {
		t.Fatalf("expected one language, got %#v", payload.Languages)
	}
	if payload.Languages[0].ID != "js-ts" || !slices.Equal(payload.Languages[0].Aliases, []string{"javascript", "typescript"}) {
		t.Fatalf("unexpected language metadata: %#v", payload.Languages)
	}
	if !slices.Contains(payload.LanguageModes, language.Auto) || !slices.Contains(payload.LanguageModes, language.All) {
		t.Fatalf("expected auto/all language modes, got %#v", payload.LanguageModes)
	}
	if payload.EffectiveThresholds.LowConfidenceWarningPercent == 0 {
		t.Fatalf("expected threshold defaults in metadata")
	}
}

func TestServeProcessesFramedInitialize(t *testing.T) {
	var input bytes.Buffer
	writeTestFrame(t, &input, mustJSON(t, rpcRequest{
		JSONRPC: jsonrpcVersion,
		ID:      json.RawMessage(`"init-1"`),
		Method:  methodInitialize,
		Params:  json.RawMessage(`{"protocolVersion":"2025-06-18"}`),
	}))

	var output bytes.Buffer
	server := NewServer(Options{ServerVersion: "test"})
	if err := server.Serve(context.Background(), &input, &output); err != nil {
		t.Fatalf("serve: %v", err)
	}

	frame, err := readFrame(bufio.NewReader(&output))
	if err != nil {
		t.Fatalf("read output frame: %v", err)
	}
	var response rpcResponse
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if response.Error != nil {
		t.Fatalf("unexpected initialize error: %#v", response.Error)
	}
	data, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result initializeResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("expected protocol echo, got %q", result.ProtocolVersion)
	}
	if result.ServerInfo.Version != "test" {
		t.Fatalf("expected server version, got %q", result.ServerInfo.Version)
	}
}

func TestHandleUnknownMethodReturnsJSONRPCError(t *testing.T) {
	server := NewServer(Options{})
	response := server.handlePayload(context.Background(), mustJSON(t, rpcRequest{
		JSONRPC: jsonrpcVersion,
		ID:      json.RawMessage(`1`),
		Method:  "nope",
	}))
	if response == nil || response.Error == nil {
		t.Fatalf("expected json-rpc error, got %#v", response)
	}
	if response.Error.Code != codeMethodNotFound {
		t.Fatalf("expected method-not-found code, got %#v", response.Error)
	}
}

func TestAnalysisErrorResultClassifiesCancellation(t *testing.T) {
	cancelled := analysisErrorResult(context.Canceled)
	payload := cancelled.StructuredContent.(map[string]any)
	if payload["error"].(map[string]any)["code"] != errorCodeCancelled {
		t.Fatalf("expected cancelled code, got %#v", payload)
	}

	failed := analysisErrorResult(errors.New("boom"))
	payload = failed.StructuredContent.(map[string]any)
	if payload["error"].(map[string]any)["code"] != errorCodeToolFailed {
		t.Fatalf("expected tool failed code, got %#v", payload)
	}
}

func assertAnalysisRequest(t *testing.T, req analysis.Request) {
	t.Helper()
	assertAnalysisRequestBasics(t, req)
	assertAnalysisRequestOptions(t, req)
	assertAnalysisRequestThresholds(t, req)
}

func assertAnalysisRequestBasics(t *testing.T, req analysis.Request) {
	t.Helper()
	if req.Dependency != "lodash" {
		t.Fatalf("expected dependency lodash, got %q", req.Dependency)
	}
	if req.TopN != 0 {
		t.Fatalf("expected dependency analysis topN 0, got %d", req.TopN)
	}
	if req.Language != "js-ts" {
		t.Fatalf("expected js-ts language, got %q", req.Language)
	}
	if req.ScopeMode != analysis.ScopeModeRepo {
		t.Fatalf("expected repo scope, got %q", req.ScopeMode)
	}
	if !slices.Equal(req.IncludePatterns, []string{"src/**"}) || !slices.Equal(req.ExcludePatterns, []string{"vendor/**"}) {
		t.Fatalf("unexpected scope patterns: include=%#v exclude=%#v", req.IncludePatterns, req.ExcludePatterns)
	}
}

func assertAnalysisRequestOptions(t *testing.T, req analysis.Request) {
	t.Helper()
	if req.Cache == nil || req.Cache.Enabled || req.Cache.Path != ".cache/lopper" || !req.Cache.ReadOnly {
		t.Fatalf("unexpected cache options: %#v", req.Cache)
	}
	if req.RuntimeProfile != "browser-import" || req.RuntimeTracePath != "trace.ndjson" || !req.RuntimeTracePathExplicit {
		t.Fatalf("unexpected runtime options: %#v", req)
	}
}

func assertAnalysisRequestThresholds(t *testing.T, req analysis.Request) {
	t.Helper()
	if req.LowConfidenceWarningPercent == nil || *req.LowConfidenceWarningPercent != 33 {
		t.Fatalf("unexpected low-confidence threshold: %#v", req.LowConfidenceWarningPercent)
	}
	if req.MinUsagePercentForRecommendations == nil || *req.MinUsagePercentForRecommendations != 44 {
		t.Fatalf("unexpected min usage threshold: %#v", req.MinUsagePercentForRecommendations)
	}
	if req.RemovalCandidateWeights == nil || req.RemovalCandidateWeights.Usage != 0.6 {
		t.Fatalf("unexpected removal weights: %#v", req.RemovalCandidateWeights)
	}
}

func callToolResult(t *testing.T, server *Server, name string, args map[string]any) toolCallResult {
	t.Helper()
	result, rpcErr := server.callTool(context.Background(), mustJSON(t, map[string]any{
		"name":      name,
		"arguments": args,
	}))
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %#v", rpcErr)
	}
	return result
}

func sampleReport(repoPath string) report.Report {
	return report.Report{
		SchemaVersion: report.SchemaVersion,
		RepoPath:      repoPath,
		Dependencies: []report.DependencyReport{
			{Name: "lodash", Language: "js-ts", UsedExportsCount: 5, TotalExportsCount: 10, UsedPercent: 50},
		},
		Summary: &report.Summary{
			DependencyCount:   1,
			UsedExportsCount:  5,
			TotalExportsCount: 10,
			UsedPercent:       50,
		},
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal file: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func writeTestFrame(t *testing.T, writer *bytes.Buffer, payload []byte) {
	t.Helper()
	if err := writeFrame(writer, payload); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func policyTraceSource(trace []report.PolicyMergeTrace, field string) string {
	for _, item := range trace {
		if item.Field == field {
			return item.Source
		}
	}
	return ""
}
