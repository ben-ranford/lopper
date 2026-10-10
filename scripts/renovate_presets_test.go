package scripts

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRenovateDoesNotInheritPresets(t *testing.T) {
	t.Parallel()

	var config any
	readJSONConfig(t, "renovate.json", &config)
	if err := validateRenovatePresetInheritance(config, "renovate"); err != nil {
		t.Fatal(err)
	}
}

// Hosted Renovate is not version-pinned. Inspecting the local JSON cannot prove
// the review policy of mutable transitive presets, so all settings stay local.
func validateRenovatePresetInheritance(value any, path string) error {
	switch node := value.(type) {
	case map[string]any:
		if _, exists := node["extends"]; exists {
			return fmt.Errorf("%s must not use extends; keep reviewable settings in renovate.json", path)
		}
		for key, child := range node {
			if err := validateRenovatePresetInheritance(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range node {
			if err := validateRenovatePresetInheritance(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestRenovatePresetInheritance(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		config   string
		wantPath string
	}{
		{name: "local settings", config: `{"dependencyDashboard":true,"packageRules":[{"matchPackageNames":["*"],"automerge":false}]}`},
		{name: "top level transitive preset", config: `{"extends":["config:recommended"],"automerge":false}`, wantPath: "renovate"},
		{name: "top level update override preset", config: `{"extends":[":automergeMinor"]}`, wantPath: "renovate"},
		{name: "empty inheritance", config: `{"extends":[]}`, wantPath: "renovate"},
		{name: "null inheritance", config: `{"extends":null}`, wantPath: "renovate"},
		{name: "update type preset", config: `{"minor":{"extends":[":automergeAll"]}}`, wantPath: "renovate.minor"},
		{name: "package rule preset", config: `{"packageRules":[{"extends":[":automergeAll"]}]}`, wantPath: "renovate.packageRules[0]"},
		{name: "nested update preset", config: `{"packageRules":[{"minor":{"extends":[":automergeAll"]}}]}`, wantPath: "renovate.packageRules[0].minor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config any
			if err := json.Unmarshal([]byte(tc.config), &config); err != nil {
				t.Fatal(err)
			}
			err := validateRenovatePresetInheritance(config, "renovate")
			if tc.wantPath == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), tc.wantPath+" must not use extends;") {
				t.Fatalf("preset inheritance = %v, want rejection at %s", err, tc.wantPath)
			}
		})
	}
}

// This is a repository policy guard, not a substitute for Renovate's schema or
// inspection of external hosted configuration (which may apply force last).
func validateRenovateReviewContexts(value any, path string) error {
	if err := validateRenovatePresetInheritance(value, path); err != nil {
		return err
	}
	switch node := value.(type) {
	case map[string]any:
		return validateRenovateReviewObject(node, path)
	case []any:
		for index, child := range node {
			if err := validateRenovateReviewContexts(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateRenovateReviewObject(node map[string]any, path string) error {
	for key, child := range node {
		if err := validateRenovateReviewSetting(key, child, path); err != nil {
			return err
		}
		if err := validateRenovateReviewContexts(child, path+"."+key); err != nil {
			return err
		}
	}
	return nil
}

func validateRenovateReviewSetting(key string, value any, path string) error {
	switch key {
	case "force", "postUpgradeTasks":
		return fmt.Errorf("%s.%s must not bypass repository review policy", path, key)
	case "automerge", "platformAutomerge", "autoApprove", "enabled":
		setting, ok := value.(bool)
		if !ok || setting != (key == "enabled") {
			return fmt.Errorf("%s.%s must be boolean %t", path, key, key == "enabled")
		}
	}
	return nil
}

func TestRenovateRawReviewPolicy(t *testing.T) {
	t.Parallel()
	var config any
	readJSONConfig(t, "renovate.json", &config)
	if err := validateRenovateReviewContexts(config, "renovate"); err != nil {
		t.Fatal(err)
	}
}

func TestRenovateReviewContextOverrides(t *testing.T) {
	t.Parallel()
	contexts := append([]string{"npm", "custom", "packageRules", "nested"}, renovateUpdateTypeBlocks...)
	contexts = append([]string{"root"}, contexts...)
	for _, context := range contexts {
		t.Run(context, func(t *testing.T) {
			for _, bad := range []string{
				`{"automerge":null}`, `{"automerge":"false"}`, `{"automerge":0}`, `{"automerge":true}`,
				`{"platformAutomerge":true}`, `{"platformAutomerge":null}`, `{"platformAutomerge":"false"}`,
				`{"autoApprove":true}`, `{"autoApprove":null}`, `{"autoApprove":"false"}`,
				`{"enabled":false}`, `{"enabled":null}`, `{"enabled":"true"}`,
				`{"force":{"automerge":true}}`, `{"force":{}}`, `{"force":null}`,
				`{"postUpgradeTasks":{"commands":["anything"]}}`, `{"extends":null}`, `{"extends":[]}`, `{"extends":[":automergeAll"]}`,
			} {
				var child any
				if err := json.Unmarshal([]byte(bad), &child); err != nil {
					t.Fatal(err)
				}
				value := renovateReviewContextFixture(context, child)
				if err := validateRenovateReviewContexts(value, "renovate"); err == nil {
					t.Fatalf("accepted %s in %s", bad, context)
				}
			}
		})
	}
	var safe any
	if err := json.Unmarshal([]byte(`{"automerge":false,"platformAutomerge":false,"npm":{"enabled":true},"minor":{"automerge":false},"packageRules":[{"autoApprove":false}]}`), &safe); err != nil {
		t.Fatal(err)
	}
	if err := validateRenovateReviewContexts(safe, "renovate"); err != nil {
		t.Fatal(err)
	}
}

func renovateReviewContextFixture(context string, child any) any {
	switch context {
	case "root":
		return child
	case "packageRules":
		return map[string]any{context: []any{child}}
	case "nested":
		return map[string]any{"packageRules": []any{map[string]any{"minor": child}}}
	default:
		return map[string]any{context: child}
	}
}

func TestRenovateExplicitRecommendedPolicy(t *testing.T) {
	t.Parallel()
	var config map[string]any
	readJSONConfig(t, "renovate.json", &config)
	for key, want := range map[string]any{"dependencyDashboard": true, "semanticCommits": "enabled", "semanticCommitScope": "deps", "automerge": false, "platformAutomerge": false} {
		if config[key] != want {
			t.Errorf("%s = %v, want %v", key, config[key], want)
		}
	}
	if _, exists := config["dependencyDashboardApproval"]; exists {
		t.Fatal("dashboard must not gate update creation")
	}
}

func TestRenovateReviewedRules(t *testing.T) {
	t.Parallel()
	var config struct {
		PackageRules []map[string]any `json:"packageRules"`
	}
	readJSONConfig(t, "renovate.json", &config)
	for _, raw := range []string{
		"{\"matchPackageNames\":[\"*\"],\"semanticCommitType\":\"chore\"}",
		"{\"matchDepTypes\":[\"dependencies\",\"require\"],\"semanticCommitType\":\"fix\"}",
		"{\"groupName\":\"GitHub Artifact Actions\",\"matchManagers\":[\"github-actions\"],\"matchPackageNames\":[\"actions/download-artifact\",\"actions/upload-artifact\"],\"matchUpdateTypes\":[\"major\"]}",
		"{\"matchCurrentVersion\":\">=4\",\"matchDatasources\":[\"github-tags\"],\"matchPackageNames\":[\"actions/attest-build-provenance\"],\"replacementName\":\"actions/attest\"}",
		"{\"matchDatasources\":[\"github-tags\"],\"matchPackageNames\":[\"google-github-actions/release-please-action\"],\"replacementName\":\"googleapis/release-please-action\"}",
		"{\"matchManagers\":[\"npm\"],\"matchPackageNames\":[\"@types/node\"],\"versioning\":\"node\"}",
		"{\"changelogUrl\":\"{{sourceUrl}}/compare/{{currentDigest}}..{{newDigest}}\",\"matchDatasources\":[\"github-digest\",\"github-releases\",\"github-tags\"],\"matchUpdateTypes\":[\"digest\"]}",
		"{\"changelogUrl\":\"{{sourceUrl}}/compare/{{currentDigest}}..{{newDigest}}\",\"matchDatasources\":[\"git-refs\",\"git-tags\"],\"matchJsonata\":[\"$detectPlatform(sourceUrl) = 'github'\"],\"matchUpdateTypes\":[\"digest\"]}",
		"{\"matchManagers\":[\"gomod\"],\"matchUpdateTypes\":[\"major\",\"minor\",\"patch\"],\"prBodyDefinitions\":{\"Change\":\"{{#if (containsString depName 'golang.org/x/')}}[`{{{displayFrom}}}` → `{{{displayTo}}}`](https://cs.opensource.google/{{{replace '^golang\\\\.org' 'go' depName}}}/+/refs/tags/{{{currentValue}}}...refs/tags/{{{newValue}}}){{else}}`{{{displayFrom}}}` → `{{{displayTo}}}`{{/if}}\"}}",
		"{\"matchManagers\":[\"gomod\"],\"prBodyDefinitions\":{\"Package\":\"{{#if (containsString depName 'golang.org/x/')}}[{{{depName}}}](https://pkg.go.dev/{{{depName}}}){{else}}{{{depNameLinked}}}{{/if}}\"}}",
	} {
		var want map[string]any
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, rule := range config.PackageRules {
			if reflect.DeepEqual(rule, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing exact reviewed rule %s", raw)
		}
	}
}
