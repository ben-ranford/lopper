package featureflags

import "testing"

func TestSetWithOverridesPreservesOtherChoices(t *testing.T) {
	original, err := DefaultRegistry().Resolve(ResolveOptions{Enable: []string{"dart-source-attribution"}, Disable: []string{"baseline-store-discovery"}})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := original.WithOverrides(Overrides{Enable: []string{"stave-tui-preview"}})
	if err != nil {
		t.Fatal(err)
	}
	if !changed.Enabled("stave-tui-preview") || original.Enabled("stave-tui-preview") {
		t.Fatal("override mutated original or failed to apply")
	}
	if !changed.Enabled("dart-source-attribution") || changed.Enabled("baseline-store-discovery") {
		t.Fatal("unrelated decisions lost")
	}
	if _, err := original.WithOverrides(Overrides{Enable: []string{"unknown"}}); err == nil {
		t.Fatal("unknown override accepted")
	}
}
