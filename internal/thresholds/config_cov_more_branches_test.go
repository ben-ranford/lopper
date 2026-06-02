package thresholds

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

const packAPolicySource = "pack-a"

func TestThresholdConfigRejectsInvalidRepoPath(t *testing.T) {
	if _, err := LoadWithPolicy("\x00", ""); err == nil {
		t.Fatalf("expected LoadWithPolicy to reject invalid repo path")
	}
}

func TestThresholdConfigNormalizeBranches(t *testing.T) {
	if got := normalizePathPatterns([]string{" ", "\t"}); len(got) != 0 {
		t.Fatalf("expected blank path patterns to normalize to empty, got %#v", got)
	}
	if got := normalizePathPatterns(make([]string, 0)); len(got) != 0 {
		t.Fatalf("expected explicit empty path patterns to remain empty, got %#v", got)
	}
}

func TestThresholdConfigMergeBranches(t *testing.T) {
	maxUncertain := 2
	merged := mergeOverrides(Overrides{}, Overrides{MaxUncertainImportCount: &maxUncertain})
	if merged.MaxUncertainImportCount == nil || *merged.MaxUncertainImportCount != 2 {
		t.Fatalf("expected max_uncertain_import_count override to merge, got %#v", merged)
	}

	merged = mergeOverrides(Overrides{LicenseDenyList: []string{"GPL-3.0-ONLY"}, licenseDenyListSet: true}, Overrides{LicenseDenyList: make([]string, 0), licenseDenyListSet: true})
	if len(merged.LicenseDenyList) != 0 {
		t.Fatalf("expected explicit empty deny list to clear inherited deny list, got %#v", merged.LicenseDenyList)
	}

	scope := mergeScope(PathScope{Include: []string{"src/**"}, Exclude: []string{"vendor/**"}, includeSet: true, excludeSet: true}, PathScope{Include: make([]string, 0), Exclude: make([]string, 0), includeSet: true, excludeSet: true})
	if len(scope.Include) != 0 || len(scope.Exclude) != 0 {
		t.Fatalf("expected explicit empty scope lists to clear inherited scope, got %#v", scope)
	}

	features := mergeFeatures(FeatureConfig{Enable: []string{"alpha"}, Disable: []string{"beta"}, enableSet: true, disableSet: true}, FeatureConfig{Enable: make([]string, 0), Disable: make([]string, 0), enableSet: true, disableSet: true})
	if len(features.Enable) != 0 || len(features.Disable) != 0 {
		t.Fatalf("expected explicit empty feature lists to clear inherited features, got %#v", features)
	}
}

func TestThresholdConfigPolicySourcesAndRemoteValidation(t *testing.T) {
	sources := (&resolveMergeResult{appliedSourcesLow: []string{packAPolicySource, defaultPolicySource, packAPolicySource}}).policySourcesHighToLow()
	if !reflect.DeepEqual(sources, []string{packAPolicySource, defaultPolicySource}) {
		t.Fatalf("unexpected policy source ordering: %#v", sources)
	}

	if _, _, err := canonicalPolicyLocation("https://example.com/policy.yml#bad-pin"); err == nil {
		t.Fatalf("expected canonicalPolicyLocation to reject invalid remote pin")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	location := server.URL + "/policy.yml#sha256=" + strings.Repeat("a", 64)
	if _, err := readRemotePolicyFile(location); err == nil || !strings.Contains(err.Error(), "unexpected status") {
		t.Fatalf("expected remote status error, got %v", err)
	}
}

func TestThresholdConfigRootContainment(t *testing.T) {
	if isPathUnderRoot("\x00", filepath.Join(t.TempDir(), "policy.yml")) {
		t.Fatalf("expected invalid root path to fail root containment check")
	}
}

func TestThresholdConfigPolicyTraceTracksMergeSources(t *testing.T) {
	repoDir := t.TempDir()
	packsDir := filepath.Join(repoDir, "packs")
	if err := os.MkdirAll(packsDir, 0o750); err != nil {
		t.Fatalf("mkdir packs dir: %v", err)
	}

	basePack := filepath.Join(packsDir, "base.yml")
	if err := os.WriteFile(basePack, []byte("thresholds:\n  fail_on_increase_percent: 7\n  removal_candidate_weight_usage: 0.6\n"), 0o600); err != nil {
		t.Fatalf("write base pack: %v", err)
	}

	overlayPack := filepath.Join(packsDir, "overlay.yml")
	if err := os.WriteFile(overlayPack, []byte("thresholds:\n  removal_candidate_weight_usage: 0.7\n  license_fail_on_deny: true\n"), 0o600); err != nil {
		t.Fatalf("write overlay pack: %v", err)
	}

	configPath := filepath.Join(repoDir, ".lopper.yml")
	config := "policy:\n  packs:\n    - ./packs/base.yml\n    - ./packs/overlay.yml\nthresholds:\n  fail_on_increase_percent: 11\n  license_include_registry_provenance: true\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write repo config: %v", err)
	}

	result, err := LoadWithPolicy(repoDir, "")
	if err != nil {
		t.Fatalf("load config with policy trace: %v", err)
	}
	if got := policyTraceSource(result.PolicyTrace, "thresholds.fail_on_increase_percent"); got != configPath {
		t.Fatalf("expected repo config to supply fail_on_increase_percent, got %q", got)
	}
	if got := policyTraceSource(result.PolicyTrace, "removal_candidate_weights.usage"); got != overlayPack {
		t.Fatalf("expected overlay pack to supply removal_candidate_weights.usage, got %q", got)
	}
	if got := policyTraceSource(result.PolicyTrace, "license.fail_on_deny"); got != overlayPack {
		t.Fatalf("expected overlay pack to supply license.fail_on_deny, got %q", got)
	}
	if got := policyTraceSource(result.PolicyTrace, "license.include_registry_provenance"); got != configPath {
		t.Fatalf("expected repo config to supply license.include_registry_provenance, got %q", got)
	}
}

func policyTraceSource(trace []report.PolicyMergeTrace, field string) string {
	for _, item := range trace {
		if item.Field == field {
			return item.Source
		}
	}
	return ""
}
