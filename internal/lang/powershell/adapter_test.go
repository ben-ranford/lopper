package powershell

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestPowerShellAdapterIdentityAndDetection(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFile(t, filepath.Join(repo, "Demo.psd1"), `@{
  RootModule = 'Demo.psm1'
  RequiredModules = @('Pester')
}
`)
	writePowerShellFile(t, filepath.Join(repo, "scripts", "run.ps1"), "Import-Module Pester\n")
	writePowerShellFile(t, filepath.Join(repo, "modules", "Demo.psm1"), "Import-Module Pester\n")

	adapter := NewAdapter()
	assertPowerShellAdapterIdentity(t, adapter)

	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	assertPowerShellDetectionResult(t, detection, err, repo)

	matched, err := adapter.Detect(context.Background(), repo)
	assertPowerShellDetectWrapper(t, matched, err)
}

func TestPowerShellAdapterAnalyseDependencyAndTopN(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFile(t, filepath.Join(repo, "Demo.psd1"), `@{
  RootModule = 'Demo.psm1'
  RequiredModules = @(
    'Pester',
    @{ ModuleName = 'Az.Accounts' }
  )
}
`)
	writePowerShellFile(t, filepath.Join(repo, "scripts", "run.ps1"), `#Requires -Modules @('Pester')
Import-Module -Name Pester
using module 'Az.Accounts'
Import-Module $dynamic
`)

	adapter := NewAdapter()
	depReport, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "pester",
	})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	assertPowerShellDependencyAnalysis(t, depReport)
	if !containsPowerShellWarning(depReport.Warnings, "dynamic import-module expression") {
		t.Fatalf("expected dynamic import warning, got %#v", depReport.Warnings)
	}

	topReport, err := adapter.Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 5})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}
	assertPowerShellTopDependencyNames(t, topReport.Dependencies, "pester", "az.accounts")
}

func TestPowerShellAdapterAnalyseNoFilesAndNoDeclarationsWarnings(t *testing.T) {
	repo := t.TempDir()
	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 5})
	if err != nil {
		t.Fatalf("analyse empty repo: %v", err)
	}
	if !containsPowerShellWarning(reportData.Warnings, "no PowerShell files found") {
		t.Fatalf("expected missing file warning, got %#v", reportData.Warnings)
	}
	if !containsPowerShellWarning(reportData.Warnings, "no PowerShell module declarations found") {
		t.Fatalf("expected missing declaration warning, got %#v", reportData.Warnings)
	}
}

func TestPowerShellReportHelpersCoverNilAndEmptyBranches(t *testing.T) {
	if got := sortedDependencyUnion(); len(got) != 0 {
		t.Fatalf("expected empty dependency union for empty maps, got %#v", got)
	}
	if got := buildPowerShellDependencyProvenance(powerShellDependencySource{}); got != nil {
		t.Fatalf("expected nil provenance for empty manifest source, got %#v", got)
	}

	scan := scanResult{
		DeclaredDependencies: map[string]struct{}{"pester": {}},
		DeclaredSources: map[string]powerShellDependencySource{
			"pester": {ManifestPaths: map[string]struct{}{"Demo.psd1": {}}},
		},
		ImportedDependencies: map[string]struct{}{},
		Files:                nil,
	}
	dep, warnings := buildDependencyReport("pester", scan)
	if dep.Name != "pester" {
		t.Fatalf("expected dependency name to be preserved, got %#v", dep)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "no static module usage") {
		t.Fatalf("expected no-usage warning for report shaping, got %#v", warnings)
	}
	if len(dep.Recommendations) != 1 || dep.Recommendations[0].Code != "remove-unused-module" {
		t.Fatalf("expected remove-unused-module recommendation, got %#v", dep.Recommendations)
	}

	if weights := resolveRemovalCandidateWeights(nil); weights != report.DefaultRemovalCandidateWeights() {
		t.Fatalf("expected default removal-candidate weights, got %#v", weights)
	}
}

func writePowerShellFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertPowerShellAdapterIdentity(t *testing.T, adapter *Adapter) {
	t.Helper()
	if got := adapter.ID(); got != adapterID {
		t.Fatalf("PowerShell adapter id = %q, want %q", got, adapterID)
	}
	if got := adapter.Aliases(); !slices.Equal(got, []string{"ps", "pwsh"}) {
		t.Fatalf("PowerShell aliases = %#v, want ps/pwsh", got)
	}
}

func assertPowerShellDetectionResult(t *testing.T, detection language.Detection, err error, repo string) {
	t.Helper()
	if err != nil {
		t.Fatalf("PowerShell DetectWithConfidence returned error: %v", err)
	}
	switch {
	case !detection.Matched:
		t.Fatalf("PowerShell repo was not detected")
	case detection.Confidence < 35:
		t.Fatalf("PowerShell detection confidence = %d, want at least 35", detection.Confidence)
	case len(detection.Roots) == 0:
		t.Fatalf("PowerShell detection returned no roots")
	case !slices.Contains(detection.Roots, repo):
		t.Fatalf("PowerShell detection roots = %#v, want repo root %q", detection.Roots, repo)
	}
}

func assertPowerShellDetectWrapper(t *testing.T, matched bool, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("PowerShell Detect returned error: %v", err)
	}
	if !matched {
		t.Fatalf("PowerShell Detect returned false")
	}
}

func assertPowerShellDependencyAnalysis(t *testing.T, depReport report.Report) {
	t.Helper()
	if len(depReport.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(depReport.Dependencies))
	}

	dependency := depReport.Dependencies[0]
	if dependency.Language != adapterID {
		t.Fatalf("expected dependency language %q, got %#v", adapterID, dependency)
	}
	if dependency.Name != "pester" {
		t.Fatalf("expected dependency name pester, got %#v", dependency)
	}
	if dependency.UsedExportsCount == 0 {
		t.Fatalf("expected static usage attribution from Import-Module and #Requires, got %#v", dependency)
	}
	assertPowerShellManifestProvenance(t, dependency)
	assertPowerShellImportProvenance(t, dependency)
}

func assertPowerShellManifestProvenance(t *testing.T, dependency report.DependencyReport) {
	t.Helper()
	if dependency.Provenance == nil || dependency.Provenance.Source != dependencySourceManifest {
		t.Fatalf("expected manifest provenance on dependency, got %#v", dependency.Provenance)
	}
	if len(dependency.Provenance.Signals) == 0 || dependency.Provenance.Signals[0] != "Demo.psd1" {
		t.Fatalf("expected manifest signal in provenance, got %#v", dependency.Provenance)
	}
}

func assertPowerShellImportProvenance(t *testing.T, dependency report.DependencyReport) {
	t.Helper()
	if len(dependency.UsedImports) == 0 {
		t.Fatalf("expected used imports for pester, got %#v", dependency)
	}

	importProvenance := strings.Join(dependency.UsedImports[0].Provenance, ",")
	for _, expected := range []string{usageSourceImportModule, usageSourceRequiresModule} {
		if !strings.Contains(importProvenance, expected) {
			t.Fatalf("expected import provenance to include %q, got %#v", expected, dependency.UsedImports)
		}
	}
}

func assertPowerShellTopDependencyNames(t *testing.T, dependencies []report.DependencyReport, expectedNames ...string) {
	t.Helper()
	for _, expected := range expectedNames {
		if slices.ContainsFunc(dependencies, func(dep report.DependencyReport) bool {
			return dep.Name == expected
		}) {
			continue
		}
		t.Fatalf("expected %q in top report dependencies, got %#v", expected, powerShellDependencyNames(dependencies))
	}
}

func powerShellDependencyNames(dependencies []report.DependencyReport) []string {
	names := make([]string, 0, len(dependencies))
	for _, dep := range dependencies {
		names = append(names, dep.Name)
	}
	return names
}

func containsPowerShellWarning(warnings []string, fragment string) bool {
	fragment = strings.ToLower(strings.TrimSpace(fragment))
	for _, warning := range warnings {
		if strings.Contains(strings.ToLower(warning), fragment) {
			return true
		}
	}
	return false
}
