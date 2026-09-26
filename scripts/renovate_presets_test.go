package scripts

import (
	"encoding/json"
	"fmt"
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
