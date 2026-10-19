package scripts

import (
	"regexp"
	"slices"
	"testing"
)

func TestRenovateTracksIndependentMarketplaceExpectation(t *testing.T) {
	t.Parallel()
	var config struct {
		CustomManagers []struct {
			ManagerFilePatterns []string `json:"managerFilePatterns"`
			MatchStrings        []string `json:"matchStrings"`
			CustomType          string   `json:"customType"`
			DatasourceTemplate  string   `json:"datasourceTemplate"`
			DepNameTemplate     string   `json:"depNameTemplate"`
			VersioningTemplate  string   `json:"versioningTemplate"`
		} `json:"customManagers"`
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
