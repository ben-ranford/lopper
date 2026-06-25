package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/terminal"
)

type Detail struct {
	Analyzer analysis.Analyser
	Out      io.Writer
	RepoPath string
	Language string
}

const noneLabel = "  (none)"

func NewDetail(out io.Writer, analyzer analysis.Analyser, repoPath string, language string) *Detail {
	if language == "" {
		language = "auto"
	}
	return &Detail{
		Analyzer: analyzer,
		Out:      out,
		RepoPath: repoPath,
		Language: language,
	}
}

func (d *Detail) Show(ctx context.Context, dependency string) error {
	if dependency == "" {
		return fmt.Errorf("dependency name is required")
	}

	languageID, dependency := parseDependencyLanguage(d.Language, dependency)

	reportData, err := d.Analyzer.Analyse(ctx, analysis.Request{
		RepoPath:   d.RepoPath,
		Dependency: dependency,
		Language:   languageID,
	})
	if err != nil {
		return err
	}
	if len(reportData.Dependencies) == 0 {
		return d.printNoData(dependency)
	}

	return d.printDependency(mapDetailDependencyView(reportData.Dependencies[0]), reportData.Warnings)
}

func (d *Detail) showLoadedSummary(dependency string, reportView summaryReportView) error {
	if dependency == "" {
		return fmt.Errorf("dependency name is required")
	}

	languageID, dependency := parseDependencyLanguage(d.Language, dependency)
	dep, ok := findSummaryDependencyDetail(reportView.Dependencies, languageID, dependency)
	if !ok {
		return d.printNoData(dependency)
	}
	return d.printDependency(dep, reportView.Warnings)
}

func (d *Detail) printNoData(dependency string) error {
	return writef(d.Out, "No data for dependency %q\n", dependency)
}

func (d *Detail) printDependency(dep detailDependencyView, warnings []string) error {
	if err := printDependencyHeader(d.Out, dep); err != nil {
		return err
	}
	if err := printDependencySections(d.Out, dep); err != nil {
		return err
	}
	return printWarnings(d.Out, warnings)
}

func parseDependencyLanguage(defaultLanguage, dependency string) (string, string) {
	if parts := strings.SplitN(dependency, ":", 2); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	return defaultLanguage, dependency
}

func findSummaryDependencyDetail(dependencies []summaryDependencyView, languageID, dependency string) (detailDependencyView, bool) {
	for _, dep := range dependencies {
		if dep.Name != dependency {
			continue
		}
		if !summaryDependencyLanguageMatches(languageID, dep.Language) {
			continue
		}
		return summaryDependencyDetailView(dep), true
	}
	return detailDependencyView{}, false
}

func summaryDependencyLanguageMatches(languageID, dependencyLanguage string) bool {
	switch strings.TrimSpace(languageID) {
	case "", "auto", "all":
		return true
	default:
		return dependencyLanguage == "" || dependencyLanguage == languageID
	}
}

func printDependencyHeader(out io.Writer, dep detailDependencyView) error {
	if err := writef(out, "Dependency detail: %s\n", dep.Name); err != nil {
		return err
	}
	if dep.Language != "" {
		if err := writef(out, "Language: %s\n", dep.Language); err != nil {
			return err
		}
	}
	if err := writef(out, "Used exports: %d\n", dep.UsedExportsCount); err != nil {
		return err
	}
	if err := writef(out, "Total exports: %d\n", dep.TotalExportsCount); err != nil {
		return err
	}
	return writef(out, "Used percent: %.1f%%\n\n", dep.UsedPercent)
}

func printDependencySections(out io.Writer, dep detailDependencyView) error {
	printers := []func(io.Writer) error{
		func(w io.Writer) error { return printReachabilityConfidence(w, dep.ReachabilityConfidence) },
		func(w io.Writer) error { return printRemovalCandidate(w, dep.RemovalCandidate) },
		func(w io.Writer) error { return printRuntimeUsage(w, dep.RuntimeUsage) },
		func(w io.Writer) error { return printRuntimeDelta(w, dep.RuntimeDelta) },
		func(w io.Writer) error { return printRiskCues(w, dep.RiskCues) },
		func(w io.Writer) error { return printRecommendations(w, dep.Recommendations) },
		func(w io.Writer) error { return printCodemod(w, dep.Codemod, detailCodemodActionTarget(dep)) },
		func(w io.Writer) error { return printImportList(w, "Used imports", dep.UsedImports) },
		func(w io.Writer) error { return printImportList(w, "Unused imports", dep.UnusedImports) },
		func(w io.Writer) error { return printExportsList(w, "Unused exports", dep.UnusedExports) },
	}
	for _, printer := range printers {
		if err := printer(out); err != nil {
			return err
		}
	}
	return nil
}

func printWarnings(out io.Writer, warnings []string) error {
	if len(warnings) == 0 {
		return nil
	}
	if err := writeln(out, "Warnings:"); err != nil {
		return err
	}
	for _, warning := range warnings {
		if err := writef(out, "- %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}

// renderList prints a titled, counted section with items formatted by the provided function.
// If items is empty, a "(none)" placeholder is printed followed by a trailing blank line
// (normalised from the prior per-function behaviour for consistent section separation).
func renderList[T any](out io.Writer, title string, items []T, format func(io.Writer, T) error) error {
	if err := writef(out, "%s (%d)\n", title, len(items)); err != nil {
		return err
	}
	if len(items) == 0 {
		if err := writeln(out, noneLabel); err != nil {
			return err
		}
		return writeln(out, "")
	}
	for _, elem := range items {
		if err := format(out, elem); err != nil {
			return err
		}
	}
	return writeln(out, "")
}

func printImportList(out io.Writer, title string, imports []detailImportView) error {
	return renderList(out, title, imports, func(w io.Writer, imp detailImportView) error {
		locationHint := ""
		if len(imp.Locations) > 0 {
			locationHint = fmt.Sprintf(" (%s:%d)", imp.Locations[0].File, imp.Locations[0].Line)
		}
		if err := writef(w, "  - %s from %s%s\n", imp.Name, imp.Module, locationHint); err != nil {
			return err
		}
		for _, provenance := range imp.Provenance {
			if err := writef(w, "    provenance: %s\n", provenance); err != nil {
				return err
			}
		}
		return nil
	})
}

func printExportsList(out io.Writer, title string, exports []detailSymbolRefView) error {
	return renderList(out, title, exports, func(w io.Writer, ref detailSymbolRefView) error {
		module := ref.Module
		if module == "" {
			module = "(unknown)"
		}
		return writef(w, "  - %s (%s)\n", ref.Name, module)
	})
}

func printRiskCues(out io.Writer, cues []detailRiskCueView) error {
	return renderList(out, "Risk cues", cues, func(w io.Writer, cue detailRiskCueView) error {
		return writef(w, "  - [%s] %s: %s\n", strings.ToUpper(cue.Severity), cue.Code, cue.Message)
	})
}

func printRecommendations(out io.Writer, recommendations []detailRecommendationView) error {
	return renderList(out, "Recommendations", recommendations, func(w io.Writer, rec detailRecommendationView) error {
		if err := writef(w, "  - [%s] %s: %s\n", strings.ToUpper(rec.Priority), rec.Code, rec.Message); err != nil {
			return err
		}
		if rec.Rationale != "" {
			return writef(w, "    rationale: %s\n", rec.Rationale)
		}
		return nil
	})
}

func printCodemod(out io.Writer, codemod *detailCodemodView, actionTargets ...string) error {
	if err := writeln(out, "Codemod preview"); err != nil {
		return err
	}
	if codemod == nil {
		if err := writeln(out, noneLabel); err != nil {
			return err
		}
		return writeln(out, "")
	}

	if err := printCodemodMode(out, codemod.Mode); err != nil {
		return err
	}
	if err := printCodemodSuggestions(out, codemod.Suggestions); err != nil {
		return err
	}
	if err := printCodemodSkips(out, codemod.Skips); err != nil {
		return err
	}
	if err := printCodemodActionHint(out, codemod.Suggestions, actionTargets...); err != nil {
		return err
	}
	if err := printDetailCodemodApply(out, codemod.Apply); err != nil {
		return err
	}
	return writeln(out, "")
}

func printCodemodMode(out io.Writer, mode string) error {
	if mode == "" {
		return nil
	}
	return writef(out, "  - mode: %s\n", mode)
}

func printCodemodSuggestions(out io.Writer, suggestions []detailCodemodSuggestionView) error {
	if err := writef(out, "  - suggestions: %d\n", len(suggestions)); err != nil {
		return err
	}
	for _, suggestion := range suggestions {
		if err := writef(out, "    - %s:%d %s -> %s\n", suggestion.File, suggestion.Line, suggestion.FromModule, suggestion.ToModule); err != nil {
			return err
		}
	}
	return nil
}

func printCodemodSkips(out io.Writer, skips []detailCodemodSkipView) error {
	if err := writef(out, "  - skips: %d\n", len(skips)); err != nil {
		return err
	}
	for _, skip := range skips {
		if err := writef(out, "    - %s:%d [%s] %s\n", skip.File, skip.Line, skip.ReasonCode, skip.Message); err != nil {
			return err
		}
	}
	return nil
}

func printCodemodActionHint(out io.Writer, suggestions []detailCodemodSuggestionView, actionTargets ...string) error {
	if len(suggestions) == 0 {
		return nil
	}
	actionTarget := firstNonEmpty(actionTargets...)
	if actionTarget == "" {
		return nil
	}
	return writef(out, "  - action: apply-codemod %s --confirm\n", actionTarget)
}

func detailCodemodActionTarget(dep detailDependencyView) string {
	if strings.TrimSpace(dep.Name) == "" {
		return ""
	}
	if strings.TrimSpace(dep.Language) == "" {
		return dep.Name
	}
	return dep.Language + ":" + dep.Name
}

func printDetailCodemodApply(out io.Writer, apply *report.CodemodApplyReport) error {
	if apply == nil {
		return nil
	}
	if err := writeln(out, "  - apply results:"); err != nil {
		return err
	}
	lines := []string{
		fmt.Sprintf("    applied: %d file(s), %d patch(es)", apply.AppliedFiles, apply.AppliedPatches),
		fmt.Sprintf("    skipped: %d file(s), %d patch(es)", apply.SkippedFiles, apply.SkippedPatches),
		fmt.Sprintf("    failed: %d file(s), %d patch(es)", apply.FailedFiles, apply.FailedPatches),
	}
	if apply.BackupPath != "" {
		lines = append(lines, fmt.Sprintf("    backup: %s", apply.BackupPath))
	}
	for _, line := range lines {
		if err := writeln(out, line); err != nil {
			return err
		}
	}
	for _, result := range apply.Results {
		line := fmt.Sprintf("    - %s %s (%d patch(es))", result.Status, result.File, result.PatchCount)
		if strings.TrimSpace(result.Message) != "" {
			line += ": " + result.Message
		}
		if err := writeln(out, line); err != nil {
			return err
		}
	}
	return nil
}

func printRuntimeUsage(out io.Writer, usage *detailRuntimeUsageView) error {
	if err := writeln(out, "Runtime usage"); err != nil {
		return err
	}
	if usage == nil {
		return writeNoneAndBlankLine(out)
	}
	lines := []string{fmt.Sprintf("  - load count: %d", usage.LoadCount)}
	if usage.Correlation != "" {
		lines = append(lines, fmt.Sprintf("  - correlation: %s", usage.Correlation))
	}
	if usage.RuntimeOnly {
		lines = append(lines, "  - runtime-only: true (no static imports detected)")
	}
	if len(usage.Modules) > 0 {
		lines = append(lines, fmt.Sprintf("  - modules: %s", formatRuntimeModules(usage.Modules)))
	}
	if len(usage.ParentModules) > 0 {
		lines = append(lines, fmt.Sprintf("  - parent modules: %s", formatRuntimeModules(usage.ParentModules)))
	}
	if len(usage.Entrypoints) > 0 {
		lines = append(lines, fmt.Sprintf("  - entrypoints: %s", formatRuntimeModules(usage.Entrypoints)))
	}
	if len(usage.TopSymbols) > 0 {
		lines = append(lines, fmt.Sprintf("  - top symbols: %s", formatRuntimeSymbols(usage.TopSymbols)))
	}
	for _, line := range lines {
		if err := writeln(out, line); err != nil {
			return err
		}
	}
	return writeln(out, "")
}

func printRuntimeDelta(out io.Writer, delta *detailRuntimeDeltaView) error {
	if delta == nil {
		return nil
	}
	if err := writeln(out, "Runtime baseline delta"); err != nil {
		return err
	}
	lines := []string{
		fmt.Sprintf("  - comparable: %t", delta.Comparable),
		fmt.Sprintf("  - baseline runtime data: %t", delta.BaselinePresent),
		fmt.Sprintf("  - current runtime data: %t", delta.CurrentPresent),
	}
	if delta.BaselineLoadCount != nil {
		lines = append(lines, fmt.Sprintf("  - baseline load count: %d", *delta.BaselineLoadCount))
	}
	if delta.CurrentLoadCount != nil {
		lines = append(lines, fmt.Sprintf("  - current load count: %d", *delta.CurrentLoadCount))
	}
	if delta.LoadCountDelta != nil {
		lines = append(lines, fmt.Sprintf("  - load count delta: %+d", *delta.LoadCountDelta))
	}
	if delta.BaselineCorrelation != "" || delta.CurrentCorrelation != "" {
		lines = append(lines, fmt.Sprintf("  - correlation: %s -> %s", delta.BaselineCorrelation, delta.CurrentCorrelation))
	}
	if len(delta.ChangeTypes) > 0 {
		changeTypes := make([]string, 0, len(delta.ChangeTypes))
		for _, changeType := range delta.ChangeTypes {
			changeTypes = append(changeTypes, string(changeType))
		}
		lines = append(lines, fmt.Sprintf("  - change types: %s", strings.Join(changeTypes, ", ")))
	}
	if delta.NewRuntimeLoads {
		lines = append(lines, "  - new runtime loads: true")
	}
	if delta.RemovedRuntimeLoads {
		lines = append(lines, "  - removed runtime loads: true")
	}
	if delta.RuntimeOnlyRegression {
		lines = append(lines, "  - runtime-only regression: true")
	}
	if delta.RuntimeOnlyImprovement {
		lines = append(lines, "  - runtime-only improvement: true")
	}
	appendRuntimeModuleDeltaLine(&lines, "modules added", delta.ModulesAdded)
	appendRuntimeModuleDeltaLine(&lines, "modules removed", delta.ModulesRemoved)
	appendRuntimeModuleDeltaLine(&lines, "modules changed", delta.ModulesChanged)
	appendRuntimeModuleDeltaLine(&lines, "parent modules added", delta.ParentModulesAdded)
	appendRuntimeModuleDeltaLine(&lines, "parent modules removed", delta.ParentModulesRemoved)
	appendRuntimeModuleDeltaLine(&lines, "parent modules changed", delta.ParentModulesChanged)
	appendRuntimeModuleDeltaLine(&lines, "entrypoints added", delta.EntrypointsAdded)
	appendRuntimeModuleDeltaLine(&lines, "entrypoints removed", delta.EntrypointsRemoved)
	appendRuntimeModuleDeltaLine(&lines, "entrypoints changed", delta.EntrypointsChanged)
	if err := writeLines(out, lines); err != nil {
		return err
	}
	return writeln(out, "")
}

func printReachabilityConfidence(out io.Writer, confidence *detailReachabilityConfidenceView) error {
	if err := writeln(out, "Reachability confidence"); err != nil {
		return err
	}
	if confidence == nil {
		return writeNoneAndBlankLine(out)
	}
	if err := writeLines(out, reachabilityConfidenceLines(confidence)); err != nil {
		return err
	}
	if err := printReachabilitySignals(out, confidence.Signals); err != nil {
		return err
	}
	return writeln(out, "")
}

func reachabilityConfidenceLines(confidence *detailReachabilityConfidenceView) []string {
	lines := []string{
		fmt.Sprintf("  - model: %s", confidence.Model),
		fmt.Sprintf("  - score: %.1f", confidence.Score),
	}
	if confidence.Summary != "" {
		lines = append(lines, fmt.Sprintf("  - summary: %s", confidence.Summary))
	}
	if len(confidence.RationaleCodes) > 0 {
		lines = append(lines, fmt.Sprintf("  - rationale codes: %s", strings.Join(confidence.RationaleCodes, ", ")))
	}
	return lines
}

func printReachabilitySignals(out io.Writer, signals []detailReachabilitySignalView) error {
	if len(signals) == 0 {
		return nil
	}
	if err := writeln(out, "  - signals:"); err != nil {
		return err
	}
	for _, signal := range signals {
		if err := writef(out, "    - %s: score=%.1f weight=%.3f contribution=%.1f\n", signal.Code, signal.Score, signal.Weight, signal.Contribution); err != nil {
			return err
		}
		if signal.Rationale == "" {
			continue
		}
		if err := writef(out, "      rationale: %s\n", signal.Rationale); err != nil {
			return err
		}
	}
	return nil
}

func printRemovalCandidate(out io.Writer, candidate *detailRemovalCandidateView) error {
	if err := writeln(out, "Removal candidate scoring"); err != nil {
		return err
	}
	if candidate == nil {
		return writeNoneAndBlankLine(out)
	}
	lines := []string{
		fmt.Sprintf("  - score: %.1f", candidate.Score),
		fmt.Sprintf("  - usage: %.1f", candidate.Usage),
		fmt.Sprintf("  - impact: %.1f", candidate.Impact),
		fmt.Sprintf("  - confidence: %.1f", candidate.Confidence),
	}
	for _, line := range lines {
		if err := writeln(out, line); err != nil {
			return err
		}
	}
	if len(candidate.Rationale) == 0 {
		return writeln(out, "")
	}
	if err := writeln(out, "  - rationale:"); err != nil {
		return err
	}
	for _, line := range candidate.Rationale {
		if err := writef(out, "    - %s\n", line); err != nil {
			return err
		}
	}
	return writeln(out, "")
}

func writeNoneAndBlankLine(out io.Writer) error {
	if err := writeln(out, noneLabel); err != nil {
		return err
	}
	return writeln(out, "")
}

func writeLines(out io.Writer, lines []string) error {
	for _, line := range lines {
		if err := writeln(out, line); err != nil {
			return err
		}
	}
	return nil
}

func writef(out io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(out, format, sanitizeOutputArgs(args)...)
	return err
}

func writeln(out io.Writer, args ...any) error {
	_, err := fmt.Fprintln(out, sanitizeOutputArgs(args)...)
	return err
}

func sanitizeOutputArgs(args []any) []any {
	sanitizedArgs := make([]any, len(args))
	for i, arg := range args {
		switch value := arg.(type) {
		case string:
			sanitizedArgs[i] = sanitizeTerminalString(value)
		default:
			sanitizedArgs[i] = arg
		}
	}
	return sanitizedArgs
}

func sanitizeTerminalString(value string) string {
	return terminal.SanitizeString(value)
}

func formatRuntimeModules(modules []detailRuntimeModuleView) string {
	items := make([]string, 0, len(modules))
	for _, module := range modules {
		items = append(items, fmt.Sprintf("%s (%d)", module.Module, module.Count))
	}
	return strings.Join(items, ", ")
}

func formatRuntimeSymbols(symbols []detailRuntimeSymbolView) string {
	items := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol.Module != "" {
			items = append(items, fmt.Sprintf("%s [%s] (%d)", symbol.Symbol, symbol.Module, symbol.Count))
			continue
		}
		items = append(items, fmt.Sprintf("%s (%d)", symbol.Symbol, symbol.Count))
	}
	return strings.Join(items, ", ")
}

func appendRuntimeModuleDeltaLine(lines *[]string, label string, deltas []detailRuntimeModuleDeltaView) {
	if len(deltas) == 0 {
		return
	}
	items := make([]string, 0, len(deltas))
	for _, delta := range deltas {
		items = append(items, fmt.Sprintf("%s (%d -> %d, %+d)", delta.Module, delta.BaselineCount, delta.CurrentCount, delta.CountDelta))
	}
	*lines = append(*lines, fmt.Sprintf("  - %s: %s", label, strings.Join(items, ", ")))
}

func isDetailCommand(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false
	}
	command, args, ok := strings.Cut(input, " ")
	if !ok {
		return "", false
	}
	if command != "open" && command != "detail" {
		return "", false
	}
	dependency := strings.TrimSpace(args)
	if dependency == "" {
		return "", false
	}
	return dependency, true
}
