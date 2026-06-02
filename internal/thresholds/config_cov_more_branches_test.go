package thresholds

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const packAPolicySource = "pack-a"

func TestThresholdConfigAdditionalBranches(t *testing.T) {
	if _, err := LoadWithPolicy("\x00", ""); err == nil {
		t.Fatalf("expected LoadWithPolicy to reject invalid repo path")
	}

	if got := normalizePathPatterns([]string{" ", "\t"}); len(got) != 0 {
		t.Fatalf("expected blank path patterns to normalize to empty, got %#v", got)
	}
	if got := normalizePathPatterns([]string{}); got == nil || len(got) != 0 {
		t.Fatalf("expected explicit empty path patterns to remain allocated, got %#v", got)
	}

	maxUncertain := 2
	merged := mergeOverrides(Overrides{}, Overrides{MaxUncertainImportCount: &maxUncertain})
	if merged.MaxUncertainImportCount == nil || *merged.MaxUncertainImportCount != 2 {
		t.Fatalf("expected max_uncertain_import_count override to merge, got %#v", merged)
	}
	merged = mergeOverrides(Overrides{LicenseDenyList: []string{"GPL-3.0-ONLY"}}, Overrides{LicenseDenyList: []string{}})
	if merged.LicenseDenyList == nil || len(merged.LicenseDenyList) != 0 {
		t.Fatalf("expected explicit empty deny list to clear inherited deny list, got %#v", merged.LicenseDenyList)
	}

	scope := mergeScope(
		PathScope{Include: []string{"src/**"}, Exclude: []string{"vendor/**"}},
		PathScope{Include: []string{}, Exclude: []string{}},
	)
	if scope.Include == nil || len(scope.Include) != 0 || scope.Exclude == nil || len(scope.Exclude) != 0 {
		t.Fatalf("expected explicit empty scope lists to clear inherited scope, got %#v", scope)
	}

	features := mergeFeatures(
		FeatureConfig{Enable: []string{"alpha"}, Disable: []string{"beta"}},
		FeatureConfig{Enable: []string{}, Disable: []string{}},
	)
	if features.Enable == nil || len(features.Enable) != 0 || features.Disable == nil || len(features.Disable) != 0 {
		t.Fatalf("expected explicit empty feature lists to clear inherited features, got %#v", features)
	}

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

	if isPathUnderRoot("\x00", filepath.Join(t.TempDir(), "policy.yml")) {
		t.Fatalf("expected invalid root path to fail root containment check")
	}
}
