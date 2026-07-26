package shared

import "testing"

func TestFallbackDependencyReturnsNormalizedModulePrefix(t *testing.T) {
	got := FallbackDependency("com.example.module", func(value string) string {
		return "normalized:" + value
	})
	if got != "normalized:com.example" {
		t.Fatalf("unexpected fallback dependency: %q", got)
	}
}

func TestRootedWalkBudgetWarningTrimsScopeWhenLimitIsUnset(t *testing.T) {
	warning := rootedWalkBudgetWarning("  Gradle version catalog scan  ", "traversal entries", 0)
	if warning != "Gradle version catalog scan reached a rooted walk limit; results may be partial" {
		t.Fatalf("unexpected zero-limit rooted walk warning: %q", warning)
	}
}
