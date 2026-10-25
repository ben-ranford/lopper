package scripts

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"testing"
)

type renovateMarketplaceManager struct {
	ManagerFilePatterns []string `json:"managerFilePatterns"`
	MatchStrings        []string `json:"matchStrings"`
	CustomType          string   `json:"customType"`
	DatasourceTemplate  string   `json:"datasourceTemplate"`
	DepNameTemplate     string   `json:"depNameTemplate"`
	VersioningTemplate  string   `json:"versioningTemplate"`
}

type marketplaceNPMManifest struct {
	DevDependencies map[string]string `json:"devDependencies"`
}

type marketplaceNPMLockfile struct {
	Packages map[string]struct {
		Version         string            `json:"version"`
		Integrity       string            `json:"integrity"`
		DevDependencies map[string]string `json:"devDependencies"`
	} `json:"packages"`
}

func TestRenovateTracksIndependentMarketplaceExpectation(t *testing.T) {
	t.Parallel()
	var config struct {
		CustomManagers []renovateMarketplaceManager `json:"customManagers"`
	}
	readJSONConfig(t, "renovate.json", &config)
	for _, manager := range config.CustomManagers {
		if !slices.Contains(manager.ManagerFilePatterns, `/^scripts/release_workflow_config_test\.go$/`) {
			continue
		}
		if len(manager.ManagerFilePatterns) != 1 || len(manager.MatchStrings) != 1 || manager.CustomType != "regex" || manager.DatasourceTemplate != "npm" || manager.DepNameTemplate != "@vscode/vsce" || manager.VersioningTemplate != "npm" {
			t.Fatal("Marketplace expectation must use one narrowly scoped npm regex manager")
		}
		re := regexp.MustCompile(manager.MatchStrings[0])
		source := readConfig(t, "scripts/release_workflow_config_test.go")
		matches := re.FindAllStringSubmatch(source, -1)
		if len(matches) != 1 || re.SubexpIndex("currentValue") < 0 {
			t.Fatalf("Marketplace expectation matches = %v, want exactly one currentValue", matches)
		}
		for _, unrelated := range []string{
			`const unrelatedVersion = "4.0.0"`,
			`const expectedMarketplaceVSCEVersionFixture = "4.0.0"`,
			`// const expectedMarketplaceVSCEVersion = "4.0.0"`,
			`const expectedMarketplaceVSCEVersion = "4.0.0-invalid"`,
		} {
			if re.MatchString(unrelated) {
				t.Fatalf("Marketplace matcher selected unrelated assertion %q", unrelated)
			}
		}
		return
	}
	t.Fatal("Renovate does not track the independent Marketplace version expectation")
}

func TestMarketplaceToolingPinRejectsMismatchAndInvalidIntegrity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, version, integrity string
		valid                    bool
	}{
		{"coordinated current pin", expectedMarketplaceVSCEVersion, "sha512-bound", true},
		{"mismatched version", "999.0.0", "sha512-bound", false},
		{"missing integrity", expectedMarketplaceVSCEVersion, "", false},
		{"wrong algorithm", expectedMarketplaceVSCEVersion, "sha256-bound", false},
		{"empty digest", expectedMarketplaceVSCEVersion, "sha512-", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateMarketplaceToolingPin(tc.version, tc.integrity, expectedMarketplaceVSCEVersion)
			if (err == nil) != tc.valid {
				t.Fatalf("pin validation = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

// This exercises the configured captures and pin validation with a simulated
// update; it does not run Renovate or regenerate a lockfile from the registry.
func TestRenovateCoordinatedVSCEMajorUpdate(t *testing.T) {
	t.Parallel()
	var config struct {
		CustomManagers []renovateMarketplaceManager `json:"customManagers"`
	}
	readJSONConfig(t, "renovate.json", &config)

	workflowManager := marketplaceManagerFor(t, config.CustomManagers, `/^\.github/workflows/release\.yml$/`)
	expectationManager := marketplaceManagerFor(t, config.CustomManagers, `/^scripts/release_workflow_config_test\.go$/`)
	workflow := readConfig(t, ".github/workflows/release.yml")
	expectation := readConfig(t, "scripts/release_workflow_config_test.go")
	major := regexp.MustCompile(`^(\d+)\.`).FindStringSubmatch(expectedMarketplaceVSCEVersion)
	if len(major) != 2 {
		t.Fatalf("expected Marketplace version %q has no major component", expectedMarketplaceVSCEVersion)
	}
	var currentMajor int
	if _, err := fmt.Sscanf(major[1], "%d", &currentMajor); err != nil {
		t.Fatal(err)
	}
	nextMajor := fmt.Sprintf("%d.0.0", currentMajor+1)

	updatedWorkflow := rewriteRenovateCaptures(t, workflow, workflowManager.MatchStrings, expectedMarketplaceVSCEVersion, nextMajor)
	updatedExpectation := rewriteRenovateCaptures(t, expectation, expectationManager.MatchStrings, expectedMarketplaceVSCEVersion, nextMajor)
	assertRenovateCaptures(t, updatedWorkflow, workflowManager.MatchStrings, nextMajor, 4)
	assertRenovateCaptures(t, updatedExpectation, expectationManager.MatchStrings, nextMajor, 1)

	manifest, lockfile := readMarketplaceNPMSnapshots(t)
	manifest.DevDependencies["@vscode/vsce"] = "^" + nextMajor
	rootPackage := lockfile.Packages[""]
	rootPackage.DevDependencies["@vscode/vsce"] = "^" + nextMajor
	lockfile.Packages[""] = rootPackage
	vscePackage := lockfile.Packages["node_modules/@vscode/vsce"]
	vscePackage.Version = nextMajor
	lockfile.Packages["node_modules/@vscode/vsce"] = vscePackage
	for name, got := range map[string]string{
		"manifest":      manifest.DevDependencies["@vscode/vsce"],
		"lockfile root": lockfile.Packages[""].DevDependencies["@vscode/vsce"],
	} {
		if want := "^" + nextMajor; got != want {
			t.Fatalf("updated %s VSCE range = %q, want %q", name, got, want)
		}
	}
	if got := lockfile.Packages["node_modules/@vscode/vsce"].Version; got != nextMajor {
		t.Fatalf("updated VSCE lock version = %q, want %q", got, nextMajor)
	}
	if err := validateMarketplaceToolingPin(vscePackage.Version, vscePackage.Integrity, nextMajor); err != nil {
		t.Fatalf("coordinated VSCE major update failed strict lock validation: %v", err)
	}
	if err := validateMarketplaceToolingPin(vscePackage.Version, vscePackage.Integrity, expectedMarketplaceVSCEVersion); err == nil {
		t.Fatal("old independent expectation accepted coordinated major update")
	}
	if err := validateMarketplaceToolingPin(expectedMarketplaceVSCEVersion, vscePackage.Integrity, nextMajor); err == nil {
		t.Fatal("old locked VSCE version accepted coordinated major update")
	}
}

func marketplaceManagerFor(t *testing.T, managers []renovateMarketplaceManager, filePattern string) renovateMarketplaceManager {
	t.Helper()
	for _, manager := range managers {
		if slices.Contains(manager.ManagerFilePatterns, filePattern) {
			if manager.CustomType != "regex" || manager.DatasourceTemplate != "npm" || manager.DepNameTemplate != "@vscode/vsce" || manager.VersioningTemplate != "npm" {
				t.Fatalf("Marketplace manager for %s has unexpected configuration: %#v", filePattern, manager)
			}
			return manager
		}
	}
	t.Fatalf("no Marketplace manager targets %s", filePattern)
	return renovateMarketplaceManager{}
}

func rewriteRenovateCaptures(t *testing.T, source string, patterns []string, oldVersion, newVersion string) string {
	t.Helper()
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatchIndex(source, -1)
		capture := re.SubexpIndex("currentValue")
		if len(matches) != 1 || capture < 1 {
			t.Fatalf("Renovate pattern %q found %d captures, want one currentValue", pattern, len(matches))
		}
		start, end := matches[0][capture*2], matches[0][capture*2+1]
		if got := source[start:end]; got != oldVersion {
			t.Fatalf("Renovate pattern %q captured %q, want %q", pattern, got, oldVersion)
		}
		source = source[:start] + newVersion + source[end:]
	}
	return source
}

func assertRenovateCaptures(t *testing.T, source string, patterns []string, want string, wantCount int) {
	t.Helper()
	count := 0
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		matches := re.FindAllStringSubmatch(source, -1)
		if len(matches) != 1 {
			t.Fatalf("Renovate pattern %q matched %d entries, want one", pattern, len(matches))
		}
		capture := re.SubexpIndex("currentValue")
		if capture < 1 || matches[0][capture] != want {
			t.Fatalf("Renovate pattern %q captured %v, want %q", pattern, matches[0], want)
		}
		count += len(matches)
	}
	if count != wantCount {
		t.Fatalf("Renovate captured %d VSCE pins, want %d", count, wantCount)
	}
}

func readMarketplaceNPMSnapshots(t *testing.T) (marketplaceNPMManifest, marketplaceNPMLockfile) {
	t.Helper()
	var manifest marketplaceNPMManifest
	if err := json.Unmarshal([]byte(readConfig(t, "extensions/vscode-lopper/package.json")), &manifest); err != nil {
		t.Fatal(err)
	}
	var lockfile marketplaceNPMLockfile
	if err := json.Unmarshal([]byte(readConfig(t, "extensions/vscode-lopper/package-lock.json")), &lockfile); err != nil {
		t.Fatal(err)
	}
	if manifest.DevDependencies["@vscode/vsce"] == "" || lockfile.Packages[""].DevDependencies["@vscode/vsce"] == "" {
		t.Fatal("VSCE must be present in manifest and lockfile root devDependencies")
	}
	vsce, ok := lockfile.Packages["node_modules/@vscode/vsce"]
	if !ok || vsce.Version != expectedMarketplaceVSCEVersion {
		t.Fatalf("VSCE lock entry = %#v, want current version %s", vsce, expectedMarketplaceVSCEVersion)
	}
	if err := validateMarketplaceToolingPin(vsce.Version, vsce.Integrity, expectedMarketplaceVSCEVersion); err != nil {
		t.Fatal(err)
	}
	return manifest, lockfile
}
