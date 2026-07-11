package featureflags

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestNewRegistryValidatesEntries(t *testing.T) {
	assertNewRegistryErrorContains(t, []Flag{{Code: "", Name: "alpha", Lifecycle: LifecyclePreview}}, "feature code is required", "missing code")
	assertNewRegistryErrorContains(t, []Flag{{Code: "BAD-0001", Name: "alpha", Lifecycle: LifecyclePreview}}, "invalid feature code", "invalid code")
	assertNewRegistryErrorContains(t, []Flag{{Code: "LOP-FEAT-0001", Name: "Alpha", Lifecycle: LifecyclePreview}}, "invalid feature name", "invalid name")
	assertNewRegistryErrorContains(t, []Flag{{Code: "LOP-FEAT-001", Name: "alpha", Lifecycle: LifecyclePreview}}, "must use LOP-FEAT-NNNN", "short code")
	assertNewRegistryErrorContains(t, []Flag{{Code: "LOP-FEAT-00A1", Name: "alpha", Lifecycle: LifecyclePreview}}, "suffix must be numeric", "nonnumeric code")
	assertNewRegistryErrorContains(t, []Flag{{Code: "LOP-FEAT-0001", Name: "", Lifecycle: LifecyclePreview}}, "feature name is required", "missing name")
	assertNewRegistryErrorContains(t, []Flag{{Code: "LOP-FEAT-0001", Name: "alpha", DeprecatedNames: []string{"bad name"}, Lifecycle: LifecyclePreview}}, "invalid feature name", "invalid deprecated name")
	assertNewRegistryErrorContains(t, []Flag{{Code: "LOP-FEAT-0001", Name: "alpha", DeprecatedNames: []string{"alpha"}, Lifecycle: LifecyclePreview}}, "duplicates canonical name", "canonical deprecated name")
	assertNewRegistryErrorContains(t, []Flag{{Code: "LOP-FEAT-0001", Name: "alpha", Lifecycle: "unknown"}}, "invalid feature lifecycle", "invalid lifecycle")
	assertNewRegistryErrorContains(t, []Flag{{Code: "LOP-FEAT-0001", Name: "alpha", Lifecycle: LifecyclePreview, FirstStableRelease: "nope"}}, "invalid first stable release", "invalid first stable release")
}

func TestDefaultRegistryAndLookup(t *testing.T) {
	if err := ValidateDefaultRegistry(); err != nil {
		t.Fatalf("expected embedded default registry to be valid, got %v", err)
	}
	defaultFlags := DefaultRegistry().Flags()
	assertDefaultFlag(t, defaultFlags, "dart-source-attribution", "LOP-FEAT-0001")
	assertDefaultFlag(t, defaultFlags, "lockfile-drift-ecosystem-expansion", "LOP-FEAT-0002")
	assertDefaultFlag(t, defaultFlags, "swift-carthage", "LOP-FEAT-0003")
	assertDefaultFlag(t, defaultFlags, "powershell-adapter", "LOP-FEAT-0004")
	assertDefaultFlag(t, defaultFlags, "go-vendored-provenance", "LOP-FEAT-0005")
	assertDefaultFlagRelease(t, defaultFlags, "baseline-provenance-runtime-context", "LOP-FEAT-0006", "v1.6.0")
	assertDefaultFlagRelease(t, defaultFlags, "vscode-multi-root-workflows", "LOP-FEAT-0007", "v1.6.0")
	assertDefaultFlagRelease(t, defaultFlags, "mcp-server", "LOP-FEAT-0008", "v1.6.0")
	if defaultFlags[0].FirstStableRelease != "v1.5.0" {
		t.Fatalf("expected default flags to retain first stable release history, got %#v", defaultFlags[0])
	}
	assertDefaultLookup(t, "LOP-FEAT-0001", "dart-source-attribution", false)
	assertDefaultLookup(t, "dart-source-attribution", "dart-source-attribution", false)
	assertDefaultLookup(t, "dart-source-attribution-preview", "dart-source-attribution", true)
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
	copiedFlags := registry.Flags()
	copiedFlags[0].Name = "mutated"
	copiedFlags[1].DeprecatedNames[0] = "mutated"
	if got, _ := registry.Lookup("LOP-FEAT-0001"); got.Name != "preview-flag" {
		t.Fatalf("expected Flags to return a defensive copy, got %#v", got)
	}
	if got, _ := registry.Lookup("legacy-stable-flag"); got.DeprecatedNames[0] != "legacy-stable-flag" {
		t.Fatalf("expected deprecated names to be defensively copied, got %#v", got)
	}
}

type v180StableFeature struct {
	code       string
	name       string
	legacyName string
}

func TestV180FeatureQualificationDefaults(t *testing.T) {
	stable := []v180StableFeature{
		{code: "LOP-FEAT-0014", name: "python-runtime-capture", legacyName: "python-runtime-capture-preview"},
		{code: "LOP-FEAT-0016", name: "python-codemod-suggestions", legacyName: "python-codemod-suggestions-preview"},
	}
	preview := []string{"LOP-FEAT-0013", "LOP-FEAT-0015", "LOP-FEAT-0017"}

	for _, channel := range []Channel{ChannelDev, ChannelRelease} {
		t.Run(string(channel), func(t *testing.T) {
			resolved := mustResolveV180Features(t, ResolveOptions{Channel: channel})
			for _, want := range stable {
				assertV180StableFeature(t, channel, resolved, want)
			}
			for _, code := range preview {
				assertV180PreviewFeature(t, channel, resolved, code)
			}
		})
	}
}

func mustResolveV180Features(t *testing.T, options ResolveOptions) Set {
	t.Helper()
	resolved, err := DefaultRegistry().Resolve(options)
	if err != nil {
		t.Fatalf("resolve %s features: %v", options.Channel, err)
	}
	return resolved
}

func assertV180StableFeature(t *testing.T, channel Channel, defaults Set, want v180StableFeature) {
	t.Helper()
	flag, ok := DefaultRegistry().Lookup(want.code)
	if !ok || flag.Name != want.name || flag.Lifecycle != LifecycleStable {
		t.Fatalf("expected %s to be stable as %s, got %#v", want.code, want.name, flag)
	}
	if !defaults.Enabled(want.code) {
		t.Fatalf("expected %s enabled in %s defaults", want.code, channel)
	}
	legacy, ok := DefaultRegistry().LookupReference(want.legacyName)
	if !ok || !legacy.Deprecated || legacy.ReplacementRef != want.name {
		t.Fatalf("expected deprecated alias %s to point at %s, got %#v", want.legacyName, want.name, legacy)
	}
	disabled := mustResolveV180Features(t, ResolveOptions{Channel: channel, Disable: []string{want.name}})
	if disabled.Enabled(want.code) {
		t.Fatalf("expected explicit disable to override %s stable default", want.code)
	}
}

func assertV180PreviewFeature(t *testing.T, channel Channel, defaults Set, code string) {
	t.Helper()
	flag, ok := DefaultRegistry().Lookup(code)
	if !ok || flag.Lifecycle != LifecyclePreview {
		t.Fatalf("expected %s to remain preview, got %#v", code, flag)
	}
	if defaults.Enabled(code) {
		t.Fatalf("expected %s disabled in %s defaults", code, channel)
	}
}

func TestCatalogParseAndFormat(t *testing.T) {
	flags, err := ParseCatalog([]byte(`[
		{"code":"LOP-FEAT-0002","name":"stable-flag","description":"Stable behavior","lifecycle":"stable","firstStableRelease":"1.5.0"},
		{"code":"LOP-FEAT-0001","name":"preview-flag","description":"Preview behavior","lifecycle":"preview"}
	]`))
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	if len(flags) != 2 || flags[0].Code != "LOP-FEAT-0001" || flags[1].Code != "LOP-FEAT-0002" {
		t.Fatalf("expected catalog flags sorted by code, got %#v", flags)
	}
	if flags[1].FirstStableRelease != "v1.5.0" {
		t.Fatalf("expected first stable release to normalize, got %#v", flags[1])
	}
	data, err := FormatCatalog(flags)
	if err != nil {
		t.Fatalf("format catalog: %v", err)
	}
	formatted := string(data)
	if !strings.Contains(formatted, `"code": "LOP-FEAT-0001"`) || !strings.Contains(formatted, `"firstStableRelease": "v1.5.0"`) || !strings.HasSuffix(formatted, "\n") {
		t.Fatalf("expected formatted JSON catalog with newline, got %q", formatted)
	}

	if _, err := ParseCatalog([]byte(`{"code":"LOP-FEAT-0001"}`)); err == nil {
		t.Fatalf("expected non-array catalog to fail")
	}
	if _, err := ParseCatalog([]byte(`[{"code":"LOP-FEAT-0001","name":"alpha","lifecycle":"preview","unknown":true}]`)); err == nil {
		t.Fatalf("expected unknown catalog field to fail")
	}
	if _, err := ParseCatalog([]byte(`[] []`)); err == nil {
		t.Fatalf("expected multiple JSON values to fail")
	}
	if _, err := ParseCatalog([]byte(`[{"code":"LOP-FEAT-0001","name":"bad name","lifecycle":"preview"}]`)); err == nil {
		t.Fatalf("expected catalog entries that fail registry validation to fail")
	}
	if _, err := FormatCatalog([]Flag{{Code: "bad", Name: "alpha", Lifecycle: LifecyclePreview}}); err == nil {
		t.Fatalf("expected invalid catalog flag to fail")
	}
}

func TestDefaultRegistryFallback(t *testing.T) {
	originalCatalog := embeddedCatalog
	originalRegistry := defaultRegistry
	originalErr := defaultRegistryErr
	t.Cleanup(func() {
		embeddedCatalog = originalCatalog
		defaultRegistry = originalRegistry
		defaultRegistryErr = originalErr
	})

	embeddedCatalog = []byte(`not json`)
	registry, err := newDefaultRegistry()
	if err == nil {
		t.Fatalf("expected invalid embedded catalog to fail")
	}
	if got := registry.Flags(); len(got) != 0 {
		t.Fatalf("expected invalid default registry fallback to be empty, got %#v", got)
	}

	defaultRegistry = registry
	defaultRegistryErr = err
	if got := DefaultRegistry().Flags(); len(got) != 0 {
		t.Fatalf("expected default registry error fallback to be empty, got %#v", got)
	}
	if got := emptyRegistry().Flags(); len(got) != 0 {
		t.Fatalf("expected explicit empty registry to be empty, got %#v", got)
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

	_, err = NewRegistry([]Flag{
		{Code: "LOP-FEAT-0001", Name: "alpha", DeprecatedNames: []string{"legacy-alpha"}, Lifecycle: LifecycleStable},
		{Code: "LOP-FEAT-0002", Name: "legacy-alpha", Lifecycle: LifecyclePreview},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate feature name") {
		t.Fatalf("expected duplicate deprecated name error, got %v", err)
	}
}

func TestNextCodeAllocatesGeneratedCodes(t *testing.T) {
	expectedDefaultNextCode := expectedNextDefaultCode(t)
	if code, err := DefaultRegistry().NextCode(); err != nil || code != expectedDefaultNextCode {
		t.Fatalf("expected embedded registry to allocate next code, got %q err=%v", code, err)
	}
	if code, err := (*Registry)(nil).NextCode(); err != nil || code != expectedDefaultNextCode {
		t.Fatalf("expected nil registry to allocate next code from defaults, got %q err=%v", code, err)
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
	corrupt := &Registry{flags: []Flag{{Code: "bad"}}}
	if code, err := corrupt.NextCode(); err == nil || code != "" {
		t.Fatalf("expected corrupt registry code to fail, got %q err=%v", code, err)
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
	})
	if err != nil {
		t.Fatalf("resolve explicit preview enable: %v", err)
	}
	if !resolved.Enabled("preview-flag") {
		t.Fatalf("expected explicit enable to turn preview flag on")
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
	lock, err = ParseReleaseLock([]byte(`{"release":"v1","notes":{"":"ignored"}}`))
	if err != nil {
		t.Fatalf("parse lock with blank note: %v", err)
	}
	if len(lock.Notes) != 0 {
		t.Fatalf("expected blank note refs to be dropped, got %#v", lock.Notes)
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

func TestReleaseLockSetParsingAndLookup(t *testing.T) {
	locks, err := ParseReleaseLocks([]byte(`[
		{"release":" v1.4.2 ","defaultOn":[" preview-flag ",""],"notes":{" preview-flag ":"reviewed"}},
		{"release":"1.4.3","defaultOn":["stable-flag"]}
	]`))
	if err != nil {
		t.Fatalf("parse release locks: %v", err)
	}
	if len(locks) != 2 || locks[0].Release != "v1.4.2" || locks[0].DefaultOn[0] != "preview-flag" {
		t.Fatalf("unexpected normalized release locks: %#v", locks)
	}
	if locks[0].Notes["preview-flag"] != "reviewed" {
		t.Fatalf("expected normalized release note, got %#v", locks[0].Notes)
	}
	if _, err := ParseReleaseLocks([]byte(`{"release":"v1.4.2"}`)); err == nil {
		t.Fatalf("expected non-array release locks to fail")
	}
	if _, err := ParseReleaseLocks([]byte(`[{"release":" "}]`)); err == nil || !strings.Contains(err.Error(), "release is required") {
		t.Fatalf("expected blank release lock to fail, got %v", err)
	}
	if _, err := ParseReleaseLocks([]byte(`[{"release":"v1","unknown":true}]`)); err == nil {
		t.Fatalf("expected unknown release lock field to fail")
	}
	if _, err := ParseReleaseLocks([]byte(`[{"release":"v1"},{"release":" v1 "}]`)); err == nil || !strings.Contains(err.Error(), "duplicate release lock") {
		t.Fatalf("expected duplicate release lock to fail, got %v", err)
	}
	if _, err := ParseReleaseLocks([]byte(`[] []`)); err == nil {
		t.Fatalf("expected multiple JSON values to fail")
	}
}

const validDefaultReleaseLocksJSON = `[
	{"release":"v1.4.2","defaultOn":["preview-flag"],"notes":{"preview-flag":"reviewed"}}
]`

func TestDefaultReleaseLockReturnsMatchingCopy(t *testing.T) {
	withDefaultReleaseLocks(t, validDefaultReleaseLocksJSON)

	lock := mustDefaultReleaseLock(t, "1.4.2")
	if lock.Release != "v1.4.2" || lock.DefaultOn[0] != "preview-flag" {
		t.Fatalf("expected release lock match, got %#v", lock)
	}
	lock.DefaultOn[0] = "mutated"
	next := mustDefaultReleaseLock(t, "v1.4.2")
	if next.DefaultOn[0] != "preview-flag" {
		t.Fatalf("expected default release lock to return a copy, got %#v", next)
	}
}

func TestDefaultReleaseLockReturnsNilForMissingRelease(t *testing.T) {
	withDefaultReleaseLocks(t, validDefaultReleaseLocksJSON)

	assertNoDefaultReleaseLock(t, "v1.4.3")
	assertNoDefaultReleaseLock(t, " ")
}

func TestValidateDefaultReleaseLocksAcceptsEmbeddedData(t *testing.T) {
	withDefaultReleaseLocks(t, validDefaultReleaseLocksJSON)

	if err := ValidateDefaultReleaseLocks(); err != nil {
		t.Fatalf("validate default release locks: %v", err)
	}
}

func TestDefaultReleaseLocksRejectInvalidData(t *testing.T) {
	assertDefaultReleaseLocksInvalid(t, `[{"release":"v1.4.2","defaultOn":["missing"]}]`, "unknown feature")
	assertDefaultReleaseLocksInvalid(t, `not-json`, "invalid release locks JSON")
}

func withDefaultReleaseLocks(t *testing.T, data string) {
	t.Helper()
	originalLocks := embeddedReleaseLocks
	originalRegistry := defaultRegistry
	originalErr := defaultRegistryErr
	t.Cleanup(func() {
		embeddedReleaseLocks = originalLocks
		defaultRegistry = originalRegistry
		defaultRegistryErr = originalErr
	})
	defaultRegistry = testRegistry(t)
	defaultRegistryErr = nil
	embeddedReleaseLocks = []byte(data)
}

func mustDefaultReleaseLock(t *testing.T, release string) *ReleaseLock {
	t.Helper()
	lock, err := DefaultReleaseLock(release)
	if err != nil {
		t.Fatalf("default release lock: %v", err)
	}
	if lock == nil {
		t.Fatalf("expected release lock for %q", release)
	}
	return lock
}

func assertNoDefaultReleaseLock(t *testing.T, release string) {
	t.Helper()
	lock, err := DefaultReleaseLock(release)
	if err != nil || lock != nil {
		t.Fatalf("expected missing release lock to be nil, lock=%#v err=%v", lock, err)
	}
}

func assertDefaultReleaseLocksInvalid(t *testing.T, data string, want string) {
	t.Helper()
	withDefaultReleaseLocks(t, data)
	if err := ValidateDefaultReleaseLocks(); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected default release lock validation to fail with %q, got %v", want, err)
	}
	if lock, err := DefaultReleaseLock("v1.4.2"); err == nil || lock != nil {
		t.Fatalf("expected default release lock lookup to validate refs, lock=%#v err=%v", lock, err)
	}
}

func assertNewRegistryErrorContains(t *testing.T, flags []Flag, want, label string) {
	t.Helper()
	_, err := NewRegistry(flags)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected %s error containing %q, got %v", label, want, err)
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
	manifest, err = (*Registry)(nil).Manifest(ResolveOptions{})
	if err != nil {
		t.Fatalf("expected nil registry manifest to defer to defaults, manifest=%#v err=%v", manifest, err)
	}
	assertManifestFlag(t, manifest, "dart-source-attribution", true)
	assertManifestFlag(t, manifest, "lockfile-drift-ecosystem-expansion", true)
	assertManifestFlag(t, manifest, "swift-carthage", true)
	assertManifestFlag(t, manifest, "powershell-adapter", true)
	assertManifestFlag(t, manifest, "go-vendored-provenance", true)
	assertManifestFlag(t, manifest, "baseline-provenance-runtime-context", true)
	assertManifestFlag(t, manifest, "vscode-multi-root-workflows", true)
	assertManifestFlag(t, manifest, "mcp-server", true)
}

func TestFormatManifest(t *testing.T) {
	manifest := []ManifestEntry{{Code: "LOP-FEAT-0001", Name: "preview-flag", Lifecycle: LifecyclePreview}}
	data, err := FormatManifest(manifest)
	if err != nil {
		t.Fatalf("format manifest: %v", err)
	}
	if !strings.Contains(string(data), `"name": "preview-flag"`) || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("unexpected manifest JSON: %q", string(data))
	}
}

func TestEnabledFlag(t *testing.T) {
	resolved, err := testRegistry(t).Resolve(ResolveOptions{Channel: ChannelRolling})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertEnabledCodes(t, resolved, "LOP-FEAT-0001", "LOP-FEAT-0002")
	assertEnabledFlag(t, resolved, " preview-flag ")
	assertEnabledFlag(t, resolved, " legacy-stable-flag ")
	assertUnknownFeatureDisabled(t, resolved, "missing")
	assertFeatureSnapshot(t, resolved, "LOP-FEAT-0001", "LOP-FEAT-0002")
	assertNilFeatureSet(t)
}

func assertEnabledFlag(t *testing.T, set Set, ref string) {
	t.Helper()
	enabled, err := set.EnabledFlag(ref)
	if err != nil || !enabled {
		t.Fatalf("expected %q enabled, enabled=%v err=%v", ref, enabled, err)
	}
}

func assertUnknownFeatureDisabled(t *testing.T, set Set, ref string) {
	t.Helper()
	enabled, err := set.EnabledFlag(ref)
	if err == nil || enabled {
		t.Fatalf("expected unknown feature error for %q, enabled=%v err=%v", ref, enabled, err)
	}
}

func assertEnabledCodes(t *testing.T, set Set, want ...string) {
	t.Helper()
	if got := set.EnabledCodes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected enabled codes %#v, got %#v", want, got)
	}
}

func assertFeatureSnapshot(t *testing.T, set Set, want ...string) {
	t.Helper()
	snapshot := set.Snapshot()
	if len(snapshot) != len(want) {
		t.Fatalf("expected feature snapshot keys %#v, got %#v", want, snapshot)
	}
	for _, code := range want {
		if !snapshot[code] {
			t.Fatalf("expected feature snapshot to enable %s, got %#v", code, snapshot)
		}
	}
}

func assertNilFeatureSet(t *testing.T) {
	t.Helper()
	var empty *Set
	if empty.Enabled("preview-flag") {
		t.Fatalf("expected nil set to report disabled")
	}
	if got := empty.EnabledCodes(); len(got) != 0 {
		t.Fatalf("expected nil set enabled codes to be empty, got %#v", got)
	}
	if snapshot := empty.Snapshot(); len(snapshot) != 0 {
		t.Fatalf("expected nil set snapshot to be empty, got %#v", snapshot)
	}
	if refs := empty.DeprecatedReferences(); len(refs) != 0 {
		t.Fatalf("expected nil set deprecated refs to be empty, got %#v", refs)
	}
	if warnings := empty.DeprecationWarnings(); len(warnings) != 0 {
		t.Fatalf("expected nil set deprecation warnings to be empty, got %#v", warnings)
	}
}

func TestDefaultRegistryGraduatedDefaultsAndDisable(t *testing.T) {
	registry := DefaultRegistry()
	assertGraduatedDefaults(t, registry, ChannelDev)
	assertGraduatedDefaults(t, registry, ChannelRelease)
}

func TestDefaultRegistryLegacyPreviewDisable(t *testing.T) {
	registry := DefaultRegistry()
	disabled, err := registry.Resolve(ResolveOptions{
		Channel: ChannelRelease,
		Disable: []string{"swift-carthage-preview"},
	})
	if err != nil {
		t.Fatalf("resolve explicit disable: %v", err)
	}
	if disabled.Enabled("swift-carthage-preview") {
		t.Fatalf("expected explicit disable to turn off swift-carthage-preview")
	}
	if disabled.Enabled("swift-carthage") {
		t.Fatalf("expected explicit legacy disable to turn off swift-carthage")
	}
	if warnings := disabled.DeprecationWarnings(); len(warnings) != 1 || !strings.Contains(warnings[0], `"swift-carthage-preview"`) || !strings.Contains(warnings[0], `"swift-carthage"`) {
		t.Fatalf("expected legacy disable deprecation warning, got %#v", warnings)
	}
}

func TestDefaultRegistryDedupesDeprecatedReferences(t *testing.T) {
	registry := DefaultRegistry()
	duplicateLegacy, err := registry.Resolve(ResolveOptions{
		Channel: ChannelRelease,
		Enable:  []string{"swift-carthage-preview", "swift-carthage-preview"},
	})
	if err != nil {
		t.Fatalf("resolve duplicate legacy enable: %v", err)
	}
	refs := duplicateLegacy.DeprecatedReferences()
	if len(refs) != 1 || refs[0].Code != "LOP-FEAT-0003" || refs[0].Name != "swift-carthage-preview" || refs[0].Replacement != "swift-carthage" {
		t.Fatalf("expected duplicate legacy refs to be deduped, got %#v", refs)
	}
	refs[0].Name = "mutated"
	if got := duplicateLegacy.DeprecatedReferences(); got[0].Name != "swift-carthage-preview" {
		t.Fatalf("expected deprecated refs to return a copy, got %#v", got)
	}
}

func assertGraduatedDefaults(t *testing.T, registry *Registry, channel Channel) {
	t.Helper()
	resolved, err := registry.Resolve(ResolveOptions{Channel: channel})
	if err != nil {
		t.Fatalf("resolve %s defaults: %v", channel, err)
	}
	for _, name := range defaultGraduatedFeatureNames() {
		if !resolved.Enabled(name) {
			t.Fatalf("expected %s default-on in %s channel", name, channel)
		}
	}
}

func defaultGraduatedFeatureNames() []string {
	return []string{
		"dart-source-attribution",
		"lockfile-drift-ecosystem-expansion",
		"swift-carthage",
		"powershell-adapter",
		"go-vendored-provenance",
		"baseline-provenance-runtime-context",
		"vscode-multi-root-workflows",
		"mcp-server",
	}
}

func TestDefaultRegistryLegacyPreviewNamesResolve(t *testing.T) {
	registry := DefaultRegistry()
	dev, err := registry.Resolve(ResolveOptions{Channel: ChannelDev})
	if err != nil {
		t.Fatalf("resolve dev defaults: %v", err)
	}
	for _, name := range []string{"dart-source-attribution-preview", "swift-carthage-preview", "mcp-server-preview"} {
		if !dev.Enabled(name) {
			t.Fatalf("expected legacy name %s to resolve in dev channel", name)
		}
	}
}

func TestEnabledCodes(t *testing.T) {
	registry := testRegistry(t)
	resolved, err := registry.Resolve(ResolveOptions{
		Channel: ChannelRelease,
		Enable:  []string{"preview-flag"},
	})
	if err != nil {
		t.Fatalf("resolve enabled codes: %v", err)
	}
	if got := resolved.EnabledCodes(); !reflect.DeepEqual(got, []string{"LOP-FEAT-0001", "LOP-FEAT-0002"}) {
		t.Fatalf("expected sorted enabled feature codes, got %#v", got)
	}

	resolved, err = registry.Resolve(ResolveOptions{
		Channel: ChannelDev,
		Disable: []string{"stable-flag"},
	})
	if err != nil {
		t.Fatalf("resolve disabled codes: %v", err)
	}
	if got := resolved.EnabledCodes(); len(got) != 0 {
		t.Fatalf("expected no enabled feature codes after disable override, got %#v", got)
	}

	var empty *Set
	if got := empty.EnabledCodes(); len(got) != 0 {
		t.Fatalf("expected nil set enabled codes to be empty, got %#v", got)
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
			Code:               "LOP-FEAT-0002",
			Name:               "stable-flag",
			DeprecatedNames:    []string{"legacy-stable-flag"},
			Description:        "Stable behavior",
			Lifecycle:          LifecycleStable,
			FirstStableRelease: "v1.5.0",
		},
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func assertDefaultFlag(t *testing.T, flags []Flag, name, code string) {
	t.Helper()
	for _, flag := range flags {
		if flag.Name == name {
			if flag.Code != code {
				t.Fatalf("expected default flag %s to use code %s, got %#v", name, code, flag)
			}
			return
		}
	}
	t.Fatalf("expected default registry to include %s, got %#v", name, flags)
}

func assertDefaultFlagRelease(t *testing.T, flags []Flag, name, code, release string) {
	t.Helper()
	for _, flag := range flags {
		if flag.Name == name {
			if flag.Code != code {
				t.Fatalf("expected default flag %s to use code %s, got %#v", name, code, flag)
			}
			if flag.FirstStableRelease != release {
				t.Fatalf("expected default flag %s to record first stable release %s, got %#v", name, release, flag)
			}
			return
		}
	}
	t.Fatalf("expected default registry to include %s, got %#v", name, flags)
}

func assertDefaultLookup(t *testing.T, ref, canonicalName string, deprecated bool) {
	t.Helper()
	result, ok := DefaultRegistry().LookupReference(ref)
	if !ok {
		t.Fatalf("expected default registry lookup for %s to succeed", ref)
	}
	if result.Flag.Name != canonicalName || result.Deprecated != deprecated {
		t.Fatalf("unexpected lookup for %s: %#v", ref, result)
	}
	if deprecated && result.ReplacementRef != canonicalName {
		t.Fatalf("expected deprecated lookup for %s to point at %s, got %#v", ref, canonicalName, result)
	}
}

func assertManifestFlag(t *testing.T, manifest []ManifestEntry, name string, enabled bool) {
	t.Helper()
	for _, entry := range manifest {
		if entry.Name == name {
			if entry.EnabledByDefault != enabled {
				t.Fatalf("expected manifest entry %s enabled=%v, got %#v", name, enabled, entry)
			}
			return
		}
	}
	t.Fatalf("expected manifest to include %s, got %#v", name, manifest)
}

func expectedNextDefaultCode(t *testing.T) string {
	t.Helper()
	maxSuffix := 0
	for _, flag := range DefaultRegistry().Flags() {
		suffix, err := strconv.Atoi(strings.TrimPrefix(flag.Code, featureCodePrefix))
		if err != nil {
			t.Fatalf("parse default feature code %s: %v", flag.Code, err)
		}
		if suffix > maxSuffix {
			maxSuffix = suffix
		}
	}
	return fmt.Sprintf("%s%04d", featureCodePrefix, maxSuffix+1)
}
