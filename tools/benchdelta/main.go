package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type samples struct {
	bytesPerOp  []float64
	allocsPerOp []float64
}

type benchmarkData map[string]samples

type benchmarkInput struct {
	data       benchmarkData
	incomplete []string
	invalid    []string
}

type deltaThresholds struct {
	bytesPct  float64
	allocsPct float64
}

type comparisonRow struct {
	name            string
	baseBytes       float64
	headBytes       float64
	baseAllocs      float64
	headAllocs      float64
	bytesDeltaPct   string
	allocsDeltaPct  string
	regressedBytes  bool
	regressedAllocs bool
}

const (
	exitCodePassed     = 0
	exitCodeRegression = 1
	exitCodeInvalid    = 2

	benchmarkMetricNsPerOp     = "ns/op"
	benchmarkMetricBytesPerOp  = "B/op"
	benchmarkMetricAllocsPerOp = "allocs/op"
)

type benchmarkLineStatus uint8

const (
	benchmarkLineIgnored benchmarkLineStatus = iota
	benchmarkLineComplete
	benchmarkLineIncomplete
	benchmarkLineInvalid
)

type comparisonOutcome struct {
	exitCode         int
	status           string
	result           string
	diagnostics      []string
	diagnosticsTitle string
}

type comparisonSummary struct {
	rows          []comparisonRow
	baseCount     int
	headCount     int
	headOnlyNames []string
	baseOnlyNames []string
	limits        deltaThresholds
	outcome       comparisonOutcome
}

func main() {
	basePath := flag.String("base", "", "path to base benchmark output")
	headPath := flag.String("head", "", "path to head benchmark output")
	maxBytesPct := flag.Float64("max-bytes-pct", 15, "maximum allowed bytes/op increase percentage")
	maxAllocsPct := flag.Float64("max-allocs-pct", 10, "maximum allowed allocs/op increase percentage")
	summaryOut := flag.String("summary-out", "", "path to write markdown summary")
	flag.Parse()

	if strings.TrimSpace(*basePath) == "" || strings.TrimSpace(*headPath) == "" {
		exitErr(errors.New("both -base and -head are required"))
	}

	limits := deltaThresholds{
		bytesPct:  *maxBytesPct,
		allocsPct: *maxAllocsPct,
	}

	baseInput, err := parseBenchmarkFile(*basePath)
	if err != nil {
		exitWithSummary(*summaryOut, invalidInputSummary(limits, "unavailable", "unavailable", fmt.Sprintf("base benchmark input could not be read: %v", err)))
	}
	headInput, err := parseBenchmarkFile(*headPath)
	if err != nil {
		exitWithSummary(*summaryOut, invalidInputSummary(limits, benchmarkCountLabel(len(baseInput.data)), "unavailable", fmt.Sprintf("head benchmark input could not be read: %v", err)))
	}

	summary, statusCode := compareBenchmarks(baseInput, headInput, limits)
	fmt.Print(summary)
	if *summaryOut != "" {
		if err := os.WriteFile(*summaryOut, []byte(summary), 0o600); err != nil {
			exitErr(fmt.Errorf("write summary: %w", err))
		}
	}
	if statusCode != exitCodePassed {
		os.Exit(statusCode)
	}
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(exitCodeInvalid)
}

func exitWithSummary(summaryOut, summary string) {
	fmt.Print(summary)
	if strings.TrimSpace(summaryOut) != "" {
		if err := os.WriteFile(summaryOut, []byte(summary), 0o600); err != nil {
			exitErr(fmt.Errorf("write summary: %w", err))
		}
	}
	os.Exit(exitCodeInvalid)
}

func parseBenchmarkFile(path string) (result benchmarkInput, err error) {
	file, err := safeio.OpenFile(path)
	if err != nil {
		return benchmarkInput{}, err
	}
	defer func() {
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	result = benchmarkInput{data: make(benchmarkData)}
	currentPkg := ""

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "pkg: "):
			currentPkg = strings.TrimSpace(strings.TrimPrefix(line, "pkg: "))
		case strings.HasPrefix(line, "Benchmark"):
			name, sample, status, diagnostic := parseBenchmarkLine(currentPkg, line)
			switch status {
			case benchmarkLineComplete:
				existing := result.data[name]
				existing.bytesPerOp = append(existing.bytesPerOp, sample.bytesPerOp...)
				existing.allocsPerOp = append(existing.allocsPerOp, sample.allocsPerOp...)
				result.data[name] = existing
			case benchmarkLineIncomplete:
				result.incomplete = append(result.incomplete, diagnostic)
			case benchmarkLineInvalid:
				result.invalid = append(result.invalid, diagnostic)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return benchmarkInput{}, err
	}
	return result, nil
}

func parseBenchmarkLine(currentPkg, line string) (string, samples, benchmarkLineStatus, string) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return "", samples{}, benchmarkLineIgnored, ""
	}

	benchmarkName := normalizeBenchmarkName(fields[0])
	if currentPkg == "" {
		currentPkg = "unknown-package"
	}
	key := currentPkg + "/" + benchmarkName
	var sample samples
	flags := benchmarkSampleFlags{}

	for i := 2; i+1 < len(fields); i += 2 {
		status, diagnostic := collectBenchmarkMetric(fields[i+1], fields[i], key, &sample, &flags)
		if status != benchmarkLineComplete {
			return key, samples{}, status, diagnostic
		}
	}

	return finalizeBenchmarkLine(key, sample, flags)
}

type benchmarkSampleFlags struct {
	hasNsPerOp bool
	hasBytes   bool
	hasAllocs  bool
}

func collectBenchmarkMetric(unit, rawValue, key string, sample *samples, flags *benchmarkSampleFlags) (benchmarkLineStatus, string) {
	value, err := strconv.ParseFloat(rawValue, 64)
	if err != nil {
		return benchmarkLineComplete, ""
	}

	switch unit {
	case benchmarkMetricNsPerOp:
		flags.hasNsPerOp = true
	case benchmarkMetricBytesPerOp:
		if !isFinite(value) {
			return benchmarkLineInvalid, invalidSampleDiagnostic(key, benchmarkMetricBytesPerOp, rawValue)
		}
		flags.hasBytes = true
		sample.bytesPerOp = append(sample.bytesPerOp, value)
	case benchmarkMetricAllocsPerOp:
		if !isFinite(value) {
			return benchmarkLineInvalid, invalidSampleDiagnostic(key, benchmarkMetricAllocsPerOp, rawValue)
		}
		flags.hasAllocs = true
		sample.allocsPerOp = append(sample.allocsPerOp, value)
	}

	return benchmarkLineComplete, ""
}

func finalizeBenchmarkLine(key string, sample samples, flags benchmarkSampleFlags) (string, samples, benchmarkLineStatus, string) {
	if !flags.hasBytes && !flags.hasAllocs {
		if flags.hasNsPerOp {
			return key, samples{}, benchmarkLineIncomplete, incompleteSampleDiagnostic(key, true, true)
		}
		return "", samples{}, benchmarkLineIgnored, ""
	}
	if !flags.hasBytes || !flags.hasAllocs {
		return key, samples{}, benchmarkLineIncomplete, incompleteSampleDiagnostic(key, !flags.hasBytes, !flags.hasAllocs)
	}
	return key, sample, benchmarkLineComplete, ""
}

func normalizeBenchmarkName(name string) string {
	idx := strings.LastIndex(name, "-")
	if idx <= 0 {
		return name
	}
	suffix := name[idx+1:]
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return name
		}
	}
	return name[:idx]
}

func incompleteSampleDiagnostic(name string, missingBytes, missingAllocs bool) string {
	missing := make([]string, 0, 2)
	if missingBytes {
		missing = append(missing, benchmarkMetricBytesPerOp)
	}
	if missingAllocs {
		missing = append(missing, benchmarkMetricAllocsPerOp)
	}
	return fmt.Sprintf("`%s` missing %s", name, strings.Join(missing, " and "))
}

func invalidSampleDiagnostic(name, metric, value string) string {
	return fmt.Sprintf("`%s` has non-finite %s value %q", name, metric, value)
}

func compareBenchmarks(baseInput, headInput benchmarkInput, limits deltaThresholds) (string, int) {
	matchedNames := intersectKeys(baseInput.data, headInput.data)
	comparableNames, mismatchDiagnostics := comparableMatchedNames(matchedNames, baseInput.data, headInput.data)
	headOnlyNames := differenceKeys(headInput.data, baseInput.data)
	baseOnlyNames := differenceKeys(baseInput.data, headInput.data)

	rows, hasRegression := buildComparisonRows(comparableNames, baseInput.data, headInput.data, limits)
	outcome := classifyComparisonOutcome(baseInput, headInput, mismatchDiagnostics, matchedNames, headOnlyNames, baseOnlyNames, hasRegression)

	var buf bytes.Buffer
	writeComparisonSummary(&buf, comparisonSummary{
		rows:          rows,
		baseCount:     len(baseInput.data),
		headCount:     len(headInput.data),
		headOnlyNames: headOnlyNames,
		baseOnlyNames: baseOnlyNames,
		limits:        limits,
		outcome:       outcome,
	})

	return buf.String(), outcome.exitCode
}

func buildComparisonRows(matchedNames []string, baseData, headData benchmarkData, limits deltaThresholds) ([]comparisonRow, bool) {
	rows := make([]comparisonRow, 0, len(matchedNames))
	hasRegression := false

	for _, name := range matchedNames {
		row := newComparisonRow(name, baseData[name], headData[name], limits)
		hasRegression = hasRegression || row.regressedBytes || row.regressedAllocs
		rows = append(rows, row)
	}

	return rows, hasRegression
}

func newComparisonRow(name string, baseSample, headSample samples, limits deltaThresholds) comparisonRow {
	baseBytes := average(baseSample.bytesPerOp)
	headBytes := average(headSample.bytesPerOp)
	baseAllocs := average(baseSample.allocsPerOp)
	headAllocs := average(headSample.allocsPerOp)
	bytesPct, bytesRegressed := percentDelta(baseBytes, headBytes, limits.bytesPct)
	allocsPct, allocsRegressed := percentDelta(baseAllocs, headAllocs, limits.allocsPct)

	return comparisonRow{
		name:            name,
		baseBytes:       baseBytes,
		headBytes:       headBytes,
		baseAllocs:      baseAllocs,
		headAllocs:      headAllocs,
		bytesDeltaPct:   bytesPct,
		allocsDeltaPct:  allocsPct,
		regressedBytes:  bytesRegressed,
		regressedAllocs: allocsRegressed,
	}
}

func writeComparisonSummary(buf *bytes.Buffer, summary comparisonSummary) {
	buf.WriteString("## Memory Benchmarks\n\n")
	fmt.Fprintf(buf, "Thresholds: bytes/op <= +%.1f%%, allocs/op <= +%.1f%%\n\n", summary.limits.bytesPct, summary.limits.allocsPct)
	fmt.Fprintf(buf, "Base benchmarks: %s\n", benchmarkCountLabel(summary.baseCount))
	fmt.Fprintf(buf, "Head benchmarks: %s\n\n", benchmarkCountLabel(summary.headCount))

	showInvalidOnlySections := summary.outcome.diagnosticsTitle == ""
	writeComparisonTable(buf, summary.rows, showInvalidOnlySections)
	if showInvalidOnlySections {
		writeList(buf, "Head-only benchmarks (missing on base):", summary.headOnlyNames, func(item string) string {
			return fmt.Sprintf("`%s`", item)
		})
		writeList(buf, "Base-only benchmarks (missing on head):", summary.baseOnlyNames, func(item string) string {
			return fmt.Sprintf("`%s`", item)
		})
	}
	writeList(buf, summary.outcome.diagnosticsTitle, summary.outcome.diagnostics, func(item string) string { return item })

	switch summary.outcome.status {
	case "incomplete", "invalid":
		fmt.Fprintf(buf, "Comparison status: %s\n", summary.outcome.status)
		fmt.Fprintf(buf, "%s\n", summary.outcome.result)
	case "regression":
		buf.WriteString("Result: memory benchmark regression detected.\n")
	default:
		buf.WriteString("Result: memory benchmark gate passed.\n")
	}
}

func invalidInputSummary(limits deltaThresholds, baseCount, headCount, diagnostic string) string {
	var buf bytes.Buffer
	buf.WriteString("## Memory Benchmarks\n\n")
	fmt.Fprintf(&buf, "Thresholds: bytes/op <= +%.1f%%, allocs/op <= +%.1f%%\n\n", limits.bytesPct, limits.allocsPct)
	fmt.Fprintf(&buf, "Base benchmarks: %s\n", baseCount)
	fmt.Fprintf(&buf, "Head benchmarks: %s\n\n", headCount)
	writeList(&buf, "Input errors:", []string{diagnostic}, func(item string) string { return item })
	buf.WriteString("Comparison status: invalid\n")
	buf.WriteString("Result: benchmark input could not be read for a safe memory comparison.\n")
	return buf.String()
}

func writeComparisonTable(buf *bytes.Buffer, rows []comparisonRow, showEmptyMessage bool) {
	if len(rows) == 0 {
		if showEmptyMessage {
			buf.WriteString("No overlapping benchmark names were found between base and head.\n\n")
		}
		return
	}

	buf.WriteString("| Benchmark | Base B/op | Head B/op | Delta B/op | Base allocs/op | Head allocs/op | Delta allocs/op | Status |\n")
	buf.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, row := range rows {
		fmt.Fprintf(buf, "| `%s` | %.1f | %.1f | %s | %.1f | %.1f | %s | %s |\n", row.name, row.baseBytes, row.headBytes, row.bytesDeltaPct, row.baseAllocs, row.headAllocs, row.allocsDeltaPct, comparisonStatus(row))
	}
	buf.WriteString("\n")
}

func writeList(buf *bytes.Buffer, title string, items []string, formatItem func(string) string) {
	if len(items) == 0 {
		return
	}

	buf.WriteString(title)
	buf.WriteString("\n")
	for _, item := range items {
		fmt.Fprintf(buf, "- %s\n", formatItem(item))
	}
	buf.WriteString("\n")
}

func benchmarkCountLabel(total int) string {
	if total == 0 {
		return "none"
	}
	return strconv.Itoa(total)
}

func classifyComparisonOutcome(baseInput, headInput benchmarkInput, mismatchDiagnostics, matchedNames, headOnlyNames, baseOnlyNames []string, hasRegression bool) comparisonOutcome {
	if diagnostics := invalidDiagnostics(baseInput, headInput); len(diagnostics) > 0 {
		return comparisonOutcome{
			exitCode:         exitCodeInvalid,
			status:           "invalid",
			result:           "Result: benchmark input contained invalid memory samples; each B/op and allocs/op value must be finite.",
			diagnostics:      diagnostics,
			diagnosticsTitle: "Invalid benchmark samples:",
		}
	}
	if diagnostics := incompleteDiagnostics(baseInput, headInput, mismatchDiagnostics); len(diagnostics) > 0 {
		return comparisonOutcome{
			exitCode:         exitCodeInvalid,
			status:           "incomplete",
			result:           "Result: benchmark input contained incomplete memory samples; each benchmark line must include both B/op and allocs/op.",
			diagnostics:      diagnostics,
			diagnosticsTitle: "Incomplete benchmark samples:",
		}
	}
	if len(baseInput.data) == 0 || len(headInput.data) == 0 || len(matchedNames) == 0 || len(headOnlyNames) > 0 || len(baseOnlyNames) > 0 {
		return comparisonOutcome{
			exitCode: exitCodeInvalid,
			status:   "invalid",
			result:   "Result: every selected benchmark must be present on both base and head.",
		}
	}
	if hasRegression {
		return comparisonOutcome{exitCode: exitCodeRegression, status: "regression"}
	}
	return comparisonOutcome{exitCode: exitCodePassed, status: "passed"}
}

func invalidDiagnostics(baseInput, headInput benchmarkInput) []string {
	diagnostics := make([]string, 0, len(baseInput.invalid)+len(headInput.invalid))
	for _, diagnostic := range baseInput.invalid {
		diagnostics = append(diagnostics, "base: "+diagnostic)
	}
	for _, diagnostic := range headInput.invalid {
		diagnostics = append(diagnostics, "head: "+diagnostic)
	}
	return diagnostics
}

func incompleteDiagnostics(baseInput, headInput benchmarkInput, mismatchDiagnostics []string) []string {
	diagnostics := make([]string, 0, len(baseInput.incomplete)+len(headInput.incomplete)+len(mismatchDiagnostics))
	for _, diagnostic := range baseInput.incomplete {
		diagnostics = append(diagnostics, "base: "+diagnostic)
	}
	for _, diagnostic := range headInput.incomplete {
		diagnostics = append(diagnostics, "head: "+diagnostic)
	}
	diagnostics = append(diagnostics, mismatchDiagnostics...)
	return diagnostics
}

func comparableMatchedNames(matchedNames []string, baseData, headData benchmarkData) ([]string, []string) {
	comparableNames := make([]string, 0, len(matchedNames))
	diagnostics := make([]string, 0)
	for _, name := range matchedNames {
		baseCount := sampleCount(baseData[name])
		headCount := sampleCount(headData[name])
		if baseCount != headCount {
			diagnostics = append(diagnostics, sampleCountMismatchDiagnostic(name, baseCount, headCount))
			continue
		}
		comparableNames = append(comparableNames, name)
	}
	return comparableNames, diagnostics
}

func sampleCount(sample samples) int {
	return len(sample.bytesPerOp)
}

func sampleCountMismatchDiagnostic(name string, baseCount, headCount int) string {
	return fmt.Sprintf("sample-count mismatch for `%s`: base=%d head=%d", name, baseCount, headCount)
}

func comparisonStatus(row comparisonRow) string {
	if row.regressedBytes || row.regressedAllocs {
		return "regression"
	}
	return "ok"
}

func intersectKeys(left, right benchmarkData) []string {
	keys := make([]string, 0)
	for key := range left {
		if _, ok := right[key]; ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func differenceKeys(left, right benchmarkData) []string {
	keys := make([]string, 0)
	for key := range left {
		if _, ok := right[key]; !ok {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func average(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func percentDelta(base, head, limit float64) (string, bool) {
	switch {
	case base == 0 && head == 0:
		return "0.0%", false
	case base == 0 && head > 0:
		return "new non-zero", true
	default:
		delta := ((head - base) / base) * 100
		return fmt.Sprintf("%+.1f%%", delta), delta > limit
	}
}
