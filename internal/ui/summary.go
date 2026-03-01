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
	Analyzer  analysis.Analyser
	Formatter *report.Formatter
	Out       io.Writer
	In        io.Reader
	TopN      int
	PageSize  int
	Language  string
}

func NewSummary(out io.Writer, in io.Reader, analyzer analysis.Analyser, formatter *report.Formatter) *Summary {
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
	refreshInPlace := supportsScreenRefresh(s.Out)
	for {
		if refreshInPlace {
			if err := clearSummaryScreen(s.Out); err != nil {
				return err
			}
		}
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
	_, err = fmt.Fprint(s.Out, output)
	return err
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
		if _, err := fmt.Fprintln(s.Out, "Unknown command. Type 'help' for options."); err != nil {
			return false, err
		}
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
	sortByWaste     sortMode = "waste"
	sortByName      sortMode = "name"
	sortByNameAlias          = "alpha"
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
	_ = out
	state.showHelp = true
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
	mode, ok := parseSortModeStrict(fields[1])
	if !ok {
		return false
	}
	setSortMode(state, mode)
	return true
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
	setSortMode(state, toggleSortMode(state.sortMode))
	return true
}

func setSortMode(state *summaryState, mode sortMode) {
	state.sortMode = mode
	state.page = 1
}

func supportsScreenRefresh(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func clearSummaryScreen(out io.Writer) error {
	_, err := fmt.Fprint(out, "\033[H\033[2J")
	return err
}

func summaryHelpText() string {
	return "Commands:\n" +
		"  filter <text>        Filter dependencies by name\n" +
		"  sort name|alpha|waste  Sort by name or waste\n" +
		"  s                    Toggle sort mode\n" +
		"  a                    Sort by name (alpha)\n" +
		"  w                    Sort by waste\n" +
		"  page <n>             Jump to page number\n" +
		"  next | prev          Page navigation\n" +
		"  n | p                Page shortcuts\n" +
		"  size <n>             Change page size\n" +
		"  open <dependency>    Show dependency detail\n" +
		"  open <lang>:<dep>    Detail in multi-language mode\n" +
		"  refresh              Re-render the current view\n" +
		"  q                    Quit\n\n"
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
	if score, ok := report.RemovalCandidateScore(dep); ok {
		return score, true
	}
	if dep.TotalExportsCount == 0 {
		return 0, false
	}
	usedPercent := dep.UsedPercent
	if usedPercent <= 0 && dep.TotalExportsCount > 0 {
		usedPercent = (float64(dep.UsedExportsCount) / float64(dep.TotalExportsCount)) * 100
	}
	return 100 - usedPercent, true
}

func pageCount(total, pageSize int) int {
	if pageSize <= 0 {
		return 1
	}
	if total <= 0 {
		return 1
	}
	return (total + pageSize - 1) / pageSize
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
	mode, ok := parseSortModeStrict(value)
	if !ok {
		return sortByWaste
	}
	return mode
}

func parseSortModeStrict(value string) (sortMode, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(sortByName), sortByNameAlias:
		return sortByName, true
	case string(sortByWaste):
		return sortByWaste, true
	default:
		return sortByWaste, false
	}
}

func toggleSortMode(mode sortMode) sortMode {
	if mode == sortByWaste {
		return sortByName
	}
	return sortByWaste
}

func runSummaryDependencyPipeline(reportData report.Report, state summaryState) ([]report.DependencyReport, []report.DependencyReport, summaryState, int) {
	filtered := filterDependencies(reportData.Dependencies, state.filter)
	sorted := sortDependencies(filtered, state.sortMode)
	totalPages := pageCount(len(sorted), state.pageSize)
	state.page = normalizeSummaryPage(state.page, totalPages)
	paged := paginateDependencies(sorted, state.page, state.pageSize)
	return sorted, paged, state, totalPages
}

func normalizeSummaryPage(page int, totalPages int) int {
	if page > totalPages {
		return totalPages
	}
	if page < 1 {
		return 1
	}
	return page
}

func buildSummaryDisplayReport(reportData report.Report, sorted []report.DependencyReport, paged []report.DependencyReport) report.Report {
	display := reportData
	display.Dependencies = paged
	display.Summary = report.ComputeSummary(sorted)
	display.LanguageBreakdown = report.ComputeLanguageBreakdown(sorted)
	return display
}

func (s *Summary) formatSummaryDisplay(display report.Report) (string, error) {
	return s.Formatter.Format(display, report.FormatTable)
}

func renderSummaryFrame(formatted string, state summaryState, totalPages int, totalDependencies int) string {
	var builder strings.Builder
	fmt.Fprintln(&builder, "Lopper TUI (summary)")
	fmt.Fprintf(&builder, "Sort: %s | Page: %d/%d | Page size: %d | Total deps: %d\n", state.sortMode, state.page, totalPages, state.pageSize, totalDependencies)
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
	return builder.String()
}

func (s *Summary) renderSummary(reportData report.Report, state summaryState) (string, error) {
	sorted, paged, state, totalPages := runSummaryDependencyPipeline(reportData, state)
	display := buildSummaryDisplayReport(reportData, sorted, paged)
	formatted, err := s.formatSummaryDisplay(display)
	if err != nil {
		return "", err
	}
	return renderSummaryFrame(formatted, state, totalPages, len(sorted)), nil
}
