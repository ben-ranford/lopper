package thresholds

import "testing"

func TestThresholdConfigAdditionalRemotePolicyBranches(t *testing.T) {
	t.Run("resolver surfaces canonical location failures", func(t *testing.T) {
		if _, err := newPackResolver(t.TempDir()).resolveFile("https://example.com/policy.yml#bad-pin", packTrust{}); err == nil {
			t.Fatalf("expected resolveFile to reject invalid canonical policy locations")
		}
	})

	t.Run("reject remote pack ref against parent URL", func(t *testing.T) {
		if _, err := resolvePackRef("https://example.com/policies/root.yml", "../shared/base.yml"); err == nil {
			t.Fatal("expected remote parent pack reference rejection")
		}
	})

}
