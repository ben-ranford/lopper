package ui

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

type Detail struct {
	Analyzer  analysis.Analyser
	Formatter *report.Formatter
	Out       io.Writer
	RepoPath  string
	Language  string
}

const noneLabel = "  (none)"

func NewDetail(out io.Writer, analyzer analysis.Analyser, formatter *report.Formatter, repoPath string, language string) *Detail {
	if language == "" {
		language = "auto"
	}
	return &Detail{
		Analyzer:  analyzer,
		Formatter: formatter,
		Out:       out,
		RepoPath:  repoPath,
		Language:  language,
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
		if err := writef(d.Out, "No data for dependency %q\n", dependency); err != nil {
			return err
		}
		return nil
	}

	dep := reportData.Dependencies[0]
	if err := printDependencyHeader(d.Out, dep); err != nil {
		return err
	}
	if err := printDependencySections(d.Out, dep); err != nil {
		return err
	}
	return printWarnings(d.Out, reportData.Warnings)
}

func parseDependencyLanguage(defaultLanguage, dependency string) (string, string) {
	if parts := strings.SplitN(dependency, ":", 2); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return parts[0], parts[1]
	}
	return defaultLanguage, dependency
}

func printDependencyHeader(out io.Writer, dep report.DependencyReport) error {
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

func printDependencySections(out io.Writer, dep report.DependencyReport) error {
	printers := []func(io.Writer) error{
		func(w io.Writer) error { return printRemovalCandidate(w, dep.RemovalCandidate) },
		func(w io.Writer) error { return printRuntimeUsage(w, dep.RuntimeUsage) },
		func(w io.Writer) error { return printRiskCues(w, dep.RiskCues) },
		func(w io.Writer) error { return printRecommendations(w, dep.Recommendations) },
		func(w io.Writer) error { return printCodemod(w, dep.Codemod) },
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

func printImportList(out io.Writer, title string, imports []report.ImportUse) error {
	return renderList(out, title, imports, func(w io.Writer, imp report.ImportUse) error {
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

func printExportsList(out io.Writer, title string, exports []report.SymbolRef) error {
	return renderList(out, title, exports, func(w io.Writer, ref report.SymbolRef) error {
		module := ref.Module
		if module == "" {
			module = "(unknown)"
		}
		return writef(w, "  - %s (%s)\n", ref.Name, module)
	})
}

func printRiskCues(out io.Writer, cues []report.RiskCue) error {
	return renderList(out, "Risk cues", cues, func(w io.Writer, cue report.RiskCue) error {
		return writef(w, "  - [%s] %s: %s\n", strings.ToUpper(cue.Severity), cue.Code, cue.Message)
	})
}

func printRecommendations(out io.Writer, recommendations []report.Recommendation) error {
	return renderList(out, "Recommendations", recommendations, func(w io.Writer, rec report.Recommendation) error {
		if err := writef(w, "  - [%s] %s: %s\n", strings.ToUpper(rec.Priority), rec.Code, rec.Message); err != nil {
			return err
		}
		if rec.Rationale != "" {
			return writef(w, "    rationale: %s\n", rec.Rationale)
		}
		return nil
	})
}

func printCodemod(out io.Writer, codemod *report.CodemodReport) error {
	if err := writeln(out, "Codemod preview"); err != nil {
		return err
	}
	if codemod == nil {
		if err := writeln(out, noneLabel); err != nil {
			return err
		}
		return writeln(out, "")
	}
	if codemod.Mode != "" {
		if err := writef(out, "  - mode: %s\n", codemod.Mode); err != nil {
			return err
		}
	}
	if err := writef(out, "  - suggestions: %d\n", len(codemod.Suggestions)); err != nil {
		return err
	}
	for _, suggestion := range codemod.Suggestions {
		if err := writef(out, "    - %s:%d %s -> %s\n", suggestion.File, suggestion.Line, suggestion.FromModule, suggestion.ToModule); err != nil {
			return err
		}
	}
	if err := writef(out, "  - skips: %d\n", len(codemod.Skips)); err != nil {
		return err
	}
	for _, skip := range codemod.Skips {
		if err := writef(out, "    - %s:%d [%s] %s\n", skip.File, skip.Line, skip.ReasonCode, skip.Message); err != nil {
			return err
		}
	}
	return writeln(out, "")
}

func printRuntimeUsage(out io.Writer, usage *report.RuntimeUsage) error {
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

func printRemovalCandidate(out io.Writer, candidate *report.RemovalCandidate) error {
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

func writef(out io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(out, format, args...)
	return err
}

func writeln(out io.Writer, args ...any) error {
	_, err := fmt.Fprintln(out, args...)
	return err
}

func formatRuntimeModules(modules []report.RuntimeModuleUsage) string {
	items := make([]string, 0, len(modules))
	for _, module := range modules {
		items = append(items, fmt.Sprintf("%s (%d)", module.Module, module.Count))
	}
	return strings.Join(items, ", ")
}

func formatRuntimeSymbols(symbols []report.RuntimeSymbolUsage) string {
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

func isDetailCommand(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false
	}
	fields := strings.Fields(input)
	if len(fields) < 2 {
		return "", false
	}
	if fields[0] != "open" && fields[0] != "detail" {
		return "", false
	}
	return fields[1], true
}
