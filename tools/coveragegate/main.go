package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type packageCoverage struct {
	name              string
	coveredStatements int
	totalStatements   int
}

type coverageReport struct {
	totalCoveredStatements int
	totalStatements        int
	packages               []packageCoverage
}

type gateResult struct {
	totalCoverage   float64
	failingPackages []packageCoverage
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("coveragegate", flag.ContinueOnError)
	flags.SetOutput(stderr)

	coverProfile := flags.String("coverprofile", "", "path to the Go coverage profile")
	totalMin := flags.Float64("min", 0, "minimum total coverage percentage")
	packageMin := flags.Float64("package-min", 0, "minimum per-package coverage percentage")
	totalOut := flags.String("total-out", "", "path to write the total coverage percentage")
	packagesOut := flags.String("packages-out", "", "path to write package coverage percentages")
	packageFailuresOut := flags.String("package-failures-out", "", "path to write formatted failing package lines")

	if err := flags.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*coverProfile) == "" {
		return exitErr(stderr, errors.New("-coverprofile is required"))
	}
	if *totalMin < 0 {
		return exitErr(stderr, errors.New("-min must be non-negative"))
	}
	if *packageMin < 0 {
		return exitErr(stderr, errors.New("-package-min must be non-negative"))
	}

	report, err := parseCoverageProfile(*coverProfile)
	if err != nil {
		return exitErr(stderr, fmt.Errorf("parse coverage profile: %w", err))
	}

	result := evaluateCoverage(report, *packageMin)
	if _, err := fmt.Fprintf(stdout, "Total coverage: %s (required: >= %s)\n", formatCoverage(result.totalCoverage), formatThreshold(*totalMin)); err != nil {
		return exitErr(stderr, err)
	}
	if *packageMin > 0 {
		if _, err := fmt.Fprintf(stdout, "Per-package coverage minimum: %s\n", formatThreshold(*packageMin)); err != nil {
			return exitErr(stderr, err)
		}
	}

	if err := writeOutputs(*totalOut, *packagesOut, *packageFailuresOut, report, result); err != nil {
		return exitErr(stderr, err)
	}

	if result.totalCoverage < *totalMin {
		if _, err := fmt.Fprintf(stdout, "Coverage gate failed: %s < %s\n", formatCoverage(result.totalCoverage), formatThreshold(*totalMin)); err != nil {
			return exitErr(stderr, err)
		}
		return 1
	}
	if len(result.failingPackages) > 0 {
		if _, err := fmt.Fprintln(stdout, "Coverage gate failed: packages below minimum:"); err != nil {
			return exitErr(stderr, err)
		}
		if _, err := fmt.Fprint(stdout, renderPackageFailures(result.failingPackages)); err != nil {
			return exitErr(stderr, err)
		}
		return 1
	}
	return 0
}

func exitErr(stderr io.Writer, err error) int {
	if _, writeErr := fmt.Fprintln(stderr, err); writeErr != nil {
		return 2
	}
	return 2
}

func parseCoverageProfile(path string) (report coverageReport, err error) {
	file, err := safeio.OpenFile(path)
	if err != nil {
		return coverageReport{}, err
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	packageTotals := make(map[string]*packageCoverage)
	report = coverageReport{}

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if lineNo == 1 {
			if !strings.HasPrefix(line, "mode: ") {
				return coverageReport{}, fmt.Errorf("line 1: expected coverage mode header")
			}
			continue
		}
		if line == "" {
			continue
		}

		pkgName, statements, covered, err := parseCoverageLine(line)
		if err != nil {
			return coverageReport{}, fmt.Errorf("line %d: %w", lineNo, err)
		}

		report.totalStatements += statements
		if covered {
			report.totalCoveredStatements += statements
		}

		pkg := packageTotals[pkgName]
		if pkg == nil {
			pkg = &packageCoverage{name: pkgName}
			packageTotals[pkgName] = pkg
		}
		pkg.totalStatements += statements
		if covered {
			pkg.coveredStatements += statements
		}
	}

	if err := scanner.Err(); err != nil {
		return coverageReport{}, err
	}
	if lineNo == 0 {
		return coverageReport{}, errors.New("empty coverage profile")
	}
	if report.totalStatements == 0 {
		return coverageReport{}, errors.New("coverage profile has no statements")
	}

	report.packages = make([]packageCoverage, 0, len(packageTotals))
	for _, pkg := range packageTotals {
		report.packages = append(report.packages, *pkg)
	}
	sort.Slice(report.packages, func(i, j int) bool {
		return report.packages[i].name < report.packages[j].name
	})

	return report, nil
}

func parseCoverageLine(line string) (string, int, bool, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return "", 0, false, fmt.Errorf("invalid coverage entry %q", line)
	}

	filePath, _, ok := strings.Cut(fields[0], ":")
	if !ok || strings.TrimSpace(filePath) == "" {
		return "", 0, false, fmt.Errorf("invalid file path in %q", line)
	}

	statements, err := strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, false, fmt.Errorf("invalid statement count %q", fields[1])
	}
	if statements < 0 {
		return "", 0, false, fmt.Errorf("invalid negative statement count %d", statements)
	}

	count, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return "", 0, false, fmt.Errorf("invalid execution count %q", fields[2])
	}

	pkgName := packageForFile(filePath)
	if pkgName == "" {
		return "", 0, false, fmt.Errorf("could not resolve package for %q", filePath)
	}

	return pkgName, statements, count > 0, nil
}

func packageForFile(filePath string) string {
	dir := filepath.Dir(strings.TrimSpace(filePath))
	if dir == "." {
		return ""
	}
	return filepath.ToSlash(dir)
}

func evaluateCoverage(report coverageReport, packageMin float64) gateResult {
	result := gateResult{
		totalCoverage: roundCoverage(report.totalCoveredStatements, report.totalStatements),
	}
	if packageMin <= 0 {
		return result
	}

	for _, pkg := range report.packages {
		if roundCoverage(pkg.coveredStatements, pkg.totalStatements) < packageMin {
			result.failingPackages = append(result.failingPackages, pkg)
		}
	}
	return result
}

func roundCoverage(covered, total int) float64 {
	if total <= 0 {
		return 0
	}
	percent := (float64(covered) * 100) / float64(total)
	return math.Round(percent*10) / 10
}

func formatCoverage(percent float64) string {
	return fmt.Sprintf("%.1f%%", percent)
}

func formatThreshold(percent float64) string {
	if percent == math.Trunc(percent) {
		return fmt.Sprintf("%.0f%%", percent)
	}
	return fmt.Sprintf("%.1f%%", percent)
}

func writeOutputs(totalOut, packagesOut, packageFailuresOut string, report coverageReport, result gateResult) error {
	if totalOut != "" {
		if err := writeFile(totalOut, fmt.Sprintf("%.1f\n", result.totalCoverage)); err != nil {
			return fmt.Errorf("write total coverage: %w", err)
		}
	}
	if packagesOut != "" {
		if err := writeFile(packagesOut, renderPackageCoverage(report.packages)); err != nil {
			return fmt.Errorf("write package coverage: %w", err)
		}
	}
	if packageFailuresOut != "" {
		if err := writeFile(packageFailuresOut, renderPackageFailures(result.failingPackages)); err != nil {
			return fmt.Errorf("write package failures: %w", err)
		}
	}
	return nil
}

func renderPackageCoverage(packages []packageCoverage) string {
	if len(packages) == 0 {
		return ""
	}

	var b strings.Builder
	for _, pkg := range packages {
		fmt.Fprintf(&b, "%s\t%.1f\n", pkg.name, roundCoverage(pkg.coveredStatements, pkg.totalStatements))
	}
	return b.String()
}

func renderPackageFailures(packages []packageCoverage) string {
	if len(packages) == 0 {
		return ""
	}

	var b strings.Builder
	for _, pkg := range packages {
		fmt.Fprintf(&b, "- %s: %s\n", pkg.name, formatCoverage(roundCoverage(pkg.coveredStatements, pkg.totalStatements)))
	}
	return b.String()
}

func writeFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(contents), 0o600)
}
