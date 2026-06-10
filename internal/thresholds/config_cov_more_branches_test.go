package thresholds

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestThresholdConfigPolicyTraceHelperBranches(t *testing.T) {
	if got := policyTraceFromMap(nil); len(got) != 0 {
		t.Fatalf("expected nil policy trace for empty map, got %#v", got)
	}

	baseTrace := map[string]string{
		"thresholds.fail_on_increase_percent": "defaults",
		"custom.z":                            "base",
	}
	higherTrace := map[string]string{
		"thresholds.fail_on_increase_percent": "repo",
		"custom.a":                            "pack",
		"custom.blank":                        " ",
	}
	merged := mergePolicyTrace(baseTrace, higherTrace)
	if merged["thresholds.fail_on_increase_percent"] != "repo" {
		t.Fatalf("expected higher trace source to win, got %#v", merged)
	}
	if _, ok := merged["custom.blank"]; ok {
		t.Fatalf("expected blank trace source to be ignored, got %#v", merged)
	}

	trace := policyTraceFromMap(merged)
	if len(trace) != 3 {
		t.Fatalf("expected known field plus sorted extras, got %#v", trace)
	}
	if trace[0].Field != "thresholds.fail_on_increase_percent" || trace[1].Field != "custom.a" || trace[2].Field != "custom.z" {
		t.Fatalf("unexpected policy trace ordering: %#v", trace)
	}
}

func TestThresholdConfigReadPolicyLocationBranches(t *testing.T) {
	localPolicy := filepath.Join(t.TempDir(), "policy.yml")
	policyData := []byte("thresholds:\n  fail_on_increase_percent: 3\n")
	if err := os.WriteFile(localPolicy, policyData, 0o600); err != nil {
		t.Fatalf("write local policy: %v", err)
	}
	if data, err := readPolicyLocation(localPolicy, packTrust{}, false); err != nil || string(data) != string(policyData) {
		t.Fatalf("expected untrusted local read to succeed, data=%q err=%v", data, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write(policyData); err != nil {
			t.Errorf("write remote policy response: %v", err)
		}
	}))
	defer server.Close()

	sum := sha256.Sum256(policyData)
	remotePolicy := server.URL + "/policy.yml#sha256=" + hex.EncodeToString(sum[:])
	if data, err := readPolicyLocation(remotePolicy, packTrust{}, true); err != nil || string(data) != string(policyData) {
		t.Fatalf("expected pinned remote policy read to succeed, data=%q err=%v", data, err)
	}
}

func TestThresholdConfigResolvePackErrorBranches(t *testing.T) {
	repo := t.TempDir()
	current := filepath.Join(repo, ".lopper.yml")
	resolver := newPackResolver(repo)

	if _, err := resolver.resolvePack(current, packTrust{localRoot: repo}, 0, " "); err == nil || !strings.Contains(err.Error(), "pack reference must not be empty") {
		t.Fatalf("expected empty pack reference error, got %v", err)
	}

	remotePack := "https://example.com/policy.yml#sha256=" + strings.Repeat("a", 64)
	if _, err := resolver.resolvePack(current, packTrust{localRoot: repo}, 1, remotePack); err == nil || !strings.Contains(err.Error(), "remote policy packs are disabled") {
		t.Fatalf("expected remote pack disabled error, got %v", err)
	}
}
