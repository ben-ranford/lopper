package analysis

import (
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestComposerIdentityUsesDirectLocksAndExactManifestPins(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, composerIdentityManifestName), `{
  "require": {
    "php": "^8.2",
	"php-64bit": "8.2.0",
	"hhvm": "4.0.0",
	"composer": "2.7.0",
    "composer-plugin-api": "2.6.0",
    "composer-runtime-api": "2.2.0",
	"php-http/discovery": "^1.20",
    "ext-json": "*",
    "lib-icu": "*",
    "monolog/monolog": "^3.0",
    "acme/pinned": "v1.2.3",
    "acme/ranged": "~2.0",
    "acme/invalid": "1..2",
    "acme/branch": "dev-main",
    "vendor_with_underscore/package_name": "2.0.0"
  },
  "require-dev": {
    "phpunit/phpunit": "10.5.0"
  }
}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, composerIdentityLockName), `{
  "packages": [
    {"name": "monolog/monolog", "version": "v3.9.0"},
    {"name": "psr/log", "version": "3.0.2"},
	{"name": "php-http/discovery", "version": "1.20.0"},
    {"name": "composer-plugin-api", "version": "2.6.0"},
	{"name": "composer-plugin-api", "version": "2.7.0"},
    {"name": "vendor_with_underscore/package_name", "version": "v2.0.1"}
  ],
  "packages-dev": [
    {"name": "phpunit/phpunit", "version": "10.5.1"}
  ]
}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "target", "plugin", composerIdentityManifestName), `{"require":{"monolog/monolog":"^4.0","nested/package":"4.0.0"}}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "target", "plugin", composerIdentityLockName), `{"packages":[{"name":"monolog/monolog","version":"4.0.0"},{"name":"nested/package","version":"4.0.0"}]}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "vendor", composerIdentityManifestName), `{"require":{"ignored/package":"9.9.9"}}`)

	known := []string{
		"monolog/monolog", "acme/pinned", "vendor-with-underscore/package-name",
		"phpunit/phpunit", "php-http/discovery",
	}
	unknown := []string{
		"php", "php-64bit", "hhvm", "composer", "composer-plugin-api", "composer-runtime-api",
		"ext-json", "lib-icu", "acme/ranged", "acme/invalid", "acme/branch", "psr/log", "nested/package", "ignored/package",
	}
	reportData := identityReportForDependencies("php", append(known, unknown...))

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "php", "monolog/monolog"), composerIdentity("monolog/monolog", "3.9.0", identityStatusResolved, composerIdentityLockName))
	assertIdentity(t, findIdentityDependency(t, reportData, "php", "phpunit/phpunit"), composerIdentity("phpunit/phpunit", "10.5.1", identityStatusResolved, composerIdentityLockName))
	assertIdentity(t, findIdentityDependency(t, reportData, "php", "php-http/discovery"), composerIdentity("php-http/discovery", "1.20.0", identityStatusResolved, composerIdentityLockName))
	assertIdentity(t, findIdentityDependency(t, reportData, "php", "vendor-with-underscore/package-name"), composerIdentity("vendor-with-underscore/package-name", "2.0.1", identityStatusResolved, composerIdentityLockName))
	assertIdentity(t, findIdentityDependency(t, reportData, "php", "acme/pinned"), composerIdentity("acme/pinned", "1.2.3", identityStatusDeclared, composerIdentityManifestName))
	for _, name := range unknown {
		assertUnknownIdentity(t, findIdentityDependency(t, reportData, "php", name), "composer", name)
	}
}

func TestComposerIdentityDiscoveryUsesOnlyRootPackageFiles(t *testing.T) {
	repoPath := t.TempDir()
	rootManifest := filepath.Join(repoPath, composerIdentityManifestName)
	testutil.MustWriteFile(t, rootManifest, `{"require":{"root/package":"1.0.0"}}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "nested", composerIdentityManifestName), `{"require":{"nested/package":"2.0.0"}}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "nested", composerIdentityLockName), `{"packages":[{"name":"nested/package","version":"2.0.0"}]}`)

	snapshot := identityManifestSnapshot{}
	warnings := newIdentityWarningCollector(repoPath)
	discoverComposerIdentityManifests(repoPath, &snapshot, warnings)

	if len(snapshot.composerFiles) != 1 || snapshot.composerFiles[0] != rootManifest {
		t.Fatalf("expected only the root Composer manifest, got %#v", snapshot.composerFiles)
	}
	if got := warnings.list(); len(got) != 0 {
		t.Fatalf("expected a missing optional root lock to be ignored, got %#v", got)
	}
}

func TestPubIdentityUsesHostedDirectLocksAndExactManifestPins(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, pubIdentityManifestYAMLName), `name: app
dependencies:
  http: ^1.2.0
  pinned: 2.3.4
  ranged: ">=1.0.0 <2.0.0"
  any_package: any
  hosted_pin:
    hosted: https://pub.example.test
    version: 3.4.5
  local_package:
    path: ../local_package
  git_package:
    git: https://example.test/repo.git
  sdk_package:
    sdk: flutter
  bad_lock: ^1.0.0
dev_dependencies:
  test: 1.25.2
dependency_overrides:
  override_package: 4.0.0
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, pubIdentityLockName), `packages:
  http:
    dependency: "direct main"
    description: {name: http, url: "https://pub.dev"}
    source: hosted
    version: "1.2.1"
  pinned:
    dependency: "direct main"
    description: {name: pinned}
    source: hosted
    version: "2.3.4"
  test:
    dependency: "direct dev"
    description: {name: test}
    source: hosted
    version: "1.25.3"
  override_package:
    dependency: "direct overridden"
    description: {name: override_package}
    source: hosted
    version: "4.0.1"
  local_package:
    dependency: "direct main"
    description: {path: ../local_package}
    source: path
    version: "1.0.0"
  git_package:
    dependency: "direct main"
    description: {url: "https://example.test/repo.git"}
    source: git
    version: "1.0.0"
  sdk_package:
    dependency: "direct main"
    description: flutter
    source: sdk
    version: "0.0.0"
  transitive_package:
    dependency: transitive
    description: {name: transitive_package}
    source: hosted
    version: "5.0.0"
  indirect_package:
    dependency: indirect
    description: {name: indirect_package}
    source: hosted
    version: "5.1.0"
  lock_only:
    dependency: "direct main"
    description: {name: lock_only}
    source: hosted
    version: "6.0.0"
  bad_lock:
    dependency: "direct main"
    description: {name: bad_lock}
    source: hosted
    version: "1..2"
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "target", "package", pubIdentityManifestYMLName), "dependencies:\n  nested_package: 7.0.0\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, ".dart_tool", pubIdentityManifestYAMLName), "dependencies:\n  ignored_package: 9.9.9\n")

	known := []string{"http", "pinned", "test", "override_package", "hosted_pin", "lock_only", "nested_package"}
	unknown := []string{"ranged", "any_package", "local_package", "git_package", "sdk_package", "transitive_package", "indirect_package", "bad_lock", "ignored_package"}
	reportData := identityReportForDependencies("dart", append(known, unknown...))

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "dart", "http"), pubIdentity("http", "1.2.1", identityStatusResolved, pubIdentityLockName))
	assertIdentity(t, findIdentityDependency(t, reportData, "dart", "pinned"), pubIdentity("pinned", "2.3.4", identityStatusResolved, pubIdentityLockName))
	assertIdentity(t, findIdentityDependency(t, reportData, "dart", "test"), pubIdentity("test", "1.25.3", identityStatusResolved, pubIdentityLockName))
	assertIdentity(t, findIdentityDependency(t, reportData, "dart", "override_package"), pubIdentity("override_package", "4.0.1", identityStatusResolved, pubIdentityLockName))
	assertIdentity(t, findIdentityDependency(t, reportData, "dart", "hosted_pin"), pubIdentity("hosted_pin", "3.4.5", identityStatusDeclared, pubIdentityManifestYAMLName))
	assertIdentity(t, findIdentityDependency(t, reportData, "dart", "lock_only"), pubIdentity("lock_only", "6.0.0", identityStatusResolved, pubIdentityLockName))
	assertIdentity(t, findIdentityDependency(t, reportData, "dart", "nested_package"), pubIdentity("nested_package", "7.0.0", identityStatusDeclared, "target/package/"+pubIdentityManifestYMLName))
	for _, name := range unknown {
		assertUnknownIdentity(t, findIdentityDependency(t, reportData, "dart", name), "pub", name)
	}
}

func TestComposerAndPubIdentityDiscoveryIsGatedByReportLanguage(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "target", composerIdentityManifestName), "{")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "target", pubIdentityManifestYAMLName), "dependencies: [")
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "go", Name: "example.com/module"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	if len(reportData.Warnings) != 0 {
		t.Fatalf("expected unrelated Composer/pub manifests to be ignored, got %#v", reportData.Warnings)
	}
}

func TestComposerAndPubIdentityWarnOnMalformedFiles(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, composerIdentityManifestName), "{")
	testutil.MustWriteFile(t, filepath.Join(repoPath, composerIdentityLockName), "{")
	testutil.MustWriteFile(t, filepath.Join(repoPath, pubIdentityManifestYAMLName), "dependencies: [")
	testutil.MustWriteFile(t, filepath.Join(repoPath, pubIdentityLockName), "packages: [")
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "php", Name: "acme/package"},
		{Language: "dart", Name: "http"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertWarningsExact(t, repoPath, reportData.Warnings, []string{
		"identity manifest parse failed for composer.json: invalid JSON",
		"identity manifest parse failed for composer.lock: invalid JSON",
		"identity manifest parse failed for pubspec.lock: invalid YAML",
		"identity manifest parse failed for pubspec.yaml: invalid YAML",
	})
}

func TestComposerAndPubIdentityReadFailuresAreWarnings(t *testing.T) {
	repoPath := t.TempDir()
	warnings := newIdentityWarningCollector(repoPath)
	composerManifest := filepath.Join(repoPath, composerIdentityManifestName)
	composerLock := filepath.Join(repoPath, composerIdentityLockName)
	pubManifest := filepath.Join(repoPath, pubIdentityManifestYAMLName)
	pubLock := filepath.Join(repoPath, pubIdentityLockName)

	declared, pins := readComposerIdentityDeclarations(repoPath, composerManifest, warnings)
	if len(declared) != 0 || len(pins) != 0 {
		t.Fatalf("expected missing Composer manifest to produce no declarations, got %#v, %#v", declared, pins)
	}
	if resolved := collectComposerLockIdentityEvidence(repoPath, composerLock, declared, identityIndex{}, warnings); len(resolved) != 0 {
		t.Fatalf("expected missing Composer lock to produce no resolutions, got %#v", resolved)
	}
	if pins := readPubIdentityDeclarations(repoPath, pubManifest, map[string]struct{}{}, warnings); len(pins) != 0 {
		t.Fatalf("expected missing pub manifest to produce no declarations, got %#v", pins)
	}
	resolved, nonHosted := collectPubLockIdentityEvidence(repoPath, pubLock, map[string]struct{}{}, identityIndex{}, warnings)
	if len(resolved) != 0 || len(nonHosted) != 0 {
		t.Fatalf("expected missing pub lock to produce no resolutions, got %#v, %#v", resolved, nonHosted)
	}

	assertWarningsExact(t, repoPath, warnings.list(), []string{
		"identity manifest read failed for composer.json: not found",
		"identity manifest read failed for composer.lock: not found",
		"identity manifest read failed for pubspec.lock: not found",
		"identity manifest read failed for pubspec.yaml: not found",
	})
}

func TestComposerAndPubIdentityRejectEmptyCollectorValues(t *testing.T) {
	declared, pins := readComposerIdentityDeclarations(t.TempDir(), "", nil)
	if len(declared) != 0 || len(pins) != 0 {
		t.Fatalf("expected an empty Composer path to produce no declarations, got %#v, %#v", declared, pins)
	}
	if version := normalizeComposerResolvedVersion("1.0 2.0"); version != "" {
		t.Fatalf("expected an invalid Composer lock version to be rejected, got %q", version)
	}
	if pins := appendPubIdentityDeclarations(map[string]struct{}{}, nil, map[string]any{" ": "1.2.3"}, pubIdentityManifestYAMLName); len(pins) != 0 {
		t.Fatalf("expected an empty pub package name to be rejected, got %#v", pins)
	}
	if name, declared := resolvePubIdentityLockName(map[string]struct{}{}, "", nil); name != "" || declared {
		t.Fatalf("expected empty pub lock metadata to be rejected, got %q, %t", name, declared)
	}
}

func TestComposerExactVersionValidation(t *testing.T) {
	for _, value := range []string{"", "*", "^1.2", "~1.2", ">=1.2", "1.*", "1.0 || 2.0", "dev-main", "1..2", "1.0@stable"} {
		if version, ok := exactComposerVersionConstraint(value); ok || version != "" {
			t.Fatalf("expected %q not to be an exact Composer pin, got %q, %t", value, version, ok)
		}
	}
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "1", want: "1"},
		{value: "1.2", want: "1.2"},
		{value: "1.2.3", want: "1.2.3"},
		{value: "v1.2.3", want: "1.2.3"},
		{value: "1.2.3-RC1", want: "1.2.3-RC1"},
		{value: "1.2.3.4", want: "1.2.3.4"},
	} {
		if version, ok := exactComposerVersionConstraint(test.value); !ok || version != test.want {
			t.Fatalf("unexpected Composer pin for %q: %q, %t", test.value, version, ok)
		}
	}
}

func TestPubExactVersionValidation(t *testing.T) {
	for _, value := range []string{"", "any", "^1.2.3", ">=1.2.3 <2.0.0", "1.2", "v1.2.3", "1..2", "01.2.3"} {
		if version, ok := exactPubVersion(value); ok || version != "" {
			t.Fatalf("expected %q not to be an exact pub pin, got %q, %t", value, version, ok)
		}
	}
	for _, value := range []string{"1.2.3", "1.2.3-beta.1", "1.2.3+4"} {
		if version, ok := exactPubVersion(value); !ok || version != value {
			t.Fatalf("unexpected pub pin for %q: %q, %t", value, version, ok)
		}
	}
}

func TestExactPubManifestVersionRejectsNonRegistrySources(t *testing.T) {
	for _, value := range []any{
		nil,
		1,
		map[string]any{},
		map[string]any{"version": "^1.2.3"},
		map[string]any{"version": "1.2.3", "path": "../package"},
		map[string]any{"version": "1.2.3", "git": "https://example.test/repo.git"},
		map[string]any{"version": "1.2.3", "sdk": "flutter"},
	} {
		if version, ok := exactPubManifestVersion(value); ok || version != "" {
			t.Fatalf("expected %#v not to be an exact hosted pub pin, got %q, %t", value, version, ok)
		}
	}
	for _, value := range []any{"1.2.3", map[string]any{"version": "1.2.3", "hosted": "https://pub.example.test"}} {
		if version, ok := exactPubManifestVersion(value); !ok || version != "1.2.3" {
			t.Fatalf("unexpected exact hosted pub pin for %#v: %q, %t", value, version, ok)
		}
	}
}

func identityReportForDependencies(language string, names []string) report.Report {
	dependencies := make([]report.DependencyReport, 0, len(names))
	for _, name := range names {
		dependencies = append(dependencies, report.DependencyReport{Language: language, Name: name})
	}
	return report.Report{Dependencies: dependencies}
}

func composerIdentity(name, version, status, source string) report.DependencyIdentity {
	return report.DependencyIdentity{
		Ecosystem: "composer", Name: name, Version: version, VersionStatus: status,
		PURL: "pkg:composer/" + name + "@" + version, PURLStatus: identityStatusResolved,
		Source: source, Confidence: "high",
	}
}

func pubIdentity(name, version, status, source string) report.DependencyIdentity {
	return report.DependencyIdentity{
		Ecosystem: "pub", Name: name, Version: version, VersionStatus: status,
		PURL: "pkg:pub/" + name + "@" + version, PURLStatus: identityStatusResolved,
		Source: source, Confidence: "high",
	}
}

func assertUnknownIdentity(t *testing.T, dependency report.DependencyReport, ecosystem, name string) {
	t.Helper()
	assertIdentity(t, dependency, report.DependencyIdentity{
		Ecosystem: ecosystem, Name: name, VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low",
	})
}
