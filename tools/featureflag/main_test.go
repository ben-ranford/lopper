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
	} {
		assertRunErrorContains(t, tc.name, tc.args, tc.want)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "bad add flag", args: []string{"add", "--definitely-not-a-flag"}},
		{name: "bad graduate flag", args: []string{"graduate", "--definitely-not-a-flag"}},
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
	if len(manifest) == 0 {
		t.Fatalf("expected embedded manifest entries, got %#v", manifest)
	}
	if manifest[0].Name != "dart-source-attribution-preview" || manifest[0].EnabledByDefault {
		t.Fatalf("expected dart-source-attribution-preview default-off in release channel, got %#v", manifest[0])
	}
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
