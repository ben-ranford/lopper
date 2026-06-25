package ui

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

type stubSummaryActionRunner struct {
	applyCalls  int
	applyReq    CodemodApplyRequest
	applyReport report.Report
	applyErr    error

	saveCalls  int
	saveReq    BaselineSaveRequest
	saveReport report.Report
	savePath   string
	saveErr    error
}

func (s *stubSummaryActionRunner) ApplyCodemod(_ context.Context, req CodemodApplyRequest) (report.Report, error) {
	s.applyCalls++
	s.applyReq = req
	return s.applyReport, s.applyErr
}

func (s *stubSummaryActionRunner) SaveBaseline(_ context.Context, req BaselineSaveRequest) (report.Report, string, error) {
	s.saveCalls++
	s.saveReq = req
	return s.saveReport, s.savePath, s.saveErr
}

func TestSummaryCodemodApplyCommandRequiresConfirmationAndMergesResults(t *testing.T) {
	applyReport := &report.CodemodApplyReport{
		AppliedFiles:   1,
		AppliedPatches: 2,
		SkippedFiles:   1,
		SkippedPatches: 1,
		BackupPath:     ".artifacts/lopper-codemod-backups/lodash.json",
		Results: []report.CodemodApplyResult{
			{File: "src/index.js", Status: "applied", PatchCount: 2},
			{File: "src/skip.js", Status: "skipped", PatchCount: 1, Message: "reason codes: namespace-import"},
		},
	}
	actions := &stubSummaryActionRunner{
		applyReport: report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "lodash", Language: "js-ts", Codemod: &report.CodemodReport{Apply: applyReport}},
			},
		},
	}

	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = actions
	reportView := mapSummaryReportView(report.Report{
		Dependencies: []report.DependencyReport{
			{
				Name:     "lodash",
				Language: "js-ts",
				Codemod: &report.CodemodReport{
					Mode: "suggest-only",
					Suggestions: []report.CodemodSuggestion{
						{File: "src/index.js", Line: 1, FromModule: "lodash", ToModule: "lodash/map"},
					},
				},
			},
		},
	})
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste, selectedDependency: "js-ts:lodash"}

	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "apply-codemod"); err != nil || quit {
		t.Fatalf("expected unconfirmed apply to continue, quit=%v err=%v", quit, err)
	}
	if actions.applyCalls != 0 {
		t.Fatalf("expected unconfirmed apply to avoid mutation, got %d calls", actions.applyCalls)
	}
	if !strings.Contains(out.String(), "requires --confirm") {
		t.Fatalf("expected confirmation message, got %q", out.String())
	}

	out.Reset()
	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "apply-codemod --confirm --allow-dirty"); err != nil || quit {
		t.Fatalf("expected confirmed apply to continue, quit=%v err=%v", quit, err)
	}
	if actions.applyCalls != 1 {
		t.Fatalf("expected one apply call, got %d", actions.applyCalls)
	}
	if actions.applyReq.Dependency != "lodash" || actions.applyReq.Language != "js-ts" || !actions.applyReq.AllowDirty {
		t.Fatalf("unexpected apply request: %#v", actions.applyReq)
	}
	if reportView.Dependencies[0].CodemodApply != applyReport {
		t.Fatalf("expected apply report to merge into current view, got %#v", reportView.Dependencies[0].CodemodApply)
	}
	output := out.String()
	if !strings.Contains(output, "Codemod apply results for js-ts:lodash") ||
		!strings.Contains(output, "backup: .artifacts/lopper-codemod-backups/lodash.json") ||
		!strings.Contains(output, "applied src/index.js") ||
		!strings.Contains(output, "skipped src/skip.js") {
		t.Fatalf("expected codemod apply details in output, got %q", output)
	}
}

func TestSummarySaveBaselineCommandSupportsLabelAndDefaultCommitKey(t *testing.T) {
	actions := &stubSummaryActionRunner{
		saveReport: report.Report{
			Dependencies: []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
		},
		savePath: ".artifacts/lopper-baselines/label_nightly.json",
	}

	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = actions
	reportView := summaryReportView{}
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}

	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "save-baseline nightly"); err != nil || quit {
		t.Fatalf("expected save label to continue, quit=%v err=%v", quit, err)
	}
	if actions.saveReq.BaselineStorePath != defaultTUIBaselineStorePath || actions.saveReq.BaselineLabel != "nightly" {
		t.Fatalf("unexpected label save request: %#v", actions.saveReq)
	}
	if opts.BaselineStorePath != defaultTUIBaselineStorePath {
		t.Fatalf("expected options to remember baseline store, got %q", opts.BaselineStorePath)
	}
	if !strings.Contains(out.String(), "Saved baseline label:nightly") {
		t.Fatalf("expected saved baseline message, got %q", out.String())
	}

	out.Reset()
	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "save-baseline --store custom-baselines"); err != nil || quit {
		t.Fatalf("expected default-key save to continue, quit=%v err=%v", quit, err)
	}
	if !strings.HasPrefix(actions.saveReq.BaselineKey, "commit:") || actions.saveReq.BaselineLabel != "" {
		t.Fatalf("expected default commit key save request, got %#v", actions.saveReq)
	}
	if actions.saveReq.BaselineStorePath != "custom-baselines" {
		t.Fatalf("expected explicit store to win, got %q", actions.saveReq.BaselineStorePath)
	}
}

func TestSummaryCompareBaselineCommandRefreshesReportAndDetailDeltas(t *testing.T) {
	tmp := t.TempDir()
	baselineReport := report.Report{
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   mustParseTime(t, "2024-01-01T00:00:00Z"),
		RepoPath:      "/repo",
		Dependencies: []report.DependencyReport{
			{
				Name:              "alpha",
				UsedExportsCount:  1,
				TotalExportsCount: 10,
				UsedPercent:       10,
				RuntimeUsage:      &report.RuntimeUsage{LoadCount: 1, Correlation: report.RuntimeCorrelationStaticOnly},
			},
		},
	}
	baselinePath, err := report.SaveSnapshot(tmp, "label:base", baselineReport, mustParseTime(t, "2024-01-02T00:00:00Z"))
	if err != nil {
		t.Fatalf("save baseline snapshot: %v", err)
	}

	currentReport := report.Report{
		SchemaVersion: report.SchemaVersion,
		GeneratedAt:   mustParseTime(t, "2024-02-01T00:00:00Z"),
		RepoPath:      "/repo",
		Dependencies: []report.DependencyReport{
			{
				Name:              "alpha",
				UsedExportsCount:  3,
				TotalExportsCount: 10,
				UsedPercent:       30,
				RuntimeUsage:      &report.RuntimeUsage{LoadCount: 3, Correlation: report.RuntimeCorrelationRuntimeOnly, RuntimeOnly: true},
			},
		},
	}

	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{report: currentReport}, report.NewFormatter())
	reportView := mapSummaryReportView(currentReport)
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}

	if quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "compare-baseline --file "+baselinePath); err != nil || quit {
		t.Fatalf("expected compare to continue, quit=%v err=%v", quit, err)
	}
	if reportView.BaselineComparison == nil {
		t.Fatalf("expected baseline comparison to refresh current view")
	}
	if !strings.Contains(out.String(), "Baseline comparison refreshed") {
		t.Fatalf("expected compare refresh message, got %q", out.String())
	}

	out.Reset()
	detail := NewDetail(&out, nil, ".", "auto")
	if err := detail.showLoadedSummary("alpha", reportView); err != nil {
		t.Fatalf("show refreshed detail: %v", err)
	}
	if !strings.Contains(out.String(), "Runtime baseline delta") || !strings.Contains(out.String(), "load count delta: +2") {
		t.Fatalf("expected refreshed detail to include runtime baseline delta, got %q", out.String())
	}
}

func TestSummaryDetailShowsCodemodAction(t *testing.T) {
	dep := detailDependencyView{
		Name:     "lodash",
		Language: "js-ts",
		Codemod: &detailCodemodView{
			Mode: "suggest-only",
			Suggestions: []detailCodemodSuggestionView{
				{File: "src/index.js", Line: 1, FromModule: "lodash", ToModule: "lodash/map"},
			},
		},
	}
	var out bytes.Buffer
	if err := printCodemod(&out, dep.Codemod, detailCodemodActionTarget(dep)); err != nil {
		t.Fatalf("print codemod: %v", err)
	}
	if !strings.Contains(out.String(), "action: apply-codemod js-ts:lodash --confirm") {
		t.Fatalf("expected explicit codemod apply action, got %q", out.String())
	}
}

func TestParseSummaryActionVariantsAndErrors(t *testing.T) {
	if _, ok, err := parseSummaryAction("", nil); ok || err != nil {
		t.Fatalf("expected empty input to be ignored, ok=%t err=%v", ok, err)
	}
	if _, ok, err := parseSummaryAction("noop", nil); ok || err != nil {
		t.Fatalf("expected unknown input to be ignored, ok=%t err=%v", ok, err)
	}

	action, ok, err := parseSummaryAction("codemod-apply selected dep --confirm", nil)
	if !ok || err != nil || action.kind != summaryActionApplyCodemod || action.dependency != "selected dep" || !action.confirm {
		t.Fatalf("unexpected codemod alias parse: action=%#v ok=%t err=%v", action, ok, err)
	}
	action, ok, err = parseSummaryAction("apply-codemod --confirm", &summaryState{selectedDependency: "js-ts:lodash"})
	if !ok || err != nil || action.dependency != "js-ts:lodash" {
		t.Fatalf("expected selected dependency fallback, action=%#v ok=%t err=%v", action, ok, err)
	}
	if _, ok, err := parseSummaryAction("apply-codemod --bad", nil); !ok || err == nil {
		t.Fatalf("expected unknown apply option error, ok=%t err=%v", ok, err)
	}

	action, ok, err = parseSummaryAction("baseline save --label nightly --store baselines", nil)
	if !ok || err != nil || action.kind != summaryActionSaveBaseline || action.baselineLabel != "nightly" || action.baselineStorePath != "baselines" {
		t.Fatalf("unexpected baseline save parse: action=%#v ok=%t err=%v", action, ok, err)
	}
	action, ok, err = parseSummaryAction("baseline compare --key commit:abc --store baselines", nil)
	if !ok || err != nil || action.kind != summaryActionCompareBaseline || action.baselineKey != "commit:abc" || action.baselineStorePath != "baselines" {
		t.Fatalf("unexpected baseline compare parse: action=%#v ok=%t err=%v", action, ok, err)
	}
	if _, ok, err := parseSummaryAction("baseline compare --label nope", nil); !ok || err == nil {
		t.Fatalf("expected compare label error, ok=%t err=%v", ok, err)
	}
	action, ok, err = parseSummaryAction("compare-baseline label base", nil)
	if !ok || err != nil || action.baselineTarget != "label base" {
		t.Fatalf("unexpected compare positional parse: action=%#v ok=%t err=%v", action, ok, err)
	}
	if _, ok, err := parseSummaryAction("save-baseline --key commit:abc release", nil); !ok || err == nil {
		t.Fatalf("expected key plus label conflict, ok=%t err=%v", ok, err)
	}
	if _, ok, err := parseSummaryAction("save-baseline --store", nil); !ok || err == nil {
		t.Fatalf("expected missing store value error, ok=%t err=%v", ok, err)
	}
}

func TestSummaryCodemodApplyCommandUnavailableAndFailureMessages(t *testing.T) {
	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	reportView := summaryReportView{}
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}

	if err := summary.runSummaryCodemodApply(context.Background(), &opts, &reportView, summaryAction{kind: summaryActionApplyCodemod, confirm: true}); err != nil {
		t.Fatalf("empty dependency message: %v", err)
	}
	if !strings.Contains(out.String(), "Open a dependency detail first") {
		t.Fatalf("expected empty dependency guidance, got %q", out.String())
	}

	out.Reset()
	if err := summary.runSummaryCodemodApply(context.Background(), &opts, &reportView, summaryAction{kind: summaryActionApplyCodemod, dependency: "lodash", confirm: true}); err != nil {
		t.Fatalf("unavailable action message: %v", err)
	}
	if !strings.Contains(out.String(), "unavailable") {
		t.Fatalf("expected unavailable message, got %q", out.String())
	}

	out.Reset()
	summary.Actions = &stubSummaryActionRunner{applyErr: errors.New("dirty worktree")}
	if err := summary.runSummaryCodemodApply(context.Background(), &opts, &reportView, summaryAction{kind: summaryActionApplyCodemod, dependency: "lodash", confirm: true}); err != nil {
		t.Fatalf("failed action message: %v", err)
	}
	output := out.String()
	if strings.Contains(output, "No safe codemod apply results") || !strings.Contains(output, "dirty worktree") {
		t.Fatalf("expected failure message without no-result text, got %q", output)
	}
}

func TestSummaryCodemodApplyUsesFallbackReportWhenDependencyDoesNotMatch(t *testing.T) {
	applyReport := &report.CodemodApplyReport{
		FailedFiles:   1,
		FailedPatches: 1,
		Results: []report.CodemodApplyResult{
			{File: "src/index.js", Status: "failed", PatchCount: 1, Message: "manual follow-up required"},
		},
	}
	actions := &stubSummaryActionRunner{
		applyReport: report.Report{
			Dependencies: []report.DependencyReport{
				{Name: "other", Codemod: &report.CodemodReport{Apply: applyReport}},
			},
		},
	}

	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = actions
	reportView := mapSummaryReportView(report.Report{Dependencies: []report.DependencyReport{{Name: "lodash"}}})
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}

	if err := summary.runSummaryCodemodApply(context.Background(), &opts, &reportView, summaryAction{kind: summaryActionApplyCodemod, dependency: "lodash", confirm: true}); err != nil {
		t.Fatalf("fallback apply report: %v", err)
	}
	if reportView.Dependencies[0].CodemodApply != applyReport {
		t.Fatalf("expected fallback report to merge into selected dependency, got %#v", reportView.Dependencies[0].CodemodApply)
	}
	if !strings.Contains(out.String(), "failed src/index.js") {
		t.Fatalf("expected fallback apply details in output, got %q", out.String())
	}
}

func TestSummaryCodemodApplyReportsNoSafeResults(t *testing.T) {
	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	summary.Actions = &stubSummaryActionRunner{applyReport: report.Report{}}
	reportView := summaryReportView{}
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}

	if err := summary.runSummaryCodemodApply(context.Background(), &opts, &reportView, summaryAction{kind: summaryActionApplyCodemod, dependency: "lodash", confirm: true}); err != nil {
		t.Fatalf("no safe codemod result message: %v", err)
	}
	if !strings.Contains(out.String(), "No safe codemod apply results for lodash") {
		t.Fatalf("expected no-result message, got %q", out.String())
	}
}

func TestBuildSummaryBaselineCompareOptions(t *testing.T) {
	opts := Options{RepoPath: ".", TopN: 20, Language: "all", BaselineStorePath: "existing", BaselineKey: "commit:prev"}

	nextOpts, target, err := buildSummaryBaselineCompareOptions(opts, summaryAction{kind: summaryActionCompareBaseline, baselineTarget: "/tmp/base.json"})
	if err != nil {
		t.Fatalf("absolute file target: %v", err)
	}
	if target != "/tmp/base.json" || nextOpts.BaselinePath != "/tmp/base.json" || nextOpts.BaselineStorePath != "" || nextOpts.BaselineKey != "" {
		t.Fatalf("unexpected file compare options: target=%q opts=%#v", target, nextOpts)
	}

	nextOpts, target, err = buildSummaryBaselineCompareOptions(opts, summaryAction{kind: summaryActionCompareBaseline, baselineKey: "label:nightly", baselineStorePath: "custom"})
	if err != nil {
		t.Fatalf("key target: %v", err)
	}
	if target != "label:nightly" || nextOpts.BaselineStorePath != "custom" || nextOpts.BaselineKey != "label:nightly" || nextOpts.BaselinePath != "" {
		t.Fatalf("unexpected key compare options: target=%q opts=%#v", target, nextOpts)
	}

	nextOpts, target, err = buildSummaryBaselineCompareOptions(opts, summaryAction{kind: summaryActionCompareBaseline, baselinePath: "explicit.json"})
	if err != nil {
		t.Fatalf("explicit file option: %v", err)
	}
	if target != "explicit.json" || nextOpts.BaselinePath != "explicit.json" || nextOpts.BaselineStorePath != "" || nextOpts.BaselineKey != "" {
		t.Fatalf("unexpected explicit file options: target=%q opts=%#v", target, nextOpts)
	}

	nextOpts, target, err = buildSummaryBaselineCompareOptions(opts, summaryAction{kind: summaryActionCompareBaseline})
	if err != nil {
		t.Fatalf("stored key fallback: %v", err)
	}
	if target != "commit:prev" || nextOpts.BaselineStorePath != "existing" || nextOpts.BaselineKey != "commit:prev" {
		t.Fatalf("unexpected stored key fallback: target=%q opts=%#v", target, nextOpts)
	}

	if _, _, err := buildSummaryBaselineCompareOptions(Options{}, summaryAction{kind: summaryActionCompareBaseline}); err == nil {
		t.Fatalf("expected missing compare target to fail")
	}

	if summaryBaselineTargetLooksLikeFile("") {
		t.Fatalf("empty compare target should not look like a file")
	}
	if summaryBaselineTargetLooksLikeFile("commit:abc") {
		t.Fatalf("baseline key should not look like a file")
	}
	if !summaryBaselineTargetLooksLikeFile("baseline.json") {
		t.Fatalf("json target should look like a file")
	}
}

func TestPrintCodemodApplyResultsInDetail(t *testing.T) {
	codemod := &detailCodemodView{
		Apply: &report.CodemodApplyReport{
			AppliedFiles:   1,
			AppliedPatches: 2,
			SkippedFiles:   1,
			SkippedPatches: 1,
			FailedFiles:    1,
			FailedPatches:  1,
			BackupPath:     ".artifacts/backups/lodash.json",
			Results: []report.CodemodApplyResult{
				{File: "src/index.js", Status: "applied", PatchCount: 2},
				{File: "src/manual.js", Status: "failed", PatchCount: 1, Message: "manual follow-up required"},
			},
		},
	}

	var out bytes.Buffer
	if err := printCodemod(&out, codemod); err != nil {
		t.Fatalf("print codemod apply: %v", err)
	}
	output := out.String()
	for _, expected := range []string{
		"apply results",
		"backup: .artifacts/backups/lodash.json",
		"applied src/index.js",
		"failed src/manual.js",
		"manual follow-up required",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected detail output to contain %q, got %q", expected, output)
		}
	}
}

func TestSummaryBaselineActionMessagesAndErrors(t *testing.T) {
	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	reportView := summaryReportView{}
	opts := Options{RepoPath: t.TempDir(), TopN: 20, Language: "all"}

	if err := summary.runSummaryAction(context.Background(), &opts, &reportView, nil, summaryAction{kind: "unknown"}); err != nil {
		t.Fatalf("unknown summary action should be ignored: %v", err)
	}

	if err := summary.runSummaryBaselineSave(context.Background(), &opts, &reportView, summaryAction{kind: summaryActionSaveBaseline, baselineLabel: "nightly"}); err != nil {
		t.Fatalf("unavailable save message: %v", err)
	}
	if !strings.Contains(out.String(), "Baseline save is unavailable") {
		t.Fatalf("expected unavailable baseline save message, got %q", out.String())
	}

	out.Reset()
	summary.Actions = &stubSummaryActionRunner{}
	if err := summary.runSummaryBaselineSave(context.Background(), &opts, &reportView, summaryAction{kind: summaryActionSaveBaseline}); err != nil {
		t.Fatalf("invalid save message: %v", err)
	}
	if !strings.Contains(out.String(), "Invalid command") || !strings.Contains(out.String(), "unable to resolve git commit") {
		t.Fatalf("expected invalid save message, got %q", out.String())
	}

	out.Reset()
	summary.Actions = &stubSummaryActionRunner{saveErr: errors.New("disk full")}
	if err := summary.runSummaryBaselineSave(context.Background(), &opts, &reportView, summaryAction{kind: summaryActionSaveBaseline, baselineLabel: "nightly"}); err != nil {
		t.Fatalf("failed save message: %v", err)
	}
	if !strings.Contains(out.String(), "Baseline save failed: disk full") {
		t.Fatalf("expected save failure message, got %q", out.String())
	}

	out.Reset()
	actions := &stubSummaryActionRunner{
		saveReport: report.Report{Dependencies: []report.DependencyReport{{Name: "dep"}}},
	}
	summary.Actions = actions
	if err := summary.runSummaryBaselineSave(context.Background(), &opts, nil, summaryAction{kind: summaryActionSaveBaseline, baselineLabel: "nightly", baselineStorePath: "store"}); err != nil {
		t.Fatalf("save with fallback path: %v", err)
	}
	if !strings.Contains(out.String(), "store/label_nightly.json") {
		t.Fatalf("expected fallback saved path in output, got %q", out.String())
	}
}

func TestSummaryBaselineCompareErrorMessages(t *testing.T) {
	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	reportView := summaryReportView{}
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}

	if err := summary.runSummaryBaselineCompare(context.Background(), &opts, &reportView, &state, summaryAction{kind: summaryActionCompareBaseline}); err != nil {
		t.Fatalf("invalid compare message: %v", err)
	}
	if !strings.Contains(out.String(), "Invalid command") || !strings.Contains(out.String(), "baseline key or file is required") {
		t.Fatalf("expected invalid compare message, got %q", out.String())
	}

	out.Reset()
	summary.Analyzer = &stubAnalyzer{err: errors.New("analysis failed")}
	if err := summary.runSummaryBaselineCompare(context.Background(), &opts, &reportView, &state, summaryAction{kind: summaryActionCompareBaseline, baselineKey: "label:base"}); err != nil {
		t.Fatalf("failed compare message: %v", err)
	}
	if !strings.Contains(out.String(), "Baseline compare failed: analysis failed") {
		t.Fatalf("expected compare failure message, got %q", out.String())
	}

	out.Reset()
	if err := writeBaselineCompareResult(&out, "label:base", nil); err != nil {
		t.Fatalf("write nil baseline comparison: %v", err)
	}
	if !strings.Contains(out.String(), "Baseline comparison refreshed for label:base") {
		t.Fatalf("expected nil comparison refresh message, got %q", out.String())
	}
}

func TestSummaryActionParserMissingValues(t *testing.T) {
	testCases := []string{
		"save-baseline --label",
		"compare-baseline --key",
		"compare-baseline --file",
		"save-baseline --file baseline.json",
	}
	for _, input := range testCases {
		if _, ok, err := parseSummaryAction(input, nil); !ok || err == nil {
			t.Fatalf("expected parse error for %q, ok=%t err=%v", input, ok, err)
		}
	}
}

func TestDetailCodemodActionTargetFallbacks(t *testing.T) {
	if got := detailCodemodActionTarget(detailDependencyView{}); got != "" {
		t.Fatalf("expected empty action target, got %q", got)
	}
	if got := detailCodemodActionTarget(detailDependencyView{Name: "lodash"}); got != "lodash" {
		t.Fatalf("expected dependency-only action target, got %q", got)
	}
}

func TestSummaryActionInputInvalidCommandUsesActionError(t *testing.T) {
	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	reportView := summaryReportView{}
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}

	quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "apply-codemod --bad")
	if err != nil || quit {
		t.Fatalf("expected invalid action to continue, quit=%t err=%v", quit, err)
	}
	if !strings.Contains(out.String(), "Invalid command: unknown apply-codemod option") {
		t.Fatalf("expected invalid action message, got %q", out.String())
	}
}

func TestSummaryDetailInputStoresSelectedDependency(t *testing.T) {
	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), &stubAnalyzer{}, report.NewFormatter())
	reportView := mapSummaryReportView(report.Report{
		Dependencies: []report.DependencyReport{{Name: "lodash", Language: "js-ts", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
	})
	opts := Options{RepoPath: ".", TopN: 20, Language: "all"}
	state := summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}

	quit, err := summary.handleSummaryInputMutable(context.Background(), &opts, &reportView, &state, "open js-ts:lodash")
	if err != nil || quit {
		t.Fatalf("expected detail open to continue, quit=%t err=%v", quit, err)
	}
	if state.selectedDependency != "js-ts:lodash" {
		t.Fatalf("expected selected dependency to be stored, got %q", state.selectedDependency)
	}
	if !strings.Contains(out.String(), "Dependency detail: lodash") {
		t.Fatalf("expected detail output, got %q", out.String())
	}
}

func TestSummaryBaselinePathResolutionBranches(t *testing.T) {
	reportData := report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies:  []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}},
	}
	unchanged, err := applySummaryBaselineIfNeeded(reportData, Options{})
	if err != nil {
		t.Fatalf("no baseline should not fail: %v", err)
	}
	if len(unchanged.Dependencies) != 1 || unchanged.Dependencies[0].Name != "dep" {
		t.Fatalf("expected report to remain unchanged, got %#v", unchanged)
	}

	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	path, key, _, shouldApply, err := resolveSummaryBaselinePaths(".", Options{BaselinePath: baselinePath})
	if err != nil || !shouldApply || path != baselinePath || key != "" {
		t.Fatalf("unexpected explicit baseline path resolution: path=%q key=%q apply=%t err=%v", path, key, shouldApply, err)
	}

	if _, _, _, _, err := resolveSummaryBaselinePaths(t.TempDir(), Options{BaselineStorePath: "store"}); err == nil {
		t.Fatalf("expected baseline store without key in non-git repo to fail")
	}
	if _, err := applySummaryBaselineIfNeeded(reportData, Options{BaselinePath: filepath.Join(t.TempDir(), "missing.json")}); err == nil {
		t.Fatalf("expected missing baseline file to fail")
	}

	baselineStore := t.TempDir()
	baselineReport := report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies:  []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}},
	}
	savedPath, err := report.SaveSnapshot(baselineStore, "label:base", baselineReport, mustParseTime(t, "2024-01-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("save baseline snapshot: %v", err)
	}
	currentReport := report.Report{
		SchemaVersion: report.SchemaVersion,
		Dependencies:  []report.DependencyReport{{Name: "dep", UsedExportsCount: 2, TotalExportsCount: 2, UsedPercent: 100}},
	}
	applied, err := applySummaryBaselineIfNeeded(currentReport, Options{BaselinePath: savedPath})
	if err != nil {
		t.Fatalf("apply explicit baseline snapshot: %v", err)
	}
	if applied.BaselineComparison == nil || applied.BaselineComparison.BaselineKey != "label:base" {
		t.Fatalf("expected loaded baseline key to be applied, got %#v", applied.BaselineComparison)
	}
}

func TestSummaryActionHelpersNoOps(t *testing.T) {
	if got := findCodemodApplyReport(report.Report{}, "lodash"); got != nil {
		t.Fatalf("expected no apply report, got %#v", got)
	}

	reportView := mapSummaryReportView(report.Report{Dependencies: []report.DependencyReport{{Name: "lodash"}}})
	mergeCodemodApplyReport(nil, "lodash", &report.CodemodApplyReport{})
	mergeCodemodApplyReport(&reportView, "lodash", nil)
	mergeCodemodApplyReport(&reportView, "react", &report.CodemodApplyReport{AppliedFiles: 1})
	if reportView.Dependencies[0].CodemodApply != nil {
		t.Fatalf("expected no-op merges to leave dependency unchanged")
	}

	var out bytes.Buffer
	if err := writeCodemodApplyReport(&out, "lodash", nil); err != nil {
		t.Fatalf("nil codemod apply report should not fail: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("expected nil codemod apply report to write nothing, got %q", out.String())
	}
}
