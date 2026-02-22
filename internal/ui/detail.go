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
	Analyzer  analysis.Analyzer
	Formatter report.Formatter
	Out       io.Writer
	RepoPath  string
	Language  string
}

const noneLabel = "  (none)"

func NewDetail(out io.Writer, analyzer analysis.Analyzer, formatter report.Formatter, repoPath string, language string) *Detail {
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

	languageID := d.Language
	if parts := strings.SplitN(dependency, ":", 2); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		languageID = parts[0]
		dependency = parts[1]
	}

	reportData, err := d.Analyzer.Analyse(ctx, analysis.Request{
		RepoPath:   d.RepoPath,
		Dependency: dependency,
		Language:   languageID,
	})
	if err != nil {
		return err
	}
	if len(reportData.Dependencies) == 0 {
		_, _ = fmt.Fprintf(d.Out, "No data for dependency %q\n", dependency)
		return nil
	}

	dep := reportData.Dependencies[0]
	_, _ = fmt.Fprintf(d.Out, "Dependency detail: %s\n", dep.Name)
	if dep.Language != "" {
		_, _ = fmt.Fprintf(d.Out, "Language: %s\n", dep.Language)
	}
	_, _ = fmt.Fprintf(d.Out, "Used exports: %d\n", dep.UsedExportsCount)
	_, _ = fmt.Fprintf(d.Out, "Total exports: %d\n", dep.TotalExportsCount)
	_, _ = fmt.Fprintf(d.Out, "Used percent: %.1f%%\n\n", dep.UsedPercent)
	printRemovalCandidate(d.Out, dep.RemovalCandidate)
	printRuntimeUsage(d.Out, dep.RuntimeUsage)
	printRiskCues(d.Out, dep.RiskCues)
	printRecommendations(d.Out, dep.Recommendations)
	printCodemod(d.Out, dep.Codemod)

	printImportList(d.Out, "Used imports", dep.UsedImports)
	printImportList(d.Out, "Unused imports", dep.UnusedImports)
	printExportsList(d.Out, "Unused exports", dep.UnusedExports)

	if len(reportData.Warnings) > 0 {
		_, _ = fmt.Fprintln(d.Out, "Warnings:")
		for _, warning := range reportData.Warnings {
			_, _ = fmt.Fprintf(d.Out, "- %s\n", warning)
		}
	}

	return nil
}

// renderList prints a titled, counted section with items formatted by the provided function.
// If items is empty, a "(none)" placeholder is printed followed by a trailing blank line
// (normalised from the prior per-function behaviour for consistent section separation).
func renderList[T any](out io.Writer, title string, items []T, format func(io.Writer, T)) {
	_, _ = fmt.Fprintf(out, "%s (%d)\n", title, len(items))
	if len(items) == 0 {
		_, _ = fmt.Fprintln(out, noneLabel)
		_, _ = fmt.Fprintln(out, "")
		return
	}
	for _, item := range items {
		format(out, item)
	}
	_, _ = fmt.Fprintln(out, "")
}

func printImportList(out io.Writer, title string, imports []report.ImportUse) {
	renderList(out, title, imports, func(w io.Writer, imp report.ImportUse) {
		locationHint := ""
		if len(imp.Locations) > 0 {
			locationHint = fmt.Sprintf(" (%s:%d)", imp.Locations[0].File, imp.Locations[0].Line)
		}
		_, _ = fmt.Fprintf(w, "  - %s from %s%s\n", imp.Name, imp.Module, locationHint)
		for _, provenance := range imp.Provenance {
			_, _ = fmt.Fprintf(w, "    provenance: %s\n", provenance)
		}
	})
}

func printExportsList(out io.Writer, title string, exports []report.SymbolRef) {
	renderList(out, title, exports, func(w io.Writer, ref report.SymbolRef) {
		module := ref.Module
		if module == "" {
			module = "(unknown)"
		}
		_, _ = fmt.Fprintf(w, "  - %s (%s)\n", ref.Name, module)
	})
}

func printRiskCues(out io.Writer, cues []report.RiskCue) {
	renderList(out, "Risk cues", cues, func(w io.Writer, cue report.RiskCue) {
		_, _ = fmt.Fprintf(w, "  - [%s] %s: %s\n", strings.ToUpper(cue.Severity), cue.Code, cue.Message)
	})
}

func printRecommendations(out io.Writer, recommendations []report.Recommendation) {
	renderList(out, "Recommendations", recommendations, func(w io.Writer, rec report.Recommendation) {
		_, _ = fmt.Fprintf(w, "  - [%s] %s: %s\n", strings.ToUpper(rec.Priority), rec.Code, rec.Message)
		if rec.Rationale != "" {
			_, _ = fmt.Fprintf(w, "    rationale: %s\n", rec.Rationale)
		}
	})
}

func printCodemod(out io.Writer, codemod *report.CodemodReport) {
	_, _ = fmt.Fprintln(out, "Codemod preview")
	if codemod == nil {
		_, _ = fmt.Fprintln(out, noneLabel)
		_, _ = fmt.Fprintln(out, "")
		return
	}
	if codemod.Mode != "" {
		_, _ = fmt.Fprintf(out, "  - mode: %s\n", codemod.Mode)
	}
	_, _ = fmt.Fprintf(out, "  - suggestions: %d\n", len(codemod.Suggestions))
	for _, suggestion := range codemod.Suggestions {
		_, _ = fmt.Fprintf(out, "    - %s:%d %s -> %s\n", suggestion.File, suggestion.Line, suggestion.FromModule, suggestion.ToModule)
	}
	_, _ = fmt.Fprintf(out, "  - skips: %d\n", len(codemod.Skips))
	for _, skip := range codemod.Skips {
		_, _ = fmt.Fprintf(out, "    - %s:%d [%s] %s\n", skip.File, skip.Line, skip.ReasonCode, skip.Message)
	}
	_, _ = fmt.Fprintln(out, "")
}

func printRuntimeUsage(out io.Writer, usage *report.RuntimeUsage) {
	_, _ = fmt.Fprintln(out, "Runtime usage")
	if usage == nil {
		_, _ = fmt.Fprintln(out, noneLabel)
		_, _ = fmt.Fprintln(out, "")
		return
	}
	_, _ = fmt.Fprintf(out, "  - load count: %d\n", usage.LoadCount)
	if usage.Correlation != "" {
		_, _ = fmt.Fprintf(out, "  - correlation: %s\n", usage.Correlation)
	}
	if usage.RuntimeOnly {
		_, _ = fmt.Fprintln(out, "  - runtime-only: true (no static imports detected)")
	}
	if len(usage.Modules) > 0 {
		_, _ = fmt.Fprintf(out, "  - modules: %s\n", formatRuntimeModules(usage.Modules))
	}
	if len(usage.TopSymbols) > 0 {
		_, _ = fmt.Fprintf(out, "  - top symbols: %s\n", formatRuntimeSymbols(usage.TopSymbols))
	}
	_, _ = fmt.Fprintln(out, "")
}

func printRemovalCandidate(out io.Writer, candidate *report.RemovalCandidate) {
	_, _ = fmt.Fprintln(out, "Removal candidate scoring")
	if candidate == nil {
		_, _ = fmt.Fprintln(out, noneLabel)
		_, _ = fmt.Fprintln(out, "")
		return
	}
	_, _ = fmt.Fprintf(out, "  - score: %.1f\n", candidate.Score)
	_, _ = fmt.Fprintf(out, "  - usage: %.1f\n", candidate.Usage)
	_, _ = fmt.Fprintf(out, "  - impact: %.1f\n", candidate.Impact)
	_, _ = fmt.Fprintf(out, "  - confidence: %.1f\n", candidate.Confidence)
	if len(candidate.Rationale) > 0 {
		_, _ = fmt.Fprintln(out, "  - rationale:")
		for _, line := range candidate.Rationale {
			_, _ = fmt.Fprintf(out, "    - %s\n", line)
		}
	}
	_, _ = fmt.Fprintln(out, "")
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
