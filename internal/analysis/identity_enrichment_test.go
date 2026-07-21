package analysis

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestAnnotateDependencyIdentitiesUsesSupportedManifests(t *testing.T) {
	repoPath := t.TempDir()
	writeIdentityFixtures(t, repoPath)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "go", Name: "github.com/acme/go-lib"},
		{Language: "js-ts", Name: "@scope/pkg"},
		{Language: "python", Name: "my-package-name"},
		{Language: "python", Name: "requests"},
		{Language: "python", Name: "click"},
		{Language: "python", Name: "flask"},
		{Language: "jvm", Name: "maven-lib"},
		{Language: "jvm", Name: "gradle-lib"},
		{Language: "jvm", Name: "locked-lib"},
		{Language: "jvm", Name: "locked-direct-lib"},
		{Language: "swift", Name: "Alamofire"},
		{Language: "swift", Name: "AFNetworking"},
		{Language: "swift", Name: "Firebase/Analytics"},
		{Language: "swift", Name: "RxSwift"},
		{Language: "rust", Name: "mystery"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)
	for _, tc := range []struct {
		language string
		name     string
		want     report.DependencyIdentity
	}{
		{language: "go", name: "github.com/acme/go-lib", want: report.DependencyIdentity{Ecosystem: "golang", Name: "github.com/acme/go-lib", Version: "v1.2.3", VersionStatus: identityStatusResolved, PURL: "pkg:golang/github.com/acme/go-lib@v1.2.3", PURLStatus: identityStatusResolved, Source: "go.mod", Confidence: "high"}},
		{language: "js-ts", name: "@scope/pkg", want: report.DependencyIdentity{Ecosystem: "npm", Name: "@scope/pkg", Version: "2.3.4", VersionStatus: identityStatusResolved, PURL: "pkg:npm/%40scope/pkg@2.3.4", PURLStatus: identityStatusResolved, Source: "node_modules/@scope/pkg/package.json", Confidence: "high"}},
		{language: "python", name: "my-package-name", want: report.DependencyIdentity{Ecosystem: "pypi", Name: "my-package-name", Version: "1.2.3", VersionStatus: identityStatusResolved, PURL: "pkg:pypi/my-package-name@1.2.3", PURLStatus: identityStatusResolved, Source: "requirements.txt", Confidence: "medium"}},
		{language: "python", name: "click", want: report.DependencyIdentity{Ecosystem: "pypi", Name: "click", Version: "8.1.7", VersionStatus: identityStatusResolved, PURL: "pkg:pypi/click@8.1.7", PURLStatus: identityStatusResolved, Source: "requirements.txt", Confidence: "medium"}},
		{language: "python", name: "flask", want: report.DependencyIdentity{Ecosystem: "pypi", Name: "flask", Version: "3.0.0", VersionStatus: identityStatusResolved, PURL: "pkg:pypi/flask@3.0.0", PURLStatus: identityStatusResolved, Source: "Pipfile.lock", Confidence: "high"}},
		{language: "jvm", name: "maven-lib", want: report.DependencyIdentity{Ecosystem: "maven", Name: "maven-lib", Namespace: "com.acme", Version: "3.1.0", VersionStatus: identityStatusDeclared, PURL: "pkg:maven/com.acme/maven-lib@3.1.0", PURLStatus: identityStatusResolved, Source: "pom.xml", Confidence: "high"}},
		{language: "jvm", name: "gradle-lib", want: report.DependencyIdentity{Ecosystem: "maven", Name: "gradle-lib", Namespace: "com.acme", Version: "4.5.6", VersionStatus: identityStatusDeclared, PURL: "pkg:maven/com.acme/gradle-lib@4.5.6", PURLStatus: identityStatusResolved, Source: "build.gradle", Confidence: "high"}},
		{language: "jvm", name: "locked-lib", want: report.DependencyIdentity{Ecosystem: "maven", Name: "locked-lib", VersionStatus: identityStatusUnknown, PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low"}},
		{language: "jvm", name: "locked-direct-lib", want: report.DependencyIdentity{Ecosystem: "maven", Name: "locked-direct-lib", Namespace: "com.acme", Version: "8.8.8", VersionStatus: identityStatusResolved, PURL: "pkg:maven/com.acme/locked-direct-lib@8.8.8", PURLStatus: identityStatusResolved, Source: "build.gradle", Confidence: "high"}},
		{language: "swift", name: "Alamofire", want: report.DependencyIdentity{Ecosystem: "swift", Name: "alamofire", Version: "5.8.1", VersionStatus: identityStatusResolved, PURL: "pkg:swift/alamofire@5.8.1", PURLStatus: identityStatusResolved, Source: "Package.resolved", Confidence: "high"}},
		{language: "swift", name: "AFNetworking", want: report.DependencyIdentity{Ecosystem: "cocoapods", Name: "afnetworking", Version: "4.0.1", VersionStatus: identityStatusResolved, PURL: "pkg:cocoapods/afnetworking@4.0.1", PURLStatus: identityStatusResolved, Source: "Podfile.lock", Confidence: "high"}},
		{language: "swift", name: "Firebase/Analytics", want: report.DependencyIdentity{Ecosystem: "cocoapods", Name: "firebase/analytics", Version: "10.0.0", VersionStatus: identityStatusResolved, PURL: "pkg:cocoapods/firebase%2Fanalytics@10.0.0", PURLStatus: identityStatusResolved, Source: "Podfile.lock", Confidence: "high"}},
		{language: "swift", name: "RxSwift", want: report.DependencyIdentity{Ecosystem: "swift", Name: "rxswift", Version: "6.8.0", VersionStatus: identityStatusResolved, PURL: "pkg:swift/rxswift@6.8.0", PURLStatus: identityStatusResolved, Source: "Cartfile.resolved", Confidence: "high"}},
		{language: "rust", name: "mystery", want: report.DependencyIdentity{Ecosystem: "cargo", Name: "mystery", VersionStatus: identityStatusUnknown, PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low"}},
	} {
		assertIdentity(t, findIdentityDependency(t, reportData, tc.language, tc.name), tc.want)
	}
	requests := findIdentityDependency(t, reportData, "python", "requests").Identity
	if requests.VersionStatus != identityStatusConflicting || requests.PURLStatus != identityPURLUnavailable || requests.Version != "" {
		t.Fatalf("expected conflicting requests identity, got %#v", requests)
	}
	for _, want := range []string{"2.31.0 from poetry.lock", "2.32.0 from requirements.txt"} {
		if !strings.Contains(strings.Join(requests.Conflicts, "\n"), want) {
			t.Fatalf("expected requests conflict %q, got %#v", want, requests.Conflicts)
		}
	}
}

func writeIdentityFixtures(t *testing.T, repoPath string) {
	t.Helper()

	testutil.MustWriteFile(t, filepath.Join(repoPath, "go.mod"), "module example.com/app\n\nrequire (\n\tgithub.com/acme/go-lib v1.2.3\n)\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", "@scope", "pkg", "package.json"), `{"name":"@scope/pkg","version":"2.3.4"}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", "parent", "node_modules", "@scope", "pkg", "package.json"), `{"name":"@scope/pkg","version":"9.9.9"}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "poetry.lock"), `[[package]]
name = "requests"
version = "2.31.0"
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "requirements.txt"), "requests==2.32.0\nClick[security, socks] == 8.1.7\nMy_Package.Name==1.2.3\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Pipfile.lock"), `{
  "_meta": {
    "hash": {"sha256": "abc123"}
  },
  "default": {
    "Flask": {"version": "==3.0.0"}
  },
  "develop": {
    "pytest": {"version": "==8.0.0"}
  }
}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pom.xml"), `<project>
  <dependencies>
    <dependency>
      <groupId>com.acme</groupId>
      <artifactId>maven-lib</artifactId>
      <version>3.1.0</version>
    </dependency>
  </dependencies>
</project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "build.gradle"), `dependencies {
	implementation group: "com.acme", name: "gradle-lib", version: "4.5.6"
    implementation "com.acme:locked-direct-lib"
}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "gradle.lockfile"), "com.acme:locked-lib:7.8.9=runtimeClasspath\ncom.acme:locked-direct-lib:8.8.8=runtimeClasspath\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Package.resolved"), `{
  "pins": [
    {
      "identity": "Alamofire",
      "location": "https://github.com/Alamofire/Alamofire.git",
      "state": {"version": "5.8.1"}
    }
  ],
  "object": {
    "pins": [
      {
        "package": "NIO",
        "repositoryURL": "https://github.com/apple/swift-nio.git",
        "state": {"version": "2.60.0"}
      }
    ]
  }
}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Podfile.lock"), `PODS:
  - AFNetworking (4.0.1)
  - Firebase/Analytics (10.0.0)
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Cartfile.resolved"), "github \"ReactiveX/RxSwift\" \"6.8.0\"\n")
}

func findIdentityDependency(t *testing.T, reportData report.Report, languageID, name string) report.DependencyReport {
	t.Helper()

	for _, dependency := range reportData.Dependencies {
		if dependency.Language == languageID && dependency.Name == name {
			if dependency.Identity == nil {
				t.Fatalf("expected identity for %s/%s", languageID, name)
			}
			return dependency
		}
	}
	t.Fatalf("dependency %s/%s not found", languageID, name)
	return report.DependencyReport{}
}

func assertIdentity(t *testing.T, dependency report.DependencyReport, want report.DependencyIdentity) {
	t.Helper()

	got := dependency.Identity
	if got.Ecosystem != want.Ecosystem ||
		got.Name != want.Name ||
		got.Namespace != want.Namespace ||
		got.Version != want.Version ||
		got.VersionStatus != want.VersionStatus ||
		got.PURL != want.PURL ||
		got.PURLStatus != want.PURLStatus ||
		got.Source != want.Source ||
		got.Confidence != want.Confidence {
		t.Fatalf("unexpected identity for %s/%s:\n got: %#v\nwant: %#v", dependency.Language, dependency.Name, got, want)
	}
}

func assertSingleIdentityEvidence(t *testing.T, index identityIndex, key, wantVersion, wantSource string) {
	t.Helper()

	evidence := index[key]
	if len(evidence) != 1 || evidence[0].Version != wantVersion || evidence[0].Source != wantSource {
		t.Fatalf("unexpected identity evidence for %s: %#v", key, evidence)
	}
}

func assertIdentityEvidenceVersions(t *testing.T, index identityIndex, key, wantSource string, wantVersions ...string) {
	t.Helper()

	evidence := index[key]
	gotVersions := make([]string, 0, len(evidence))
	for _, item := range evidence {
		if item.Source != wantSource {
			t.Fatalf("unexpected identity evidence source for %s: %#v", key, evidence)
		}
		gotVersions = append(gotVersions, item.Version)
	}
	if !reflect.DeepEqual(gotVersions, wantVersions) {
		t.Fatalf("unexpected identity evidence versions for %s: got %#v want %#v", key, gotVersions, wantVersions)
	}
}

func assertJSIdentityConflict(t *testing.T, dependency report.DependencyReport, wantConflicts []string) {
	t.Helper()

	got := dependency.Identity
	if got.VersionStatus != identityStatusConflicting || got.Version != "" || got.PURL != "" || got.PURLStatus != identityPURLUnavailable {
		t.Fatalf("expected conflicting JS identity for %s/%s, got %#v", dependency.Language, dependency.Name, got)
	}
	if !reflect.DeepEqual(got.Conflicts, wantConflicts) {
		t.Fatalf("unexpected JS identity conflicts for %s/%s: got %#v want %#v", dependency.Language, dependency.Name, got.Conflicts, wantConflicts)
	}
}

func assertNoIdentityEvidence(t *testing.T, index identityIndex, key string) {
	t.Helper()

	if evidence := index[key]; len(evidence) != 0 {
		t.Fatalf("expected no identity evidence for %s, got %#v", key, evidence)
	}
}

func TestIdentityEnrichmentEmptyInputsAndEvidenceDefaults(t *testing.T) {
	annotateDependencyIdentities("", nil)
	emptyReport := report.Report{}
	annotateDependencyIdentities("", &emptyReport)

	index := identityIndex{}
	addIdentityEvidence(index, identityEvidence{})
	if len(index) != 0 {
		t.Fatalf("expected blank identity evidence to be ignored, got %#v", index)
	}
	addIdentityEvidence(index, identityEvidence{Language: "Go", Ecosystem: "golang", Name: "Example.com/Lib", Version: "v1.0.0", Source: "go.mod"})
	stored := index[identityKey("go", "example.com/lib")]
	if len(stored) != 1 || stored[0].Status != identityStatusDeclared {
		t.Fatalf("expected default declared identity evidence, got %#v", stored)
	}
}

func TestDependencyIdentityStateFallbackBranches(t *testing.T) {
	state := newDependencyIdentityState(report.DependencyReport{Language: "go", Name: "example.com/lib"}, 1)
	state.applySource("")
	if state.source != "" || len(state.evidenceLabels) != 0 {
		t.Fatalf("expected blank evidence source to be ignored, got %#v", state)
	}

	state = dependencyIdentityState{
		ecosystem: " ",
		name:      "example.com/lib",
		version:   "v1.0.0",
		status:    identityStatusResolved,
	}
	purl, status := state.packageURL()
	if purl != "" || status != identityPURLUnavailable {
		t.Fatalf("expected invalid package URL fallback, got purl=%q status=%q", purl, status)
	}
}

func TestIdentityEnrichmentLanguageAndPackageHelpers(t *testing.T) {
	for _, tc := range []struct {
		language string
		want     string
	}{
		{language: "kotlin-android", want: "maven"},
		{language: "dotnet", want: "nuget"},
		{language: "php", want: "composer"},
		{language: "dart", want: "pub"},
		{language: "unknown", want: "unknown"},
	} {
		if got := ecosystemForLanguage(tc.language); got != tc.want {
			t.Fatalf("ecosystemForLanguage(%q)=%q want %q", tc.language, got, tc.want)
		}
	}
	if got := packageURL("", "", "name", "1.0.0"); got != "" {
		t.Fatalf("expected package URL without ecosystem to be empty, got %q", got)
	}
	if got := packageURL("golang", "", "github.com/acme/go-lib", "v1.2.3"); got != "pkg:golang/github.com/acme/go-lib@v1.2.3" {
		t.Fatalf("expected Go package URL to preserve canonical slash-separated path, got %q", got)
	}
	if got := packageURL("golang", "", "stdlib", "v1.0.0"); got != "pkg:golang/stdlib@v1.0.0" {
		t.Fatalf("expected single-segment Go package URL to remain a single name segment, got %q", got)
	}
	if got := packageURL("golang", "", "example.com/acme/lib/v2", "v2.3.4"); got != "pkg:golang/example.com/acme/lib/v2@v2.3.4" {
		t.Fatalf("expected Go package URL to preserve semantic import version path segments, got %q", got)
	}
	if got := packageURL("golang", "", "example.com/acme/lib", "v1.2.3+incompatible"); got != "pkg:golang/example.com/acme/lib@v1.2.3%2Bincompatible" {
		t.Fatalf("expected package URL to encode a literal plus without changing it to a space, got %q", got)
	}
	if got := packageURL("npm", "", "@scope/pkg", "1.0.0"); got != "pkg:npm/%40scope/pkg@1.0.0" {
		t.Fatalf("expected scoped npm package URL to retain encoded @ scope, got %q", got)
	}
	if got := packageURL("packagist", "", "acme/lib", "1.2.3"); got != "pkg:composer/acme/lib@1.2.3" {
		t.Fatalf("expected composer alias ecosystem to round-trip through canonical PURL type, got %q", got)
	}
	if got := packageURL("npm", "", "", "1.0.0"); got != "" {
		t.Fatalf("expected package URL without name to be empty, got %q", got)
	}
	if got := report.CanonicalPackageNameForEcosystem("pypi", "My_Package.Name"); got != "my-package-name" {
		t.Fatalf("expected python package normalization to use report package rules, got %q", got)
	}
	if got := normalizeIdentityNameForLanguage("python", "my_.package"); got != "my-package" {
		t.Fatalf("expected python identity keys to use PEP 503 normalization, got %q", got)
	}
	if got := confidenceRank("unknown"); got != 0 {
		t.Fatalf("expected unknown confidence rank to be zero, got %d", got)
	}
	if got := versionStatus("", identityStatusResolved); got != identityStatusUnknown {
		t.Fatalf("expected blank version to be unknown, got %q", got)
	}
}

func TestAnnotateDependencyIdentitiesCanonicalizesPyPIAliasesAcrossEvidenceSources(t *testing.T) {
	for _, tc := range []struct {
		name           string
		fixtureName    string
		fixtureBody    string
		wantSource     string
		wantConfidence string
	}{
		{
			name:           "requirements declaration alias",
			fixtureName:    "requirements.txt",
			fixtureBody:    "My__Package==1.2.3\n",
			wantSource:     "requirements.txt",
			wantConfidence: "medium",
		},
		{
			name:           "uv lock alias",
			fixtureName:    "uv.lock",
			fixtureBody:    "[[package]]\nname = \"My__Package\"\nversion = \"1.2.3\"\n",
			wantSource:     "uv.lock",
			wantConfidence: "high",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertCanonicalPyPIAliasIdentity(t, tc.fixtureName, tc.fixtureBody, tc.wantSource, tc.wantConfidence)
		})
	}
}

func assertCanonicalPyPIAliasIdentity(t *testing.T, fixtureName, fixtureBody, wantSource, wantConfidence string) {
	t.Helper()

	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, fixtureName), fixtureBody)
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "python", Name: "my_.package"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	identity := findIdentityDependency(t, reportData, "python", "my_.package").Identity
	if identity.Name != "my-package" {
		t.Fatalf("expected canonical PyPI identity name, got %#v", identity)
	}
	if identity.Version != "1.2.3" || identity.VersionStatus != identityStatusResolved {
		t.Fatalf("expected resolved canonical PyPI version, got %#v", identity)
	}
	if identity.PURL != "pkg:pypi/my-package@1.2.3" || identity.PURLStatus != identityStatusResolved {
		t.Fatalf("expected canonical PyPI PURL, got %#v", identity)
	}
	if identity.Source != wantSource || identity.Confidence != wantConfidence {
		t.Fatalf("unexpected PyPI identity provenance: got %#v want source=%q confidence=%q", identity, wantSource, wantConfidence)
	}
}

func TestIdentityEnrichmentParserEdgeCases(t *testing.T) {
	requires := parseGoModRequires("require\nrequire    example.com/lib v1.0.0\nrequire\t(\nexample.com/block v2.0.0\n)\nexample.com/ignored v1.0.0\n")
	wantRequires := []dependencyVersion{
		{name: "example.com/lib", version: "v1.0.0"},
		{name: "example.com/block", version: "v2.0.0"},
	}
	if !reflect.DeepEqual(requires, wantRequires) {
		t.Fatalf("unexpected parsed go.mod requires: %#v", requires)
	}
	if name, version := parsePodLockEntry(42); name != "" || version != "" {
		t.Fatalf("expected unsupported pod lock entry to be blank, got %s %s", name, version)
	}
	if name, version := parsePodLockEntry(map[string]any{"Firebase/Core (10.0.0)": []any{"Firebase/CoreOnly"}}); name != "Firebase/Core" || version != "10.0.0" {
		t.Fatalf("unexpected pod lock map entry parse: %s %s", name, version)
	}
	if got := deriveSwiftName("https://github.com/apple/swift-nio.git"); got != "swift-nio" {
		t.Fatalf("unexpected derived swift name: %q", got)
	}
	if got := deriveSwiftName("   "); got != "" {
		t.Fatalf("expected blank swift name fallback, got %q", got)
	}
}

func TestDirectNodeModulePackageClassification(t *testing.T) {
	nodeModulesPath := filepath.Join(t.TempDir(), "node_modules")
	for _, tc := range []struct {
		rel  string
		want bool
	}{
		{rel: "lib/package.json", want: true},
		{rel: "@scope/pkg/package.json", want: true},
		{rel: ".cache/package.json"},
		{rel: "@scope/.cache/package.json"},
		{rel: "@.cache/pkg/package.json"},
		{rel: "@scope/pkg/nested/package.json"},
		{rel: "parent/node_modules/lib/package.json"},
		{rel: "lib/readme.md"},
	} {
		path := filepath.Join(nodeModulesPath, filepath.FromSlash(tc.rel))
		if got := isDirectNodeModulePackage(nodeModulesPath, path); got != tc.want {
			t.Fatalf("isDirectNodeModulePackage(%q)=%t want %t", tc.rel, got, tc.want)
		}
	}
	if isDirectNodeModulePackage(nodeModulesPath, filepath.Join(filepath.Dir(nodeModulesPath), "package.json")) {
		t.Fatalf("expected package outside node_modules to be rejected")
	}
}

func TestCollectGoIdentityEvidenceFindsNestedModulesDeterministicallyAndSkipsIgnoredTrees(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "go.mod"), "module example.com/root\n\nrequire github.com/acme/root v1.0.0\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "services", "api", "go.mod"), "module example.com/api\n\nrequire github.com/acme/api v2.0.0\n")
	blockedGoMod := filepath.Join(repoPath, "services", "blocked", "go.mod")
	if err := os.MkdirAll(filepath.Dir(blockedGoMod), 0o755); err != nil {
		t.Fatalf("mkdir blocked go.mod dir: %v", err)
	}
	outsideGoMod := filepath.Join(t.TempDir(), "go.mod")
	testutil.MustWriteFile(t, outsideGoMod, "module example.com/blocked\n\nrequire github.com/acme/blocked v3.0.0\n")
	if err := os.Symlink(outsideGoMod, blockedGoMod); err != nil {
		t.Fatalf("symlink blocked go.mod: %v", err)
	}
	testutil.MustWriteFile(t, filepath.Join(repoPath, "generated", "go.mod"), "module example.com/generated\n\nrequire github.com/acme/generated v9.9.9\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Generated", "go.mod"), "module example.com/generated-upper\n\nrequire github.com/acme/generated-upper v5.5.5\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "cache", "go.mod"), "module example.com/cache\n\nrequire github.com/acme/cache v8.8.8\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Cache", "go.mod"), "module example.com/cache-upper\n\nrequire github.com/acme/cache-upper v4.4.4\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, ".cache", "ignored", "go.mod"), "module example.com/dot-cache\n\nrequire github.com/acme/dot-cache v7.7.7\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, ".CACHE", "ignored", "go.mod"), "module example.com/dot-cache-upper\n\nrequire github.com/acme/dot-cache-upper v3.3.3\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "vendor", "ignored", "go.mod"), "module example.com/vendor\n\nrequire github.com/acme/vendor v6.6.6\n")

	got := findGoModFiles(repoPath)
	want := []string{
		filepath.Join(repoPath, "go.mod"),
		filepath.Join(repoPath, "services", "api", "go.mod"),
		filepath.Join(repoPath, "services", "blocked", "go.mod"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findGoModFiles() = %#v, want %#v", got, want)
	}

	index := identityIndex{}
	collectGoIdentityEvidence(repoPath, index)
	assertSingleIdentityEvidence(t, index, identityKey("go", "github.com/acme/root"), "v1.0.0", "go.mod")
	assertSingleIdentityEvidence(t, index, identityKey("go", "github.com/acme/api"), "v2.0.0", "services/api/go.mod")
	for _, ignored := range []string{
		"github.com/acme/blocked",
		"github.com/acme/cache",
		"github.com/acme/cache-upper",
		"github.com/acme/dot-cache",
		"github.com/acme/dot-cache-upper",
		"github.com/acme/generated",
		"github.com/acme/generated-upper",
		"github.com/acme/vendor",
	} {
		assertNoIdentityEvidence(t, index, identityKey("go", ignored))
	}
}

func TestCollectGoIdentityEvidenceSkipsMatchingReplaceDirectives(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "go.mod"), `module example.com/app

go 1.26.5

require (
	example.com/local v1.0.0
	example.com/forked v1.2.0
	example.com/kept v1.3.0
	example.com/version-mismatch v1.4.0
)

replace example.com/local => ./local

replace example.com/forked v1.2.0 => example.com/fork v1.2.1

replace example.com/version-mismatch v1.3.0 => example.com/other v1.3.1
`)

	index := identityIndex{}
	collectGoIdentityEvidence(repoPath, index)

	assertNoIdentityEvidence(t, index, identityKey("go", "example.com/local"))
	assertNoIdentityEvidence(t, index, identityKey("go", "example.com/forked"))
	assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/kept"), "v1.3.0", "go.mod")
	assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/version-mismatch"), "v1.4.0", "go.mod")
}

func TestCollectGoIdentityEvidenceSkipsWorkspaceReplaceDirectives(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, goWorkFileName), `go 1.26.5

use ./app

replace example.com/local => ./fork

replace example.com/forked v1.2.0 => example.com/fork v1.2.1

replace example.com/version-mismatch v1.3.0 => example.com/other v1.3.1
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "app", "go.mod"), `module example.com/app

go 1.26.5

require (
	example.com/local v1.0.0
	example.com/forked v1.2.0
	example.com/kept v1.3.0
	example.com/version-mismatch v1.4.0
)
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "outside", "go.mod"), `module example.com/outside

go 1.26.5

require example.com/local v1.0.0
`)

	index := identityIndex{}
	collectGoIdentityEvidence(repoPath, index)

	assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/local"), "v1.0.0", "outside/go.mod")
	assertNoIdentityEvidence(t, index, identityKey("go", "example.com/forked"))
	assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/kept"), "v1.3.0", "app/go.mod")
	assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/version-mismatch"), "v1.4.0", "app/go.mod")
}

func TestCollectGoIdentityEvidenceMatchesSymlinkedWorkspaceMembers(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, goWorkFileName), `go 1.26.5

use ./member

replace example.com/replaced => ./fork
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "app", "go.mod"), `module example.com/app

go 1.26.5

require (
	example.com/replaced v1.0.0
	example.com/kept v2.0.0
)
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "outside", "go.mod"), `module example.com/outside

go 1.26.5

require example.com/replaced v1.0.0
`)
	if err := os.Symlink("app", filepath.Join(repoPath, "member")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	index := identityIndex{}
	collectGoIdentityEvidence(repoPath, index)

	assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/replaced"), "v1.0.0", "outside/go.mod")
	assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/kept"), "v2.0.0", "app/go.mod")
}

func TestCollectGoIdentityEvidenceRecoversWorkspaceReplacementsFromMalformedGoWork(t *testing.T) {
	for _, tc := range []struct {
		name       string
		directives string
	}{
		{
			name: "single directives",
			directives: `use ./app

replace example.com/local => ./fork
replace example.com/forked v1.2.0 => example.com/fork v1.2.1`,
		},
		{
			name: "whitespace-separated blocks",
			directives: `use	(
	./app
)

replace    (
	example.com/local => ./fork
	example.com/forked v1.2.0 => example.com/fork v1.2.1
)`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repoPath, goWorkFileName), "go 1.26.5\n\n"+tc.directives+"\n\ninvalid directive\n")
			testutil.MustWriteFile(t, filepath.Join(repoPath, "app", "go.mod"), `module example.com/app

go 1.26.5

require (
	example.com/local v1.0.0
	example.com/forked v1.2.0
	example.com/kept v1.3.0
	example.com/version-mismatch v1.4.0
)
`)
			testutil.MustWriteFile(t, filepath.Join(repoPath, "outside", "go.mod"), `module example.com/outside

go 1.26.5

require example.com/local v1.0.0
`)

			index, warnings := collectIdentityEvidence(repoPath, identityEvidenceLanguages{})

			assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/local"), "v1.0.0", "outside/go.mod")
			assertNoIdentityEvidence(t, index, identityKey("go", "example.com/forked"))
			assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/kept"), "v1.3.0", "app/go.mod")
			assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/version-mismatch"), "v1.4.0", "app/go.mod")
			if len(warnings) != 0 {
				t.Fatalf("expected recoverable workspace fallback to stay silent, got %#v", warnings)
			}
		})
	}
}

func TestCollectGoIdentityEvidenceFailsClosedForMalformedWorkspaceReplaceDirective(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, goWorkFileName), `go 1.26.5

use ./app

replace example.com/uncertain =>
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "app", "go.mod"), `module example.com/app

go 1.26.5

require (
	example.com/uncertain v1.0.0
	example.com/member-only v2.0.0
)
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "outside", "go.mod"), `module example.com/outside

go 1.26.5

require example.com/uncertain v1.0.0
`)

	index, warnings := collectIdentityEvidence(repoPath, identityEvidenceLanguages{})

	assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/uncertain"), "v1.0.0", "outside/go.mod")
	assertNoIdentityEvidence(t, index, identityKey("go", "example.com/member-only"))
	assertWarningsExact(t, repoPath, warnings, []string{
		"identity manifest parse failed for go.work: invalid manifest",
	})
}

func TestCollectGoIdentityEvidenceRecoversValidWorkspaceUsesAroundMalformedSibling(t *testing.T) {
	for _, tc := range []struct {
		name string
		uses string
	}{
		{name: "single directives", uses: "use ./app\nuse ./broken extra"},
		{name: "block", uses: "use (\n\t./app\n\t./broken extra\n)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repoPath, goWorkFileName), "go 1.26.5\n\n"+tc.uses+"\n\nreplace example.com/replaced => ./fork\n")
			testutil.MustWriteFile(t, filepath.Join(repoPath, "app", "go.mod"), `module example.com/app

go 1.26.5

require (
	example.com/replaced v1.0.0
	example.com/member-only v2.0.0
)
`)
			testutil.MustWriteFile(t, filepath.Join(repoPath, "outside", "go.mod"), `module example.com/outside

go 1.26.5

require example.com/replaced v1.0.0
`)

			index, warnings := collectIdentityEvidence(repoPath, identityEvidenceLanguages{})

			assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/replaced"), "v1.0.0", "outside/go.mod")
			assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/member-only"), "v2.0.0", "app/go.mod")
			assertWarningsExact(t, repoPath, warnings, []string{
				"identity manifest parse failed for go.work: invalid manifest",
			})
		})
	}
}

func TestCollectGoIdentityEvidenceRecoversSpacedReplaceBlocksFromMalformedGoMod(t *testing.T) {
	for _, opener := range []string{"replace\t(", "replace    ("} {
		t.Run(opener, func(t *testing.T) {
			repoPath := t.TempDir()
			content := `module example.com/app

require	(
	example.com/replaced v1.0.0
	example.com/kept v2.0.0
)

` + opener + `
	example.com/replaced => ./fork
)

invalid directive
`
			testutil.MustWriteFile(t, filepath.Join(repoPath, "go.mod"), content)
			if requires := parseGoModRequires(content); len(requires) != 2 {
				t.Fatalf("expected fallback scanner to recover both requirements, got %#v", requires)
			}

			index, warnings := collectIdentityEvidence(repoPath, identityEvidenceLanguages{})

			assertNoIdentityEvidence(t, index, identityKey("go", "example.com/replaced"))
			assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/kept"), "v2.0.0", "go.mod")
			if len(warnings) != 0 {
				t.Fatalf("expected recoverable replacement fallback to stay silent, got %#v", warnings)
			}
		})
	}
}

func TestCollectGoIdentityEvidenceFailsClosedForMalformedReplaceDirective(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "go.mod"), `module example.com/app

require example.com/uncertain v1.0.0

replace example.com/uncertain =>
`)

	index, warnings := collectIdentityEvidence(repoPath, identityEvidenceLanguages{})

	assertNoIdentityEvidence(t, index, identityKey("go", "example.com/uncertain"))
	assertWarningsExact(t, repoPath, warnings, []string{
		"identity manifest parse failed for go.mod: invalid manifest",
	})
}

func TestAnnotateDependencyIdentitiesBuildsCanonicalGoPURLsWithoutChangingLookupNames(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "go.mod"), "module example.com/app\n\nrequire (\n\tgithub.com/acme/go-lib v1.2.3\n\tvanity v0.9.0\n\texample.com/acme/lib/v2 v2.3.4\n)\n")

	index := identityIndex{}
	collectGoIdentityEvidence(repoPath, index)
	assertSingleIdentityEvidence(t, index, identityKey("go", "github.com/acme/go-lib"), "v1.2.3", "go.mod")
	assertSingleIdentityEvidence(t, index, identityKey("go", "vanity"), "v0.9.0", "go.mod")
	assertSingleIdentityEvidence(t, index, identityKey("go", "example.com/acme/lib/v2"), "v2.3.4", "go.mod")

	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "go", Name: "github.com/acme/go-lib"},
		{Language: "go", Name: "vanity"},
		{Language: "go", Name: "example.com/acme/lib/v2"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "go", "github.com/acme/go-lib"), report.DependencyIdentity{
		Ecosystem: "golang", Name: "github.com/acme/go-lib", Version: "v1.2.3", VersionStatus: identityStatusResolved,
		PURL: "pkg:golang/github.com/acme/go-lib@v1.2.3", PURLStatus: identityStatusResolved, Source: "go.mod", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "go", "vanity"), report.DependencyIdentity{
		Ecosystem: "golang", Name: "vanity", Version: "v0.9.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:golang/vanity@v0.9.0", PURLStatus: identityStatusResolved, Source: "go.mod", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "go", "example.com/acme/lib/v2"), report.DependencyIdentity{
		Ecosystem: "golang", Name: "example.com/acme/lib/v2", Version: "v2.3.4", VersionStatus: identityStatusResolved,
		PURL: "pkg:golang/example.com/acme/lib/v2@v2.3.4", PURLStatus: identityStatusResolved, Source: "go.mod", Confidence: "high",
	})
}

func TestFindGoModFilesReturnsEmptySliceForMissingRoots(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if got := findGoModFiles(missing); len(got) != 0 {
		t.Fatalf("expected missing root to return no go.mod files, got %#v", got)
	}
}

func TestCollectPackageLockIdentityEvidenceUsesDirectPackagesAndDependencyFallback(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{
	  "packages": {
	    "": {"name": "workspace", "version": "1.0.0"},
	    "node_modules/pkg": {"version": "npm:1.2.3 (integrity)"},
	    "node_modules/blank-version": {"version": "   "},
	    "node_modules/@scope/tool": {"name": "@scope/tool", "version": "2.3.4"},
	    "packages/app/node_modules/react": {"version": "18.3.1"},
	    "packages/app/node_modules/@scope/ui": {"version": "4.5.6"},
	    "node_modules/.cache": {"version": "0.0.1"},
	    "node_modules/pkg/node_modules/nested": {"version": "9.9.9"},
	    "packages/app/node_modules/react/node_modules/loose-envify": {"version": "1.4.0"}
	  },
	  "dependencies": {
	    "pkg": {"version": "9.9.9"},
	    "left-pad": {"version": " 0.1.0 "},
	    "blank": {"version": "   "}
	  }
	}`)

	index := identityIndex{}
	if got := collectPackageLockIdentityEvidence(repoPath, index); got != 5 {
		t.Fatalf("collectPackageLockIdentityEvidence() = %d, want 5", got)
	}
	for key, want := range map[string]string{
		identityKey("js-ts", "pkg"):         "1.2.3",
		identityKey("js-ts", "@scope/tool"): "2.3.4",
		identityKey("js-ts", "react"):       "18.3.1",
		identityKey("js-ts", "@scope/ui"):   "4.5.6",
		identityKey("js-ts", "left-pad"):    "0.1.0",
	} {
		assertSingleIdentityEvidence(t, index, key, want, "package-lock.json")
	}
	assertNoIdentityEvidence(t, index, identityKey("js-ts", "loose-envify"))
}

func assertPNPMDependencyVersions(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{name: "string", value: "1.0.0", want: "1.0.0"},
		{name: "map", value: map[string]any{"version": "2.0.0"}, want: "2.0.0"},
		{name: "unsupported", value: 42, want: ""},
	} {
		if got := parsePNPMDependencyVersion(tc.value); got != tc.want {
			t.Fatalf("parsePNPMDependencyVersion(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestYarnLockfileParsingHelpersCoverSelectors(t *testing.T) {
	selectors := parseYarnLockPackages(`
"@scope/pkg@^1.0.0", "@scope/pkg@npm:^1.0.0":
  version "npm:1.2.3 (via peer)"

left-pad@^1.0.0:
  version "1.3.0"

alias@npm:is-number@^7.0.0:
  version "7.0.0"

not-a-selector:
  resolved "skip"
`)
	if got := sortedJSLockVersions(selectors["@scope/pkg"]); !reflect.DeepEqual(got, []string{"1.2.3"}) {
		t.Fatalf("expected scoped selector version, got %#v", selectors)
	}
	if got := sortedJSLockVersions(selectors["left-pad"]); !reflect.DeepEqual(got, []string{"1.3.0"}) {
		t.Fatalf("expected unscoped selector version, got %#v", selectors)
	}
	if got := sortedJSLockVersions(selectors["is-number"]); !reflect.DeepEqual(got, []string{"7.0.0"}) {
		t.Fatalf("expected npm alias target version, got %#v", selectors)
	}
	if _, ok := selectors["alias"]; ok {
		t.Fatalf("expected npm alias lookup name to resolve to its target, got %#v", selectors)
	}
	addYarnLockPackageSelectors(selectors, ` , left-pad@^1.0.0`, "1.3.0")
	if got := sortedJSLockVersions(selectors["left-pad"]); !reflect.DeepEqual(got, []string{"1.3.0"}) {
		t.Fatalf("expected blank selectors to be ignored while retaining left-pad, got %#v", selectors)
	}
	if _, ok := selectors[""]; ok {
		t.Fatalf("expected blank selector entries to be ignored, got %#v", selectors)
	}
}

func TestPNPMLockfileParsingHelpersCoverImporterVersions(t *testing.T) {
	assertPNPMDependencyVersions(t)
	items := []pnpmLockIdentity{{lookupName: "existing", name: "existing", version: "9.9.9"}}
	dependencies := map[string]any{
		"existing": "1.0.0",
		"new":      map[string]any{"version": "npm:2.0.0 (dev)"},
		"blank":    map[string]any{"version": "   "},
	}
	addPNPMImporterDependencies(&items, dependencies, nil)
	if !reflect.DeepEqual(items, []pnpmLockIdentity{
		{lookupName: "existing", name: "existing", version: "9.9.9"},
		{lookupName: "existing", name: "existing", version: "1.0.0"},
		{lookupName: "new", name: "new", version: "2.0.0"},
	}) {
		t.Fatalf("unexpected importer dependencies: %#v", items)
	}
}

func TestJSLockfileEvidenceSkipsEmptyVersions(t *testing.T) {
	index := identityIndex{}
	if count := addJSLockEvidence(index, "yarn.lock", jsLockPackages{
		"empty":    {},
		"left-pad": {"1.3.0": {}},
	}); count != 1 {
		t.Fatalf("expected only the valid JS lock package to count, got %d", count)
	}
	assertNoIdentityEvidence(t, index, identityKey("js-ts", "empty"))
	assertSingleIdentityEvidence(t, index, identityKey("js-ts", "left-pad"), "1.3.0", "yarn.lock")
}

func TestJSLockfileSelectorHelpers(t *testing.T) {
	if got := normalizeJSLockVersion(" npm:3.4.5 (deduped) "); got != "3.4.5" {
		t.Fatalf("normalizeJSLockVersion() = %q, want 3.4.5", got)
	}
	for _, tc := range []struct {
		name     string
		selector string
		want     string
	}{
		{name: "scoped without version", selector: " @scope/pkg ", want: ""},
		{name: "scoped", selector: "@scope/pkg@^2.0.0", want: "@scope/pkg"},
		{name: "unscoped", selector: "left-pad@^1.0.0", want: "left-pad"},
		{name: "scoped npm alias", selector: "@alias/pkg@npm:@target/pkg@^2.0.0", want: "@target/pkg"},
		{name: "plain selector", selector: "plain-selector", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseYarnSelectorName(tc.selector); got != tc.want {
				t.Fatalf("parseYarnSelectorName(%q) = %q, want %q", tc.selector, got, tc.want)
			}
		})
	}
}

func TestPackageIdentityPathClassification(t *testing.T) {
	if !isVisibleNodeModuleScope("@scope") || isVisibleNodeModuleScope("scope") {
		t.Fatalf("unexpected node module scope classification")
	}
	if !shouldSkipPackageIdentityDir("generated") || shouldSkipPackageIdentityDir("src") {
		t.Fatalf("unexpected package identity directory skipping classification")
	}
}

func TestPackageLockDirectPackageNameRejectsNestedAndHiddenPackages(t *testing.T) {
	for _, tc := range []struct {
		path         string
		declaredName string
		wantName     string
		wantOK       bool
	}{
		{path: "node_modules/pkg", wantName: "pkg", wantOK: true},
		{path: "node_modules/@scope/tool", wantName: "@scope/tool", wantOK: true},
		{path: "packages/app/node_modules/react", wantName: "react", wantOK: true},
		{path: "packages/app/node_modules/@scope/ui", wantName: "@scope/ui", wantOK: true},
		{path: "node_modules/pkg", declaredName: "declared", wantName: "declared", wantOK: true},
		{path: "node_modules/.cache", wantOK: false},
		{path: "node_modules/@scope/.cache", wantOK: false},
		{path: "node_modules/pkg/node_modules/nested", wantOK: false},
		{path: "packages/app/node_modules/react/node_modules/loose-envify", wantOK: false},
		{path: "packages/app/node_modules/react/extra", wantOK: false},
		{path: "pkg", wantOK: false},
	} {
		gotName, gotOK := packageLockDirectPackageName(tc.path, tc.declaredName)
		if gotName != tc.wantName || gotOK != tc.wantOK {
			t.Fatalf("packageLockDirectPackageName(%q, %q) = (%q, %t), want (%q, %t)", tc.path, tc.declaredName, gotName, gotOK, tc.wantName, tc.wantOK)
		}
	}
}

func TestMavenQualifiedLookupHelpersPreferIdentityCoordinatesAndIgnoreBlankNamespaces(t *testing.T) {
	got, ok := qualifiedIdentityLookupKey(report.DependencyReport{
		Language: "jvm",
		Name:     "org.demo:ignored",
		Identity: &report.DependencyIdentity{
			Name:      "artifact",
			Namespace: " com.acme ",
		},
	})
	if !ok || got != qualifiedIdentityKey("jvm", "com.acme", "artifact") {
		t.Fatalf("qualifiedIdentityLookupKey() = (%q, %t)", got, ok)
	}

	if isQualifiedIdentityRequired("jvm", []identityEvidence{
		{Namespace: "   "},
		{Namespace: "com.acme"},
		{Namespace: "com.acme"},
	}) {
		t.Fatalf("expected blank and duplicate namespaces to avoid qualified-identity requirement")
	}
}

func TestCollectJSIdentityEvidencePrefersDirectNodeModulesPackages(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", "left-pad", "package.json"), `{"name":"left-pad","version":"1.3.0"}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", "docs", "README.md"), "ignored")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", "parent", "node_modules", "nested", "package.json"), `{"name":"nested","version":"9.9.9"}`)

	index := identityIndex{}
	collectJSIdentityEvidence(repoPath, index)
	assertSingleIdentityEvidence(t, index, identityKey("js-ts", "left-pad"), "1.3.0", "node_modules/left-pad/package.json")
	assertNoIdentityEvidence(t, index, identityKey("js-ts", "nested"))
	if err := collectJSIdentityEvidenceEntry(repoPath, filepath.Join(repoPath, "node_modules"), "", nil, os.ErrPermission, index, nil); err != nil {
		t.Fatalf("expected walk error branch to be ignored, got %v", err)
	}
}

func TestCollectJSIdentityEvidenceFallsBackToLockfilesWithoutNodeModules(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pnpm-lock.yaml"), `importers:
  .:
    dependencies:
      alpha: 1.0.0
      beta:
        version: npm:2.0.0 (dev)
    optionalDependencies:
      gamma: 3.0.0
`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "js-ts", Name: "alpha"},
		{Language: "js-ts", Name: "beta"},
		{Language: "js-ts", Name: "gamma"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "alpha"), report.DependencyIdentity{Ecosystem: "npm", Name: "alpha", Version: "1.0.0", VersionStatus: identityStatusResolved, PURL: "pkg:npm/alpha@1.0.0", PURLStatus: identityStatusResolved, Source: "pnpm-lock.yaml", Confidence: "high"})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "beta"), report.DependencyIdentity{Ecosystem: "npm", Name: "beta", Version: "2.0.0", VersionStatus: identityStatusResolved, PURL: "pkg:npm/beta@2.0.0", PURLStatus: identityStatusResolved, Source: "pnpm-lock.yaml", Confidence: "high"})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "gamma"), report.DependencyIdentity{Ecosystem: "npm", Name: "gamma", Version: "3.0.0", VersionStatus: identityStatusResolved, PURL: "pkg:npm/gamma@3.0.0", PURLStatus: identityStatusResolved, Source: "pnpm-lock.yaml", Confidence: "high"})
}

func TestCollectJSIdentityEvidenceResolvesPNPMAliasTargets(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pnpm-lock.yaml"), `importers:
  .:
    dependencies:
      lp:
        specifier: npm:left-pad
        version: left-pad@1.3.0
      explicit-alias: npm:is-number@7.0.0
      scalar-selectorless: npm:has-flag
      scoped-selectorless:
        specifier: npm:@babel/core
        version: "@babel/core@7.24.0"
      scoped-alias:
        specifier: npm:@scope/pkg@^2.0.0
        version: "@scope/pkg@2.1.0(peer@1.0.0)"
`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "js-ts", Name: "lp"},
		{Language: "js-ts", Name: "explicit-alias"},
		{Language: "js-ts", Name: "scalar-selectorless"},
		{Language: "js-ts", Name: "scoped-selectorless"},
		{Language: "js-ts", Name: "scoped-alias"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "lp"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/left-pad@1.3.0", PURLStatus: identityStatusResolved, Source: "pnpm-lock.yaml", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "explicit-alias"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "is-number", Version: "7.0.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/is-number@7.0.0", PURLStatus: identityStatusResolved, Source: "pnpm-lock.yaml", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "scalar-selectorless"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "scalar-selectorless", VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "scoped-selectorless"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "@babel/core", Version: "7.24.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/%40babel/core@7.24.0", PURLStatus: identityStatusResolved, Source: "pnpm-lock.yaml", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "scoped-alias"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "@scope/pkg", Version: "2.1.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/%40scope/pkg@2.1.0", PURLStatus: identityStatusResolved, Source: "pnpm-lock.yaml", Confidence: "high",
	})
}

func TestCollectJSIdentityEvidenceResolvesPNPMLockfileV54References(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lockfile string
	}{
		{name: "root", lockfile: `lockfileVersion: 5.4
specifiers:
  core: npm:@babel/core@7.24.0
  lp: npm:left-pad@1.3.0
  react-dom: 18.2.0
  rd: npm:react-dom@18.2.0
dependencies:
  core: /@babel/core/7.24.0
  lp: /left-pad/1.3.0
  react-dom: 18.2.0_react@18.2.0
  rd: /react-dom/18.2.0_react@18.2.0
`},
		{name: "workspace importer", lockfile: `lockfileVersion: 5.4
importers:
  .:
    specifiers:
      core: npm:@babel/core@7.24.0
      lp: npm:left-pad@1.3.0
      react-dom: 18.2.0
      rd: npm:react-dom@18.2.0
    dependencies:
      core: /@babel/core/7.24.0
      lp: /left-pad/1.3.0
      react-dom: 18.2.0_react@18.2.0
      rd: /react-dom/18.2.0_react@18.2.0
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoPath := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repoPath, pnpmLockFileName), tc.lockfile)
			reportData := report.Report{Dependencies: []report.DependencyReport{
				{Language: "js-ts", Name: "core"},
				{Language: "js-ts", Name: "lp"},
				{Language: "js-ts", Name: "react-dom"},
				{Language: "js-ts", Name: "rd"},
			}}

			annotateDependencyIdentities(repoPath, &reportData)

			assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "core"), report.DependencyIdentity{
				Ecosystem: "npm", Name: "@babel/core", Version: "7.24.0", VersionStatus: identityStatusResolved,
				PURL: "pkg:npm/%40babel/core@7.24.0", PURLStatus: identityStatusResolved, Source: pnpmLockFileName, Confidence: "high",
			})
			assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "lp"), report.DependencyIdentity{
				Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", VersionStatus: identityStatusResolved,
				PURL: "pkg:npm/left-pad@1.3.0", PURLStatus: identityStatusResolved, Source: pnpmLockFileName, Confidence: "high",
			})
			assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "react-dom"), report.DependencyIdentity{
				Ecosystem: "npm", Name: "react-dom", Version: "18.2.0", VersionStatus: identityStatusResolved,
				PURL: "pkg:npm/react-dom@18.2.0", PURLStatus: identityStatusResolved, Source: pnpmLockFileName, Confidence: "high",
			})
			assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "rd"), report.DependencyIdentity{
				Ecosystem: "npm", Name: "react-dom", Version: "18.2.0", VersionStatus: identityStatusResolved,
				PURL: "pkg:npm/react-dom@18.2.0", PURLStatus: identityStatusResolved, Source: pnpmLockFileName, Confidence: "high",
			})
		})
	}
}

func TestCollectJSIdentityEvidenceRejectsPNPMLocalReferences(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, pnpmLockFileName), `lockfileVersion: 5.4
importers:
  packages/app:
    specifiers:
      workspace-lib: workspace:*
    dependencies:
      workspace-lib: link:../workspace-lib
      linked-lib: link:../linked-lib
      vendored-lib: file:../vendored-lib
`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "js-ts", Name: "workspace-lib"},
		{Language: "js-ts", Name: "linked-lib"},
		{Language: "js-ts", Name: "vendored-lib"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	for _, name := range []string{"workspace-lib", "linked-lib", "vendored-lib"} {
		assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", name), report.DependencyIdentity{
			Ecosystem: "npm", Name: name, VersionStatus: identityStatusUnknown,
			PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low",
		})
	}
}

func TestParsePNPMDependencyIdentityFailsClosedOnAliasTargetMismatch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lookupName string
		value      any
		wantName   string
	}{
		{name: "selectorless scalar", lookupName: "alias", value: "npm:left-pad", wantName: "left-pad"},
		{name: "selectorless scoped scalar", lookupName: "scoped-alias", value: "npm:@babel/core", wantName: "@babel/core"},
		{name: "target mismatch", lookupName: "alias", value: map[string]any{
			"specifier": "npm:left-pad@^1.0.0",
			"version":   "is-number@7.0.0",
		}, wantName: "left-pad"},
		{name: "selectorless target mismatch", lookupName: "selectorless-alias", value: map[string]any{
			"specifier": "npm:left-pad",
			"version":   "is-number@7.0.0",
		}, wantName: "left-pad"},
		{name: "legacy target mismatch", lookupName: "legacy-alias", value: map[string]any{
			"specifier": "npm:left-pad@1.3.0",
			"version":   "/is-number/7.0.0",
		}, wantName: "left-pad"},
		{name: "empty alias target", lookupName: "invalid-alias", value: map[string]any{
			"specifier": "npm:",
			"version":   "left-pad@1.3.0",
		}, wantName: "invalid-alias"},
		{name: "scope without package", lookupName: "invalid-alias", value: map[string]any{
			"specifier": "npm:@scope",
			"version":   "left-pad@1.3.0",
		}, wantName: "invalid-alias"},
		{name: "empty scoped package", lookupName: "invalid-alias", value: map[string]any{
			"specifier": "npm:@scope/",
			"version":   "left-pad@1.3.0",
		}, wantName: "invalid-alias"},
		{name: "empty selector", lookupName: "invalid-alias", value: map[string]any{
			"specifier": "npm:left-pad@",
			"version":   "left-pad@1.3.0",
		}, wantName: "invalid-alias"},
		{name: "unsupported shape", lookupName: "unsupported", value: 42, wantName: "unsupported"},
		{name: "workspace reference", lookupName: "workspace-lib", value: map[string]any{
			"specifier": "workspace:*",
			"version":   "link:../workspace-lib",
		}, wantName: "workspace-lib"},
		{name: "link reference", lookupName: "linked-lib", value: "link:../linked-lib", wantName: "linked-lib"},
		{name: "file reference", lookupName: "vendored-lib", value: "file:../vendored-lib", wantName: "vendored-lib"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, version := parsePNPMDependencyIdentity(tc.lookupName, tc.value)
			if name != tc.wantName || version != "" {
				t.Fatalf("expected unresolved pnpm identity %q, got %q@%q", tc.wantName, name, version)
			}
		})
	}
}

func TestParsePNPMAliasReferenceAcceptsSelectorlessTargets(t *testing.T) {
	for _, tc := range []struct {
		reference   string
		wantName    string
		wantVersion string
		wantOK      bool
	}{
		{reference: "npm:left-pad", wantName: "left-pad", wantOK: true},
		{reference: "npm:left-pad@^1.0.0", wantName: "left-pad", wantVersion: "^1.0.0", wantOK: true},
		{reference: "npm:@babel/core", wantName: "@babel/core", wantOK: true},
		{reference: "npm:@babel/core@^7.0.0", wantName: "@babel/core", wantVersion: "^7.0.0", wantOK: true},
		{reference: "left-pad"},
		{reference: "npm:"},
		{reference: "npm:@scope"},
		{reference: "npm:@scope/"},
		{reference: "npm:@scope/pkg/extra"},
		{reference: "npm:left-pad@"},
		{reference: "npm:left pad"},
	} {
		gotName, gotVersion, gotOK := parsePNPMAliasReference(tc.reference)
		if gotName != tc.wantName || gotVersion != tc.wantVersion || gotOK != tc.wantOK {
			t.Fatalf("parsePNPMAliasReference(%q) = (%q, %q, %t), want (%q, %q, %t)", tc.reference, gotName, gotVersion, gotOK, tc.wantName, tc.wantVersion, tc.wantOK)
		}
	}
}

func TestCollectJSLockfileIdentityEvidenceWrapperUsesDiscoveredPaths(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{
	  "dependencies": {
	    "alpha": {"version": "1.0.0"}
	  }
	}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "generated", "yarn.lock"), `"ignored@^1.0.0":
  version "9.9.9"
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Generated", "package-lock.json"), `{
	  "dependencies": {
	    "ignored-generated": {"version": "8.8.8"}
	  }
	}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Cache", "yarn.lock"), `"ignored-cache@^1.0.0":
  version "7.7.7"
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, ".CACHE", "pnpm-lock.yaml"), `importers:
  .:
    dependencies:
      ignored-dot-cache: 6.6.6
`)

	index := identityIndex{}
	collectJSLockfileIdentityEvidence(repoPath, index)
	assertSingleIdentityEvidence(t, index, identityKey("js-ts", "alpha"), "1.0.0", "package-lock.json")
	assertNoIdentityEvidence(t, index, identityKey("js-ts", "ignored"))
	assertNoIdentityEvidence(t, index, identityKey("js-ts", "ignored-generated"))
	assertNoIdentityEvidence(t, index, identityKey("js-ts", "ignored-cache"))
	assertNoIdentityEvidence(t, index, identityKey("js-ts", "ignored-dot-cache"))
}

func TestCollectJSIdentityEvidenceAggregatesRootAndNestedSourcesWithoutMasking(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", "left-pad", "package.json"), `{"name":"left-pad","version":"1.3.0"}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{
	  "packages": {
	    "node_modules/left-pad": {"version": "1.3.0"},
	    "node_modules/react": {"version": "18.3.1"}
	  },
	  "dependencies": {
	    "axios": {"version": "1.7.0"}
	  }
	}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages", "app", "yarn.lock"), `"chalk@^5.0.0":
  version "5.3.0"
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages", "web", "pnpm-lock.yaml"), `importers:
  .:
    dependencies:
      react: 19.0.0
      zod:
        version: 3.23.8
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "vendor", "ignored", "package-lock.json"), `{"dependencies":{"ignored":{"version":"9.9.9"}}}`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "js-ts", Name: "left-pad"},
		{Language: "js-ts", Name: "axios"},
		{Language: "js-ts", Name: "chalk"},
		{Language: "js-ts", Name: "zod"},
		{Language: "js-ts", Name: "react"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "left-pad"), report.DependencyIdentity{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", VersionStatus: identityStatusResolved, PURL: "pkg:npm/left-pad@1.3.0", PURLStatus: identityStatusResolved, Source: "node_modules/left-pad/package.json", Confidence: "high"})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "axios"), report.DependencyIdentity{Ecosystem: "npm", Name: "axios", Version: "1.7.0", VersionStatus: identityStatusResolved, PURL: "pkg:npm/axios@1.7.0", PURLStatus: identityStatusResolved, Source: "package-lock.json", Confidence: "high"})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "chalk"), report.DependencyIdentity{Ecosystem: "npm", Name: "chalk", Version: "5.3.0", VersionStatus: identityStatusResolved, PURL: "pkg:npm/chalk@5.3.0", PURLStatus: identityStatusResolved, Source: "packages/app/yarn.lock", Confidence: "high"})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "zod"), report.DependencyIdentity{Ecosystem: "npm", Name: "zod", Version: "3.23.8", VersionStatus: identityStatusResolved, PURL: "pkg:npm/zod@3.23.8", PURLStatus: identityStatusResolved, Source: "packages/web/pnpm-lock.yaml", Confidence: "high"})
	assertJSIdentityConflict(t, findIdentityDependency(t, reportData, "js-ts", "react"), []string{"18.3.1 from package-lock.json", "19.0.0 from packages/web/pnpm-lock.yaml"})
}

func TestCollectJSIdentityEvidenceKeepsPartialSourceFallbacksAndDeterministicDedupe(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{
	  "dependencies": {
	    "alpha": {"version": "1.0.0"}
	  }
	}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages", "app", "package-lock.json"), `{
	  "dependencies": {
	    "alpha": {"version": "1.0.0"},
	    "beta": {"version": "2.0.0"}
	  }
	}`)

	index := identityIndex{}
	collectJSIdentityEvidence(repoPath, index)
	assertSingleIdentityEvidence(t, index, identityKey("js-ts", "alpha"), "1.0.0", "package-lock.json")
	assertSingleIdentityEvidence(t, index, identityKey("js-ts", "beta"), "2.0.0", "packages/app/package-lock.json")
}

func TestCollectPipfileLockEvidenceWarnsOnPresentMalformedSectionsAndResolvesValidPackages(t *testing.T) {
	repoPath := t.TempDir()
	path := filepath.Join(repoPath, "Pipfile.lock")
	testutil.MustWriteFile(t, path, `{
	  "default": {
	    "Requests": {"version": "==2.32.3"}
	  },
	  "develop": ["not-an-object"]
	}`)

	index := identityIndex{}
	warnings := newIdentityWarningCollector(repoPath)
	collectPipfileLockEvidence(repoPath, path, index, warnings)
	assertSingleIdentityEvidence(t, index, identityKey("python", "requests"), "2.32.3", "Pipfile.lock")
	assertNoIdentityEvidence(t, index, identityKey("python", "pytest"))
	assertWarningsExact(t, repoPath, warnings.list(), []string{"identity manifest parse failed for Pipfile.lock develop section: invalid JSON"})
}

func TestJSLockfileCollectorsIgnoreInvalidDocuments(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pnpm-lock.yaml"), `importers: [`)

	index := identityIndex{}
	if got := collectPackageLockIdentityEvidence(repoPath, index); got != 0 {
		t.Fatalf("expected invalid package-lock.json to add no evidence, got %d", got)
	}
	if got := collectPNPMLockIdentityEvidence(repoPath, index); got != 0 {
		t.Fatalf("expected invalid pnpm-lock.yaml to add no evidence, got %d", got)
	}
	if len(index) != 0 {
		t.Fatalf("expected invalid JS lockfiles to leave index empty, got %#v", index)
	}
}

func TestMavenPropertyResolutionHelpersResolveNestedValuesAndRejectCycles(t *testing.T) {
	props := pomProperties{Entries: []pomProperty{
		{Value: "ignored"},
		{XMLName: xml.Name{Local: "revision"}, Value: " ${release} "},
		{XMLName: xml.Name{Local: "release"}, Value: "1.2.3"},
	}}
	values := props.values()
	if values["revision"] != "${release}" {
		t.Fatalf("expected trimmed property value, got %#v", values)
	}
	if got := resolveMavenVersion(" ${revision} ", values); got != "1.2.3" {
		t.Fatalf("resolveMavenVersion() = %q, want 1.2.3", got)
	}
	if got := resolveMavenVersion("${missing}", values); got != "" {
		t.Fatalf("expected unresolved Maven property to return empty version, got %q", got)
	}
	cyclic := map[string]string{"loop": "${loop}"}
	if got := resolveMavenVersion("${loop}", cyclic); got != "" {
		t.Fatalf("expected cyclic Maven property to return empty version, got %q", got)
	}
	deep := map[string]string{"a": "${b}", "b": "${c}", "c": "${d}", "d": "${e}", "e": "${f}", "f": "${g}", "g": "${h}", "h": "${i}", "i": "${j}"}
	if got := resolveMavenVersion("${a}", deep); got != "" {
		t.Fatalf("expected overly deep Maven property chain to return empty version, got %q", got)
	}
	if got := sanitizeMavenVersion(" ${revision} "); got != "" {
		t.Fatalf("expected unresolved property placeholder to sanitize to empty, got %q", got)
	}
	if got := sanitizeMavenVersion(" 1.2.3 "); got != "1.2.3" {
		t.Fatalf("expected concrete Maven version to be preserved, got %q", got)
	}
	if count := addJSLockEvidence(identityIndex{}, "pnpm-lock.yaml", nil); count != 0 {
		t.Fatalf("expected no JS lock evidence to be added for empty items, got %d", count)
	}
}

func TestCollectGradleIdentityEvidenceFromPathsScopesLocksToOwningProjectAndIgnoresInputOrder(t *testing.T) {
	repoPath := t.TempDir()
	rootBuild := filepath.Join(repoPath, "build.gradle")
	rootLock := filepath.Join(repoPath, "gradle.lockfile")
	subprojectLockOnly := filepath.Join(repoPath, "apps", "a-lock-only", "gradle.lockfile")
	subprojectBuild := filepath.Join(repoPath, "apps", "b-owned", "build.gradle.kts")
	subprojectLock := filepath.Join(repoPath, "apps", "b-owned", "gradle.lockfile")
	otherProjectBuild := filepath.Join(repoPath, "apps", "z-other", "build.gradle")

	testutil.MustWriteFile(t, rootBuild, `dependencies { implementation group: "com.root", name: "root-lib" }`)
	testutil.MustWriteFile(t, rootLock, "com.root:root-lib:1.0.1=runtimeClasspath\n")
	testutil.MustWriteFile(t, subprojectLockOnly, "com.root:root-lib:9.9.9=runtimeClasspath\n")
	testutil.MustWriteFile(t, subprojectBuild, `dependencies { implementation(group = "com.sub", name = "sub-lib") }`)
	testutil.MustWriteFile(t, subprojectLock, "com.sub:sub-lib:2.0.0=runtimeClasspath\ncom.sub:transitive-only:7.7.7=runtimeClasspath\n")
	testutil.MustWriteFile(t, otherProjectBuild, `dependencies { implementation "com.other:other-lib:3.0.0" }`)

	indexA := identityIndex{}
	collectGradleIdentityEvidenceFromPaths(repoPath, indexA, []string{otherProjectBuild, subprojectBuild, rootBuild}, []string{subprojectLockOnly, subprojectLock, rootLock}, nil)

	indexB := identityIndex{}
	collectGradleIdentityEvidenceFromPaths(repoPath, indexB, []string{rootBuild, subprojectBuild, otherProjectBuild}, []string{rootLock, subprojectLock, subprojectLockOnly}, nil)

	if !reflect.DeepEqual(indexA, indexB) {
		t.Fatalf("expected Gradle evidence to ignore lexical input order, got %#v vs %#v", indexA, indexB)
	}

	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "jvm", Name: "root-lib"},
		{Language: "jvm", Name: "sub-lib"},
		{Language: "jvm", Name: "transitive-only"},
	}}
	annotateDependencyIdentities(repoPath, &reportData)
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "root-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "root-lib", Namespace: "com.root", Version: "1.0.1", VersionStatus: identityStatusResolved,
		PURL: "pkg:maven/com.root/root-lib@1.0.1", PURLStatus: identityStatusResolved, Source: "build.gradle", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "sub-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "sub-lib", Namespace: "com.sub", Version: "2.0.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:maven/com.sub/sub-lib@2.0.0", PURLStatus: identityStatusResolved, Source: "apps/b-owned/build.gradle.kts", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "transitive-only"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "transitive-only", VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low",
	})

	rootEvidence := indexA[qualifiedIdentityKey("jvm", "com.root", "root-lib")]
	for _, item := range rootEvidence {
		if item.Source == "apps/a-lock-only/gradle.lockfile" || item.Version == "9.9.9" {
			t.Fatalf("expected lock in another project directory to remain unauthorized, got %#v", rootEvidence)
		}
	}
}

func TestCollectPackageLockIdentityEvidenceRetainsDistinctWorkspaceVersions(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{
	  "packages": {
	    "": {"name": "workspace", "version": "1.0.0"},
	    "packages/app-a/node_modules/react": {"version": "18.2.0"},
	    "packages/app-b/node_modules/react": {"version": "18.3.1"},
	    "packages/app-c/node_modules/react": {"version": "18.2.0"}
	  },
	  "dependencies": {
	    "react": {"version": "99.0.0"}
	  }
	}`)

	index := identityIndex{}
	if got := collectPackageLockIdentityEvidence(repoPath, index); got != 1 {
		t.Fatalf("collectPackageLockIdentityEvidence() = %d, want 1 unique package name", got)
	}
	assertIdentityEvidenceVersions(t, index, identityKey("js-ts", "react"), "package-lock.json", "18.2.0", "18.3.1")

	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "react"}}}
	annotateDependencyIdentities(repoPath, &reportData)
	assertJSIdentityConflict(t, findIdentityDependency(t, reportData, "js-ts", "react"), []string{
		"18.2.0 from package-lock.json",
		"18.3.1 from package-lock.json",
	})
}

func TestAnnotateDependencyIdentitiesRetainsConflictingYarnSelectorVersions(t *testing.T) {
	fixture := `"left-pad@^1.0.0":
  version "1.3.0"

"left-pad@~1.0.0":
  version "1.1.3"

"left-pad@npm:^1.0.0":
  version "1.3.0"
`
	assertJSLockfileConflictIdentity(t, "yarn.lock", fixture, "left-pad", []string{
		"1.1.3 from yarn.lock",
		"1.3.0 from yarn.lock",
	})
}

func TestAnnotateDependencyIdentitiesParsesYarnBerryVersionFields(t *testing.T) {
	fixture := `"left-pad@npm:^1.0.0":
  version: 1.3.0

"is-number@npm:^7.0.0":
  version: "7.0.0"
`
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "yarn.lock"), fixture)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "js-ts", Name: "left-pad"},
		{Language: "js-ts", Name: "is-number"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "left-pad"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "left-pad", Version: "1.3.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/left-pad@1.3.0", PURLStatus: identityStatusResolved, Source: "yarn.lock", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "is-number"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "is-number", Version: "7.0.0", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/is-number@7.0.0", PURLStatus: identityStatusResolved, Source: "yarn.lock", Confidence: "high",
	})
}

func TestAnnotateDependencyIdentitiesRetainsConflictingPNPMImporterVersions(t *testing.T) {
	fixture := `importers:
  packages/a:
    dependencies:
      lodash: 4.17.20
  packages/b:
    dependencies:
      lodash:
        version: npm:4.17.21 (prod)
  packages/c:
    devDependencies:
      lodash: 4.17.20
`
	assertJSLockfileConflictIdentity(t, "pnpm-lock.yaml", fixture, "lodash", []string{
		"4.17.20 from pnpm-lock.yaml",
		"4.17.21 from pnpm-lock.yaml",
	})
}

func assertJSLockfileConflictIdentity(t *testing.T, fixtureName, fixtureBody, dependencyName string, wantConflicts []string) {
	t.Helper()

	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, fixtureName), fixtureBody)

	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "js-ts", Name: dependencyName}}}
	annotateDependencyIdentities(repoPath, &reportData)
	assertJSIdentityConflict(t, findIdentityDependency(t, reportData, "js-ts", dependencyName), wantConflicts)
}

func TestCollectIdentityEvidenceIgnoresMalformedSources(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", ".cache", "package.json"), `{"name":"ignored","version":"1.0.0"}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", "noname", "package.json"), `{"version":"1.0.0"}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "node_modules", "bad", "package.json"), `{`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "poetry.lock"), `package = [1]`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Pipfile.lock"), `{`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pom.xml"), `<project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Package.resolved"), `{`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Podfile.lock"), `PODS: [`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "requirements.txt"), "requests>=2\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, ".venv", "requirements.txt"), "ignored==1.0.0\n")

	index, _ := collectIdentityEvidence(repoPath, identityEvidenceLanguages{python: true})
	if len(index) != 0 {
		t.Fatalf("expected malformed identity sources to be ignored, got %#v", index)
	}
}

func TestIdentityCollectorsCoverUnreadableAndInvalidFallbacks(t *testing.T) {
	repoPath := t.TempDir()
	unreadablePackage := filepath.Join(repoPath, "node_modules", "blocked", "package.json")
	testutil.MustWriteFile(t, unreadablePackage, `{"name":"blocked","version":"1.0.0"}`)
	if err := os.Chmod(unreadablePackage, 0); err != nil {
		t.Fatalf("chmod unreadable package: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(unreadablePackage, 0o600); err != nil {
			t.Errorf("restore package permissions: %v", err)
		}
	})
	badLock := filepath.Join(repoPath, "bad-poetry.lock")
	testutil.MustWriteFile(t, badLock, "=")

	index := identityIndex{}
	collectJSIdentityEvidence(repoPath, index)
	collectPythonTOMLLockEvidence(repoPath, badLock, index, nil)
	if len(index) != 0 {
		t.Fatalf("expected invalid identity sources to be ignored, got %#v", index)
	}
	if got := relativeIdentitySource("/repo", "relative/path"); got != "relative/path" {
		t.Fatalf("expected absolute/relative fallback source, got %q", got)
	}
}

func TestIdentityCollectorsIgnoreReadErrors(t *testing.T) {
	repoPath := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "missing.lock")
	index := identityIndex{}

	collectPythonTOMLLockEvidence(repoPath, outsidePath, index, nil)
	collectPipfileLockEvidence(repoPath, outsidePath, index, nil)
	collectRequirementsEvidence(repoPath, outsidePath, index, nil)
	collectPomIdentityEvidence(repoPath, outsidePath, index, nil)
	collectGradleIdentityEvidence(repoPath, outsidePath, index)
	collectGradleLockIdentityEvidence(repoPath, outsidePath, index)
	collectSwiftPackageResolvedEvidence(repoPath, outsidePath, index, nil)
	collectPodfileLockEvidence(repoPath, outsidePath, index, nil)
	collectCarthageResolvedEvidence(repoPath, outsidePath, index, nil)
	addMavenEvidence(index, "", "artifact", "1.0.0", "pom.xml", identityStatusResolved)
	addSwiftEvidence(index, "", "1.0.0", "swift", "Package.resolved")
	collectJSIdentityEvidence(outsidePath, index)
	collectPythonIdentityEvidence(outsidePath, index)
	collectJVMIdentityEvidence(outsidePath, index)
	collectSwiftIdentityEvidence(outsidePath, index)

	if len(index) != 0 {
		t.Fatalf("expected read-error identity evidence to be ignored, got %#v", index)
	}
	if got := relativeIdentitySource("\x00", "path"); got == "" {
		t.Fatalf("expected relative identity source, got %q", got)
	}
	if got := firstNonBlankString("", ""); got != "" {
		t.Fatalf("expected blank firstNonBlankString fallback, got %q", got)
	}
}

func TestHasPythonRuntimeCandidateIgnoresNilAndNonPythonAdapters(t *testing.T) {
	pythonCandidate := language.Candidate{Adapter: &testServiceAdapter{id: "python"}}
	jsCandidate := language.Candidate{Adapter: &testServiceAdapter{id: "js-ts"}}
	if !hasPythonRuntimeCandidate("", []language.Candidate{pythonCandidate}) {
		t.Fatalf("expected python adapter candidate to be detected")
	}
	if hasPythonRuntimeCandidate("", []language.Candidate{{Adapter: nil}, jsCandidate}) {
		t.Fatalf("expected non-python candidates to be ignored")
	}
}

func TestLanguageSpecificIdentityCollectorsSkipIgnoredDirectories(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "uv.lock"), "[[package]]\nname = \"uvicorn\"\nversion = \"0.30.0\"\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, ".venv", "requirements.txt"), "ignored==9.9.9\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "build.gradle.kts"), `dependencies { implementation("com.acme:kotlin-lib:1.2.3") }`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "vendor", "pom.xml"), `<project><dependencies><dependency><groupId>com.skip</groupId><artifactId>vendor-lib</artifactId><version>9.9.9</version></dependency></dependencies></project>`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Package.resolved"), `{"pins":[{"identity":"Alamofire","state":{"version":"5.8.1"}}]}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "Podfile.lock"), "PODS:\n  - AFNetworking (4.0.1)\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "vendor", "Podfile.lock"), "PODS:\n  - IgnoredPod (9.9.9)\n")

	index := identityIndex{}
	collectPythonIdentityEvidence(repoPath, index)
	collectJVMIdentityEvidence(repoPath, index)
	collectSwiftIdentityEvidence(repoPath, index)
	assertSingleIdentityEvidence(t, index, identityKey("python", "uvicorn"), "0.30.0", "uv.lock")
	assertNoIdentityEvidence(t, index, identityKey("python", "ignored"))
	assertSingleIdentityEvidence(t, index, identityKey("jvm", "kotlin-lib"), "1.2.3", "build.gradle.kts")
	assertNoIdentityEvidence(t, index, identityKey("jvm", "vendor-lib"))
	assertSingleIdentityEvidence(t, index, identityKey("swift", "alamofire"), "5.8.1", "Package.resolved")
	assertSingleIdentityEvidence(t, index, identityKey("swift", "afnetworking"), "4.0.1", "Podfile.lock")
	assertNoIdentityEvidence(t, index, identityKey("swift", "ignoredpod"))
}

func TestPrepareAnalysisReturnsNormalizedRepoPath(t *testing.T) {
	repoPath := t.TempDir()
	svc := &Service{Registry: language.NewRegistry()}
	got, err := svc.prepareAnalysis(Request{RepoPath: repoPath, Language: "all"})
	if err != nil {
		t.Fatalf("prepareAnalysis() error = %v", err)
	}
	want, err := filepath.Abs(repoPath)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", repoPath, err)
	}
	if got != want {
		t.Fatalf("prepareAnalysis() = %q, want %q", got, want)
	}
}

func TestAnnotateDependencyIdentitiesUsesJSLockfilesWithoutNodeModules(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{
  "packages": {
    "": {"name": "app", "version": "1.0.0"},
    "node_modules/@scope/pkg": {"name": "@scope/pkg", "version": "2.3.4"},
    "node_modules/lodash": {"version": "4.17.21"}
  },
  "dependencies": {
    "@scope/pkg": {"version": "9.9.9"},
    "lodash": {"version": "4.17.20"}
  }
}`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "yarn.lock"), `"@scope/pkg@^1.0.0":
  version "8.8.8"
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pnpm-lock.yaml"), `importers:
  .:
    dependencies:
      "@scope/pkg":
        version: 7.7.7
`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "js-ts", Name: "@scope/pkg"},
		{Language: "js-ts", Name: "lodash"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertJSIdentityConflict(t, findIdentityDependency(t, reportData, "js-ts", "@scope/pkg"), []string{
		"2.3.4 from package-lock.json",
		"7.7.7 from pnpm-lock.yaml",
		"8.8.8 from yarn.lock",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "lodash"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "lodash", Version: "4.17.21", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/lodash@4.17.21", PURLStatus: identityStatusResolved, Source: "package-lock.json", Confidence: "high",
	})
}

func TestAnnotateDependencyIdentitiesConflictsAcrossJSLockfilesWithoutNodeModules(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "yarn.lock"), `"left-pad@^1.0.0":
  version "1.3.0"
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pnpm-lock.yaml"), `importers:
  .:
    dependencies:
      left-pad:
        version: 9.9.9
`)
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "left-pad"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertJSIdentityConflict(t, findIdentityDependency(t, reportData, "js-ts", "left-pad"), []string{
		"1.3.0 from yarn.lock",
		"9.9.9 from pnpm-lock.yaml",
	})
}

func TestAnnotateDependencyIdentitiesUsesPackageLockDirectVersionOverFallbackEntry(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "package-lock.json"), `{
  "packages": {
    "": {"name": "app", "version": "1.0.0"},
    "node_modules/lodash": {"version": "4.17.21"}
  },
  "dependencies": {
    "lodash": {"version": "4.17.20"}
  }
}`)
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "js-ts", Name: "lodash"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "js-ts", "lodash"), report.DependencyIdentity{
		Ecosystem: "npm", Name: "lodash", Version: "4.17.21", VersionStatus: identityStatusResolved,
		PURL: "pkg:npm/lodash@4.17.21", PURLStatus: identityStatusResolved, Source: "package-lock.json", Confidence: "high",
	})
}

func TestAnnotateDependencyIdentitiesResolvesMavenPropertyVersions(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pom.xml"), `<project>
  <properties>
    <lib.version>${release.version}</lib.version>
    <release.version>3.1.0</release.version>
  </properties>
  <dependencies>
    <dependency>
      <groupId>com.acme</groupId>
      <artifactId>maven-lib</artifactId>
      <version>${lib.version}</version>
    </dependency>
    <dependency>
      <groupId>com.acme</groupId>
      <artifactId>unknown-lib</artifactId>
      <version>${missing.version}</version>
    </dependency>
  </dependencies>
</project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "jvm", Name: "maven-lib"},
		{Language: "jvm", Name: "unknown-lib"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "maven-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "maven-lib", Namespace: "com.acme", Version: "3.1.0", VersionStatus: identityStatusDeclared,
		PURL: "pkg:maven/com.acme/maven-lib@3.1.0", PURLStatus: identityStatusResolved, Source: "pom.xml", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "unknown-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "unknown-lib", Namespace: "com.acme", VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "pom.xml", Confidence: "high",
	})
}

func TestAnnotateDependencyIdentitiesResolvesManagedMavenVersions(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pom.xml"), `<project>
  <properties>
    <managed.version>${release.version}</managed.version>
    <release.version>2.4.0</release.version>
  </properties>
  <dependencyManagement>
    <dependencies>
      <dependency>
        <groupId>com.acme</groupId>
        <artifactId>managed-lib</artifactId>
        <version>${managed.version}</version>
      </dependency>
      <dependency>
        <groupId>com.acme</groupId>
        <artifactId>explicit-lib</artifactId>
        <version>1.0.0</version>
      </dependency>
      <dependency>
        <groupId>com.acme</groupId>
        <artifactId>managed-only-lib</artifactId>
        <version>4.0.0</version>
      </dependency>
    </dependencies>
  </dependencyManagement>
  <dependencies>
    <dependency>
      <groupId>com.acme</groupId>
      <artifactId>managed-lib</artifactId>
    </dependency>
    <dependency>
      <groupId>com.acme</groupId>
      <artifactId>explicit-lib</artifactId>
      <version>3.0.0</version>
    </dependency>
    <dependency>
      <groupId>com.acme</groupId>
      <artifactId>unmanaged-lib</artifactId>
    </dependency>
  </dependencies>
</project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "jvm", Name: "managed-lib"},
		{Language: "jvm", Name: "explicit-lib"},
		{Language: "jvm", Name: "managed-only-lib"},
		{Language: "jvm", Name: "unmanaged-lib"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "managed-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "managed-lib", Namespace: "com.acme", Version: "2.4.0", VersionStatus: identityStatusDeclared,
		PURL: "pkg:maven/com.acme/managed-lib@2.4.0", PURLStatus: identityStatusResolved, Source: "pom.xml", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "explicit-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "explicit-lib", Namespace: "com.acme", Version: "3.0.0", VersionStatus: identityStatusDeclared,
		PURL: "pkg:maven/com.acme/explicit-lib@3.0.0", PURLStatus: identityStatusResolved, Source: "pom.xml", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "managed-only-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "managed-only-lib", Namespace: "com.acme", Version: "4.0.0", VersionStatus: identityStatusDeclared,
		PURL: "pkg:maven/com.acme/managed-only-lib@4.0.0", PURLStatus: identityStatusResolved, Source: "pom.xml", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "unmanaged-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "unmanaged-lib", Namespace: "com.acme", VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "pom.xml", Confidence: "high",
	})
}

func TestAnnotateDependencyIdentitiesRequiresQualifiedLookupForDuplicateMavenArtifacts(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "pom.xml"), `<project>
  <dependencies>
    <dependency>
      <groupId>com.acme</groupId>
      <artifactId>shared-lib</artifactId>
      <version>1.2.3</version>
    </dependency>
    <dependency>
      <groupId>org.demo</groupId>
      <artifactId>shared-lib</artifactId>
      <version>9.9.9</version>
    </dependency>
  </dependencies>
</project>`)
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "jvm", Name: "shared-lib"},
		{Language: "jvm", Name: "com.acme:shared-lib"},
		{Language: "jvm", Name: "org.demo:shared-lib"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "shared-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "shared-lib", VersionStatus: identityStatusUnknown,
		PURLStatus: identityPURLUnavailable, Source: "language-adapter", Confidence: "low",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "com.acme:shared-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "shared-lib", Namespace: "com.acme", Version: "1.2.3", VersionStatus: identityStatusDeclared,
		PURL: "pkg:maven/com.acme/shared-lib@1.2.3", PURLStatus: identityStatusResolved, Source: "pom.xml", Confidence: "high",
	})
	assertIdentity(t, findIdentityDependency(t, reportData, "jvm", "org.demo:shared-lib"), report.DependencyIdentity{
		Ecosystem: "maven", Name: "shared-lib", Namespace: "org.demo", Version: "9.9.9", VersionStatus: identityStatusDeclared,
		PURL: "pkg:maven/org.demo/shared-lib@9.9.9", PURLStatus: identityStatusResolved, Source: "pom.xml", Confidence: "high",
	})
}
