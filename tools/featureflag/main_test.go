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

func TestRunFeatureFlagErrors(t *testing.T) {
	if err := run(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("expected usage error, got %v", err)
	}
	if err := run([]string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected unknown command error, got %v", err)
	}
	if err := run([]string{"add"}); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Fatalf("expected missing name error, got %v", err)
	}
	if err := run([]string{"add", "--name", "new-flag", "extra"}); err == nil || !strings.Contains(err.Error(), "too many arguments") {
		t.Fatalf("expected extra argument error, got %v", err)
	}
	if err := run([]string{"add", "--definitely-not-a-flag"}); err == nil {
		t.Fatalf("expected flag parse error")
	}
	if err := run([]string{"validate"}); err != nil {
		t.Fatalf("expected embedded registry validation to pass, got %v", err)
	}
	if err := run([]string{"manifest"}); err != nil {
		t.Fatalf("expected embedded manifest to render, got %v", err)
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
	oldManifest := manifestEntriesFn
	oldFormat := formatManifestFn
	defer func() {
		validateDefaultRegistryFn = oldValidate
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
	manifestEntriesFn = func(*featureflags.Registry) ([]featureflags.ManifestEntry, error) {
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

func TestReleaseManifest(t *testing.T) {
	manifest, err := releaseManifest(featureflags.DefaultRegistry())
	if err != nil {
		t.Fatalf("release manifest: %v", err)
	}
	if len(manifest) != 0 {
		t.Fatalf("expected empty embedded manifest, got %#v", manifest)
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
