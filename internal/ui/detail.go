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
	printRuntimeUsage(d.Out, dep.RuntimeUsage)
	printRiskCues(d.Out, dep.RiskCues)
	printRecommendations(d.Out, dep.Recommendations)

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

func printImportList(out io.Writer, title string, imports []report.ImportUse) {
	_, _ = fmt.Fprintf(out, "%s (%d)\n", title, len(imports))
	if len(imports) == 0 {
		_, _ = fmt.Fprintln(out, noneLabel)
		return
	}

	for _, imp := range imports {
		locationHint := ""
		if len(imp.Locations) > 0 {
			locationHint = fmt.Sprintf(" (%s:%d)", imp.Locations[0].File, imp.Locations[0].Line)
		}
		_, _ = fmt.Fprintf(out, "  - %s from %s%s\n", imp.Name, imp.Module, locationHint)
	}
	_, _ = fmt.Fprintln(out, "")
}

func printExportsList(out io.Writer, title string, exports []report.SymbolRef) {
	_, _ = fmt.Fprintf(out, "%s (%d)\n", title, len(exports))
	if len(exports) == 0 {
		_, _ = fmt.Fprintln(out, noneLabel)
		return
	}

	for _, ref := range exports {
		module := ref.Module
		if module == "" {
			module = "(unknown)"
		}
		_, _ = fmt.Fprintf(out, "  - %s (%s)\n", ref.Name, module)
	}
	_, _ = fmt.Fprintln(out, "")
}

func printRiskCues(out io.Writer, cues []report.RiskCue) {
	_, _ = fmt.Fprintf(out, "Risk cues (%d)\n", len(cues))
	if len(cues) == 0 {
		_, _ = fmt.Fprintln(out, noneLabel)
		_, _ = fmt.Fprintln(out, "")
		return
	}

	for _, cue := range cues {
		_, _ = fmt.Fprintf(out, "  - [%s] %s: %s\n", strings.ToUpper(cue.Severity), cue.Code, cue.Message)
	}
	_, _ = fmt.Fprintln(out, "")
}

func printRecommendations(out io.Writer, recommendations []report.Recommendation) {
	_, _ = fmt.Fprintf(out, "Recommendations (%d)\n", len(recommendations))
	if len(recommendations) == 0 {
		_, _ = fmt.Fprintln(out, noneLabel)
		_, _ = fmt.Fprintln(out, "")
		return
	}

	for _, recommendation := range recommendations {
		_, _ = fmt.Fprintf(out, "  - [%s] %s: %s\n", strings.ToUpper(recommendation.Priority), recommendation.Code, recommendation.Message)
		if recommendation.Rationale != "" {
			_, _ = fmt.Fprintf(out, "    rationale: %s\n", recommendation.Rationale)
		}
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
	if usage.RuntimeOnly {
		_, _ = fmt.Fprintln(out, "  - runtime-only: true (no static imports detected)")
	}
	_, _ = fmt.Fprintln(out, "")
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
