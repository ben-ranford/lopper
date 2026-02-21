package ui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/analysis"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestSummaryCommandHandlersHelpAndFiltering(t *testing.T) {
	state := &summaryState{page: 2, pageSize: 10, sortMode: sortByWaste}
	var out bytes.Buffer

	if !applySummaryCommand(state, "help", &out) || !state.showHelp {
		t.Fatalf("expected help command to set help flag")
	}
	if out.Len() != 0 {
		t.Fatalf("expected help command to rely on re-render instead of direct output")
	}
	if !applySummaryCommand(state, "filter lodash", io.Discard) || state.filter != "lodash" || state.page != 1 {
		t.Fatalf("expected filter command to set filter and reset page")
	}
	if !applySummaryCommand(state, "filter", io.Discard) || state.filter != "" {
		t.Fatalf("expected bare filter command to clear filter")
	}
	if !applySummaryCommand(state, "sort name", io.Discard) || state.sortMode != sortByName {
		t.Fatalf("expected sort name to apply")
	}
	if !applySummaryCommand(state, "sort waste", io.Discard) || state.sortMode != sortByWaste {
		t.Fatalf("expected sort waste to apply")
	}
	if !applySummaryCommand(state, "sort alpha", io.Discard) || state.sortMode != sortByName {
		t.Fatalf("expected sort alpha alias to apply")
	}
	if applySummaryCommand(state, "sort nope", io.Discard) {
		t.Fatalf("expected invalid sort command to fail")
	}
}

func TestSummaryCommandHandlersPagingAndShortcuts(t *testing.T) {
	state := &summaryState{page: 2, pageSize: 10, sortMode: sortByWaste}
	if !applySummaryCommand(state, "page 3", io.Discard) || state.page != 3 {
		t.Fatalf("expected page command to apply")
	}
	if !applySummaryCommand(state, "next", io.Discard) || state.page != 4 {
		t.Fatalf("expected next to increment page")
	}
	if !applySummaryCommand(state, "prev", io.Discard) || state.page != 3 {
		t.Fatalf("expected prev to decrement page")
	}
	if !applySummaryCommand(state, "size 7", io.Discard) || state.pageSize != 7 || state.page != 1 {
		t.Fatalf("expected size command to set page size and reset page")
	}
	if !applySummaryCommand(state, "s", io.Discard) {
		t.Fatalf("expected toggle sort to work")
	}
	if state.sortMode != sortByName {
		t.Fatalf("expected toggle sort to switch to name sort")
	}
	if !applySummaryCommand(state, "s", io.Discard) {
		t.Fatalf("expected second toggle sort to work")
	}
	if state.sortMode != sortByWaste {
		t.Fatalf("expected second toggle sort to switch back to waste sort")
	}
	if !applySummaryCommand(state, "w", io.Discard) || state.sortMode != sortByWaste {
		t.Fatalf("expected w shortcut to set waste sort")
	}
	if !applySummaryCommand(state, "a", io.Discard) || state.sortMode != sortByName {
		t.Fatalf("expected a shortcut to set name sort")
	}
	if applySummaryCommand(state, "unknown", io.Discard) {
		t.Fatalf("expected unknown command to return false")
	}
}

func TestSummaryHelpers(t *testing.T) {
	if _, err := parsePositiveInt("0"); err == nil {
		t.Fatalf("expected parsePositiveInt error for non-positive input")
	}
	if _, err := parsePositiveInt("abc"); err == nil {
		t.Fatalf("expected parsePositiveInt error for invalid input")
	}
	if got, err := parsePositiveInt("5"); err != nil || got != 5 {
		t.Fatalf("expected parsed positive integer, got %d err=%v", got, err)
	}

	deps := []report.DependencyReport{
		{Name: "b", Language: "js-ts", UsedPercent: 20, TotalExportsCount: 10},
		{Name: "a", Language: "python", UsedPercent: 20, TotalExportsCount: 10},
		{Name: "unknown", TotalExportsCount: 0},
	}
	if sorted := sortDependencies(deps, sortByName); sorted[0].Name != "a" {
		t.Fatalf("expected name sorting to put a first, got %q", sorted[0].Name)
	}
	if sorted := sortDependencies(deps, sortByWaste); sorted[0].Name != "a" {
		t.Fatalf("expected waste sorting tie-break to put a first, got %q", sorted[0].Name)
	}
	if pages := pageCount(0, 10); pages != 1 {
		t.Fatalf("expected page count 1 for empty list, got %d", pages)
	}
	if pages := pageCount(11, 10); pages != 2 {
		t.Fatalf("expected page count 2, got %d", pages)
	}
	paged := paginateDependencies(deps, 1, 2)
	if len(paged) != 2 {
		t.Fatalf("expected paged size 2, got %d", len(paged))
	}
	if paged := paginateDependencies(deps, 99, 2); paged != nil {
		t.Fatalf("expected nil for page out of range, got %#v", paged)
	}
	if filtered := filterDependencies(deps, "PYTHON"); len(filtered) != 1 || filtered[0].Language != "python" {
		t.Fatalf("expected filter to match language case-insensitively, got %#v", filtered)
	}
	if parseSortMode("alpha") != sortByName || parseSortMode("waste") != sortByWaste || parseSortMode("unknown") != sortByWaste || parseSortMode(" NAME ") != sortByName {
		t.Fatalf("unexpected sort mode parsing")
	}
	if mode, ok := parseSortModeStrict("waste"); !ok || mode != sortByWaste {
		t.Fatalf("expected strict parser to accept waste")
	}
	if mode, ok := parseSortModeStrict("alpha"); !ok || mode != sortByName {
		t.Fatalf("expected strict parser to accept alpha alias")
	}
	if _, ok := parseSortModeStrict("unknown"); ok {
		t.Fatalf("expected strict parser to reject unknown sort mode")
	}
	if toggleSortMode(sortByWaste) != sortByName || toggleSortMode(sortByName) != sortByWaste {
		t.Fatalf("unexpected sort toggle behavior")
	}
	if page := normalizeSummaryPage(0, 3); page != 1 {
		t.Fatalf("expected normalizeSummaryPage to floor page, got %d", page)
	}
	if page := normalizeSummaryPage(9, 3); page != 3 {
		t.Fatalf("expected normalizeSummaryPage to clamp high page, got %d", page)
	}
	if page := normalizeSummaryPage(2, 3); page != 2 {
		t.Fatalf("expected normalizeSummaryPage to keep in-range page, got %d", page)
	}
}

func TestRunSummaryDependencyPipeline(t *testing.T) {
	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "b", Language: "js-ts", UsedPercent: 10, TotalExportsCount: 10},
			{Name: "a", Language: "python", UsedPercent: 90, TotalExportsCount: 10},
			{Name: "c", Language: "go", UsedPercent: 50, TotalExportsCount: 10},
		},
	}
	state := summaryState{
		filter:   "python",
		sortMode: sortByName,
		page:     9,
		pageSize: 1,
	}
	sorted, paged, normalized, totalPages := runSummaryDependencyPipeline(reportData, state)
	if totalPages != 1 {
		t.Fatalf("expected one page after filtering, got %d", totalPages)
	}
	if normalized.page != 1 {
		t.Fatalf("expected page to normalize to 1, got %d", normalized.page)
	}
	if len(sorted) != 1 || sorted[0].Name != "a" {
		t.Fatalf("unexpected sorted dependencies: %#v", sorted)
	}
	if len(paged) != 1 || paged[0].Name != "a" {
		t.Fatalf("unexpected paged dependencies: %#v", paged)
	}
}

func TestSummaryStartAndSnapshotBranches(t *testing.T) {
	rep := report.Report{Dependencies: []report.DependencyReport{{Name: "lodash", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}}}
	analyzer := stubAnalyzer{report: rep}

	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader("help\nopen lodash\nnoop\nq\n"), analyzer, report.NewFormatter())
	if err := summary.Start(context.Background(), Options{RepoPath: ".", TopN: 1, PageSize: 1}); err != nil {
		t.Fatalf("summary start: %v", err)
	}
	if !strings.Contains(out.String(), "Dependency detail") {
		t.Fatalf("expected detail view output after open command")
	}
	if !strings.Contains(out.String(), "Unknown command") {
		t.Fatalf("expected unknown command feedback")
	}

	out.Reset()
	if err := summary.Snapshot(context.Background(), Options{RepoPath: ".", TopN: 1, PageSize: 1}, "-"); err != nil {
		t.Fatalf("snapshot to stdout: %v", err)
	}
	if !strings.Contains(out.String(), "Lopper TUI (summary)") {
		t.Fatalf("expected snapshot output on stdout")
	}
	if summary.Snapshot(context.Background(), Options{}, "") == nil {
		t.Fatalf("expected snapshot path validation error")
	}
}

func TestReadSummaryInputError(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	if _, err := readSummaryInput(reader); err == nil {
		t.Fatalf("expected readSummaryInput to return EOF on empty input")
	}
}

type errorAnalyzer struct {
	err error
}

func (e errorAnalyzer) Analyse(context.Context, analysis.Request) (report.Report, error) {
	return report.Report{}, e.err
}

func TestSummaryStartAndSnapshotAnalyzerErrors(t *testing.T) {
	expected := errors.New("analyse failed")
	summary := NewSummary(io.Discard, strings.NewReader("q\n"), errorAnalyzer{err: expected}, report.NewFormatter())
	if err := summary.Start(context.Background(), Options{RepoPath: "."}); !errors.Is(err, expected) {
		t.Fatalf("expected start analyzer error, got %v", err)
	}
	if err := summary.Snapshot(context.Background(), Options{RepoPath: "."}, filepath.Join(t.TempDir(), "snapshot.txt")); !errors.Is(err, expected) {
		t.Fatalf("expected snapshot analyzer error, got %v", err)
	}
}

func TestSummarySnapshotWriteError(t *testing.T) {
	rep := report.Report{Dependencies: []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50}}}
	summary := NewSummary(io.Discard, strings.NewReader(""), stubAnalyzer{report: rep}, report.NewFormatter())
	outPath := filepath.Join(t.TempDir(), "missing", "snapshot.txt")
	if summary.Snapshot(context.Background(), Options{RepoPath: "."}, outPath) == nil {
		t.Fatalf("expected write error for non-existent output directory")
	}
}

func TestSummaryRenderAndInputBranches(t *testing.T) {
	rep := report.Report{
		Dependencies: []report.DependencyReport{
			{Name: "a", UsedExportsCount: 1, TotalExportsCount: 2, UsedPercent: 50},
			{Name: "b", UsedExportsCount: 0, TotalExportsCount: 2, UsedPercent: 0},
		},
	}
	var out bytes.Buffer
	summary := NewSummary(&out, strings.NewReader(""), stubAnalyzer{report: rep}, report.NewFormatter())

	state := summaryState{
		filter:   "",
		sortMode: sortByWaste,
		page:     99, // force page clamp branch
		pageSize: 1,
		showHelp: true,
	}
	rendered, err := summary.renderSummary(rep, state)
	if err != nil {
		t.Fatalf("render summary: %v", err)
	}
	if !strings.Contains(rendered, "Commands:") || !strings.Contains(rendered, "Sort:") {
		t.Fatalf("expected rendered summary controls/help, got %q", rendered)
	}

	state.page = -1 // force lower bound branch
	if _, err := summary.renderSummary(rep, state); err != nil {
		t.Fatalf("render summary with negative page: %v", err)
	}

	if quit, err := summary.handleSummaryInput(context.Background(), Options{RepoPath: "."}, &state, ""); err != nil || quit {
		t.Fatalf("expected empty input to continue, quit=%v err=%v", quit, err)
	}
	if quit, err := summary.handleSummaryInput(context.Background(), Options{RepoPath: "."}, &state, "refresh"); err != nil || quit {
		t.Fatalf("expected refresh input to continue, quit=%v err=%v", quit, err)
	}
	if quit, err := summary.handleSummaryInput(context.Background(), Options{RepoPath: "."}, &state, "quit"); err != nil || !quit {
		t.Fatalf("expected quit input to terminate, quit=%v err=%v", quit, err)
	}
}

func TestSummarySnapshotStdoutWhenOutNil(t *testing.T) {
	rep := report.Report{Dependencies: []report.DependencyReport{{Name: "dep", UsedExportsCount: 1, TotalExportsCount: 1, UsedPercent: 100}}}
	summary := NewSummary(nil, strings.NewReader(""), stubAnalyzer{report: rep}, report.NewFormatter())

	// Avoid polluting test output by temporarily redirecting stdout.
	originalStdout := os.Stdout
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = writePipe
	t.Cleanup(func() {
		os.Stdout = originalStdout
		_ = readPipe.Close()
	})

	if err := summary.Snapshot(context.Background(), Options{RepoPath: "."}, "-"); err != nil {
		t.Fatalf("snapshot to stdout with nil Out: %v", err)
	}
	_ = writePipe.Close()
	buf := make([]byte, 4096)
	n, _ := readPipe.Read(buf)
	if n == 0 {
		t.Fatalf("expected snapshot output on stdout")
	}
}

func TestSummaryCommandValidationBranches(t *testing.T) {
	state := &summaryState{page: 1, pageSize: 10, sortMode: sortByWaste}
	if !applySummaryCommand(state, "   ", io.Discard) {
		t.Fatalf("expected empty command fields to be accepted")
	}
	if handleSortCommand(state, []string{"sort"}) {
		t.Fatalf("expected sort command without mode to fail")
	}
	if handlePageCommand(state, []string{"page"}) {
		t.Fatalf("expected page command without argument to fail")
	}
	if handlePageCommand(state, []string{"page", "nope"}) {
		t.Fatalf("expected page command with invalid number to fail")
	}
	if handleSizeCommand(state, []string{"size"}) {
		t.Fatalf("expected size command without argument to fail")
	}
	if handleSizeCommand(state, []string{"size", "bad"}) {
		t.Fatalf("expected size command with invalid number to fail")
	}

	if pageCount(-1, 10) != 1 {
		t.Fatalf("expected pageCount floor for negative totals")
	}
	if pageCount(10, 0) != 1 {
		t.Fatalf("expected pageCount floor for invalid page size")
	}

	deps := []report.DependencyReport{{Name: "a"}, {Name: "b"}}
	if got := paginateDependencies(deps, 0, 1); len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("expected paginateDependencies to normalize page<1, got %#v", got)
	}
	if paged := paginateDependencies(deps, 1, 0); len(paged) != 2 {
		t.Fatalf("expected paginateDependencies to return all deps for pageSize<=0, got %#v", paged)
	}

	waste, ok := dependencyWaste(report.DependencyReport{UsedExportsCount: 1, TotalExportsCount: 4, UsedPercent: 0})
	if !ok || waste != 75 {
		t.Fatalf("expected dependencyWaste fallback calculation, got waste=%f ok=%v", waste, ok)
	}
}

func TestSummaryHandleDetailErrorBranch(t *testing.T) {
	summary := NewSummary(io.Discard, strings.NewReader(""), errorAnalyzer{err: errors.New("detail failed")}, report.NewFormatter())
	state := summaryState{}
	_, err := summary.handleSummaryInput(context.Background(), Options{RepoPath: ".", Language: "auto"}, &state, "open dep")
	if err == nil {
		t.Fatalf("expected detail error to propagate")
	}
}
