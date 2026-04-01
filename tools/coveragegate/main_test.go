package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	coverProfileFlag = "-coverprofile"
	packageMinFlag   = "-package-min"
	totalOutName     = "total.txt"
	packagesOutName  = "packages.txt"
	failuresOutName  = "failures.txt"
	missingOutName   = "missing.out"
)

func TestParseCoverageProfileAndWriteOutputs(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "coverage.out")
	profileLines := []string{
		"mode: atomic",
		"github.com/ben-ranford/lopper/internal/app/app.go:1.1,2.1 3 1",
		"github.com/ben-ranford/lopper/internal/app/app.go:3.1,4.1 1 0",
		"github.com/ben-ranford/lopper/internal/runtime/runtime.go:1.1,2.1 2 1",
		"github.com/ben-ranford/lopper/internal/runtime/runtime.go:3.1,4.1 3 0",
		"",
	}
	profile := strings.Join(profileLines, "\n")
	if err := os.WriteFile(profilePath, []byte(profile), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	report, err := parseCoverageProfile(profilePath)
	if err != nil {
		t.Fatalf("parse coverage profile: %v", err)
	}
	if got := roundCoverage(report.totalCoveredStatements, report.totalStatements); got != 55.6 {
		t.Fatalf("total coverage = %.1f, want 55.6", got)
	}
	if len(report.packages) != 2 {
		t.Fatalf("package count = %d, want 2", len(report.packages))
	}

	result := evaluateCoverage(report, 60)
	if len(result.failingPackages) != 1 {
		t.Fatalf("failing packages = %d, want 1", len(result.failingPackages))
	}
	if result.failingPackages[0].name != "github.com/ben-ranford/lopper/internal/runtime" {
		t.Fatalf("unexpected failing package %q", result.failingPackages[0].name)
	}

	totalOut, packagesOut, failuresOut := coverageOutputPaths(dir)
	if err := writeOutputs(totalOut, packagesOut, failuresOut, report, result); err != nil {
		t.Fatalf("write outputs: %v", err)
	}

	assertFileContains(t, totalOut, "55.6\n")
	assertFileContains(t, packagesOut, "github.com/ben-ranford/lopper/internal/app\t75.0\n")
	assertFileContains(t, packagesOut, "github.com/ben-ranford/lopper/internal/runtime\t40.0\n")
	assertFileContains(t, failuresOut, "- github.com/ben-ranford/lopper/internal/runtime: 40.0%\n")
}

func TestEvaluateCoverageUsesRoundedPercentages(t *testing.T) {
	report := coverageReport{
		totalCoveredStatements: 1959,
		totalStatements:        2000,
		packages: []packageCoverage{
			{
				name:              "github.com/ben-ranford/lopper/internal/rounded",
				coveredStatements: 1959,
				totalStatements:   2000,
			},
		},
	}

	result := evaluateCoverage(report, 98)
	if result.totalCoverage != 98.0 {
		t.Fatalf("total coverage = %.1f, want 98.0", result.totalCoverage)
	}
	if len(result.failingPackages) != 0 {
		t.Fatalf("expected rounded package coverage to pass, got %d failures", len(result.failingPackages))
	}
}

func TestParseCoverageProfileRejectsMalformedInput(t *testing.T) {
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "coverage.out")
	if err := os.WriteFile(profilePath, []byte("mode: atomic\nnot-a-valid-line\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	if _, err := parseCoverageProfile(profilePath); err == nil {
		t.Fatal("expected malformed coverage profile to fail")
	}
}

func TestRunSuccessAndFailurePaths(t *testing.T) {
	dir := t.TempDir()
	passProfile := writeCoverageProfile(t, dir, "pass.out", []string{
		"github.com/ben-ranford/lopper/internal/pass/pass.go:1.1,2.1 2 1",
		"github.com/ben-ranford/lopper/internal/pass/pass.go:3.1,4.1 2 1",
	})
	totalOut, packagesOut, failuresOut := coverageOutputPaths(dir)

	passArgs := []string{
		coverProfileFlag, passProfile,
		"-min", "98",
		packageMinFlag, "98",
		"-total-out", totalOut,
		"-packages-out", packagesOut,
		"-package-failures-out", failuresOut,
	}
	stdout, stderr, exitCode := runWithBuffers(passArgs)
	if exitCode != 0 {
		t.Fatalf("run success exit code = %d, stderr=%q", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Total coverage: 100.0% (required: >= 98%)") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}
	assertFileContains(t, totalOut, "100.0\n")
	assertFileContains(t, packagesOut, "github.com/ben-ranford/lopper/internal/pass\t100.0\n")

	failProfile := writeCoverageProfile(t, dir, "fail.out", []string{
		"github.com/ben-ranford/lopper/internal/fail/fail.go:1.1,2.1 3 1",
		"github.com/ben-ranford/lopper/internal/fail/fail.go:3.1,4.1 1 0",
	})

	stdout.Reset()
	stderr.Reset()
	failArgs := []string{
		coverProfileFlag, failProfile,
		"-min", "98",
		packageMinFlag, "98",
	}
	exitCode = run(failArgs, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("run failure exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stdout.String(), "Coverage gate failed: 75.0% < 98%") {
		t.Fatalf("expected total failure output, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr on failure, got %q", stderr.String())
	}
}

func TestRunErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantMessage string
	}{
		{
			name:        "missing profile",
			args:        nil,
			wantMessage: "-coverprofile is required",
		},
		{
			name:        "negative total threshold",
			args:        coverageArgs(missingOutName, "-min", "-1"),
			wantMessage: "-min must be non-negative",
		},
		{
			name:        "negative package threshold",
			args:        coverageArgs(missingOutName, packageMinFlag, "-1"),
			wantMessage: "-package-min must be non-negative",
		},
		{
			name:        "missing profile file",
			args:        coverageArgs(missingOutName, "-min", "0"),
			wantMessage: "parse coverage profile:",
		},
		{
			name:        "flag parse",
			args:        []string{"-unknown-flag"},
			wantMessage: "flag provided but not defined",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, exitCode := runWithBuffers(tc.args)
			requireExitCodeTwo(t, exitCode)
			if !strings.Contains(stderr.String(), tc.wantMessage) {
				t.Fatalf("expected stderr to contain %q, got %q", tc.wantMessage, stderr.String())
			}
		})
	}
}

func TestRunWriteErrorPaths(t *testing.T) {
	dir := t.TempDir()
	profilePath := writeCoverageProfile(t, dir, "write-errors.out", []string{
		"github.com/ben-ranford/lopper/internal/pass/pass.go:1.1,2.1 2 1",
		"github.com/ben-ranford/lopper/internal/pass/pass.go:3.1,4.1 2 1",
	})

	tests := []struct {
		name        string
		args        []string
		failOnWrite int
		errText     string
	}{
		{
			name:        "total coverage write failure",
			args:        coverageArgs(profilePath, "-min", "0"),
			failOnWrite: 1,
			errText:     "stdout failed",
		},
		{
			name:        "package minimum write failure",
			args:        coverageArgs(profilePath, "-min", "0", packageMinFlag, "98"),
			failOnWrite: 2,
			errText:     "package minimum failed",
		},
		{
			name:        "total failure message write failure",
			args:        coverageArgs(profilePath, "-min", "101"),
			failOnWrite: 2,
			errText:     "failure message failed",
		},
		{
			name:        "package failure heading write failure",
			args:        coverageArgs(profilePath, "-min", "0", packageMinFlag, "101"),
			failOnWrite: 3,
			errText:     "package failure heading failed",
		},
		{
			name:        "package failure body write failure",
			args:        coverageArgs(profilePath, "-min", "0", packageMinFlag, "101"),
			failOnWrite: 4,
			errText:     "package failure body failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stdout := &failOnWriteWriter{failOn: tc.failOnWrite, err: errors.New(tc.errText)}
			var stderr bytes.Buffer

			exitCode := run(tc.args, stdout, &stderr)
			requireExitCodeTwo(t, exitCode)
			if !strings.Contains(stderr.String(), tc.errText) {
				t.Fatalf("expected stderr to contain write error, got %q", stderr.String())
			}
		})
	}
}

func writeCoverageProfile(t *testing.T, dir, name string, lines []string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	profile := strings.Join(append([]string{"mode: atomic"}, lines...), "\n") + "\n"
	if err := os.WriteFile(path, []byte(profile), 0o644); err != nil {
		t.Fatalf("write coverage profile: %v", err)
	}
	return path
}

func TestRunPackageFailureAndWriteErrorPaths(t *testing.T) {
	dir := t.TempDir()
	packageFailProfile := writeCoverageProfile(t, dir, "package-fail.out", []string{
		"github.com/ben-ranford/lopper/internal/pass/pass.go:1.1,2.1 97 1",
		"github.com/ben-ranford/lopper/internal/pass/pass.go:3.1,4.1 3 0",
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	packageFailArgs := []string{
		coverProfileFlag, packageFailProfile,
		"-min", "97",
		packageMinFlag, "98",
	}
	exitCode := run(packageFailArgs, &stdout, &stderr)
	if exitCode != 1 {
		t.Fatalf("package failure exit code = %d, want 1", exitCode)
	}
	if !strings.Contains(stdout.String(), "Coverage gate failed: packages below minimum:") {
		t.Fatalf("expected package failure output, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "- github.com/ben-ranford/lopper/internal/pass: 97.0%") {
		t.Fatalf("expected failing package output, got %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", stderr.String())
	}

	blockingFile := filepath.Join(dir, "blocking-file")
	if err := os.WriteFile(blockingFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	writeErrorArgs := []string{
		coverProfileFlag, packageFailProfile,
		"-min", "0",
		packageMinFlag, "0",
		"-total-out", filepath.Join(blockingFile, totalOutName),
	}
	exitCode = run(writeErrorArgs, &stdout, &stderr)
	requireExitCodeTwo(t, exitCode)
	if !strings.Contains(stderr.String(), "write total coverage:") {
		t.Fatalf("expected write error, got %q", stderr.String())
	}
}

func TestParseCoverageLineParsesValidUncoveredLine(t *testing.T) {
	pkg, statements, covered, err := parseCoverageLine("github.com/ben-ranford/lopper/internal/app/app.go:1.1,2.1 3 0")
	if err != nil {
		t.Fatalf("parseCoverageLine: %v", err)
	}
	if pkg != "github.com/ben-ranford/lopper/internal/app" || statements != 3 || covered {
		t.Fatalf("unexpected parse result: pkg=%q statements=%d covered=%t", pkg, statements, covered)
	}
}

func TestParseCoverageLineRejectsInvalidCases(t *testing.T) {
	for _, tc := range []string{
		"not enough fields",
		"github.com/ben-ranford/lopper/internal/app/app.go 3 1",
		"github.com/ben-ranford/lopper/internal/app/app.go:1.1,2.1 nope 1",
		"github.com/ben-ranford/lopper/internal/app/app.go:1.1,2.1 -1 1",
		"github.com/ben-ranford/lopper/internal/app/app.go:1.1,2.1 3 nope",
	} {
		if _, _, _, err := parseCoverageLine(tc); err == nil {
			t.Fatalf("expected parseCoverageLine(%q) to fail", tc)
		}
	}
}

func TestPackageForFile(t *testing.T) {
	if got := packageForFile("."); got != "" {
		t.Fatalf("packageForFile(.) = %q, want empty", got)
	}
	if got := packageForFile(" github.com/ben-ranford/lopper/internal/app/app.go "); got != "github.com/ben-ranford/lopper/internal/app" {
		t.Fatalf("packageForFile converted = %q", got)
	}
}

func TestEvaluateCoverageWithoutPackageGate(t *testing.T) {
	report := coverageReport{
		totalCoveredStatements: 2,
		totalStatements:        4,
		packages: []packageCoverage{
			{name: "pkg", coveredStatements: 1, totalStatements: 2},
		},
	}
	result := evaluateCoverage(report, 0)
	if result.totalCoverage != 50.0 {
		t.Fatalf("total coverage = %.1f", result.totalCoverage)
	}
	if len(result.failingPackages) != 0 {
		t.Fatalf("expected no failing packages, got %d", len(result.failingPackages))
	}
}

func TestCoverageFormattingHelpers(t *testing.T) {
	if got := roundCoverage(1, 0); got != 0 {
		t.Fatalf("roundCoverage zero total = %.1f", got)
	}
	if got := formatThreshold(98.5); got != "98.5%" {
		t.Fatalf("formatThreshold decimal = %q", got)
	}
	if got := renderPackageCoverage(nil); got != "" {
		t.Fatalf("renderPackageCoverage empty = %q", got)
	}
	if got := renderPackageFailures(nil); got != "" {
		t.Fatalf("renderPackageFailures empty = %q", got)
	}
}

func TestParseCoverageProfileHeaderAndStatementErrors(t *testing.T) {
	dir := t.TempDir()

	noHeader := filepath.Join(dir, "no-header.out")
	if err := os.WriteFile(noHeader, []byte("github.com/ben-ranford/lopper/internal/app/app.go:1.1,2.1 1 1\n"), 0o644); err != nil {
		t.Fatalf("write no-header profile: %v", err)
	}
	if _, err := parseCoverageProfile(noHeader); err == nil || !strings.Contains(err.Error(), "expected coverage mode header") {
		t.Fatalf("expected missing header error, got %v", err)
	}

	noStatements := filepath.Join(dir, "no-statements.out")
	if err := os.WriteFile(noStatements, []byte("mode: atomic\n"), 0o644); err != nil {
		t.Fatalf("write no-statements profile: %v", err)
	}
	if _, err := parseCoverageProfile(noStatements); err == nil || !strings.Contains(err.Error(), "coverage profile has no statements") {
		t.Fatalf("expected no statements error, got %v", err)
	}

	emptyProfile := filepath.Join(dir, "empty.out")
	if err := os.WriteFile(emptyProfile, nil, 0o644); err != nil {
		t.Fatalf("write empty profile: %v", err)
	}
	if _, err := parseCoverageProfile(emptyProfile); err == nil || !strings.Contains(err.Error(), "empty coverage profile") {
		t.Fatalf("expected empty profile error, got %v", err)
	}

	missingPackage := filepath.Join(dir, "missing-package.out")
	if err := os.WriteFile(missingPackage, []byte("mode: atomic\n./main.go:1.1,2.1 1 1\n"), 0o644); err != nil {
		t.Fatalf("write missing-package profile: %v", err)
	}
	if _, err := parseCoverageProfile(missingPackage); err == nil || !strings.Contains(err.Error(), "could not resolve package") {
		t.Fatalf("expected missing package error, got %v", err)
	}
}

func TestParseCoverageProfileScannerError(t *testing.T) {
	dir := t.TempDir()
	longLine := "github.com/ben-ranford/lopper/internal/app/app.go:1.1,2.1 " + strings.Repeat("1", 1024*1024+1) + " 1"
	profilePath := filepath.Join(dir, "too-long.out")
	if err := os.WriteFile(profilePath, []byte("mode: atomic\n"+longLine+"\n"), 0o644); err != nil {
		t.Fatalf("write long profile: %v", err)
	}

	if _, err := parseCoverageProfile(profilePath); err == nil || !strings.Contains(err.Error(), "token too long") {
		t.Fatalf("expected scanner error, got %v", err)
	}
}

func TestWriteOutputsPropagatesPackagesAndFailuresErrors(t *testing.T) {
	dir := t.TempDir()
	report := coverageReport{
		packages: []packageCoverage{{name: "pkg", coveredStatements: 1, totalStatements: 1}},
	}
	result := gateResult{
		totalCoverage:   100,
		failingPackages: []packageCoverage{{name: "pkg", coveredStatements: 1, totalStatements: 2}},
	}

	packagesBlocker := filepath.Join(dir, "packages-blocker")
	if err := os.WriteFile(packagesBlocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write packages blocker: %v", err)
	}
	if err := writeOutputs("", filepath.Join(packagesBlocker, packagesOutName), "", report, result); err == nil || !strings.Contains(err.Error(), "write package coverage") {
		t.Fatalf("expected package coverage write error, got %v", err)
	}

	failuresBlocker := filepath.Join(dir, "failures-blocker")
	if err := os.WriteFile(failuresBlocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write failures blocker: %v", err)
	}
	if err := writeOutputs("", "", filepath.Join(failuresBlocker, failuresOutName), report, result); err == nil || !strings.Contains(err.Error(), "write package failures") {
		t.Fatalf("expected package failures write error, got %v", err)
	}
}

func runWithBuffers(args []string) (bytes.Buffer, bytes.Buffer, int) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(args, &stdout, &stderr)
	return stdout, stderr, exitCode
}

func TestMainExecutesRun(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		argsIndex := -1
		for i, arg := range os.Args {
			if arg == "--" {
				argsIndex = i
				break
			}
		}
		if argsIndex == -1 {
			t.Fatal("missing helper args separator")
		}

		oldArgs := os.Args
		defer func() {
			os.Args = oldArgs
		}()
		os.Args = append([]string{oldArgs[0]}, oldArgs[argsIndex+1:]...)
		main()
		return
	}

	dir := t.TempDir()
	profilePath := writeCoverageProfile(t, dir, "main.out", []string{
		"github.com/ben-ranford/lopper/internal/main/main.go:1.1,2.1 2 1",
	})

	cmd := exec.Command(os.Args[0], "-test.run=TestMainExecutesRun", "--", coverProfileFlag, profilePath, "-min", "50")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Total coverage: 100.0% (required: >= 50%)") {
		t.Fatalf("unexpected helper output: %q", string(output))
	}
}

func TestExitErrHandlesStderrWriteFailure(t *testing.T) {
	exitCode := exitErr(&failOnWriteWriter{failOn: 1, err: errors.New("stderr failed")}, errors.New("boom"))
	requireExitCodeTwo(t, exitCode)
}

func assertFileContains(t *testing.T, path, want string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(data), want) {
		t.Fatalf("%s missing %q in %q", path, want, string(data))
	}
}

func coverageArgs(profile string, extra ...string) []string {
	args := []string{coverProfileFlag, profile}
	return append(args, extra...)
}

func coverageOutputPaths(dir string) (string, string, string) {
	return filepath.Join(dir, totalOutName), filepath.Join(dir, packagesOutName), filepath.Join(dir, failuresOutName)
}

func requireExitCodeTwo(t *testing.T, exitCode int) {
	t.Helper()

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
}

type failOnWriteWriter struct {
	writes int
	failOn int
	err    error
}

func (w *failOnWriteWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failOn {
		return 0, w.err
	}
	return len(p), nil
}
