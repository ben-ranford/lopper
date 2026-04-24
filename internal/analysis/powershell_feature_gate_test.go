package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
)

const powerShellFeatureName = "powershell-adapter-preview"

func TestServicePowerShellFeatureGateCanBeDisabled(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFixtureRepo(t, repo)

	service := NewService()
	_, err := service.Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: "powershell",
		TopN:     1,
		Features: mustResolvePowerShellFeatureSet(t, false),
	})
	if !errors.Is(err, language.ErrNoMatch) {
		t.Fatalf("expected ErrNoMatch when powershell feature flag is disabled, got %v", err)
	}
}

func TestServicePowerShellFeatureGateEnabled(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFixtureRepo(t, repo)

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath:  repo,
		Language:  "powershell",
		TopN:      5,
		Features:  mustResolvePowerShellFeatureSet(t, true),
		ScopeMode: ScopeModePackage,
	})
	if err != nil {
		t.Fatalf("analyse powershell with feature enabled: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected powershell dependencies when feature flag is enabled")
	}
	if reportData.Dependencies[0].Language != "powershell" {
		t.Fatalf("expected powershell dependency language, got %#v", reportData.Dependencies)
	}
}

func TestServiceAutoSkipsDisabledPowerShellAndFallsBack(t *testing.T) {
	repo := t.TempDir()
	writePowerShellFixtureRepo(t, repo)
	writeFile(t, filepath.Join(repo, packageJSONFileName), demoPackageJSONContent)
	writeFile(t, filepath.Join(repo, indexJSFileName), lodashMapUsageJS)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", packageJSONFileName), nodeMainPackageJSON)
	writeFile(t, filepath.Join(repo, "node_modules", "lodash", indexJSFileName), mapExportJSContent)

	service := NewService()
	reportData, err := service.Analyse(context.Background(), Request{
		RepoPath: repo,
		Language: "auto",
		TopN:     5,
		Features: mustResolvePowerShellFeatureSet(t, false),
	})
	if err != nil {
		t.Fatalf("analyse auto with disabled powershell: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies from fallback adapter")
	}
	languages := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		languages = append(languages, dep.Language)
	}
	if !slices.Contains(languages, "js-ts") {
		t.Fatalf("expected js-ts dependency rows, got %#v", reportData.Dependencies)
	}
	if slices.Contains(languages, "powershell") {
		t.Fatalf("did not expect powershell rows with feature disabled, got %#v", reportData.Dependencies)
	}
}

func writePowerShellFixtureRepo(t *testing.T, repo string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, "DemoModule.psd1"), `@{
	RootModule = "DemoModule.psm1"
	RequiredModules = @(
	  "Pester",
	  @{ ModuleName = "Az.Accounts" }
	)
}
`)
	writeFile(t, filepath.Join(repo, "script.ps1"), "Import-Module Pester\nImport-Module Az.Accounts\n")
}

func mustResolvePowerShellFeatureSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()
	flag := mustLookupPowerShellPreviewFlag(t)
	options := featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
	}
	if !enabled {
		options.Disable = []string{flag.Code}
	}
	set, err := featureflags.DefaultRegistry().Resolve(options)
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return set
}

func mustLookupPowerShellPreviewFlag(t *testing.T) featureflags.Flag {
	t.Helper()
	if err := featureflags.ValidateDefaultRegistry(); err != nil {
		t.Fatalf("validate default registry: %v", err)
	}
	flag, ok := featureflags.DefaultRegistry().Lookup(powerShellFeatureName)
	if !ok {
		t.Fatalf("expected shipped feature registry to include %q", powerShellFeatureName)
	}
	return flag
}
