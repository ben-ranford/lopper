package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/featureflags"
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

type fakeMutationRunner struct {
	captureFn       func(AnalysisMutationRequest) (AnalysisMutationRequest, error)
	captureErr      error
	applyReport     report.Report
	applyErr        error
	baselineReport  report.Report
	baselinePath    string
	baselineErr     error
	dashboardReport dashboard.Report
	dashboardPath   string
	dashboardErr    error
	applyCalled     bool
	baselineCalled  bool
	dashboardCalled bool
	lastApply       AnalysisMutationRequest
	lastBaseline    AnalysisMutationRequest
	lastDashboard   DashboardMutationRequest
}

func (f *fakeMutationRunner) CaptureAnalysisState(_ context.Context, req AnalysisMutationRequest) (AnalysisMutationRequest, error) {
	if f.captureFn != nil {
		return f.captureFn(req)
	}
	return req, f.captureErr
}

func (f *fakeMutationRunner) ApplyCodemod(_ context.Context, req AnalysisMutationRequest) (report.Report, error) {
	f.applyCalled = true
	f.lastApply = req
	return f.applyReport, f.applyErr
}

func (f *fakeMutationRunner) SaveBaseline(_ context.Context, req AnalysisMutationRequest) (report.Report, string, error) {
	f.baselineCalled = true
	f.lastBaseline = req
	return f.baselineReport, f.baselinePath, f.baselineErr
}

func (f *fakeMutationRunner) SaveDashboardBaseline(_ context.Context, req DashboardMutationRequest) (dashboard.Report, string, error) {
	f.dashboardCalled = true
	f.lastDashboard = req
	return f.dashboardReport, f.dashboardPath, f.dashboardErr
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
	server := NewServer(Options{Features: mustMutationFeatureSet(t, false)})
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
	assertToolOrder(t, tools)
	assertStrictToolSchemas(t, tools)
	byName := toolSpecsByName(tools)
	assertTopDependencySchema(t, byName[toolAnalyseTop])
	assertDependencySchema(t, byName[toolAnalyseDependency])
	assertBaselineSchema(t, byName[toolCompareBaseline])
}

func TestHandleToolsListRegistersMutationToolsWhenFeatureEnabled(t *testing.T) {
	server := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: &fakeMutationRunner{}})
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
	byName := toolSpecsByName(tools)
	for _, name := range []string{toolApplyCodemod, toolSaveBaseline, toolSaveDashboardBaseline} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("expected mutation tool %s to be registered, got %#v", name, byName)
		}
	}
	assertStrictToolSchemas(t, tools)
	assertRequiredSchemaFields(t, byName[toolApplyCodemod], "repoPath", "dependency", "confirmApply")
	assertRequiredSchemaFields(t, byName[toolSaveBaseline], "repoPath", "baselineStorePath", "confirmSave")
	assertRequiredSchemaFields(t, byName[toolSaveDashboardBaseline], "repoPath", "baselineStorePath", "confirmSave")
}

func assertToolOrder(t *testing.T, tools []toolSpec) {
	t.Helper()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	want := []string{toolAnalyseTop, toolAnalyseDependency, toolCompareBaseline, toolListLanguages}
	if !slices.Equal(names, want) {
		t.Fatalf("unexpected tools: %#v", names)
	}
}

func assertStrictToolSchemas(t *testing.T, tools []toolSpec) {
	t.Helper()
	for _, tool := range tools {
		if tool.InputSchema["additionalProperties"] != false {
			t.Fatalf("expected strict input schema for %s", tool.Name)
		}
	}
}

func toolSpecsByName(tools []toolSpec) map[string]toolSpec {
	byName := make(map[string]toolSpec, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	return byName
}

func assertTopDependencySchema(t *testing.T, tool toolSpec) {
	t.Helper()
	properties := tool.InputSchema["properties"].(map[string]any)
	if _, ok := properties["dependency"]; ok {
		t.Fatalf("top dependency schema should not advertise dependency input")
	}
}

func assertDependencySchema(t *testing.T, tool toolSpec) {
	t.Helper()
	properties := tool.InputSchema["properties"].(map[string]any)
	if _, ok := properties["topN"]; !ok {
		t.Fatalf("dependency schema should advertise topN as accepted input")
	}
}

func assertBaselineSchema(t *testing.T, tool toolSpec) {
	t.Helper()
	anyOf, ok := tool.InputSchema["anyOf"].([]map[string]any)
	if !ok || len(anyOf) != 2 {
		t.Fatalf("baseline schema should describe baselinePath or baselineStorePath alternatives, got %#v", tool.InputSchema["anyOf"])
	}
}

func assertRequiredSchemaFields(t *testing.T, tool toolSpec, names ...string) {
	t.Helper()
	required, ok := tool.InputSchema["required"].([]string)
	if !ok {
		t.Fatalf("expected required string list for %s, got %#v", tool.Name, tool.InputSchema["required"])
	}
	for _, name := range names {
		if !slices.Contains(required, name) {
			t.Fatalf("expected %s to require %s, got %#v", tool.Name, name, required)
		}
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
		"repoPath":     repo,
		"applyCodemod": true,
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

func TestMutationToolsValidateFeatureAndConfirmation(t *testing.T) {
	repo := t.TempDir()
	disabled := NewServer(Options{Features: mustMutationFeatureSet(t, false), MutationRunner: &fakeMutationRunner{}})
	result := callToolResult(t, disabled, toolApplyCodemod, map[string]any{
		"repoPath":      repo,
		"dependency":    "lodash",
		"confirmApply":  true,
		"cacheEnabled":  false,
		"timeoutMillis": 1000,
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, MutationToolsFeature) {
		t.Fatalf("expected feature flag rejection, got %#v", result)
	}

	enabled := NewServer(Options{Features: mustMutationFeatureSet(t, true), MutationRunner: &fakeMutationRunner{}})
	missingConfirm := callToolResult(t, enabled, toolApplyCodemod, map[string]any{
		"repoPath":     repo,
		"dependency":   "lodash",
		"cacheEnabled": false,
	})
	if !missingConfirm.IsError || !strings.Contains(missingConfirm.Content[0].Text, "confirmApply") {
		t.Fatalf("expected missing confirmation rejection, got %#v", missingConfirm)
	}

	unsafeStore := callToolResult(t, enabled, toolSaveBaseline, map[string]any{
		"repoPath":                    repo,
		"baselineStorePath":           "https://example.com/baselines",
		"baselineLabel":               "nightly",
		"confirmSave":                 true,
		"cacheEnabled":                false,
		"lowConfidenceWarningPercent": 25,
	})
	if !unsafeStore.IsError || !strings.Contains(unsafeStore.Content[0].Text, "local filesystem path") {
		t.Fatalf("expected unsafe path rejection, got %#v", unsafeStore)
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
		"timeoutMillis": 250,
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

func TestCallAnalyseDependencySupportsAbsoluteCachePathOutsideRepo(t *testing.T) {
	repo := writeMCPDependencyFixture(t)
	outsideCache := filepath.Join(t.TempDir(), "cache")
	server := NewServer(Options{Analyzer: analysis.NewService()})
	firstPayload := runScopedMCPDependencyAnalysis(t, server, repo, map[string]any{"cachePath": outsideCache, "cacheEnabled": true})
	assertMCPCacheMetrics(t, firstPayload.Report.Cache, outsideCache, 1, 1, 0)
	assertMCPCacheLayoutPresent(t, outsideCache)
	secondPayload := runScopedMCPDependencyAnalysis(t, server, repo, map[string]any{"cachePath": outsideCache, "cacheEnabled": true})
	assertMCPCacheMetrics(t, secondPayload.Report.Cache, outsideCache, 0, 0, 1)
}

func TestCallAnalyseDependencyRejectsSymlinkedCachePathEscape(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	fake := &fakeAnalyser{report: sampleReport(repo)}
	server := NewServer(Options{Analyzer: fake})
	result := callToolResult(t, server, toolAnalyseDependency, map[string]any{
		"repoPath":     repo,
		"dependency":   "lodash",
		"cachePath":    filepath.Join(repo, "tmp", "cache"),
		"cacheEnabled": true,
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "cachePath must stay within repoPath") {
		t.Fatalf("expected symlink escape rejection, got %#v", result)
	}
	if fake.called {
		t.Fatalf("expected analyser to remain uncalled")
	}
	if _, err := os.Stat(filepath.Join(outside, "cache")); !os.IsNotExist(err) {
		t.Fatalf("expected no outside cache writes, stat err=%v", err)
	}
}

func TestCallAnalyseDependencyRejectsCanonicalSymlinkEscapeUnderRequestedRepoAlias(t *testing.T) {
	requestedRepo, canonicalCache, outsideCache := mcpCanonicalAliasCacheEscapeFixture(t)
	fake := &fakeAnalyser{report: sampleReport(requestedRepo)}
	server := NewServer(Options{Analyzer: fake})

	result := callToolResult(t, server, toolAnalyseDependency, map[string]any{
		"repoPath":     requestedRepo,
		"dependency":   "lodash",
		"cachePath":    canonicalCache,
		"cacheEnabled": true,
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "cachePath must stay within repoPath") {
		t.Fatalf("expected canonical-form symlink escape rejection, got %#v", result)
	}
	if fake.called {
		t.Fatalf("expected analyser to remain uncalled")
	}
	if _, err := os.Stat(outsideCache); !os.IsNotExist(err) {
		t.Fatalf("expected MCP analysis not to create external cache directory, stat err=%v", err)
	}
}

func TestCallAnalyseDependencyRejectsAlternateAbsoluteRepoAliasesThatLaterEscape(t *testing.T) {
	for _, fixture := range mcpAlternateAbsoluteRepoAliasEscapeFixtures(t) {
		t.Run(fixture.name, func(t *testing.T) {
			fake := &fakeAnalyser{report: sampleReport(fixture.requestedRepo)}
			server := NewServer(Options{Analyzer: fake})
			result := callToolResult(t, server, toolAnalyseDependency, map[string]any{
				"repoPath":     fixture.requestedRepo,
				"dependency":   "lodash",
				"cachePath":    fixture.cachePath,
				"cacheEnabled": true,
			})
			if !result.IsError || !strings.Contains(result.Content[0].Text, "cachePath must stay within repoPath") {
				t.Fatalf("expected alternate-alias symlink escape rejection, got %#v", result)
			}
			if fake.called {
				t.Fatalf("expected analyser to remain uncalled")
			}
			if _, statErr := os.Stat(fixture.outsideCache); !os.IsNotExist(statErr) {
				t.Fatalf("expected MCP analysis not to create outside cache, stat err=%v", statErr)
			}
		})
	}
}

func TestCallAnalyseDependencyClassifiesExternalCacheAliasesIntoRepoBeforeAnalyzerInvocation(t *testing.T) {
	repo := t.TempDir()
	cacheSubdir := filepath.Join(repo, ".cache", "lopper")
	if err := os.MkdirAll(cacheSubdir, 0o750); err != nil {
		t.Fatalf("mkdir cache subdir: %v", err)
	}
	for _, tc := range mcpRepoCacheAliasFixtures(repo, cacheSubdir) {
		t.Run(tc.name, func(t *testing.T) {
			cacheAlias := mustMCPCacheAlias(t, tc.target)
			fake := &fakeAnalyser{report: sampleReport(repo)}
			server := NewServer(Options{Analyzer: fake})
			result := callToolResult(t, server, toolAnalyseDependency, map[string]any{
				"repoPath":     repo,
				"dependency":   "lodash",
				"cachePath":    cacheAlias,
				"cacheEnabled": true,
				"include":      []string{"**"},
			})
			assertMCPRepoCacheAliasOutcome(t, result, fake.called, analysis.InRepoCacheOptions(fake.lastReq.Cache), tc.wantReject)
			assertMCPCacheLayoutAbsent(t, tc.target)
		})
	}
}

func TestCallAnalyseDependencyScopedRequestRejectsRepoRootCachePath(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o750); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "node_modules", "lodash"), 0o750); err != nil {
		t.Fatalf("mkdir lodash fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte("{\n  \"name\": \"demo\"\n}\n"), 0o600); err != nil {
		t.Fatalf("write package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "index.js"), []byte("import { map } from \"lodash\"\nmap([1], (x) => x)\n"), 0o600); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "lodash", "package.json"), []byte("{\n  \"main\": \"index.js\"\n}\n"), 0o600); err != nil {
		t.Fatalf("write lodash package.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "lodash", "index.js"), []byte("export function map() {}\n"), 0o600); err != nil {
		t.Fatalf("write lodash index.js: %v", err)
	}
	server := NewServer(Options{Analyzer: analysis.NewService()})

	result := callToolResult(t, server, toolAnalyseDependency, map[string]any{
		"repoPath":     repo,
		"dependency":   "lodash",
		"cachePath":    repo,
		"cacheEnabled": true,
		"include":      []string{"src/**"},
	})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "scoped analysis does not allow cachePath at the repository root") {
		t.Fatalf("expected scoped repo-root cache rejection, got %#v", result)
	}
	if _, err := os.Stat(filepath.Join(repo, "keys")); !os.IsNotExist(err) {
		t.Fatalf("expected no repo-root cache writes, stat err=%v", err)
	}
}

func TestCallAnalyseDependencyProvidesDefaultCacheOptionsForScopedRequest(t *testing.T) {
	repo := t.TempDir()
	fake := &fakeAnalyser{report: sampleReport(repo)}
	server := NewServer(Options{Analyzer: fake})

	result := callToolResult(t, server, toolAnalyseDependency, map[string]any{
		"repoPath":   repo,
		"dependency": "lodash",
		"include":    []string{"src/**"},
		"exclude":    []string{"vendor/**"},
	})
	if result.IsError {
		t.Fatalf("unexpected tool error: %#v", result)
	}
	if !fake.called {
		t.Fatalf("expected analyser to be called")
	}
	if fake.lastReq.Cache == nil || !fake.lastReq.Cache.Enabled {
		t.Fatalf("expected enabled default cache options, got %#v", fake.lastReq.Cache)
	}
	if fake.lastReq.Cache.Path != "" {
		t.Fatalf("expected default MCP cache path to remain implicit, got %#v", fake.lastReq.Cache)
	}
}

func TestCallAnalyseDependencyScopedRequestReusesPinnedDefaultCacheAndReportsCanonicalPath(t *testing.T) {
	repo := writeMCPDependencyFixture(t)
	server := NewServer(Options{Analyzer: analysis.NewService()})
	expectedPinnedPath := mustResolveDefaultPinnedCachePath(t, repo)
	firstPayload := runScopedMCPDependencyAnalysis(t, server, repo, nil)
	assertMCPCacheMetrics(t, firstPayload.Report.Cache, expectedPinnedPath, 1, 1, 0)
	secondPayload := runScopedMCPDependencyAnalysis(t, server, repo, nil)
	assertMCPCacheMetrics(t, secondPayload.Report.Cache, expectedPinnedPath, 0, 0, 1)
}

type mcpRepoCacheAliasFixture struct {
	name       string
	target     string
	wantReject bool
}

func writeMCPDependencyFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, dir := range []string{filepath.Join(repo, "src"), filepath.Join(repo, "node_modules", "lodash")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir fixture dir %s: %v", dir, err)
		}
	}
	files := map[string]string{
		"package.json":                   "{\n  \"name\": \"demo\"\n}\n",
		filepath.Join("src", "index.js"): "import { map } from \"lodash\"\nmap([1], (x) => x)\n",
		filepath.Join("node_modules", "lodash", "package.json"): "{\n  \"main\": \"index.js\"\n}\n",
		filepath.Join("node_modules", "lodash", "index.js"):     "export function map() {}\n",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(repo, rel), []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", rel, err)
		}
	}
	return repo
}

func runScopedMCPDependencyAnalysis(t *testing.T, server *Server, repo string, extra map[string]any) analysisPayload {
	t.Helper()
	args := map[string]any{"repoPath": repo, "dependency": "lodash", "include": []string{"src/**"}, "exclude": []string{"vendor/**"}}
	for key, value := range extra {
		args[key] = value
	}
	result := callToolResult(t, server, toolAnalyseDependency, args)
	if result.IsError {
		t.Fatalf("unexpected tool error: %#v", result)
	}
	payload, ok := result.StructuredContent.(analysisPayload)
	if !ok {
		t.Fatalf("expected analysis payload, got %#v", result.StructuredContent)
	}
	return payload
}

func assertMCPCacheMetrics(t *testing.T, cache *report.CacheMetadata, path string, misses, writes, hits int) {
	t.Helper()
	if cache == nil || cache.Path != path || cache.Misses != misses || cache.Writes != writes || cache.Hits != hits {
		t.Fatalf("unexpected cache metrics: %#v", cache)
	}
}

func assertMCPCacheLayoutPresent(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{"keys", "objects"} {
		if _, err := os.Stat(filepath.Join(root, dir)); err != nil {
			t.Fatalf("expected %s dir in external cache root: %v", dir, err)
		}
	}
}

func mcpRepoCacheAliasFixtures(repo, cacheSubdir string) []mcpRepoCacheAliasFixture {
	return []mcpRepoCacheAliasFixture{{name: "repo root", target: repo, wantReject: true}, {name: "repo subdir", target: cacheSubdir}}
}

func mustMCPCacheAlias(t *testing.T, target string) string {
	t.Helper()
	cacheAlias := filepath.Join(t.TempDir(), "external-cache-alias")
	if err := os.Symlink(target, cacheAlias); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	return cacheAlias
}

func assertMCPRepoCacheAliasOutcome(t *testing.T, result toolCallResult, called, inRepo bool, wantReject bool) {
	t.Helper()
	if wantReject {
		if !result.IsError || !strings.Contains(result.Content[0].Text, "scoped analysis does not allow cachePath at the repository root") || called {
			t.Fatalf("expected external repo-root alias rejection, got %#v called=%t", result, called)
		}
		return
	}
	if result.IsError || !called || !inRepo {
		t.Fatalf("expected in-repo cache pin for alias, result=%#v called=%t inRepo=%t", result, called, inRepo)
	}
}

func assertMCPCacheLayoutAbsent(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{"keys", "objects"} {
		if _, statErr := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(statErr) {
			t.Fatalf("expected no cache creation under %s/%s, stat err=%v", root, dir, statErr)
		}
	}
}

func TestConfiguredMCPAnalysisUsesStableCacheIdentityAcrossRepositorySnapshots(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open-directory replacement semantics are not available on Windows")
	}
	repo, repoB, movedRepoA := setupConfiguredMCPAnalysisRepos(t)
	viewOpens := 0
	restoreHook := analysis.SetRepositoryViewHandleOpenedHookForTest(func() error {
		viewOpens++
		if err := os.Rename(repo, movedRepoA); err != nil {
			return err
		}
		return os.Rename(repoB, repo)
	})
	t.Cleanup(restoreHook)

	server := NewServer(Options{Analyzer: analysis.NewService()})
	args := analysisToolArguments{RepoPath: repo, Dependency: "lodash", ConfigPath: filepath.Join(repo, ".lopper.yml")}
	firstPayload := runConfiguredMCPAnalysis(t, server, args)
	assertConfiguredMCPAnalysisCache(t, firstPayload.Report.Cache, 1, 1, 0)
	assertStableMCPPolicyMetadata(t, firstPayload.Report, repo)
	assertMCPAnalysisCacheAbsent(t, repo, "replacement repo B received cache data")

	restoreConfiguredMCPRepoPair(t, repo, repoB, movedRepoA)
	if err := os.WriteFile(filepath.Join(repoB, ".lopper.yml"), []byte("thresholds:\n  low_confidence_warning_percent: 88\n"), 0o600); err != nil {
		t.Fatalf("change repo B config trap: %v", err)
	}

	secondPayload := runConfiguredMCPAnalysis(t, server, args)
	assertConfiguredMCPAnalysisCache(t, secondPayload.Report.Cache, 0, 0, 1)
	assertStableMCPPolicyMetadata(t, secondPayload.Report, repo)
	if viewOpens != 2 {
		t.Fatalf("repository view opens = %d, want one per MCP call", viewOpens)
	}
	assertMCPAnalysisCacheAbsent(t, repo, "replacement repo B received cache data after second call")
}

func setupConfiguredMCPAnalysisRepos(t *testing.T) (string, string, string) {
	t.Helper()
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	repoB := filepath.Join(parent, "repo-b")
	writeMCPAnalysisCacheFixture(t, repo, 11)
	writeMCPAnalysisCacheFixture(t, repoB, 77)
	return repo, repoB, filepath.Join(parent, "repo-a-original")
}

func runConfiguredMCPAnalysis(t *testing.T, server *Server, args analysisToolArguments) analysisPayload {
	t.Helper()
	result := server.runAnalysisTool(context.Background(), mustJSON(t, args), analysisToolKindDependency)
	if result.IsError {
		t.Fatalf("unexpected configured analysis error: %#v", result)
	}
	return result.StructuredContent.(analysisPayload)
}

func assertConfiguredMCPAnalysisCache(t *testing.T, cache *report.CacheMetadata, wantMisses, wantWrites, wantHits int) {
	t.Helper()
	if cache == nil || cache.Misses != wantMisses || cache.Writes != wantWrites || cache.Hits != wantHits {
		t.Fatalf("unexpected configured cache metadata: %#v", cache)
	}
}

func restoreConfiguredMCPRepoPair(t *testing.T, repo, repoB, movedRepoA string) {
	t.Helper()
	if err := os.Rename(repo, repoB); err != nil {
		t.Fatalf("restore repo B location: %v", err)
	}
	if err := os.Rename(movedRepoA, repo); err != nil {
		t.Fatalf("restore repo A location: %v", err)
	}
}

func assertMCPAnalysisCacheAbsent(t *testing.T, repo, failure string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repo, ".lopper-cache")); !os.IsNotExist(err) {
		t.Fatalf("%s: %v", failure, err)
	}
}

func writeMCPAnalysisCacheFixture(t *testing.T, repo string, lowConfidence int) {
	t.Helper()
	for _, dir := range []string{filepath.Join(repo, "src"), filepath.Join(repo, "node_modules", "lodash")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir fixture directory %s: %v", dir, err)
		}
	}
	files := map[string]string{
		".lopper.yml":                    fmt.Sprintf("thresholds:\n  low_confidence_warning_percent: %d\n", lowConfidence),
		"package.json":                   "{\n  \"name\": \"demo\"\n}\n",
		filepath.Join("src", "index.js"): "import { map } from \"lodash\"\nmap([1], (x) => x)\n",
		filepath.Join("node_modules", "lodash", "package.json"): "{\n  \"main\": \"index.js\"\n}\n",
		filepath.Join("node_modules", "lodash", "index.js"):     "export function map() {}\n",
	}
	for relativePath, content := range files {
		if err := os.WriteFile(filepath.Join(repo, relativePath), []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", relativePath, err)
		}
	}
}

func assertStableMCPPolicyMetadata(t *testing.T, reportData report.Report, repo string) {
	t.Helper()
	if reportData.EffectivePolicy == nil {
		t.Fatal("expected effective policy metadata")
	}
	serialized, err := json.Marshal(reportData.EffectivePolicy)
	if err != nil {
		t.Fatalf("marshal effective policy: %v", err)
	}
	if strings.Contains(string(serialized), "lopper-repository-snapshot-") {
		t.Fatalf("snapshot path leaked into policy metadata: %s", serialized)
	}
	if !strings.Contains(string(serialized), filepath.Join(repo, ".lopper.yml")) {
		t.Fatalf("authorized config identity missing from policy metadata: %s", serialized)
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

func mustResolveDefaultPinnedCachePath(t *testing.T, repo string) string {
	t.Helper()
	cachePath := filepath.Join(repo, ".lopper-cache")
	if err := os.MkdirAll(cachePath, 0o755); err != nil {
		t.Fatalf("mkdir default pinned cache path: %v", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(cachePath)
	if err != nil {
		t.Fatalf("resolve default pinned cache path: %v", err)
	}
	return resolvedPath
}

func mcpCanonicalAliasCacheEscapeFixture(t *testing.T) (requestedRepo, canonicalCache, outsideCache string) {
	t.Helper()

	canonicalParent := t.TempDir()
	canonicalRepo := filepath.Join(canonicalParent, "repo")
	if err := os.MkdirAll(canonicalRepo, 0o755); err != nil {
		t.Fatalf("mkdir canonical repo: %v", err)
	}
	canonicalRepo, err := filepath.EvalSymlinks(canonicalRepo)
	if err != nil {
		t.Fatalf("resolve canonical repo: %v", err)
	}
	requestedParent := filepath.Join(t.TempDir(), "requested-parent")
	if err := os.Symlink(canonicalParent, requestedParent); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(canonicalRepo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	return filepath.Join(requestedParent, "repo"),
		filepath.Join(canonicalRepo, "tmp", "cache"),
		filepath.Join(outside, "cache")
}

type mcpAlternateRepoAliasEscapeFixture struct {
	name          string
	requestedRepo string
	cachePath     string
	outsideCache  string
}

func mcpAlternateAbsoluteRepoAliasEscapeFixtures(t *testing.T) []mcpAlternateRepoAliasEscapeFixture {
	t.Helper()
	repoParent := t.TempDir()
	repo := filepath.Join(repoParent, "repo")
	if err := os.Mkdir(repo, 0o750); err != nil {
		t.Fatalf("mkdir arbitrary-alias repo: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	aliasParent := filepath.Join(t.TempDir(), "alternate-parent")
	if err := os.Symlink(repoParent, aliasParent); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	fixtures := []mcpAlternateRepoAliasEscapeFixture{{
		name:          "arbitrary alias",
		requestedRepo: repo,
		cachePath:     filepath.Join(aliasParent, "repo", "tmp", "cache"),
		outsideCache:  filepath.Join(outside, "cache"),
	}}

	systemRequestedRepo := t.TempDir()
	systemCanonicalRepo, err := filepath.EvalSymlinks(systemRequestedRepo)
	if err != nil {
		t.Fatalf("resolve system-alias repo: %v", err)
	}
	if filepath.Clean(systemRequestedRepo) == filepath.Clean(systemCanonicalRepo) {
		return fixtures
	}
	systemOutside := t.TempDir()
	if err := os.Symlink(systemOutside, filepath.Join(systemRequestedRepo, "tmp")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	return append(fixtures, mcpAlternateRepoAliasEscapeFixture{
		name:          "system absolute alias",
		requestedRepo: systemCanonicalRepo,
		cachePath:     filepath.Join(systemRequestedRepo, "tmp", "cache"),
		outsideCache:  filepath.Join(systemOutside, "cache"),
	})
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

func mustMutationFeatureSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:        "LOP-FEAT-0001",
		Name:        MutationToolsFeature,
		Description: "test mutation tools",
		Lifecycle:   featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	opts := featureflags.ResolveOptions{Channel: featureflags.ChannelDev}
	if enabled {
		opts.Enable = []string{MutationToolsFeature}
	}
	features, err := registry.Resolve(opts)
	if err != nil {
		t.Fatalf("resolve mutation feature: %v", err)
	}
	return features
}
