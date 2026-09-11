package thresholds

import (
	"strings"
	"testing"
)

func TestThresholdConfigAdditionalRuntimeBranches(t *testing.T) {
	if _, err := resolvePackRef("https://example.com/policy.yml", "%zz"); err == nil || !strings.Contains(err.Error(), "invalid remote pack reference") {
		t.Fatalf("expected invalid remote pack reference error, got %v", err)
	}
}
