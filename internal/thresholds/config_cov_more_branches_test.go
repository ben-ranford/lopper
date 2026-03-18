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
		t.Fatalf("expected blank path patterns to normalize to nil, got %#v", got)
	}

	maxUncertain := 2
	merged := mergeOverrides(Overrides{}, Overrides{MaxUncertainImportCount: &maxUncertain})
	if merged.MaxUncertainImportCount == nil || *merged.MaxUncertainImportCount != 2 {
		t.Fatalf("expected max_uncertain_import_count override to merge, got %#v", merged)
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
