package powershell

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestDetectWithConfidenceCollectsRootAndWalkSignals(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFile(t, filepath.Join(repo, "Module.psd1"), "@{ RequiredModules = @('Pester') }\n")
	writePowerShellFile(t, filepath.Join(repo, "scripts", "run.ps1"), "Import-Module Pester\n")
	writePowerShellFile(t, filepath.Join(repo, "modules", "Demo.psm1"), "Import-Module Pester\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected detection match for powershell fixture")
	}
	if detection.Confidence < 35 {
		t.Fatalf("expected shared confidence floor, got %#v", detection)
	}
	if len(detection.Roots) == 0 || !slices.Contains(detection.Roots, repo) {
		t.Fatalf("expected detection roots to include repo root, got %#v", detection.Roots)
	}
}

func TestDetectWithConfidenceSkipsBaselineIgnoredDirectories(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFile(t, filepath.Join(repo, "node_modules", "ignored.ps1"), "Import-Module Pester\n")
	writePowerShellFile(t, filepath.Join(repo, "vendor", "ignored.psm1"), "Import-Module Pester\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if detection.Matched {
		t.Fatalf("expected ignored dirs to prevent detection match, got %#v", detection)
	}
}

func TestApplyRootSignalsErrorsForInvalidRepoPath(t *testing.T) {
	repoFile := filepath.Join(t.TempDir(), "repo-file")
	if err := os.WriteFile(repoFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write repo file: %v", err)
	}
	roots := map[string]struct{}{}
	detection := language.Detection{}
	if err := applyRootSignals(repoFile, &detection, roots); err == nil {
		t.Fatalf("expected applyRootSignals to fail when repo path is not a directory")
	}
}

func TestShouldSkipPowerShellDirAndSourceHelpers(t *testing.T) {
	if !shouldSkipPowerShellDir("node_modules") {
		t.Fatalf("expected node_modules to be skipped")
	}
	if shouldSkipPowerShellDir("src") {
		t.Fatalf("expected src to not be skipped")
	}
	if !isPowerShellSource(".ps1") || !isPowerShellSource(".psm1") || !isPowerShellSource(".psd1") {
		t.Fatalf("expected powershell source extensions to match")
	}
	if isPowerShellSource(".txt") {
		t.Fatalf("expected non-powershell extension to be ignored")
	}
}

func TestScanRepoParsesManifestAndImportsAndSkipsLargeFiles(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFile(t, filepath.Join(repo, "Module.psd1"), `@{
  RequiredModules = @(
    'Pester',
    @{ ModuleName = 'Az.Accounts' }
  )
}
`)
	writePowerShellFile(t, filepath.Join(repo, "scripts", "run.ps1"), `#Requires -Modules @('Pester')
Import-Module Pester
using module 'Az.Accounts'
Import-Module $dynamic
`)

	large := strings.Repeat("# filler\n", maxScannablePowerShellBytes/8)
	writePowerShellFile(t, filepath.Join(repo, "scripts", "large.ps1"), large)

	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if _, ok := scan.DeclaredDependencies["pester"]; !ok {
		t.Fatalf("expected declared dependency pester, got %#v", scan.DeclaredDependencies)
	}
	if _, ok := scan.DeclaredDependencies["az.accounts"]; !ok {
		t.Fatalf("expected declared dependency az.accounts, got %#v", scan.DeclaredDependencies)
	}
	if _, ok := scan.ImportedDependencies["pester"]; !ok {
		t.Fatalf("expected imported dependency pester, got %#v", scan.ImportedDependencies)
	}
	if _, ok := scan.ImportedDependencies["az.accounts"]; !ok {
		t.Fatalf("expected imported dependency az.accounts, got %#v", scan.ImportedDependencies)
	}
	if len(scan.Files) == 0 {
		t.Fatalf("expected scanned files with usage attribution")
	}
	if !containsPowerShellWarning(scan.Warnings, "skipped large PowerShell file") {
		t.Fatalf("expected large-file warning, got %#v", scan.Warnings)
	}
	if !containsPowerShellWarning(scan.Warnings, "dynamic import-module expression") {
		t.Fatalf("expected dynamic import warning, got %#v", scan.Warnings)
	}
}

func TestScanRepoWarnsWhenNoDeclarationsExist(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFile(t, filepath.Join(repo, "run.ps1"), "Import-Module Pester\n")

	scan, err := scanRepo(context.Background(), repo)
	if err != nil {
		t.Fatalf("scan repo without manifests: %v", err)
	}
	if !containsPowerShellWarning(scan.Warnings, "no PowerShell module declarations found") {
		t.Fatalf("expected declaration warning, got %#v", scan.Warnings)
	}
}
