package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const graduateFeatureCatalog = `[
  {
    "code": "LOP-FEAT-0001",
    "name": "preview-flag",
    "description": "Preview behavior",
    "lifecycle": "preview"
  },
  {
    "code": "LOP-FEAT-0002",
    "name": "stable-flag",
    "description": "Stable behavior",
    "lifecycle": "stable"
  }
]`

func TestRunAddFeatureFlag(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "internal", "featureflags")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("mkdir catalog dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(catalogDir, "features.json"), `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing",
    "lifecycle": "preview"
  }
]`)
	t.Chdir(root)

	if err := run([]string{"add", "--name", "new-flag", "--description", "New behavior"}); err != nil {
		t.Fatalf("run add: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(catalogDir, "features.json"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	flags, err := featureflags.ParseCatalog(data)
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	if len(flags) != 2 || flags[1].Code != "LOP-FEAT-0002" || flags[1].Name != "new-flag" || flags[1].Lifecycle != featureflags.LifecyclePreview {
		t.Fatalf("unexpected generated feature catalog: %#v", flags)
	}
}

func TestRunGraduateFeatureFlag(t *testing.T) {
	for _, ref := range []string{"preview-flag", "LOP-FEAT-0001"} {
		assertGraduateFeature(t, ref)
	}
}

func TestRunFeatureFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "missing command", args: nil, want: "usage"},
		{name: "unknown command", args: []string{"nope"}, want: "unknown"},
		{name: "missing add name", args: []string{"add"}, want: "name is required"},
		{name: "extra add argument", args: []string{"add", "--name", "new-flag", "extra"}, want: "too many arguments"},
		{name: "missing graduate feature", args: []string{"graduate"}, want: "feature code or name is required"},
		{name: "extra graduate argument", args: []string{"graduate", "--feature", "preview-flag", "extra"}, want: "too many arguments"},
		{name: "missing previous catalog", args: []string{"pr-enforce", "--pr-title", "feat(flags): add registry"}, want: "previous feature catalog is required"},
		{name: "extra pr-enforce argument", args: []string{"pr-enforce", "--previous-catalog", "previous.json", "extra"}, want: "too many arguments"},
		{name: "missing release version", args: []string{"release-pr-comment"}, want: "release version is required"},
		{name: "extra release-pr-comment argument", args: []string{"release-pr-comment", "--release", "v1.5.0", "extra"}, want: "too many arguments"},
	} {
		assertRunErrorContains(t, tc.name, tc.args, tc.want)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "bad add flag", args: []string{"add", "--definitely-not-a-flag"}},
		{name: "bad graduate flag", args: []string{"graduate", "--definitely-not-a-flag"}},
		{name: "bad pr-enforce flag", args: []string{"pr-enforce", "--definitely-not-a-flag"}},
		{name: "bad release-pr-comment flag", args: []string{"release-pr-comment", "--definitely-not-a-flag"}},
	} {
		assertRunError(t, tc.name, tc.args)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "validate", args: []string{"validate"}},
		{name: "manifest", args: []string{"manifest"}},
		{name: "report", args: []string{"report"}},
	} {
		assertRunOK(t, tc.name, tc.args)
	}
}

func TestRunGraduateFeatureFlagRejectsBadState(t *testing.T) {
	root := t.TempDir()
	writeFeatureCatalog(t, root, graduateFeatureCatalog)
	t.Chdir(root)

	if err := run([]string{"graduate", "--feature", "missing-flag"}); err == nil || !strings.Contains(err.Error(), "unknown feature") {
		t.Fatalf("expected unknown feature error, got %v", err)
	}
	if err := run([]string{"graduate", "--feature", "stable-flag"}); err == nil || !strings.Contains(err.Error(), "already stable") {
		t.Fatalf("expected already stable error, got %v", err)
	}
}

func TestRunGraduateFeatureFlagRejectsBadCatalogState(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := run([]string{"graduate", "--feature", "preview-flag"}); err == nil || !strings.Contains(err.Error(), "read feature catalog") {
		t.Fatalf("expected missing catalog error, got %v", err)
	}

	root := t.TempDir()
	writeFeatureCatalog(t, root, `[]`)
	t.Chdir(root)

	oldNewRegistry := newRegistryFn
	t.Cleanup(func() { newRegistryFn = oldNewRegistry })
	newRegistryFn = func([]featureflags.Flag) (*featureflags.Registry, error) {
		return nil, errors.New("registry failed")
	}
	if err := run([]string{"graduate", "--feature", "preview-flag"}); err == nil || !strings.Contains(err.Error(), "registry failed") {
		t.Fatalf("expected injected registry error, got %v", err)
	}

	newRegistryFn = func([]featureflags.Flag) (*featureflags.Registry, error) {
		return featureflags.NewRegistry([]featureflags.Flag{
			{Code: "LOP-FEAT-0001", Name: "preview-flag", Lifecycle: featureflags.LifecyclePreview},
		})
	}
	if err := run([]string{"graduate", "--feature", "preview-flag"}); err == nil || !strings.Contains(err.Error(), "missing from the catalog") {
		t.Fatalf("expected missing catalog target error, got %v", err)
	}
}

func TestRunAddFeatureFlagCodeSpaceExhausted(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "internal", "featureflags")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("mkdir catalog dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(catalogDir, "features.json"), `[
  {
    "code": "LOP-FEAT-9999",
    "name": "last-flag",
    "description": "Last",
    "lifecycle": "preview"
  }
]`)
	t.Chdir(root)

	if err := run([]string{"add", "--name", "new-flag"}); err == nil || !strings.Contains(err.Error(), "feature code space exhausted") {
		t.Fatalf("expected code space exhausted error, got %v", err)
	}
}

func TestRunAddFeatureFlagRejectsBadCatalogState(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "internal", "featureflags")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("mkdir catalog dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(catalogDir, "features.json"), `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing",
    "lifecycle": "preview"
  }
]`)
	t.Chdir(root)

	if err := run([]string{"add", "--name", "existing-flag"}); err == nil || !strings.Contains(err.Error(), "duplicate feature name") {
		t.Fatalf("expected duplicate feature name error, got %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(catalogDir, "features.json"), `not-json`)
	if err := run([]string{"add", "--name", "new-flag"}); err == nil || !strings.Contains(err.Error(), "invalid feature catalog JSON") {
		t.Fatalf("expected invalid catalog error, got %v", err)
	}
}

func TestRunAddFeatureFlagGetwdAndWriteErrors(t *testing.T) {
	oldGetwd := getwdFn
	getwdFn = func() (string, error) { return "", errors.New("cwd failed") }
	if err := run([]string{"add", "--name", "new-flag"}); err == nil || !strings.Contains(err.Error(), "resolve working directory") {
		t.Fatalf("expected getwd error, got %v", err)
	}
	getwdFn = oldGetwd

	root := t.TempDir()
	catalogDir := filepath.Join(root, "internal", "featureflags")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("mkdir catalog dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(catalogDir, "features.json"), `[]`)
	t.Chdir(root)

	oldWrite := writeFileUnderFn
	writeFileUnderFn = func(string, string, []byte, os.FileMode) error {
		return errors.New("write failed")
	}
	t.Cleanup(func() {
		getwdFn = oldGetwd
		writeFileUnderFn = oldWrite
	})
	if err := run([]string{"add", "--name", "new-flag"}); err == nil || !strings.Contains(err.Error(), "write feature catalog") {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestRunGraduateFeatureFlagGetwdAndWriteErrors(t *testing.T) {
	oldGetwd := getwdFn
	getwdFn = func() (string, error) { return "", errors.New("cwd failed") }
	if err := run([]string{"graduate", "--feature", "preview-flag"}); err == nil || !strings.Contains(err.Error(), "resolve working directory") {
		t.Fatalf("expected getwd error, got %v", err)
	}
	getwdFn = oldGetwd

	root := t.TempDir()
	writeFeatureCatalog(t, root, `[
  {
    "code": "LOP-FEAT-0001",
    "name": "preview-flag",
    "description": "Preview behavior",
    "lifecycle": "preview"
  }
]`)
	t.Chdir(root)

	oldWrite := writeFileUnderFn
	writeFileUnderFn = func(string, string, []byte, os.FileMode) error {
		return errors.New("write failed")
	}
	t.Cleanup(func() {
		getwdFn = oldGetwd
		writeFileUnderFn = oldWrite
	})
	if err := run([]string{"graduate", "--feature", "preview-flag"}); err == nil || !strings.Contains(err.Error(), "write feature catalog") {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestRunAddFeatureFlagRegistryError(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "internal", "featureflags")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("mkdir catalog dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(catalogDir, "features.json"), `[]`)
	t.Chdir(root)

	oldNewRegistry := newRegistryFn
	newRegistryFn = func([]featureflags.Flag) (*featureflags.Registry, error) {
		return nil, errors.New("registry failed")
	}
	t.Cleanup(func() { newRegistryFn = oldNewRegistry })
	if err := run([]string{"add", "--name", "new-flag"}); err == nil || !strings.Contains(err.Error(), "registry failed") {
		t.Fatalf("expected injected registry error, got %v", err)
	}
}

func TestRunAddFeatureFlagMissingCatalog(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := run([]string{"add", "--name", "new-flag"}); err == nil || !strings.Contains(err.Error(), "read feature catalog") {
		t.Fatalf("expected missing catalog error, got %v", err)
	}
}

func TestRunValidateAndManifestInjectedErrors(t *testing.T) {
	oldValidate := validateDefaultRegistryFn
	oldValidateLocks := validateDefaultReleaseLocksFn
	oldManifest := manifestEntriesFn
	oldFormat := formatManifestFn
	defer func() {
		validateDefaultRegistryFn = oldValidate
		validateDefaultReleaseLocksFn = oldValidateLocks
		manifestEntriesFn = oldManifest
		formatManifestFn = oldFormat
	}()

	validateDefaultRegistryFn = func() error { return errors.New("invalid registry") }
	if err := run([]string{"validate"}); err == nil || !strings.Contains(err.Error(), "invalid registry") {
		t.Fatalf("expected injected validate error, got %v", err)
	}
	if err := run([]string{"manifest"}); err == nil || !strings.Contains(err.Error(), "invalid registry") {
		t.Fatalf("expected injected manifest validation error, got %v", err)
	}

	validateDefaultRegistryFn = oldValidate
	validateDefaultReleaseLocksFn = func() error { return errors.New("invalid locks") }
	if err := run([]string{"validate"}); err == nil || !strings.Contains(err.Error(), "invalid locks") {
		t.Fatalf("expected injected release lock validation error, got %v", err)
	}
	if err := run([]string{"manifest"}); err == nil || !strings.Contains(err.Error(), "invalid locks") {
		t.Fatalf("expected injected manifest lock validation error, got %v", err)
	}

	validateDefaultReleaseLocksFn = oldValidateLocks
	manifestEntriesFn = func(*featureflags.Registry, featureflags.Channel, string) ([]featureflags.ManifestEntry, error) {
		return nil, errors.New("manifest failed")
	}
	if err := run([]string{"manifest"}); err == nil || !strings.Contains(err.Error(), "manifest failed") {
		t.Fatalf("expected injected manifest error, got %v", err)
	}
	manifestEntriesFn = oldManifest
	formatManifestFn = func([]featureflags.ManifestEntry) ([]byte, error) {
		return nil, errors.New("format failed")
	}
	if err := run([]string{"manifest"}); err == nil || !strings.Contains(err.Error(), "format failed") {
		t.Fatalf("expected injected manifest format error, got %v", err)
	}
}

func TestManifestEntries(t *testing.T) {
	manifest, err := manifestEntries(featureflags.DefaultRegistry(), featureflags.ChannelRelease, "")
	if err != nil {
		t.Fatalf("release manifest: %v", err)
	}
	if len(manifest) < 4 {
		t.Fatalf("expected embedded manifest entries, got %#v", manifest)
	}
	assertManifestEntryDefault(t, manifest, "dart-source-attribution-preview", false)
	assertManifestEntryDefault(t, manifest, "lockfile-drift-ecosystem-expansion-preview", false)
	assertManifestEntryDefault(t, manifest, "swift-carthage-preview", false)
	assertManifestEntryDefault(t, manifest, "powershell-adapter-preview", false)
	assertManifestEntryDefault(t, manifest, "go-vendored-provenance-preview", false)
}

func TestRunManifestAndReportUseChannels(t *testing.T) {
	registry := testRegistry(t)
	oldValidate := validateDefaultRegistryFn
	oldValidateLocks := validateDefaultReleaseLocksFn
	oldDefaultRegistry := defaultRegistryFn
	t.Cleanup(func() {
		validateDefaultRegistryFn = oldValidate
		validateDefaultReleaseLocksFn = oldValidateLocks
		defaultRegistryFn = oldDefaultRegistry
	})
	validateDefaultRegistryFn = func() error { return nil }
	validateDefaultReleaseLocksFn = func() error { return nil }
	defaultRegistryFn = func() *featureflags.Registry { return registry }
	root := t.TempDir()
	t.Chdir(root)

	manifestOutput, err := captureStdout(t, func() error {
		return run([]string{"manifest", "--channel", "rolling"})
	})
	if err != nil {
		t.Fatalf("run rolling manifest: %v", err)
	}
	if !strings.Contains(manifestOutput, `"enabledByDefault": true`) {
		t.Fatalf("expected rolling manifest to enable preview flag, got %s", manifestOutput)
	}

	previousCatalog := "previous-features.json"
	testutil.MustWriteFile(t, previousCatalog, `[
		{"code":"LOP-FEAT-0002","name":"stable-flag","lifecycle":"stable"}
	]`)
	reportOutput, err := captureStdout(t, func() error {
		return run([]string{"report", "--channel", "release", "--release", "v1.4.2", "--previous-catalog", previousCatalog})
	})
	if err != nil {
		t.Fatalf("run feature report: %v", err)
	}
	for _, want := range []string{"Stable by default", "Preview available by opt-in", "Newly added preview flags", "LOP-FEAT-0001"} {
		if !strings.Contains(reportOutput, want) {
			t.Fatalf("expected report to contain %q, got %s", want, reportOutput)
		}
	}

	rollingReport, err := captureStdout(t, func() error {
		return run([]string{"report", "--channel", "rolling"})
	})
	if err != nil {
		t.Fatalf("run rolling report: %v", err)
	}
	if !strings.Contains(rollingReport, "Preview enabled by rolling channel") {
		t.Fatalf("expected rolling report preview section, got %s", rollingReport)
	}
}

func TestRunManifestAndReportRejectBadInputs(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := run([]string{"manifest", "--channel", "bad"}); err == nil || !strings.Contains(err.Error(), "invalid feature build channel") {
		t.Fatalf("expected invalid channel error, got %v", err)
	}
	if err := run([]string{"manifest", "--channel"}); err == nil {
		t.Fatalf("expected manifest flag parse error")
	}
	if err := run([]string{"manifest", "extra"}); err == nil || !strings.Contains(err.Error(), "too many arguments") {
		t.Fatalf("expected manifest extra argument error, got %v", err)
	}
	if err := run([]string{"report", "--channel"}); err == nil {
		t.Fatalf("expected report flag parse error")
	}
	if err := run([]string{"report", "--previous-catalog", filepath.Join(t.TempDir(), "missing.json")}); err == nil || !strings.Contains(err.Error(), "read previous feature catalog") {
		t.Fatalf("expected missing previous catalog error, got %v", err)
	}
	badCatalog := "bad-features.json"
	testutil.MustWriteFile(t, badCatalog, `not-json`)
	if err := run([]string{"report", "--previous-catalog", badCatalog}); err == nil || !strings.Contains(err.Error(), "parse previous feature catalog") {
		t.Fatalf("expected invalid previous catalog error, got %v", err)
	}
}

func TestRunReportInjectedErrors(t *testing.T) {
	oldValidate := validateDefaultRegistryFn
	oldValidateLocks := validateDefaultReleaseLocksFn
	oldManifest := manifestEntriesFn
	oldGetwd := getwdFn
	t.Cleanup(func() {
		validateDefaultRegistryFn = oldValidate
		validateDefaultReleaseLocksFn = oldValidateLocks
		manifestEntriesFn = oldManifest
		getwdFn = oldGetwd
	})
	validateDefaultRegistryFn = func() error { return nil }
	validateDefaultReleaseLocksFn = func() error { return nil }

	validateDefaultRegistryFn = func() error { return errors.New("report registry invalid") }
	if err := run([]string{"report"}); err == nil || !strings.Contains(err.Error(), "report registry invalid") {
		t.Fatalf("expected injected report registry error, got %v", err)
	}
	validateDefaultRegistryFn = func() error { return nil }
	validateDefaultReleaseLocksFn = func() error { return errors.New("report locks invalid") }
	if err := run([]string{"report"}); err == nil || !strings.Contains(err.Error(), "report locks invalid") {
		t.Fatalf("expected injected report locks error, got %v", err)
	}
	validateDefaultReleaseLocksFn = func() error { return nil }

	manifestEntriesFn = func(*featureflags.Registry, featureflags.Channel, string) ([]featureflags.ManifestEntry, error) {
		return nil, errors.New("report manifest failed")
	}
	if err := run([]string{"report"}); err == nil || !strings.Contains(err.Error(), "report manifest failed") {
		t.Fatalf("expected injected report manifest error, got %v", err)
	}

	manifestEntriesFn = oldManifest
	getwdFn = func() (string, error) { return "", errors.New("cwd failed") }
	if err := run([]string{"report", "--previous-catalog", "previous.json"}); err == nil || !strings.Contains(err.Error(), "resolve working directory") {
		t.Fatalf("expected previous catalog getwd error, got %v", err)
	}
}

func TestManifestEntriesReleaseLockError(t *testing.T) {
	oldReleaseLockProvider := releaseLockProviderFn
	t.Cleanup(func() { releaseLockProviderFn = oldReleaseLockProvider })
	releaseLockProviderFn = func(string) (*featureflags.ReleaseLock, error) {
		return nil, errors.New("lock failed")
	}
	if _, err := manifestEntries(testRegistry(t), featureflags.ChannelRelease, "v1.4.2"); err == nil || !strings.Contains(err.Error(), "lock failed") {
		t.Fatalf("expected release lock provider error, got %v", err)
	}
}

func TestFormatReportReleaseLockSection(t *testing.T) {
	registry := testRegistry(t)
	manifest, err := registry.Manifest(featureflags.ResolveOptions{
		Channel: featureflags.ChannelRelease,
		Lock:    &featureflags.ReleaseLock{Release: "v1.4.2", DefaultOn: []string{"preview-flag"}},
	})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	output := formatReport(featureflags.ChannelRelease, "v1.4.2", registry.Flags(), manifest, registry.Flags(), true)
	if !strings.Contains(output, "Preview locked default-on for this release") || !strings.Contains(output, "Preview behavior") {
		t.Fatalf("expected release lock report section, got %s", output)
	}
	if !strings.Contains(output, "None.") {
		t.Fatalf("expected no newly added preview flags, got %s", output)
	}
}

func TestRunPREnforceFeaturePRRequiresNewFlag(t *testing.T) {
	root := t.TempDir()
	writeFeatureCatalog(t, root, `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  }
]`)
	previousCatalog := "previous-features.json"
	testutil.MustWriteFile(t, filepath.Join(root, previousCatalog), `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  }
]`)
	t.Chdir(root)

	output, err := captureStdout(t, func() error {
		return run([]string{"pr-enforce", "--pr-title", "feat(runtime): add new feature", "--previous-catalog", previousCatalog})
	})
	if err == nil || !strings.Contains(err.Error(), "must add at least one new feature flag") {
		t.Fatalf("expected missing feature flag enforcement error, got %v", err)
	}
	if !strings.Contains(output, "Check: failed") || !strings.Contains(output, "New feature flags in this PR") {
		t.Fatalf("expected failure report, got %s", output)
	}
}

func TestRunPREnforceRejectsStableAddedFlag(t *testing.T) {
	root := t.TempDir()
	writeFeatureCatalog(t, root, `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  },
  {
    "code": "LOP-FEAT-0002",
    "name": "new-flag",
    "description": "New behavior",
    "lifecycle": "stable"
  }
]`)
	previousCatalog := "previous-features.json"
	testutil.MustWriteFile(t, filepath.Join(root, previousCatalog), `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  }
]`)
	t.Chdir(root)

	output, err := captureStdout(t, func() error {
		return run([]string{"pr-enforce", "--pr-title", "chore(ci): add workflow", "--previous-catalog", previousCatalog})
	})
	if err == nil || !strings.Contains(err.Error(), "must start as `preview`") {
		t.Fatalf("expected preview lifecycle enforcement error, got %v", err)
	}
	if !strings.Contains(output, "`LOP-FEAT-0002` `new-flag` (`stable`)") {
		t.Fatalf("expected stable flag to be listed in report, got %s", output)
	}
}

func TestRunPREnforceRejectsDuplicateFeatureFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		catalog string
		want    string
	}{
		{
			name: "duplicate code",
			catalog: `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  },
  {
    "code": "LOP-FEAT-0002",
    "name": "first-new-flag",
    "description": "New behavior",
    "lifecycle": "preview"
  },
  {
    "code": "LOP-FEAT-0002",
    "name": "second-new-flag",
    "description": "Other behavior",
    "lifecycle": "preview"
  }
]`,
			want: "Feature flag ids (`code`) must be unique: `LOP-FEAT-0002`",
		},
		{
			name: "duplicate name",
			catalog: `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  },
  {
    "code": "LOP-FEAT-0002",
    "name": "duplicate-new-flag",
    "description": "New behavior",
    "lifecycle": "preview"
  },
  {
    "code": "LOP-FEAT-0003",
    "name": "duplicate-new-flag",
    "description": "Other behavior",
    "lifecycle": "preview"
  }
]`,
			want: "Feature flag names must be unique: `duplicate-new-flag`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFeatureCatalog(t, root, tc.catalog)
			previousCatalog := "previous-features.json"
			testutil.MustWriteFile(t, filepath.Join(root, previousCatalog), `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  }
]`)
			t.Chdir(root)

			output, err := captureStdout(t, func() error {
				return run([]string{"pr-enforce", "--pr-title", "feat(flags): add registry", "--previous-catalog", previousCatalog})
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected duplicate feature flag enforcement error containing %q, got %v", tc.want, err)
			}
			if !strings.Contains(output, "Check: failed") || !strings.Contains(output, tc.want) {
				t.Fatalf("expected duplicate feature flag failure report containing %q, got %s", tc.want, output)
			}
		})
	}
}

func TestReadCurrentCatalogForPREnforcementFallbacks(t *testing.T) {
	root := t.TempDir()
	writeFeatureCatalog(t, root, `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  }
]`)
	flags, violations, err := readCurrentCatalogForPREnforcement(root)
	if err != nil {
		t.Fatalf("expected valid catalog to read, got %v", err)
	}
	if len(flags) != 1 || flags[0].Code != "LOP-FEAT-0001" || len(violations) != 0 {
		t.Fatalf("unexpected valid catalog result: flags=%#v violations=%#v", flags, violations)
	}

	writeFeatureCatalog(t, root, `[
  {
    "code": "LOP-FEAT-0002",
    "name": "first-flag",
    "description": "First flag",
    "lifecycle": "preview"
  },
  {
    "code": "LOP-FEAT-0002",
    "name": "named-flag",
    "description": "Duplicate code",
    "lifecycle": "preview"
  }
]`)
	flags, violations, err = readCurrentCatalogForPREnforcement(root)
	if err != nil {
		t.Fatalf("expected duplicate catalog to be converted to violations, got %v", err)
	}
	if len(flags) != 0 || len(violations) != 1 {
		t.Fatalf("unexpected duplicate catalog result: flags=%#v violations=%#v", flags, violations)
	}
	if !strings.Contains(violations[0], "`first-flag`") || !strings.Contains(violations[0], "`named-flag`") {
		t.Fatalf("expected duplicate violation to include colliding refs, got %#v", violations)
	}

	writeFeatureCatalog(t, root, `[
  {
    "code": "bad",
    "name": "unique-flag",
    "description": "Invalid but not duplicate",
    "lifecycle": "preview"
  }
]`)
	if _, _, err := readCurrentCatalogForPREnforcement(root); err == nil || !strings.Contains(err.Error(), "invalid feature code") {
		t.Fatalf("expected non-duplicate catalog parse error, got %v", err)
	}

	writeFeatureCatalog(t, root, `[
  {
    "code": "LOP-FEAT-0004",
    "name": "bad-lifecycle",
    "description": "Invalid lifecycle",
    "lifecycle": "not-a-lifecycle"
  },
  {
    "code": "LOP-FEAT-0004",
    "name": "duplicate-code",
    "description": "Duplicate code",
    "lifecycle": "preview"
  }
]`)
	if _, _, err := readCurrentCatalogForPREnforcement(root); err == nil || !strings.Contains(err.Error(), "invalid feature lifecycle") {
		t.Fatalf("expected invalid lifecycle to take precedence over duplicate fallback, got %v", err)
	}
}

func TestEvaluatePREnforcementSkipsAddedFlagChecksForCatalogViolations(t *testing.T) {
	current := []featureflags.Flag{{Code: "LOP-FEAT-0002", Name: "new-stable", Lifecycle: featureflags.LifecycleStable}}
	catalogViolations := []string{"Feature flag ids (`code`) must be unique: `LOP-FEAT-0002`."}
	result := evaluatePREnforcement("feat(flags): add registry", current, nil, catalogViolations)
	if len(result.AddedFlags) != 0 || len(result.InvalidAddedFlags) != 0 {
		t.Fatalf("expected catalog violations to short-circuit added flag checks, got %#v", result)
	}
	if violations := result.violations(); len(violations) != 1 || !strings.Contains(violations[0], "must be unique") {
		t.Fatalf("expected only catalog violation, got %#v", violations)
	}
}

func TestDuplicateFeatureFlagViolationsFormatsMissingRefs(t *testing.T) {
	violations := duplicateFeatureFlagViolations([]featureflags.Flag{
		{Code: "LOP-FEAT-0001"},
		{Code: "LOP-FEAT-0001", Name: "named-flag"},
	})
	if len(violations) != 1 || !strings.Contains(violations[0], "`<missing>`") || !strings.Contains(violations[0], "`named-flag`") {
		t.Fatalf("expected missing and named refs in duplicate violation, got %#v", violations)
	}
}

func TestIsDuplicateFeatureCatalogError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{err: nil, want: false},
		{err: errors.New("duplicate feature code: LOP-FEAT-0001"), want: true},
		{err: errors.New("duplicate feature name: preview-flag"), want: true},
		{err: errors.New("invalid feature lifecycle: bad"), want: false},
	} {
		if got := isDuplicateFeatureCatalogError(tc.err); got != tc.want {
			t.Fatalf("isDuplicateFeatureCatalogError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestDecodeFeatureCatalogRejectsInvalidInput(t *testing.T) {
	if _, err := decodeFeatureCatalog([]byte(`not-json`)); err == nil || !strings.Contains(err.Error(), "invalid feature catalog JSON") {
		t.Fatalf("expected invalid JSON error, got %v", err)
	}
	if _, err := decodeFeatureCatalog([]byte(`[] []`)); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("expected multiple JSON values error, got %v", err)
	}
}

func TestRunPREnforceReportsAddedPreviewFlag(t *testing.T) {
	root := t.TempDir()
	writeFeatureCatalog(t, root, `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  },
  {
    "code": "LOP-FEAT-0002",
    "name": "new-flag",
    "description": "New behavior",
    "lifecycle": "preview"
  }
]`)
	previousCatalog := "previous-features.json"
	testutil.MustWriteFile(t, filepath.Join(root, previousCatalog), `[
  {
    "code": "LOP-FEAT-0001",
    "name": "existing-flag",
    "description": "Existing behavior",
    "lifecycle": "preview"
  }
]`)
	t.Chdir(root)

	output, err := captureStdout(t, func() error {
		return run([]string{"pr-enforce", "--pr-title", "feat(vscode): add preview workflow", "--previous-catalog", previousCatalog})
	})
	if err != nil {
		t.Fatalf("expected preview feature flag enforcement success, got %v", err)
	}
	for _, want := range []string{"Check: passed", "`LOP-FEAT-0002` `new-flag` (`preview`)", "Passed. This feature PR adds at least one new preview feature flag."} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected report to contain %q, got %s", want, output)
		}
	}
}

func TestRunPREnforceGetwdAndCatalogErrors(t *testing.T) {
	oldGetwd := getwdFn
	getwdFn = func() (string, error) { return "", errors.New("cwd failed") }
	if err := run([]string{"pr-enforce", "--pr-title", "feat(ci): add gate", "--previous-catalog", "previous.json"}); err == nil || !strings.Contains(err.Error(), "resolve working directory") {
		t.Fatalf("expected getwd error, got %v", err)
	}
	getwdFn = oldGetwd

	root := t.TempDir()
	t.Chdir(root)
	testutil.MustWriteFile(t, filepath.Join(root, "previous-features.json"), `[]`)
	if err := run([]string{"pr-enforce", "--pr-title", "feat(ci): add gate", "--previous-catalog", "previous-features.json"}); err == nil || !strings.Contains(err.Error(), "read feature catalog") {
		t.Fatalf("expected current catalog read error, got %v", err)
	}

	writeFeatureCatalog(t, root, `[]`)
	testutil.MustWriteFile(t, filepath.Join(root, "bad-previous.json"), `not-json`)
	if err := run([]string{"pr-enforce", "--pr-title", "feat(ci): add gate", "--previous-catalog", "bad-previous.json"}); err == nil || !strings.Contains(err.Error(), "parse previous feature catalog") {
		t.Fatalf("expected previous catalog parse error, got %v", err)
	}
}

func TestRunReleasePRComment(t *testing.T) {
	oldValidate := validateDefaultRegistryFn
	oldValidateLocks := validateDefaultReleaseLocksFn
	oldDefaultRegistry := defaultRegistryFn
	oldReleaseLockProvider := releaseLockProviderFn
	t.Cleanup(func() {
		validateDefaultRegistryFn = oldValidate
		validateDefaultReleaseLocksFn = oldValidateLocks
		defaultRegistryFn = oldDefaultRegistry
		releaseLockProviderFn = oldReleaseLockProvider
	})
	validateDefaultRegistryFn = func() error { return nil }
	validateDefaultReleaseLocksFn = func() error { return nil }
	defaultRegistryFn = func() *featureflags.Registry { return testRegistry(t) }
	releaseLockProviderFn = func(string) (*featureflags.ReleaseLock, error) { return nil, nil }

	root := t.TempDir()
	t.Chdir(root)
	previousCatalog := "previous-features.json"
	testutil.MustWriteFile(t, filepath.Join(root, previousCatalog), `[
  {
    "code": "LOP-FEAT-0002",
    "name": "stable-flag",
    "description": "Stable behavior",
    "lifecycle": "stable"
  }
]`)

	output, err := captureStdout(t, func() error {
		return run([]string{
			"release-pr-comment",
			"--pr-title", "chore(main): release 1.5.0",
			"--previous-catalog", previousCatalog,
			"--workflow-url", "https://example.com/graduate-feature",
		})
	})
	if err != nil {
		t.Fatalf("run release-pr-comment: %v", err)
	}
	for _, want := range []string{
		"<!-- lopper-feature-flag-release-pr -->",
		"This release PR is preparing `v1.5.0`.",
		"## Feature flags",
		"### Promotion options",
		"[`graduate-feature.yml`](https://example.com/graduate-feature)",
		"### Graduation candidates",
		"`LOP-FEAT-0001` `preview-flag`",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected release PR comment to contain %q, got %s", want, output)
		}
	}
}

func TestRunReleasePRCommentRejectsCatalogErrors(t *testing.T) {
	oldValidate := validateDefaultRegistryFn
	oldValidateLocks := validateDefaultReleaseLocksFn
	oldDefaultRegistry := defaultRegistryFn
	t.Cleanup(func() {
		validateDefaultRegistryFn = oldValidate
		validateDefaultReleaseLocksFn = oldValidateLocks
		defaultRegistryFn = oldDefaultRegistry
	})
	validateDefaultRegistryFn = func() error { return nil }
	validateDefaultReleaseLocksFn = func() error { return nil }
	defaultRegistryFn = func() *featureflags.Registry { return testRegistry(t) }

	root := t.TempDir()
	t.Chdir(root)
	if err := run([]string{"release-pr-comment", "--release", "v1.5.0", "--previous-catalog", "missing.json"}); err == nil || !strings.Contains(err.Error(), "read previous feature catalog") {
		t.Fatalf("expected missing previous catalog error, got %v", err)
	}

	badCatalog := "bad-features.json"
	testutil.MustWriteFile(t, filepath.Join(root, badCatalog), `not-json`)
	if err := run([]string{"release-pr-comment", "--release", "v1.5.0", "--previous-catalog", badCatalog}); err == nil || !strings.Contains(err.Error(), "parse previous feature catalog") {
		t.Fatalf("expected bad previous catalog error, got %v", err)
	}
}

func TestRunReleasePRCommentRejectsInjectedErrors(t *testing.T) {
	oldValidate := validateDefaultRegistryFn
	oldValidateLocks := validateDefaultReleaseLocksFn
	oldDefaultRegistry := defaultRegistryFn
	oldManifestEntries := manifestEntriesFn
	t.Cleanup(func() {
		validateDefaultRegistryFn = oldValidate
		validateDefaultReleaseLocksFn = oldValidateLocks
		defaultRegistryFn = oldDefaultRegistry
		manifestEntriesFn = oldManifestEntries
	})

	root := t.TempDir()
	t.Chdir(root)
	previousCatalog := "previous-features.json"
	testutil.MustWriteFile(t, filepath.Join(root, previousCatalog), `[]`)

	validateDefaultRegistryFn = func() error { return errors.New("registry failed") }
	if err := run([]string{"release-pr-comment", "--release", "v1.5.0", "--previous-catalog", previousCatalog}); err == nil || !strings.Contains(err.Error(), "registry failed") {
		t.Fatalf("expected registry validation error, got %v", err)
	}

	validateDefaultRegistryFn = func() error { return nil }
	validateDefaultReleaseLocksFn = func() error { return errors.New("locks failed") }
	if err := run([]string{"release-pr-comment", "--release", "v1.5.0", "--previous-catalog", previousCatalog}); err == nil || !strings.Contains(err.Error(), "locks failed") {
		t.Fatalf("expected release lock validation error, got %v", err)
	}

	validateDefaultReleaseLocksFn = func() error { return nil }
	defaultRegistryFn = func() *featureflags.Registry { return testRegistry(t) }
	manifestEntriesFn = func(*featureflags.Registry, featureflags.Channel, string) ([]featureflags.ManifestEntry, error) {
		return nil, errors.New("manifest failed")
	}
	if err := run([]string{"release-pr-comment", "--release", "v1.5.0", "--previous-catalog", previousCatalog}); err == nil || !strings.Contains(err.Error(), "manifest failed") {
		t.Fatalf("expected manifest error, got %v", err)
	}

	if got := newlyAddedPreviewFlags(testRegistry(t).Flags(), nil, false); len(got) != 0 {
		t.Fatalf("expected no preview flags when not compared, got %#v", got)
	}
}

func TestFormatPREnforcementReportNonFeatureBranches(t *testing.T) {
	previewFlag := featureflags.Flag{
		Code:        "LOP-FEAT-0002",
		Name:        "new-flag",
		Description: "New behavior",
		Lifecycle:   featureflags.LifecyclePreview,
	}
	addedPreview := formatPREnforcementReport(prEnforcementResult{
		RequireFlag: false,
		AddedFlags:  []featureflags.Flag{previewFlag},
	})
	if !strings.Contains(addedPreview, "Passed. Added feature flags all start as `preview`.") {
		t.Fatalf("expected non-feature added preview success message, got %s", addedPreview)
	}

	noRequirement := formatPREnforcementReport(prEnforcementResult{})
	if !strings.Contains(noRequirement, "Passed. No new feature flag was required for this PR.") {
		t.Fatalf("expected no-requirement success message, got %s", noRequirement)
	}
}

func TestIsFeaturePRTitle(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{title: "feat: add preview workflow", want: true},
		{title: "feat(ci): add preview workflow", want: true},
		{title: "feat(ci)!: add preview workflow", want: true},
		{title: "fix(ci): repair workflow", want: false},
		{title: "refactor(ci): split workflow", want: false},
		{title: "", want: false},
	} {
		if got := isFeaturePRTitle(tc.title); got != tc.want {
			t.Fatalf("isFeaturePRTitle(%q) = %v, want %v", tc.title, got, tc.want)
		}
	}
}

func TestReleasePleaseVersionFromTitle(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  string
	}{
		{title: "chore(main): release 1.5.0", want: "v1.5.0"},
		{title: "chore(main): release v1.5.1-rc.1", want: "v1.5.1-rc.1"},
		{title: "feat(ci): add gate", want: ""},
	} {
		if got := releasePleaseVersionFromTitle(tc.title); got != tc.want {
			t.Fatalf("releasePleaseVersionFromTitle(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}

func TestFormatReleasePRCommentWithoutCandidates(t *testing.T) {
	registry := testRegistry(t)
	manifest, err := registry.Manifest(featureflags.ResolveOptions{Channel: featureflags.ChannelRelease})
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	output := formatReleasePRComment("v1.5.0", registry.Flags(), manifest, registry.Flags(), true, "")
	if !strings.Contains(output, "No newly added preview flags were detected for this release candidate.") {
		t.Fatalf("expected no-candidate note, got %s", output)
	}
	if strings.Contains(output, "### Graduation candidates") {
		t.Fatalf("did not expect graduation candidate section, got %s", output)
	}
}

func TestReadCatalog(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "internal", "featureflags")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("mkdir catalog dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(catalogDir, "features.json"), `[]`)
	flags, err := readCatalog(root)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	if len(flags) != 0 {
		t.Fatalf("expected empty catalog, got %#v", flags)
	}
}

func TestMainUsesExitFuncOnError(t *testing.T) {
	oldExit := exitFunc
	oldArgs := os.Args
	oldStderr := os.Stderr
	defer func() {
		exitFunc = oldExit
		os.Args = oldArgs
		os.Stderr = oldStderr
	}()

	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	os.Stderr = errW
	code := 0
	exitFunc = func(c int) { code = c }
	os.Args = []string{"featureflag"}

	main()
	if err := errW.Close(); err != nil {
		t.Fatalf("close stderr writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(errR); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
	if !strings.Contains(buf.String(), "usage") {
		t.Fatalf("expected usage error on stderr, got %q", buf.String())
	}
}

func testRegistry(t *testing.T) *featureflags.Registry {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{
		{Code: "LOP-FEAT-0001", Name: "preview-flag", Description: "Preview behavior", Lifecycle: featureflags.LifecyclePreview},
		{Code: "LOP-FEAT-0002", Name: "stable-flag", Description: "Stable behavior", Lifecycle: featureflags.LifecycleStable},
	})
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return registry
}

func assertGraduateFeature(t *testing.T, ref string) {
	t.Helper()
	t.Run(ref, func(t *testing.T) {
		root := t.TempDir()
		catalogDir := writeFeatureCatalog(t, root, graduateFeatureCatalog)
		t.Chdir(root)

		output, err := captureStdout(t, func() error {
			return run([]string{"graduate", "--feature", ref})
		})
		if err != nil {
			t.Fatalf("run graduate: %v", err)
		}
		flags := readFeatureCatalog(t, catalogDir)
		if flags[0].Code != "LOP-FEAT-0001" || flags[0].Lifecycle != featureflags.LifecycleStable {
			t.Fatalf("expected preview flag to graduate, got %#v", flags[0])
		}
		if !strings.Contains(output, "graduated LOP-FEAT-0001 preview-flag to stable") {
			t.Fatalf("expected graduation output, got %q", output)
		}
	})
}

func assertRunErrorContains(t *testing.T, name string, args []string, want string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("expected error containing %q, got %v", want, err)
		}
	})
}

func assertRunError(t *testing.T, name string, args []string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if err := run(args); err == nil {
			t.Fatalf("expected run error")
		}
	})
}

func assertRunOK(t *testing.T, name string, args []string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if err := run(args); err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
	})
}

func readFeatureCatalog(t *testing.T, catalogDir string) []featureflags.Flag {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(catalogDir, "features.json"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	flags, err := featureflags.ParseCatalog(data)
	if err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	return flags
}

func assertManifestEntryDefault(t *testing.T, manifest []featureflags.ManifestEntry, name string, enabled bool) {
	t.Helper()
	for _, entry := range manifest {
		if entry.Name == name {
			if entry.EnabledByDefault != enabled {
				t.Fatalf("expected %s enabledByDefault=%v, got %#v", name, enabled, entry)
			}
			return
		}
	}
	t.Fatalf("expected manifest entry for %s, got %#v", name, manifest)
}

func writeFeatureCatalog(t *testing.T, root, content string) string {
	t.Helper()
	catalogDir := filepath.Join(root, "internal", "featureflags")
	if err := os.MkdirAll(catalogDir, 0o755); err != nil {
		t.Fatalf("mkdir catalog dir: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(catalogDir, "features.json"), content)
	return catalogDir
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = writer
	runErr := fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return buf.String(), runErr
}
