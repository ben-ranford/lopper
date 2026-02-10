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

	reportData, err := d.Analyzer.Analyse(ctx, analysis.Request{
		RepoPath:   d.RepoPath,
		Dependency: dependency,
		Language:   d.Language,
	})
	if err != nil {
		return err
	}
	if len(reportData.Dependencies) == 0 {
		fmt.Fprintf(d.Out, "No data for dependency %q\n", dependency)
		return nil
	}

	dep := reportData.Dependencies[0]
	fmt.Fprintf(d.Out, "Dependency detail: %s\n", dep.Name)
	fmt.Fprintf(d.Out, "Used exports: %d\n", dep.UsedExportsCount)
	fmt.Fprintf(d.Out, "Total exports: %d\n", dep.TotalExportsCount)
	fmt.Fprintf(d.Out, "Used percent: %.1f%%\n\n", dep.UsedPercent)

	printImportList(d.Out, "Used imports", dep.UsedImports)
	printImportList(d.Out, "Unused imports", dep.UnusedImports)
	printExportsList(d.Out, "Unused exports", dep.UnusedExports)

	if len(reportData.Warnings) > 0 {
		fmt.Fprintln(d.Out, "Warnings:")
		for _, warning := range reportData.Warnings {
			fmt.Fprintf(d.Out, "- %s\n", warning)
		}
	}

	return nil
}

func printImportList(out io.Writer, title string, imports []report.ImportUse) {
	fmt.Fprintf(out, "%s (%d)\n", title, len(imports))
	if len(imports) == 0 {
		fmt.Fprintln(out, "  (none)")
		return
	}

	for _, imp := range imports {
		locationHint := ""
		if len(imp.Locations) > 0 {
			locationHint = fmt.Sprintf(" (%s:%d)", imp.Locations[0].File, imp.Locations[0].Line)
		}
		fmt.Fprintf(out, "  - %s from %s%s\n", imp.Name, imp.Module, locationHint)
	}
	fmt.Fprintln(out, "")
}

func printExportsList(out io.Writer, title string, exports []report.SymbolRef) {
	fmt.Fprintf(out, "%s (%d)\n", title, len(exports))
	if len(exports) == 0 {
		fmt.Fprintln(out, "  (none)")
		return
	}

	for _, ref := range exports {
		module := ref.Module
		if module == "" {
			module = "(unknown)"
		}
		fmt.Fprintf(out, "  - %s (%s)\n", ref.Name, module)
	}
	fmt.Fprintln(out, "")
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
