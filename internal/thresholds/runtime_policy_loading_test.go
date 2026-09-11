package thresholds

import (
	"strings"
	"testing"
)

func TestThresholdConfigAdditionalRuntimeBranches(t *testing.T) {
	if _, err := resolvePackRef("https://example.com/policy.yml", "%zz"); err == nil || !strings.Contains(err.Error(), "remote policy packs are disabled") {
		t.Fatalf("expected remote policy pack rejection, got %v", err)
	}
}
