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

	summary, hasRegression := compareBenchmarks(baseData, headData, *maxBytesPct, *maxAllocsPct)
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

func parseBenchmarkLine(currentPkg string, line string) (string, samples, bool) {
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

func compareBenchmarks(baseData, headData benchmarkData, maxBytesPct, maxAllocsPct float64) (string, bool) {
	matchedNames := intersectKeys(baseData, headData)
	newOnlyNames := differenceKeys(headData, baseData)
	baseOnlyNames := differenceKeys(baseData, headData)

	rows := make([]comparisonRow, 0, len(matchedNames))
	hasRegression := false

	for _, name := range matchedNames {
		base := baseData[name]
		head := headData[name]
		baseBytes := average(base.bytesPerOp)
		headBytes := average(head.bytesPerOp)
		baseAllocs := average(base.allocsPerOp)
		headAllocs := average(head.allocsPerOp)
		bytesPct, bytesRegressed := percentDelta(baseBytes, headBytes, maxBytesPct)
		allocsPct, allocsRegressed := percentDelta(baseAllocs, headAllocs, maxAllocsPct)
		hasRegression = hasRegression || bytesRegressed || allocsRegressed

		rows = append(rows, comparisonRow{
			name:            name,
			baseBytes:       baseBytes,
			headBytes:       headBytes,
			baseAllocs:      baseAllocs,
			headAllocs:      headAllocs,
			bytesDeltaPct:   bytesPct,
			allocsDeltaPct:  allocsPct,
			regressedBytes:  bytesRegressed,
			regressedAllocs: allocsRegressed,
		})
	}

	var buf bytes.Buffer
	buf.WriteString("## Memory Benchmarks\n\n")
	fmt.Fprintf(&buf, "Thresholds: bytes/op <= +%.1f%%, allocs/op <= +%.1f%%\n\n", maxBytesPct, maxAllocsPct)

	if len(rows) == 0 {
		buf.WriteString("No overlapping benchmark names were found between base and head.\n\n")
	} else {
		buf.WriteString("| Benchmark | Base B/op | Head B/op | Delta B/op | Base allocs/op | Head allocs/op | Delta allocs/op | Status |\n")
		buf.WriteString("| --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
		for _, row := range rows {
			status := "ok"
			if row.regressedBytes || row.regressedAllocs {
				status = "regression"
			}
			fmt.Fprintf(&buf, "| `%s` | %.1f | %.1f | %s | %.1f | %.1f | %s | %s |\n", row.name, row.baseBytes, row.headBytes, row.bytesDeltaPct, row.baseAllocs, row.headAllocs, row.allocsDeltaPct, status)
		}
		buf.WriteString("\n")
	}

	if len(newOnlyNames) > 0 {
		buf.WriteString("New-only benchmarks (reported, not gated until present on base):\n")
		for _, name := range newOnlyNames {
			fmt.Fprintf(&buf, "- `%s`\n", name)
		}
		buf.WriteString("\n")
	}

	if len(baseOnlyNames) > 0 {
		buf.WriteString("Base-only benchmarks (not compared on head):\n")
		for _, name := range baseOnlyNames {
			fmt.Fprintf(&buf, "- `%s`\n", name)
		}
		buf.WriteString("\n")
	}

	if hasRegression {
		buf.WriteString("Result: memory benchmark regression detected.\n")
	} else {
		buf.WriteString("Result: memory benchmark gate passed.\n")
	}

	return buf.String(), hasRegression
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
