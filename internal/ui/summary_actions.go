package ui

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

const defaultTUIBaselineStorePath = ".artifacts/lopper-baselines"

type summaryActionKind string

const (
	summaryActionApplyCodemod    summaryActionKind = "apply-codemod"
	summaryActionSaveBaseline    summaryActionKind = "save-baseline"
	summaryActionCompareBaseline summaryActionKind = "compare-baseline"
)

type summaryAction struct {
	kind              summaryActionKind
	dependency        string
	confirm           bool
	allowDirty        bool
	baselineStorePath string
	baselineKey       string
	baselineLabel     string
	baselinePath      string
	baselineTarget    string
}

func parseSummaryAction(input string, state *summaryState) (summaryAction, bool, error) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return summaryAction{}, false, nil
	}

	command := fields[0]
	args := fields[1:]
	if command == "baseline" && len(args) > 0 {
		command = "baseline-" + args[0]
		args = args[1:]
	}

	switch command {
	case "apply-codemod", "codemod-apply":
		return parseSummaryCodemodApplyAction(args, state)
	case "save-baseline", "baseline-save":
		return parseSummaryBaselineSaveAction(args)
	case "compare-baseline", "baseline-compare":
		return parseSummaryBaselineCompareAction(args)
	default:
		return summaryAction{}, false, nil
	}
}

func parseSummaryCodemodApplyAction(args []string, state *summaryState) (summaryAction, bool, error) {
	action := summaryAction{kind: summaryActionApplyCodemod}
	dependencyParts := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--confirm", "-y":
			action.confirm = true
		case "--allow-dirty":
			action.allowDirty = true
		default:
			if strings.HasPrefix(arg, "-") {
				return action, true, fmt.Errorf("unknown apply-codemod option: %s", arg)
			}
			dependencyParts = append(dependencyParts, arg)
		}
	}
	if len(dependencyParts) > 0 {
		action.dependency = strings.Join(dependencyParts, " ")
	} else if state != nil {
		action.dependency = state.selectedDependency
	}
	return action, true, nil
}

func parseSummaryBaselineSaveAction(args []string) (summaryAction, bool, error) {
	action := summaryAction{kind: summaryActionSaveBaseline}
	labelParts, err := parseSummaryBaselineArguments(args, &action, summaryActionSaveBaseline)
	if err != nil {
		return action, true, err
	}
	if action.baselineLabel == "" && len(labelParts) > 0 {
		action.baselineLabel = strings.Join(labelParts, " ")
	}
	if action.baselineKey != "" && action.baselineLabel != "" {
		return action, true, fmt.Errorf("save-baseline accepts either --key or a label, not both")
	}
	return action, true, nil
}

func parseSummaryBaselineCompareAction(args []string) (summaryAction, bool, error) {
	action := summaryAction{kind: summaryActionCompareBaseline}
	targetParts, err := parseSummaryBaselineArguments(args, &action, summaryActionCompareBaseline)
	if err != nil {
		return action, true, err
	}
	if len(targetParts) > 0 {
		action.baselineTarget = strings.Join(targetParts, " ")
	}
	return action, true, nil
}

func parseSummaryBaselineArguments(args []string, action *summaryAction, kind summaryActionKind) ([]string, error) {
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next, handled, err := parseSummaryBaselineOption(args, i, action, kind)
		if err != nil {
			return nil, err
		}
		if handled {
			i = next
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return nil, fmt.Errorf("unknown %s option: %s", kind, arg)
		}
		positionals = append(positionals, arg)
	}
	return positionals, nil
}

func parseSummaryBaselineOption(args []string, index int, action *summaryAction, kind summaryActionKind) (int, bool, error) {
	switch args[index] {
	case "--store":
		return readSummaryBaselineStoreOption(args, index, action)
	case "--key":
		return readSummaryBaselineKeyOption(args, index, action)
	case "--label":
		return readSummaryBaselineLabelOption(args, index, action, kind)
	case "--file":
		return readSummaryBaselineFileOption(args, index, action, kind)
	default:
		return index, false, nil
	}
}

func readSummaryBaselineStoreOption(args []string, index int, action *summaryAction) (int, bool, error) {
	value, next, err := readSummaryActionValue(args, index, args[index])
	if err != nil {
		return index, true, err
	}
	action.baselineStorePath = value
	return next, true, nil
}

func readSummaryBaselineKeyOption(args []string, index int, action *summaryAction) (int, bool, error) {
	value, next, err := readSummaryActionValue(args, index, args[index])
	if err != nil {
		return index, true, err
	}
	action.baselineKey = value
	return next, true, nil
}

func readSummaryBaselineLabelOption(args []string, index int, action *summaryAction, kind summaryActionKind) (int, bool, error) {
	if kind != summaryActionSaveBaseline {
		return index, true, fmt.Errorf("unknown %s option: %s", kind, args[index])
	}
	value, next, err := readSummaryActionValue(args, index, args[index])
	if err != nil {
		return index, true, err
	}
	action.baselineLabel = value
	return next, true, nil
}

func readSummaryBaselineFileOption(args []string, index int, action *summaryAction, kind summaryActionKind) (int, bool, error) {
	if kind != summaryActionCompareBaseline {
		return index, true, fmt.Errorf("unknown %s option: %s", kind, args[index])
	}
	value, next, err := readSummaryActionValue(args, index, args[index])
	if err != nil {
		return index, true, err
	}
	action.baselinePath = value
	return next, true, nil
}

func readSummaryActionValue(args []string, index int, option string) (string, int, error) {
	next := index + 1
	if next >= len(args) || strings.TrimSpace(args[next]) == "" {
		return "", index, fmt.Errorf("%s requires a value", option)
	}
	return strings.TrimSpace(args[next]), next, nil
}

func (s *Summary) runSummaryAction(ctx context.Context, opts *Options, reportView *summaryReportView, state *summaryState, action summaryAction) error {
	switch action.kind {
	case summaryActionApplyCodemod:
		return s.runSummaryCodemodApply(ctx, opts, reportView, action)
	case summaryActionSaveBaseline:
		return s.runSummaryBaselineSave(ctx, opts, reportView, action)
	case summaryActionCompareBaseline:
		return s.runSummaryBaselineCompare(ctx, opts, reportView, state, action)
	default:
		return nil
	}
}

func (s *Summary) runSummaryCodemodApply(ctx context.Context, opts *Options, reportView *summaryReportView, action summaryAction) error {
	if strings.TrimSpace(action.dependency) == "" {
		return writeSummaryActionMessage(s.Out, "Open a dependency detail first or pass a dependency to apply-codemod.\n")
	}
	if !action.confirm {
		return writeSummaryActionMessage(s.Out, "Codemod apply requires --confirm.\n")
	}
	if s.Actions == nil {
		return writeSummaryActionMessage(s.Out, "Codemod apply is unavailable in this TUI instance.\n")
	}

	languageID, dependencyName := parseDependencyLanguage(opts.Language, action.dependency)
	reportData, runErr := s.Actions.ApplyCodemod(ctx, CodemodApplyRequest{
		RepoPath:   opts.RepoPath,
		Dependency: dependencyName,
		TopN:       opts.TopN,
		Language:   languageID,
		AllowDirty: action.allowDirty,
	})
	applyReport := findCodemodApplyReport(reportData, action.dependency)
	if applyReport != nil && reportView != nil {
		mergeCodemodApplyReport(reportView, action.dependency, applyReport)
	}
	if applyReport != nil {
		if err := writeCodemodApplyReport(s.Out, action.dependency, applyReport); err != nil {
			return err
		}
	} else if runErr == nil {
		if err := writeSummaryActionMessage(s.Out, fmt.Sprintf("No safe codemod apply results for %s.\n", sanitizeTerminalString(action.dependency))); err != nil {
			return err
		}
	}
	if runErr != nil {
		return writeSummaryActionMessage(s.Out, fmt.Sprintf("Codemod apply failed: %s\n", sanitizeTerminalString(runErr.Error())))
	}
	return nil
}

func (s *Summary) runSummaryBaselineSave(ctx context.Context, opts *Options, reportView *summaryReportView, action summaryAction) error {
	if s.Actions == nil {
		return writeSummaryActionMessage(s.Out, "Baseline save is unavailable in this TUI instance.\n")
	}
	request, displayKey, err := buildSummaryBaselineSaveRequest(*opts, action)
	if err != nil {
		return writeSummaryActionError(s.Out, err)
	}

	reportData, savedPath, runErr := s.Actions.SaveBaseline(ctx, request)
	if runErr != nil {
		return writeSummaryActionMessage(s.Out, fmt.Sprintf("Baseline save failed: %s\n", sanitizeTerminalString(runErr.Error())))
	}
	if savedPath == "" {
		savedPath = report.BaselineSnapshotPath(request.BaselineStorePath, displayKey)
	}
	*opts = updateOptionsAfterBaselineSave(*opts, request.BaselineStorePath)
	if reportView != nil {
		*reportView = mapSummaryReportView(reportData)
	}
	return writeSummaryActionMessage(s.Out, fmt.Sprintf("Saved baseline %s to %s\n", sanitizeTerminalString(displayKey), sanitizeTerminalString(savedPath)))
}

func (s *Summary) runSummaryBaselineCompare(ctx context.Context, opts *Options, reportView *summaryReportView, state *summaryState, action summaryAction) error {
	nextOpts, target, err := buildSummaryBaselineCompareOptions(*opts, action)
	if err != nil {
		return writeSummaryActionError(s.Out, err)
	}
	nextReportView, err := s.analyseSummaryView(ctx, nextOpts)
	if err != nil {
		return writeSummaryActionMessage(s.Out, fmt.Sprintf("Baseline compare failed: %s\n", sanitizeTerminalString(err.Error())))
	}
	*opts = nextOpts
	if reportView != nil {
		*reportView = nextReportView
		clampSummaryPage(*reportView, state)
	}
	return writeBaselineCompareResult(s.Out, target, nextReportView.BaselineComparison)
}

func buildSummaryBaselineSaveRequest(opts Options, action summaryAction) (BaselineSaveRequest, string, error) {
	storePath := firstNonEmpty(action.baselineStorePath, opts.BaselineStorePath, defaultTUIBaselineStorePath)
	label := normalizeSummaryBaselineLabel(action.baselineLabel)
	key := strings.TrimSpace(action.baselineKey)
	displayKey := key
	if label != "" {
		displayKey = "label:" + label
		key = ""
	} else if key == "" {
		key = resolveSummaryCurrentBaselineKey(opts.RepoPath)
		displayKey = key
	}
	if displayKey == "" {
		return BaselineSaveRequest{}, "", fmt.Errorf("unable to resolve git commit for baseline key; pass --label or --key")
	}

	return BaselineSaveRequest{
		RepoPath:          opts.RepoPath,
		TopN:              opts.TopN,
		Language:          opts.Language,
		BaselineStorePath: storePath,
		BaselineKey:       key,
		BaselineLabel:     label,
	}, displayKey, nil
}

func updateOptionsAfterBaselineSave(opts Options, storePath string) Options {
	opts.BaselineStorePath = storePath
	return opts
}

func buildSummaryBaselineCompareOptions(opts Options, action summaryAction) (Options, string, error) {
	nextOpts := opts
	baselinePath := strings.TrimSpace(action.baselinePath)
	target := strings.TrimSpace(action.baselineTarget)
	key := strings.TrimSpace(action.baselineKey)
	if baselinePath == "" && key == "" && target != "" && summaryBaselineTargetLooksLikeFile(target) {
		baselinePath = target
	}
	if baselinePath != "" {
		nextOpts.BaselinePath = baselinePath
		nextOpts.BaselineStorePath = ""
		nextOpts.BaselineKey = ""
		return nextOpts, baselinePath, nil
	}

	if key == "" && target != "" {
		key = target
	}
	if key == "" {
		key = strings.TrimSpace(opts.BaselineKey)
	}
	if key == "" {
		return Options{}, "", fmt.Errorf("baseline key or file is required")
	}
	storePath := firstNonEmpty(action.baselineStorePath, opts.BaselineStorePath, defaultTUIBaselineStorePath)
	nextOpts.BaselinePath = ""
	nextOpts.BaselineStorePath = storePath
	nextOpts.BaselineKey = key
	return nextOpts, key, nil
}

func normalizeSummaryBaselineLabel(label string) string {
	label = strings.TrimSpace(label)
	return strings.TrimSpace(strings.TrimPrefix(label, "label:"))
}

func summaryBaselineTargetLooksLikeFile(target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	return filepath.IsAbs(target) ||
		strings.ContainsAny(target, `/\`) ||
		strings.EqualFold(filepath.Ext(target), ".json")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func findCodemodApplyReport(reportData report.Report, dependency string) *report.CodemodApplyReport {
	languageID, dependencyName := parseDependencyLanguage("", dependency)
	for i := range reportData.Dependencies {
		dep := reportData.Dependencies[i]
		if dep.Codemod == nil || dep.Codemod.Apply == nil {
			continue
		}
		if dep.Name == dependencyName && summaryDependencyLanguageMatches(languageID, dep.Language) {
			return dep.Codemod.Apply
		}
	}
	for i := range reportData.Dependencies {
		if reportData.Dependencies[i].Codemod != nil && reportData.Dependencies[i].Codemod.Apply != nil {
			return reportData.Dependencies[i].Codemod.Apply
		}
	}
	return nil
}

func mergeCodemodApplyReport(reportView *summaryReportView, dependency string, applyReport *report.CodemodApplyReport) {
	if reportView == nil || applyReport == nil {
		return
	}
	languageID, dependencyName := parseDependencyLanguage("", dependency)
	for i := range reportView.Dependencies {
		dep := &reportView.Dependencies[i]
		if dep.Name != dependencyName || !summaryDependencyLanguageMatches(languageID, dep.Language) {
			continue
		}
		dep.CodemodApply = applyReport
		if dep.detail.Codemod == nil {
			dep.detail.Codemod = &detailCodemodView{Mode: "apply"}
		}
		dep.detail.Codemod.Apply = applyReport
		return
	}
}

func writeSummaryActionError(out io.Writer, err error) error {
	return writeSummaryActionMessage(out, fmt.Sprintf("Invalid command: %s\n", sanitizeTerminalString(err.Error())))
}

func writeSummaryActionMessage(out io.Writer, message string) error {
	_, err := io.WriteString(out, message)
	return err
}

func writeCodemodApplyReport(out io.Writer, dependency string, applyReport *report.CodemodApplyReport) error {
	if applyReport == nil {
		return nil
	}
	if err := writeSummaryActionMessage(out, fmt.Sprintf("Codemod apply results for %s\n", sanitizeTerminalString(dependency))); err != nil {
		return err
	}
	lines := []string{
		fmt.Sprintf("  applied: %d file(s), %d patch(es)\n", applyReport.AppliedFiles, applyReport.AppliedPatches),
		fmt.Sprintf("  skipped: %d file(s), %d patch(es)\n", applyReport.SkippedFiles, applyReport.SkippedPatches),
		fmt.Sprintf("  failed: %d file(s), %d patch(es)\n", applyReport.FailedFiles, applyReport.FailedPatches),
	}
	if applyReport.BackupPath != "" {
		lines = append(lines, fmt.Sprintf("  backup: %s\n", sanitizeTerminalString(applyReport.BackupPath)))
	}
	for _, result := range applyReport.Results {
		line := fmt.Sprintf("  - %s %s (%d patch(es))", sanitizeTerminalString(result.Status), sanitizeTerminalString(result.File), result.PatchCount)
		if strings.TrimSpace(result.Message) != "" {
			line += ": " + sanitizeTerminalString(result.Message)
		}
		lines = append(lines, line+"\n")
	}
	for _, line := range lines {
		if err := writeSummaryActionMessage(out, line); err != nil {
			return err
		}
	}
	return writeSummaryActionMessage(out, "\n")
}

func writeBaselineCompareResult(out io.Writer, target string, comparison *report.BaselineComparison) error {
	if comparison == nil {
		return writeSummaryActionMessage(out, fmt.Sprintf("Baseline comparison refreshed for %s\n", sanitizeTerminalString(target)))
	}
	lines := []string{
		fmt.Sprintf("Baseline comparison refreshed for %s\n", sanitizeTerminalString(target)),
		fmt.Sprintf("  changed: %d, regressions: %d, progressions: %d, added: %d, removed: %d\n", len(comparison.Dependencies), len(comparison.Regressions), len(comparison.Progressions), len(comparison.Added), len(comparison.Removed)),
		fmt.Sprintf("  summary_delta: deps %+d, used %% %+0.1f, waste %% %+0.1f, unused bytes %+d\n", comparison.SummaryDelta.DependencyCountDelta, comparison.SummaryDelta.UsedPercentDelta, comparison.SummaryDelta.WastePercentDelta, comparison.SummaryDelta.UnusedBytesDelta),
	}
	for _, line := range lines {
		if err := writeSummaryActionMessage(out, line); err != nil {
			return err
		}
	}
	return nil
}
