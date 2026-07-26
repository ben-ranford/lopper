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
	"github.com/ben-ranford/lopper/internal/dashboard"
	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
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
	assertMutationCacheSchema(t, byName[toolApplyCodemod])
	assertMutationCacheSchema(t, byName[toolSaveBaseline])
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
	if cacheReadOnly, ok := properties["cacheReadOnly"].(map[string]any); !ok || cacheReadOnly["default"] != true {
		t.Fatalf("dependency schema should advertise read-only cache default, got %#v", properties["cacheReadOnly"])
	}
}

func assertMutationCacheSchema(t *testing.T, tool toolSpec) {
	t.Helper()
	properties := tool.InputSchema["properties"].(map[string]any)
	cacheReadOnly, ok := properties["cacheReadOnly"].(map[string]any)
	if !ok {
		t.Fatalf("%s should advertise cacheReadOnly input", tool.Name)
	}
	if _, ok := cacheReadOnly["default"]; ok {
		t.Fatalf("%s cacheReadOnly schema should not advertise a read-only default, got %#v", tool.Name, cacheReadOnly)
	}
	if description, ok := cacheReadOnly["description"].(string); ok && strings.Contains(description, "false is ignored") {
		t.Fatalf("%s cacheReadOnly schema should not advertise ignored writes, got %#v", tool.Name, cacheReadOnly)
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

func TestCallAnalyseTopForcesReadOnlyCacheAndLeavesOutsidePathAbsent(t *testing.T) {
	repo := t.TempDir()
	writeMCPJSFixture(t, repo)
	outsideRoot := t.TempDir()
	outsideCache := filepath.Join(outsideRoot, "outside-cache")
	server := NewServer(Options{Analyzer: analysis.NewService()})

	result := callToolResult(t, server, toolAnalyseTop, map[string]any{
		"repoPath":      repo,
		"topN":          1,
		"language":      "js-ts",
		"cachePath":     outsideCache,
		"cacheReadOnly": false,
	})
	if result.IsError {
		t.Fatalf("unexpected analyse error: %#v", result)
	}
	payload := result.StructuredContent.(analysisPayload)
	if payload.Report.Cache == nil || payload.Report.Cache.Enabled || !payload.Report.Cache.ReadOnly {
		t.Fatalf("expected disabled readonly cache metadata, got %#v", payload.Report.Cache)
	}
	if _, err := os.Stat(outsideCache); !os.IsNotExist(err) {
		t.Fatalf("expected outside cache path to remain absent, got err=%v", err)
	}
}

func TestCallAnalyseTopRejectsInitializedTraversalShapedOutsideCachePath(t *testing.T) {
	repoRoot := t.TempDir()
	repo := filepath.Join(repoRoot, "repo")
	writeMCPJSFixture(t, repo)
	outsideCache := filepath.Join(repoRoot, "outside", "cache")
	if err := os.MkdirAll(filepath.Join(outsideCache, "keys"), 0o755); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(outsideCache, "objects"), 0o755); err != nil {
		t.Fatalf("mkdir objects: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(outsideCache, "sentinel.txt"), "keep\n")
	traversalPath := filepath.Join(repo, "..", "outside", "cache")
	server := NewServer(Options{Analyzer: analysis.NewService()})

	result := callToolResult(t, server, toolAnalyseTop, map[string]any{
		"repoPath":      repo,
		"topN":          1,
		"language":      "js-ts",
		"cachePath":     traversalPath,
		"cacheReadOnly": false,
	})
	if result.IsError {
		t.Fatalf("unexpected analyse error: %#v", result)
	}
	payload := result.StructuredContent.(analysisPayload)
	if payload.Report.Cache == nil || payload.Report.Cache.Enabled || !payload.Report.Cache.ReadOnly {
		t.Fatalf("expected disabled readonly cache metadata, got %#v", payload.Report.Cache)
	}
	for _, dirName := range []string{"keys", "objects"} {
		entries, err := os.ReadDir(filepath.Join(outsideCache, dirName))
		if err != nil {
			t.Fatalf("read %s: %v", dirName, err)
		}
		if len(entries) != 0 {
			t.Fatalf("expected traversal-shaped outside cache %s dir to remain untouched, got %#v", dirName, entries)
		}
	}
	data, err := os.ReadFile(filepath.Join(outsideCache, "sentinel.txt"))
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("expected sentinel file to remain unchanged, got %q", string(data))
	}
}

func TestCallAnalyseTopLeavesSymlinkedOutsideCachePathUnmodified(t *testing.T) {
	repo := t.TempDir()
	writeMCPJSFixture(t, repo)
	outsideRoot := t.TempDir()
	outsideCache := filepath.Join(outsideRoot, "cache")
	testutil.MustWriteFile(t, filepath.Join(outsideCache, "sentinel.txt"), "keep\n")
	linkPath := filepath.Join(repo, "linked-cache")
	if err := os.Symlink(outsideCache, linkPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	server := NewServer(Options{Analyzer: analysis.NewService()})

	result := callToolResult(t, server, toolAnalyseTop, map[string]any{
		"repoPath":      repo,
		"topN":          1,
		"language":      "js-ts",
		"cachePath":     linkPath,
		"cacheReadOnly": false,
	})
	if result.IsError {
		t.Fatalf("unexpected analyse error: %#v", result)
	}
	payload := result.StructuredContent.(analysisPayload)
	if payload.Report.Cache == nil || payload.Report.Cache.Enabled || !payload.Report.Cache.ReadOnly {
		t.Fatalf("expected disabled readonly cache metadata, got %#v", payload.Report.Cache)
	}
	if _, err := os.Stat(filepath.Join(outsideCache, "keys")); !os.IsNotExist(err) {
		t.Fatalf("expected symlinked outside cache keys dir to remain absent, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(outsideCache, "objects")); !os.IsNotExist(err) {
		t.Fatalf("expected symlinked outside cache objects dir to remain absent, got err=%v", err)
	}
	data, err := os.ReadFile(filepath.Join(outsideCache, "sentinel.txt"))
	if err != nil {
		t.Fatalf("read sentinel: %v", err)
	}
	if string(data) != "keep\n" {
		t.Fatalf("expected sentinel file to remain unchanged, got %q", string(data))
	}
}

func TestCachePathReadyForReadOnlyRequiresInitializedDirectories(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	cachePath := filepath.Join(root, "cache")
	if cachePathReadyForReadOnly(cachePath) {
		t.Fatalf("expected missing cache path to be unreadable")
	}
	if err := os.MkdirAll(filepath.Join(cachePath, "keys"), 0o755); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(cachePath, "objects"), "not-a-dir\n")
	if cachePathReadyForReadOnly(cachePath) {
		t.Fatalf("expected non-directory objects path to be unreadable")
	}
	if err := os.Remove(filepath.Join(cachePath, "objects")); err != nil {
		t.Fatalf("remove objects file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cachePath, "objects"), 0o755); err != nil {
		t.Fatalf("mkdir objects: %v", err)
	}
	if !cachePathReadyForReadOnly(cachePath) {
		t.Fatalf("expected initialized cache path to be readable")
	}
}

func TestPathContainsSymlinkDetectsAncestorLinks(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp root: %v", err)
	}
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	linkRoot := filepath.Join(root, "link-root")
	if err := os.Symlink(target, linkRoot); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if !pathContainsSymlink(filepath.Join(linkRoot, "cache")) {
		t.Fatalf("expected symlinked ancestor to be detected")
	}
	if pathContainsSymlink(filepath.Join(root, "plain", "cache")) {
		t.Fatalf("expected plain path without symlinks to be allowed")
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

func writeMCPJSFixture(t *testing.T, repo string) {
	t.Helper()
	testutil.MustWriteFile(t, filepath.Join(repo, "package.json"), "{\n  \"name\": \"demo\"\n}\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "index.js"), "import { map } from \"lodash\"\nmap([1], (x) => x)\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "node_modules", "lodash", "package.json"), "{\n  \"main\": \"index.js\"\n}\n")
	testutil.MustWriteFile(t, filepath.Join(repo, "node_modules", "lodash", "index.js"), "export function map() {}\n")
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
