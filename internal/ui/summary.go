package ui

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

type Summary struct {
	Analyzer  analysis.Analyzer
	Formatter report.Formatter
	Out       io.Writer
	In        io.Reader
	TopN      int
	PageSize  int
	Language  string
}

func NewSummary(out io.Writer, in io.Reader, analyzer analysis.Analyzer, formatter report.Formatter) *Summary {
	return &Summary{
		Analyzer:  analyzer,
		Formatter: formatter,
		Out:       out,
		In:        in,
		TopN:      50,
		PageSize:  10,
		Language:  "auto",
	}
}

func (s *Summary) Start(ctx context.Context, opts Options) error {
	opts = s.applyDefaults(opts)

	reportData, err := s.Analyzer.Analyse(ctx, analysis.Request{
		RepoPath: opts.RepoPath,
		TopN:     opts.TopN,
		Language: opts.Language,
	})
	if err != nil {
		return err
	}

	reader := bufio.NewReader(s.In)
	state := buildSummaryState(opts)
	for {
		if err := s.renderSummaryOutput(reportData, state); err != nil {
			return err
		}

		input, err := readSummaryInput(reader)
		if err != nil {
			return err
		}
		quit, err := s.handleSummaryInput(ctx, opts, &state, input)
		if err != nil {
			return err
		}
		if quit {
			return nil
		}
	}
}

func (s *Summary) renderSummaryOutput(reportData report.Report, state summaryState) error {
	output, err := s.renderSummary(reportData, state)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprint(s.Out, output)
	return nil
}

func readSummaryInput(reader *bufio.Reader) (string, error) {
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

func (s *Summary) handleSummaryInput(ctx context.Context, opts Options, state *summaryState, input string) (bool, error) {
	if input == "" || input == "refresh" {
		return false, nil
	}
	if input == "q" || input == "quit" {
		return true, nil
	}
	if dependency, ok := isDetailCommand(input); ok {
		detail := NewDetail(s.Out, s.Analyzer, s.Formatter, opts.RepoPath, opts.Language)
		if err := detail.Show(ctx, dependency); err != nil {
			return false, err
		}
		return false, nil
	}
	if !applySummaryCommand(state, input, s.Out) {
		_, _ = fmt.Fprintln(s.Out, "Unknown command. Type 'help' for options.")
	}
	return false, nil
}

func filterDependencies(deps []report.DependencyReport, filter string) []report.DependencyReport {
	if filter == "" {
		return deps
	}
	filter = strings.ToLower(filter)

	filtered := make([]report.DependencyReport, 0, len(deps))
	for _, dep := range deps {
		if strings.Contains(strings.ToLower(dep.Name+" "+dep.Language), filter) {
			filtered = append(filtered, dep)
		}
	}
	return filtered
}

type sortMode string

const (
	sortByWaste sortMode = "waste"
	sortByName  sortMode = "name"
)

type summaryState struct {
	filter   string
	sortMode sortMode
	page     int
	pageSize int
	showHelp bool
}

func applySummaryCommand(state *summaryState, input string, out io.Writer) bool {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return true
	}

	switch fields[0] {
	case "help", "h", "?":
		return handleHelpCommand(state, out)
	case "filter":
		return handleFilterCommand(state, fields)
	case "sort":
		return handleSortCommand(state, fields)
	case "page":
		return handlePageCommand(state, fields)
	case "next", "n":
		state.page++
		return true
	case "prev", "p":
		state.page--
		return true
	case "size":
		return handleSizeCommand(state, fields)
	case "s":
		return handleToggleSortCommand(state)
	case "w":
		setSortMode(state, sortByWaste)
		return true
	case "a":
		setSortMode(state, sortByName)
		return true
	default:
		return false
	}
}

func handleHelpCommand(state *summaryState, out io.Writer) bool {
	state.showHelp = true
	printSummaryHelp(out)
	return true
}

func handleFilterCommand(state *summaryState, fields []string) bool {
	if len(fields) == 1 {
		state.filter = ""
		state.page = 1
		return true
	}
	state.filter = strings.Join(fields[1:], " ")
	state.page = 1
	return true
}

func handleSortCommand(state *summaryState, fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	switch fields[1] {
	case string(sortByWaste):
		setSortMode(state, sortByWaste)
		return true
	case string(sortByName):
		setSortMode(state, sortByName)
		return true
	default:
		return false
	}
}

func handlePageCommand(state *summaryState, fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	page, err := parsePositiveInt(fields[1])
	if err != nil {
		return false
	}
	state.page = page
	return true
}

func handleSizeCommand(state *summaryState, fields []string) bool {
	if len(fields) < 2 {
		return false
	}
	size, err := parsePositiveInt(fields[1])
	if err != nil {
		return false
	}
	state.pageSize = size
	state.page = 1
	return true
}

func handleToggleSortCommand(state *summaryState) bool {
	if state.sortMode == sortByWaste {
		setSortMode(state, sortByName)
		return true
	}
	setSortMode(state, sortByWaste)
	return true
}

func setSortMode(state *summaryState, mode sortMode) {
	state.sortMode = mode
	state.page = 1
}

func printSummaryHelp(out io.Writer) {
	_, _ = fmt.Fprint(out, summaryHelpText())
}

func summaryHelpText() string {
	return strings.Join([]string{
		"Commands:",
		"  filter <text>        Filter dependencies by name",
		"  sort name|waste      Sort by name or waste",
		"  s                    Toggle sort mode",
		"  a                    Sort by name (alpha)",
		"  w                    Sort by waste",
		"  page <n>             Jump to page number",
		"  next | prev          Page navigation",
		"  n | p                Page shortcuts",
		"  size <n>             Change page size",
		"  open <dependency>    Show dependency detail",
		"  open <lang>:<dep>    Detail in multi-language mode",
		"  refresh              Re-render the current view",
		"  q                    Quit",
		"",
	}, "\n")
}

func parsePositiveInt(value string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("invalid number")
	}
	return parsed, nil
}

func sortDependencies(deps []report.DependencyReport, mode sortMode) []report.DependencyReport {
	sorted := append([]report.DependencyReport(nil), deps...)
	switch mode {
	case sortByName:
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].Name == sorted[j].Name {
				return sorted[i].Language < sorted[j].Language
			}
			return sorted[i].Name < sorted[j].Name
		})
	default:
		sort.Slice(sorted, func(i, j int) bool {
			iWaste, iKnown := dependencyWaste(sorted[i])
			jWaste, jKnown := dependencyWaste(sorted[j])
			if iKnown != jKnown {
				return iKnown
			}
			if iWaste == jWaste {
				return sorted[i].Name < sorted[j].Name
			}
			return iWaste > jWaste
		})
	}
	return sorted
}

func dependencyWaste(dep report.DependencyReport) (float64, bool) {
	if dep.TotalExportsCount == 0 {
		return 0, false
	}
	usedPercent := dep.UsedPercent
	if usedPercent <= 0 && dep.TotalExportsCount > 0 {
		usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
	}
	return 100 - usedPercent, true
}

func pageCount(total int, pageSize int) int {
	if total == 0 {
		return 1
	}
	if pageSize <= 0 {
		return 1
	}
	pages := total / pageSize
	if total%pageSize != 0 {
		pages++
	}
	if pages < 1 {
		pages = 1
	}
	return pages
}

func paginateDependencies(deps []report.DependencyReport, page int, pageSize int) []report.DependencyReport {
	if pageSize <= 0 {
		return deps
	}
	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageSize
	if start >= len(deps) {
		return nil
	}
	end := start + pageSize
	if end > len(deps) {
		end = len(deps)
	}
	return deps[start:end]
}

func (s *Summary) Snapshot(ctx context.Context, opts Options, outputPath string) error {
	if outputPath == "" {
		return fmt.Errorf("snapshot output path is required")
	}
	opts = s.applyDefaults(opts)

	reportData, err := s.Analyzer.Analyse(ctx, analysis.Request{
		RepoPath: opts.RepoPath,
		TopN:     opts.TopN,
		Language: opts.Language,
	})
	if err != nil {
		return err
	}

	state := buildSummaryState(opts)
	output, err := s.renderSummary(reportData, state)
	if err != nil {
		return err
	}

	if outputPath == "-" {
		writer := s.Out
		if writer == nil {
			writer = os.Stdout
		}
		if _, err := io.WriteString(writer, output); err != nil {
			return err
		}
		return nil
	}

	if err := os.WriteFile(outputPath, []byte(output), 0o600); err != nil {
		return err
	}
	if s.Out != nil {
		fmt.Fprintf(s.Out, "Snapshot written to %s\n", outputPath)
	}
	return nil
}

func (s *Summary) applyDefaults(opts Options) Options {
	if opts.RepoPath == "" {
		opts.RepoPath = "."
	}
	if opts.Language == "" {
		opts.Language = s.Language
	}
	if opts.TopN <= 0 {
		opts.TopN = s.TopN
	}
	if opts.PageSize <= 0 {
		opts.PageSize = s.PageSize
	}
	if opts.Sort == "" {
		opts.Sort = string(sortByWaste)
	}
	return opts
}

func buildSummaryState(opts Options) summaryState {
	state := summaryState{
		filter:   opts.Filter,
		sortMode: parseSortMode(opts.Sort),
		page:     1,
		pageSize: opts.PageSize,
	}
	return state
}

func parseSortMode(value string) sortMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(sortByName), "alpha":
		return sortByName
	default:
		return sortByWaste
	}
}

func (s *Summary) renderSummary(reportData report.Report, state summaryState) (string, error) {
	filtered := filterDependencies(reportData.Dependencies, state.filter)
	sorted := sortDependencies(filtered, state.sortMode)
	totalPages := pageCount(len(sorted), state.pageSize)
	if state.page > totalPages {
		state.page = totalPages
	}
	if state.page < 1 {
		state.page = 1
	}
	paged := paginateDependencies(sorted, state.page, state.pageSize)

	display := reportData
	display.Dependencies = paged
	display.Summary = report.ComputeSummary(sorted)
	display.LanguageBreakdown = report.ComputeLanguageBreakdown(sorted)

	formatted, err := s.Formatter.Format(display, report.FormatTable)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	fmt.Fprintln(&builder, "Lopper TUI (summary)")
	fmt.Fprintf(
		&builder,
		"Sort: %s | Page: %d/%d | Page size: %d | Total deps: %d\n",
		state.sortMode,
		state.page,
		totalPages,
		state.pageSize,
		len(sorted),
	)
	if state.filter == "" {
		fmt.Fprintln(&builder, "Filter: (none)")
	} else {
		fmt.Fprintf(&builder, "Filter: %q\n", state.filter)
	}
	builder.WriteString(formatted)
	if state.showHelp {
		builder.WriteString(summaryHelpText())
	} else {
		builder.WriteString("Commands: help | open <dependency> | q\n")
	}
	return builder.String(), nil
}
