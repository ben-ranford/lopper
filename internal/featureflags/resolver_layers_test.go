package featureflags

import (
	"strings"
	"testing"
)

func TestResolveLayersKeepsDefaultsAndDiagnostics(t *testing.T) {
	registry := testRegistry(t)
	resolved, err := registry.ResolveLayers(
		ResolveOptions{Channel: ChannelDev, Enable: []string{"preview-flag"}, Disable: []string{"legacy-stable-flag"}},
		Overrides{Enable: []string{"LOP-FEAT-0002"}, Disable: []string{"LOP-FEAT-0001"}},
		Overrides{Enable: []string{"legacy-stable-flag"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Enabled("preview-flag") || !resolved.Enabled("stable-flag") {
		t.Fatalf("unexpected layered choices: %v", resolved.Snapshot())
	}
	warnings := resolved.DeprecationWarnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "legacy-stable-flag") {
		t.Fatalf("expected preserved, deduplicated alias warning: %v", warnings)
	}
	defaults, err := registry.Resolve(ResolveOptions{Channel: ChannelDev})
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Enabled("preview-flag") || !defaults.Enabled("stable-flag") {
		t.Fatalf("layer resolution changed registry defaults: %v", defaults.Snapshot())
	}
}

func TestResolveLayersRejectsInvalidLayer(t *testing.T) {
	registry := testRegistry(t)
	for _, tt := range []struct {
		name  string
		base  ResolveOptions
		layer Overrides
		want  string
	}{
		{"invalid channel", ResolveOptions{Channel: "unknown"}, Overrides{}, "invalid feature build channel"},
		{"invalid base", ResolveOptions{Enable: []string{"missing"}}, Overrides{Disable: []string{"missing"}}, "unknown feature"},
		{"base conflict", ResolveOptions{Enable: []string{"stable-flag"}, Disable: []string{"LOP-FEAT-0002"}}, Overrides{Enable: []string{"stable-flag"}}, "both enabled and disabled"},
		{"unknown higher", ResolveOptions{}, Overrides{Enable: []string{"missing"}}, "unknown feature"},
		{"higher conflict", ResolveOptions{}, Overrides{Enable: []string{"legacy-stable-flag"}, Disable: []string{"LOP-FEAT-0002"}}, "both enabled and disabled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := registry.ResolveLayers(tt.base, tt.layer)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolveLayersNilRegistryUsesDefaults(t *testing.T) {
	features, err := (*Registry)(nil).ResolveLayers(ResolveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(features.Snapshot()) != len(DefaultRegistry().Flags()) {
		t.Fatal("nil registry did not resolve default registry")
	}
}
