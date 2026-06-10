package thresholds

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

func TestThresholdConfigAdditionalPackAndFeatureBranches(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	resolver := newPackResolver(repo)

	if got := resolver.nestedPackTrust(filepath.Join(repo, "policy.yml")).localRoot; got != repo {
		t.Fatalf("expected nested repo policy trust root %q, got %q", repo, got)
	}
	if got := resolver.nestedPackTrust(filepath.Join(outside, "policy.yml")).localRoot; got != "" {
		t.Fatalf("expected outside policy to have no local trust root, got %q", got)
	}
	if err := validatePackBoundary(filepath.Join(outside, "policy.yml"), true, packTrust{localRoot: repo}); err != nil {
		t.Fatalf("expected remote pack boundary to skip local root check: %v", err)
	}
	if err := validatePackBoundary(filepath.Join(outside, "policy.yml"), false, packTrust{}); err != nil {
		t.Fatalf("expected empty trust root to skip local boundary check: %v", err)
	}

	policyPath := filepath.Join(repo, "policy.yml")
	if err := os.WriteFile(policyPath, []byte("fail_on_increase_percent: 1\n"), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	if data, err := readPolicyLocation(policyPath, packTrust{localRoot: repo}, false); err != nil || !strings.Contains(string(data), "fail_on_increase_percent") {
		t.Fatalf("expected trusted local policy read, data=%q err=%v", data, err)
	}
	if _, err := readPolicyLocation(policyPath, packTrust{localRoot: outside}, false); err == nil {
		t.Fatalf("expected local policy outside trust root to fail")
	}
	if _, err := readPolicyLocation(filepath.Join(outside, "missing.yml"), packTrust{}, false); err == nil {
		t.Fatalf("expected untrusted missing local policy read to fail")
	}

	var nilFeatures *rawFeatures
	if got := nilFeatures.toFeatureConfig(); len(got.Enable) != 0 || len(got.Disable) != 0 {
		t.Fatalf("expected nil raw features to normalize empty slices, got %#v", got)
	}
	enable := []string{" alpha ", "", "alpha"}
	disable := []string{" beta "}
	features := (&rawFeatures{Enable: &enable, Disable: &disable}).toFeatureConfig()
	if !reflect.DeepEqual(features.Enable, []string{"alpha"}) || !reflect.DeepEqual(features.Disable, []string{"beta"}) {
		t.Fatalf("unexpected normalized features: %#v", features)
	}
}
