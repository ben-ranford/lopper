package analysis

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAnnotateDependencyIdentitiesWarnsOnManifestFailuresAndPreservesDependencyOutput(t *testing.T) {
	repoPath := t.TempDir()

	testutil.MustWriteFile(t, filepath.Join(repoPath, "go.mod"), "module example.com/app\n\nrequire github.com/acme/go-lib v1.2.3\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", "left-pad", "package.json"), `{"name":"left-pad","version":"1.3.0"}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{"packages":{`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pnpm-lock.yaml"), "importers: [\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "poetry.lock"), "[[package]]\nname = \"requests\"\nversion = \"2.31.0\"\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "uv.lock"), "=")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Pipfile.lock"), `{
  "default": ["not-a-package-map"],
  "develop": "not-a-package-map"
}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "requirements.txt"), "flask>=3.0.0\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pom.xml"), "<project>")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "build.gradle"), `implementation "com.acme:gradle-lib:4.5.6"`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "gradle.lockfile"), "com.acme:gradle-lib:4.5.6=runtimeClasspath\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Package.resolved"), "{")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Podfile.lock"), "PODS: [\n")

	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{Language: "go", Name: "github.com/acme/go-lib"},
			{Language: "js-ts", Name: "left-pad"},
			{Language: "python", Name: "requests"},
			{Language: "jvm", Name: "gradle-lib"},
			{Language: "swift", Name: "alamofire"},
		},
	}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "go", "github.com/acme/go-lib"), report.DependencyIdentity{
		Ecosystem: "golang", Name: "github.com/acme/go-lib", Version: "v1.2.3", VersionStatus: identityStatusResolved,
		PURL: "pkg:golang/github.com/acme/go-lib@v1.2.3", PURLStatus: identityStatusResolved, Source: "go.mod", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "left-pad"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/left-pad@1.3.0", PURLStatus: identityStatusResolved, Source: "node_modules/left-pad/package.json", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "python", "requests"), report.DependencyIdentity{
		Ecosystem: "pypi", Name: "requests", Version: "2.31.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:pypi/requests@2.31.0", PURLStatus: identityStatusResolved, Source: "poetry.lock", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "gradle-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "gradle-lib", Namespace: "com.acme", Version: "4.5.6", VersionStatus: identityStatusResolved,
		PURL: "pkg:maven/com.acme/gradle-lib@4.5.6", PURLStatus: identityStatusResolved, Source: "build.gradle", Confidence: "high",
	})

	assertWarningsExact(t, repoPath, reportData.Warnings, []string{
		"identity manifest parse failed for Package.resolved: invalid JSON",
		"identity manifest parse failed for Pipfile.lock default section: invalid JSON",
		"identity manifest parse failed for Pipfile.lock develop section: invalid JSON",
		"identity manifest parse failed for Podfile.lock: invalid YAML",
		"identity manifest parse failed for package-lock.json: invalid JSON",
		"identity manifest parse failed for pnpm-lock.yaml: invalid YAML",
		"identity manifest parse failed for pom.xml: invalid XML",
		"identity manifest parse failed for uv.lock: invalid TOML",
	})
}

func TestAnnotateDependencyIdentitiesKeepsAbsentOptionalAndIncompleteCasesSilent(t *testing.T) {
	repoPath := t.TempDir()

	testutil.MustWriteFile(t, filepath.Join(repoPath, "requirements.txt"), "# comment only\nflask>=3.0.0\n-e ./local\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "gradle.lockfile"), "com.acme:transitive-only:1.0.0=runtimeClasspath\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Package.resolved"), `{"pins":[{"identity":"","location":"","state":{"version":""}}]}`)

	reportData := report.Report{
		Dependencies: []report.DependencyReport{
			{Language: "python", Name: "flask"},
			{Language: "jvm", Name: "transitive-only"},
			{Language: "swift", Name: "unknown"},
		},
	}

	annotateDependencyIdentities(repoPath, &reportData)

	if len(reportData.Warnings) != 0 {
		t.Fatalf("expected silent absent/optional/incomplete manifests, got %#v", reportData.Warnings)
	}
	for _, dep := range reportData.Dependencies {
		if dep.Identity == nil {
			t.Fatalf("expected fallback identity for %#v", dep)
		}
	}
}

func TestIdentityManifestDiscoveryWarnsForMissingRootWithStableRelativePath(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "missing-root")
	warnings := newIdentityWarningCollector(repoPath)

	snapshot := discoverIdentityManifestSnapshot(repoPath, warnings)

	if !reflect.DeepEqual(snapshot, identityManifestSnapshot{}) {
		t.Fatalf("expected empty snapshot for missing root, got %#v", snapshot)
	}
	assertWarningsExact(t, repoPath, warnings.list(), []string{
		"identity manifest discovery failed for .: not found",
	})
}

func TestAnnotateDependencyIdentitiesWarnsOnNodeModulesStatIOFailureAndKeepsLockfiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-referential symlink fixture is not portable on Windows")
	}

	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{
  "dependencies": {
    "left-pad": {"version": "1.3.0"}
  }
}`)
	if err := os.Symlink("node_modules", filepath.Join(repoPath, "node_modules")); err != nil {
		t.Fatalf("create self-referential node_modules symlink: %v", err)
	}

	reportData := report.Report{
		Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "left-pad"}},
	}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "left-pad"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/left-pad@1.3.0", PURLStatus: identityStatusResolved, Source: "package-lock.json", Confidence: "high",
	})
	assertWarningsExact(t, repoPath, reportData.Warnings, []string{
		"identity manifest discovery failed for node_modules: I/O error",
	})
}

func TestAnnotateDependencyIdentitiesWarnsOnDiscoveredYarnLockReadFailure(t *testing.T) {
	repoPath := t.TempDir()
	outsideYarnLock := filepath.Join(t.TempDir(), "yarn.lock")
	testutil.MustWriteFile(t, outsideYarnLock, "\"left-pad@^1.3.0\":\n  version \"1.3.0\"\n")
	if err := os.MkdirAll(filepath.Join(repoPath, "blocked-js"), 0o755); err != nil {
		t.Fatalf("mkdir blocked-js: %v", err)
	}
	if err := os.Symlink(outsideYarnLock, filepath.Join(repoPath, "blocked-js", "yarn.lock")); err != nil {
		t.Fatalf("create blocked-js yarn.lock symlink: %v", err)
	}

	reportData := report.Report{
		Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "left-pad"}},
	}

	annotateDependencyIdentities(repoPath, &reportData)

	if dependency := findIdentityDependency(t, reportData, "js-ts", "left-pad"); dependency.Identity == nil {
		t.Fatalf("expected fallback identity for js-ts/left-pad")
	}
	assertWarningsExact(t, repoPath, reportData.Warnings, []string{
		"identity manifest read failed for blocked-js/yarn.lock: I/O error",
	})
}

func TestIdentityWarningCollectorListSortsDeduplicatesAndSkipsBlankAppends(t *testing.T) {
	repoPath := t.TempDir()
	warnings := newIdentityWarningCollector(repoPath)

	warnings.append("z warning")
	warnings.append("")
	warnings.append("a warning")
	warnings.append("z warning")

	assertWarningsExact(t, repoPath, warnings.list(), []string{
		"a warning",
		"z warning",
	})
}

func TestIdentityWarningCollectorNilReceiverIgnoresFailureAndSectionWarnings(t *testing.T) {
	var warnings *identityWarningCollector

	warnings.addFailure("read", "package-lock.json", "read failed", fs.ErrPermission)
	warnings.addFailure("parse", "package-lock.json", "parse failed", nil)
	warnings.addSectionParseFailure("Pipfile.lock", "develop")
}

func TestIdentityWarningDetailPrefersPermissionAndNotFoundLabels(t *testing.T) {
	if got := identityWarningDetail("parse", "package-lock.json", fs.ErrPermission); got != "permission denied" {
		t.Fatalf("expected permission denied, got %q", got)
	}
	if got := identityWarningDetail("parse", "package-lock.json", fs.ErrNotExist); got != "not found" {
		t.Fatalf("expected not found, got %q", got)
	}
}

func TestIdentityWarningDetailUsesParseAndFallbackLabels(t *testing.T) {
	if got := identityWarningDetail("parse", "manifest.custom", errors.New("bad manifest")); got != "invalid manifest" {
		t.Fatalf("expected invalid manifest parse label, got %q", got)
	}
	if got := identityWarningDetail("read", "manifest.custom", errors.New("boom")); got != "I/O error" {
		t.Fatalf("expected I/O error fallback, got %q", got)
	}
}

func TestIdentityParseLabelUsesKnownManifestNames(t *testing.T) {
	tests := map[string]string{
		"package.json":             "invalid JSON",
		"package-lock.json":        "invalid JSON",
		"packages.lock.json":       "invalid JSON",
		"Package.resolved":         "invalid JSON",
		"Pipfile.lock":             "invalid JSON",
		"Pipfile":                  "invalid TOML",
		"pnpm-lock.yaml":           "invalid YAML",
		"Podfile.lock":             "invalid YAML",
		"pubspec.lock":             "invalid YAML",
		"pom.xml":                  "invalid XML",
		"Directory.Packages.props": "invalid XML",
		"App.csproj":               "invalid XML",
		"Lib.fsproj":               "invalid XML",
		"poetry.lock":              "invalid TOML",
		"uv.lock":                  "invalid TOML",
		"Cargo.lock":               "invalid TOML",
	}

	for path, want := range tests {
		if got := identityParseLabel(path); got != want {
			t.Fatalf("identityParseLabel(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestIdentityParseLabelFallsBackToExtensionAndDefault(t *testing.T) {
	tests := map[string]string{
		"manifest.json":  "invalid JSON",
		"manifest.yaml":  "invalid YAML",
		"manifest.yml":   "invalid YAML",
		"manifest.xml":   "invalid XML",
		"manifest.toml":  "invalid TOML",
		"manifest.other": "invalid manifest",
	}

	for path, want := range tests {
		if got := identityParseLabel(path); got != want {
			t.Fatalf("identityParseLabel(%q) = %q, want %q", path, got, want)
		}
	}
}

func assertWarningsExact(t *testing.T, repoPath string, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("warning count mismatch: got %#v want %#v", got, want)
	}
	assertSortedDedupedWarnings(t, got)
	assertWarningsUseRelativePaths(t, repoPath, got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected warnings:\n got: %#v\nwant: %#v", got, want)
	}
}

func assertSortedDedupedWarnings(t *testing.T, got []string) {
	t.Helper()

	if !sort.StringsAreSorted(got) {
		t.Fatalf("warnings should be sorted deterministically, got %#v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Fatalf("warnings should be deduplicated, got %#v", got)
		}
	}
}

func assertWarningsUseRelativePaths(t *testing.T, repoPath string, got []string) {
	t.Helper()

	for _, warning := range got {
		if strings.Contains(warning, repoPath) {
			t.Fatalf("warning leaked repo root: %q", warning)
		}
	}
}
