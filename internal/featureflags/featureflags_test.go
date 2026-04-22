package featureflags

import (
	"strings"
	"testing"
)

func TestNewRegistryValidatesEntries(t *testing.T) {
	_, err := NewRegistry([]Flag{{Code: "", Name: "alpha", Lifecycle: LifecyclePreview}})
	if err == nil || !strings.Contains(err.Error(), "feature code is required") {
		t.Fatalf("expected missing code error, got %v", err)
	}
	_, err = NewRegistry([]Flag{{Code: "BAD-0001", Name: "alpha", Lifecycle: LifecyclePreview}})
	if err == nil || !strings.Contains(err.Error(), "invalid feature code") {
		t.Fatalf("expected invalid code error, got %v", err)
	}
	_, err = NewRegistry([]Flag{{Code: "LOP-FEAT-0001", Name: "Alpha", Lifecycle: LifecyclePreview}})
	if err == nil || !strings.Contains(err.Error(), "invalid feature name") {
		t.Fatalf("expected invalid name error, got %v", err)
	}
	_, err = NewRegistry([]Flag{{Code: "LOP-FEAT-001", Name: "alpha", Lifecycle: LifecyclePreview}})
	if err == nil || !strings.Contains(err.Error(), "must use LOP-FEAT-NNNN") {
		t.Fatalf("expected short code error, got %v", err)
	}
	_, err = NewRegistry([]Flag{{Code: "LOP-FEAT-00A1", Name: "alpha", Lifecycle: LifecyclePreview}})
	if err == nil || !strings.Contains(err.Error(), "suffix must be numeric") {
		t.Fatalf("expected nonnumeric code error, got %v", err)
	}
	_, err = NewRegistry([]Flag{{Code: "LOP-FEAT-0001", Name: "", Lifecycle: LifecyclePreview}})
	if err == nil || !strings.Contains(err.Error(), "feature name is required") {
		t.Fatalf("expected missing name error, got %v", err)
	}
	_, err = NewRegistry([]Flag{{Code: "LOP-FEAT-0001", Name: "alpha", Lifecycle: "unknown"}})
	if err == nil || !strings.Contains(err.Error(), "invalid feature lifecycle") {
		t.Fatalf("expected invalid lifecycle error, got %v", err)
	}
}

func TestDefaultRegistryAndLookup(t *testing.T) {
	if got := DefaultRegistry().Flags(); len(got) != 0 {
		t.Fatalf("expected empty default registry, got %#v", got)
	}
	if flags := (*Registry)(nil).Flags(); len(flags) != 0 {
		t.Fatalf("expected nil registry flags to be empty, got %#v", flags)
	}
	if _, ok := (*Registry)(nil).Lookup("anything"); ok {
		t.Fatalf("expected nil registry lookup to miss")
	}

	registry := testRegistry(t)
	if _, ok := registry.Lookup("   "); ok {
		t.Fatalf("expected blank lookup to miss")
	}
	if got, ok := registry.Lookup("stable-flag"); !ok || got.Code != "LOP-FEAT-0002" {
		t.Fatalf("expected name lookup to find stable flag, got %#v ok=%v", got, ok)
	}
	flags := registry.Flags()
	flags[0].Name = "mutated"
	if got, _ := registry.Lookup("LOP-FEAT-0001"); got.Name != "preview-flag" {
		t.Fatalf("expected Flags to return a defensive copy, got %#v", got)
	}
}

func TestNewRegistryRejectsDuplicates(t *testing.T) {
	_, err := NewRegistry([]Flag{
		{Code: "LOP-FEAT-0001", Name: "alpha", Lifecycle: LifecyclePreview},
		{Code: "LOP-FEAT-0001", Name: "beta", Lifecycle: LifecyclePreview},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate feature code") {
		t.Fatalf("expected duplicate code error, got %v", err)
	}

	_, err = NewRegistry([]Flag{
		{Code: "LOP-FEAT-0001", Name: "alpha", Lifecycle: LifecyclePreview},
		{Code: "LOP-FEAT-0002", Name: "alpha", Lifecycle: LifecyclePreview},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate feature name") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
}

func TestNextCodeAllocatesGeneratedCodes(t *testing.T) {
	if code, err := DefaultRegistry().NextCode(); err != nil || code != "LOP-FEAT-0001" {
		t.Fatalf("expected empty registry to allocate first code, got %q err=%v", code, err)
	}
	if code, err := (*Registry)(nil).NextCode(); err != nil || code != "LOP-FEAT-0001" {
		t.Fatalf("expected nil registry to allocate first code, got %q err=%v", code, err)
	}

	registry, err := NewRegistry([]Flag{
		{Code: "LOP-FEAT-0007", Name: "later", Lifecycle: LifecyclePreview},
		{Code: "LOP-FEAT-0002", Name: "earlier", Lifecycle: LifecycleStable},
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if code, err := registry.NextCode(); err != nil || code != "LOP-FEAT-0008" {
		t.Fatalf("expected next generated code, got %q err=%v", code, err)
	}

	full, err := NewRegistry([]Flag{{Code: "LOP-FEAT-9999", Name: "last", Lifecycle: LifecyclePreview}})
	if err != nil {
		t.Fatalf("new full registry: %v", err)
	}
	if code, err := full.NextCode(); err == nil || code != "" {
		t.Fatalf("expected exhausted code space, got %q err=%v", code, err)
	}
}

func TestLifecycleAndChannelAliases(t *testing.T) {
	if got, err := NormalizeLifecycle("experimental"); err != nil || got != LifecyclePreview {
		t.Fatalf("expected experimental alias to normalize to preview, got %q err=%v", got, err)
	}
	if got, err := NormalizeLifecycle("done"); err != nil || got != LifecycleStable {
		t.Fatalf("expected done alias to normalize to stable, got %q err=%v", got, err)
	}
	if got, err := NormalizeChannel(""); err != nil || got != ChannelDev {
		t.Fatalf("expected blank channel to normalize to dev, got %q err=%v", got, err)
	}
	if got, err := NormalizeChannel("rolling"); err != nil || got != ChannelRolling {
		t.Fatalf("expected rolling channel, got %q err=%v", got, err)
	}
	if got, err := NormalizeChannel("release"); err != nil || got != ChannelRelease {
		t.Fatalf("expected release channel, got %q err=%v", got, err)
	}
	if _, err := NormalizeChannel("bad"); err == nil {
		t.Fatalf("expected invalid channel to fail")
	}
}

func TestResolveChannelDefaults(t *testing.T) {
	registry := testRegistry(t)

	dev, err := registry.Resolve(ResolveOptions{Channel: ChannelDev})
	if err != nil {
		t.Fatalf("resolve dev: %v", err)
	}
	if dev.Enabled("preview-flag") {
		t.Fatalf("expected preview flag default-off in dev")
	}
	if !dev.Enabled("stable-flag") {
		t.Fatalf("expected stable flag default-on in dev")
	}

	release, err := registry.Resolve(ResolveOptions{Channel: ChannelRelease})
	if err != nil {
		t.Fatalf("resolve release: %v", err)
	}
	if release.Enabled("preview-flag") {
		t.Fatalf("expected preview flag default-off in release")
	}
	if !release.Enabled("stable-flag") {
		t.Fatalf("expected stable flag default-on in release")
	}

	rolling, err := registry.Resolve(ResolveOptions{Channel: ChannelRolling})
	if err != nil {
		t.Fatalf("resolve rolling: %v", err)
	}
	if !rolling.Enabled("preview-flag") || !rolling.Enabled("stable-flag") {
		t.Fatalf("expected rolling to enable all flags")
	}
}

func TestResolveReleaseLockAndExplicitOverrides(t *testing.T) {
	registry := testRegistry(t)
	lock := &ReleaseLock{Release: "v1.4.2", DefaultOn: []string{"LOP-FEAT-0001"}}

	resolved, err := registry.Resolve(ResolveOptions{
		Channel: ChannelRelease,
		Lock:    lock,
		Disable: []string{"stable-flag"},
	})
	if err != nil {
		t.Fatalf("resolve release lock: %v", err)
	}
	if !resolved.Enabled("preview-flag") {
		t.Fatalf("expected release lock to enable preview flag")
	}
	if resolved.Enabled("stable-flag") {
		t.Fatalf("expected explicit disable to win over stable default")
	}

	resolved, err = registry.Resolve(ResolveOptions{
		Channel: ChannelRelease,
		Enable:  []string{"preview-flag"},
		Disable: []string{"preview-flag"},
	})
	if err == nil || !strings.Contains(err.Error(), "both enabled and disabled") {
		t.Fatalf("expected enable/disable conflict, got resolved=%v err=%v", resolved, err)
	}
}

func TestResolveRejectsUnknownReferences(t *testing.T) {
	registry := testRegistry(t)
	if _, err := registry.Resolve(ResolveOptions{Channel: "bad"}); err == nil {
		t.Fatalf("expected invalid channel to fail")
	}
	if _, err := registry.Resolve(ResolveOptions{Enable: []string{"missing"}}); err == nil {
		t.Fatalf("expected unknown explicit enable to fail")
	}
	if _, err := registry.Resolve(ResolveOptions{Disable: []string{"missing"}}); err == nil {
		t.Fatalf("expected unknown explicit disable to fail")
	}
	if _, err := registry.Resolve(ResolveOptions{
		Channel: ChannelRelease,
		Lock:    &ReleaseLock{Release: "v1.4.2", DefaultOn: []string{"missing"}},
	}); err == nil {
		t.Fatalf("expected unknown release lock ref to fail")
	}
	if resolved, err := (*Registry)(nil).Resolve(ResolveOptions{}); err != nil || resolved.Enabled("anything") {
		t.Fatalf("expected nil registry to resolve against empty defaults, resolved=%#v err=%v", resolved, err)
	}
}

func TestParseReleaseLock(t *testing.T) {
	lock, err := ParseReleaseLock([]byte(`{
		"release": " v1.4.2 ",
		"defaultOn": [" LOP-FEAT-0001 ", ""],
		"notes": {" LOP-FEAT-0001 ": " preview on "}
	}`))
	if err != nil {
		t.Fatalf("parse release lock: %v", err)
	}
	if lock.Release != "v1.4.2" {
		t.Fatalf("expected release to be trimmed, got %q", lock.Release)
	}
	if len(lock.DefaultOn) != 1 || lock.DefaultOn[0] != "LOP-FEAT-0001" {
		t.Fatalf("expected normalized defaultOn refs, got %#v", lock.DefaultOn)
	}
	if got := lock.Notes["LOP-FEAT-0001"]; got != "preview on" {
		t.Fatalf("expected normalized note, got %q", got)
	}

	if _, err := ParseReleaseLock([]byte(`{"release":""}`)); err == nil {
		t.Fatalf("expected blank release to fail")
	}
	if _, err := ParseReleaseLock([]byte(`{"release":"v1","unknown":true}`)); err == nil {
		t.Fatalf("expected unknown field to fail")
	}
	if _, err := ParseReleaseLock([]byte(`{"release":"v1"} {"release":"v2"}`)); err == nil {
		t.Fatalf("expected multiple JSON values to fail")
	}
}

func TestValidateReleaseLockRejectsDuplicatesAndUnknownNotes(t *testing.T) {
	registry := testRegistry(t)
	if err := registry.ValidateReleaseLock(nil); err != nil {
		t.Fatalf("expected nil lock to be valid, got %v", err)
	}
	if err := registry.ValidateReleaseLock(&ReleaseLock{}); err == nil || !strings.Contains(err.Error(), "release is required") {
		t.Fatalf("expected blank release to fail, got %v", err)
	}
	if err := registry.ValidateReleaseLock(&ReleaseLock{
		Release:   "v1.4.2",
		DefaultOn: []string{"LOP-FEAT-0001", "preview-flag"},
	}); err == nil || !strings.Contains(err.Error(), "duplicate feature") {
		t.Fatalf("expected duplicate lock refs to fail, got %v", err)
	}
	if err := registry.ValidateReleaseLock(&ReleaseLock{
		Release: "v1.4.2",
		Notes:   map[string]string{"missing": "note"},
	}); err == nil || !strings.Contains(err.Error(), "unknown feature note") {
		t.Fatalf("expected unknown note ref to fail, got %v", err)
	}
}

func TestManifestReportsDefaults(t *testing.T) {
	registry := testRegistry(t)
	manifest, err := registry.Manifest(ResolveOptions{
		Channel: ChannelRelease,
		Lock:    &ReleaseLock{Release: "v1.4.2", DefaultOn: []string{"preview-flag"}},
	})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(manifest) != 2 {
		t.Fatalf("expected two manifest entries, got %#v", manifest)
	}
	if manifest[0].Code != "LOP-FEAT-0001" || !manifest[0].EnabledByDefault {
		t.Fatalf("expected preview flag lock default-on, got %#v", manifest[0])
	}
	if manifest[1].Code != "LOP-FEAT-0002" || !manifest[1].EnabledByDefault {
		t.Fatalf("expected stable flag default-on, got %#v", manifest[1])
	}
	if _, err := registry.Manifest(ResolveOptions{Enable: []string{"missing"}}); err == nil {
		t.Fatalf("expected manifest to return resolver errors")
	}
	if manifest, err := (*Registry)(nil).Manifest(ResolveOptions{}); err != nil || len(manifest) != 0 {
		t.Fatalf("expected nil registry manifest to be empty, manifest=%#v err=%v", manifest, err)
	}
}

func TestEnabledFlag(t *testing.T) {
	resolved, err := testRegistry(t).Resolve(ResolveOptions{Channel: ChannelRolling})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if enabled, err := resolved.EnabledFlag(" preview-flag "); err != nil || !enabled {
		t.Fatalf("expected preview flag enabled in rolling, enabled=%v err=%v", enabled, err)
	}
	if enabled, err := resolved.EnabledFlag("missing"); err == nil || enabled {
		t.Fatalf("expected unknown feature error, enabled=%v err=%v", enabled, err)
	}
	var empty *Set
	if empty.Enabled("preview-flag") {
		t.Fatalf("expected nil set to report disabled")
	}
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := NewRegistry([]Flag{
		{
			Code:        "LOP-FEAT-0001",
			Name:        "preview-flag",
			Description: "Preview behavior",
			Lifecycle:   LifecyclePreview,
		},
		{
			Code:        "LOP-FEAT-0002",
			Name:        "stable-flag",
			Description: "Stable behavior",
			Lifecycle:   LifecycleStable,
		},
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}
