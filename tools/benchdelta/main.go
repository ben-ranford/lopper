package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
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

	baseData, err := parseBenchmarkFile(*basePath)
	if err != nil {
		exitErr(fmt.Errorf("parse base benchmarks: %w", err))
	}
	headData, err := parseBenchmarkFile(*headPath)
	if err != nil {
		exitErr(fmt.Errorf("parse head benchmarks: %w", err))
	}

	limits := deltaThresholds{
		bytesPct:  *maxBytesPct,
		allocsPct: *maxAllocsPct,
	}
	summary, hasRegression := compareBenchmarks(baseData, headData, limits)
	fmt.Print(summary)
	if *summaryOut != "" {
		if err := os.WriteFile(*summaryOut, []byte(summary), 0o600); err != nil {
			exitErr(fmt.Errorf("write summary: %w", err))
		}
	}
	if hasRegression {
		os.Exit(1)
	}
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(2)
}

func parseBenchmarkFile(path string) (result benchmarkData, err error) {
	file, err := safeio.OpenFile(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	result = make(benchmarkData)
	currentPkg := ""

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "pkg: "):
			currentPkg = strings.TrimSpace(strings.TrimPrefix(line, "pkg: "))
		case strings.HasPrefix(line, "Benchmark"):
			name, sample, ok := parseBenchmarkLine(currentPkg, line)
			if !ok {
				continue
			}
			existing := result[name]
			existing.bytesPerOp = append(existing.bytesPerOp, sample.bytesPerOp...)
			existing.allocsPerOp = append(existing.allocsPerOp, sample.allocsPerOp...)
			result[name] = existing
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func parseBenchmarkLine(currentPkg, line string) (string, samples, bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return "", samples{}, false
	}

	benchmarkName := normalizeBenchmarkName(fields[0])
	if currentPkg == "" {
		currentPkg = "unknown-package"
	}
	key := currentPkg + "/" + benchmarkName
	var sample samples

	for i := 2; i+1 < len(fields); i += 2 {
		value, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			continue
		}
		switch fields[i+1] {
		case "B/op":
			sample.bytesPerOp = append(sample.bytesPerOp, value)
		case "allocs/op":
			sample.allocsPerOp = append(sample.allocsPerOp, value)
		}
	}

	if len(sample.bytesPerOp) == 0 && len(sample.allocsPerOp) == 0 {
		return "", samples{}, false
	}
	return key, sample, true
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

func compareBenchmarks(baseData, headData benchmarkData, limits deltaThresholds) (string, bool) {
	matchedNames := intersectKeys(baseData, headData)
	newOnlyNames := differenceKeys(headData, baseData)
	baseOnlyNames := differenceKeys(baseData, headData)

	rows, hasRegression := buildComparisonRows(matchedNames, baseData, headData, limits)

	var buf bytes.Buffer
	writeComparisonSummary(&buf, rows, newOnlyNames, baseOnlyNames, limits, hasRegression)

	return buf.String(), hasRegression
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

func writeComparisonSummary(buf *bytes.Buffer, rows []comparisonRow, newOnlyNames, baseOnlyNames []string, limits deltaThresholds, hasRegression bool) {
	buf.WriteString("## Memory Benchmarks\n\n")
	fmt.Fprintf(buf, "Thresholds: bytes/op <= +%.1f%%, allocs/op <= +%.1f%%\n\n", limits.bytesPct, limits.allocsPct)

	writeComparisonTable(buf, rows)
	writeBenchmarkNameList(buf, "New-only benchmarks (reported, not gated until present on base):", newOnlyNames)
	writeBenchmarkNameList(buf, "Base-only benchmarks (not compared on head):", baseOnlyNames)

	if hasRegression {
		buf.WriteString("Result: memory benchmark regression detected.\n")
		return
	}
	buf.WriteString("Result: memory benchmark gate passed.\n")
}

func writeComparisonTable(buf *bytes.Buffer, rows []comparisonRow) {
	if len(rows) == 0 {
		buf.WriteString("No overlapping benchmark names were found between base and head.\n\n")
		return
	}

	buf.WriteString("| Benchmark | Base B/op | Head B/op | Delta B/op | Base allocs/op | Head allocs/op | Delta allocs/op | Status |\n")
	buf.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, row := range rows {
		fmt.Fprintf(buf, "| `%s` | %.1f | %.1f | %s | %.1f | %.1f | %s | %s |\n", row.name, row.baseBytes, row.headBytes, row.bytesDeltaPct, row.baseAllocs, row.headAllocs, row.allocsDeltaPct, comparisonStatus(row))
	}
	buf.WriteString("\n")
}

func writeBenchmarkNameList(buf *bytes.Buffer, title string, names []string) {
	if len(names) == 0 {
		return
	}

	buf.WriteString(title)
	buf.WriteString("\n")
	for _, name := range names {
		fmt.Fprintf(buf, "- `%s`\n", name)
	}
	buf.WriteString("\n")
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
