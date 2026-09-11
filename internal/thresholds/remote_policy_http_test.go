package thresholds

import (
	"strings"
	"testing"
)

func TestThresholdConfigAdditionalRemotePolicyBranches(t *testing.T) {
	t.Run("resolver surfaces canonical location failures", func(t *testing.T) {
		if _, err := newPackResolver(t.TempDir()).resolveFile("https://example.com/policy.yml#bad-pin", packTrust{}); err == nil {
			t.Fatalf("expected resolveFile to reject invalid canonical policy locations")
		}
	})

	t.Run("resolve remote pack ref against parent URL", func(t *testing.T) {
		current := "https://example.com/policies/root.yml#sha256=" + strings.Repeat("a", 64)
		got, err := resolvePackRef(current, "../shared/base.yml#sha256="+strings.Repeat("b", 64))
		if err != nil {
			t.Fatalf("resolve remote relative pack ref: %v", err)
		}
		want := "https://example.com/shared/base.yml#sha256=" + strings.Repeat("b", 64)
		if got != want {
			t.Fatalf("unexpected resolved remote pack ref: got %q want %q", got, want)
		}
	})

}
