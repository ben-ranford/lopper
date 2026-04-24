package dart

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

const (
	appHTTPManifest             = "name: app\ndependencies:\n  http: ^1.0.0\n"
	expectedOneDependencyReport = "expected one dependency report, got %d"
	analyseErrorFormat          = "analyse: %v"
)

func TestDartAdapterIdentityAndDetectWithConfidence(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), appHTTPManifest)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), "import 'package:http/http.dart' as http;\nvoid main() { http.Client(); }\n")
	writeFile(t, filepath.Join(repo, "packages", "feature", pubspecYAMLName), "name: feature\ndependencies:\n  collection: ^1.0.0\n")

	adapter := NewAdapter()
	if adapter.ID() != "dart" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	if !slices.Equal(adapter.Aliases(), []string{"flutter", "pub"}) {
		t.Fatalf("unexpected aliases: %#v", adapter.Aliases())
	}

	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected adapter to match")
	}
	if detection.Confidence < 35 {
		t.Fatalf("expected confidence >= 35, got %d", detection.Confidence)
	}
	if len(detection.Roots) < 2 {
		t.Fatalf("expected multiple roots, got %#v", detection.Roots)
	}

	matched, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !matched {
		t.Fatalf("expected detect wrapper to return true")
	}
}

func TestDartAdapterAnalyseDependencyAndTopN(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), `name: app
dependencies:
  http: ^1.2.0
  flutter:
    sdk: flutter
  url_launcher_android: ^6.0.0
dev_dependencies:
  test: ^1.0.0
dependency_overrides:
  http_parser: ^4.0.2
flutter:
  uses-material-design: true
`)
	writeFile(t, filepath.Join(repo, pubspecLockName), `packages:
  http:
    dependency: "direct main"
    description:
      name: http
      url: "https://pub.dev"
    source: hosted
    version: "1.2.0"
  flutter:
    dependency: "direct main"
    description: flutter
    source: sdk
    version: "0.0.0"
  url_launcher_android:
    dependency: transitive
    description:
      name: url_launcher_android
      url: "https://pub.dev"
    source: hosted
    version: "6.0.0"
  http_parser:
    dependency: "direct overridden"
    description:
      name: http_parser
      url: "https://pub.dev"
    source: hosted
    version: "4.0.2"
`)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), `import 'package:http/http.dart' as http;
import 'package:url_launcher_android/url_launcher_android.dart';
export 'package:http_parser/http_parser.dart';

void main() {
  final client = http.Client();
  client.close();
}
`)

	adapter := NewAdapter()
	depReport, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "http",
	})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(depReport.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(depReport.Dependencies))
	}
	dep := depReport.Dependencies[0]
	if dep.Language != "dart" {
		t.Fatalf("expected dart language, got %q", dep.Language)
	}
	if dep.Name != "http" {
		t.Fatalf("unexpected dependency name: %q", dep.Name)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used exports to be detected")
	}

	topReport, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     20,
	})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}
	if !containsWarning(topReport.Warnings, "dependency_overrides") {
		t.Fatalf("expected dependency_overrides warning, got %#v", topReport.Warnings)
	}
	overrideDep, ok := findDependency(topReport.Dependencies, "http_parser")
	if !ok {
		t.Fatalf("expected http_parser in top report")
	}
	if !hasRiskCueCode(overrideDep, "dependency-override") {
		t.Fatalf("expected dependency-override risk cue, got %#v", overrideDep.RiskCues)
	}
	if !hasRiskCueCode(overrideDep, "broad-imports") {
		t.Fatalf("expected broad-imports risk cue, got %#v", overrideDep.RiskCues)
	}
}

func TestDartAdapterAnalyseMultilineImportDirective(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), appHTTPManifest)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), `import 'package:http/http.dart'
    as http;

void main() {
  final client = http.Client();
  client.close();
}
`)

	depReport, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "http",
	})
	if err != nil {
		t.Fatalf("analyse multiline import dependency: %v", err)
	}
	if len(depReport.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(depReport.Dependencies))
	}
	if depReport.Dependencies[0].UsedExportsCount == 0 {
		t.Fatalf("expected multiline import alias usage to be detected")
	}
}

func TestDartAdapterUndeclaredImportRisk(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), appHTTPManifest)
	writeFile(t, filepath.Join(repo, pubspecLockName), `packages:
  http:
    dependency: "direct main"
    description: {name: http}
    source: hosted
    version: "1.0.0"
`)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), `import 'package:dio/dio.dart' as dio;
void main() {
  dio.Dio();
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "dio",
	})
	if err != nil {
		t.Fatalf(analyseErrorFormat, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if !hasRiskCueCode(dep, "undeclared-package-import") {
		t.Fatalf("expected undeclared-package-import cue, got %#v", dep.RiskCues)
	}
	if !hasRecommendationCode(dep, "declare-missing-dependency") {
		t.Fatalf("expected declare-missing-dependency recommendation, got %#v", dep.Recommendations)
	}
	if !containsWarning(reportData.Warnings, `could not resolve Dart package import "dio"`) {
		t.Fatalf("expected unresolved import warning, got %#v", reportData.Warnings)
	}
}

func TestDartAdapterLockOnlyTransitiveImportStillFlagsUndeclaredRisk(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), appHTTPManifest)
	writeFile(t, filepath.Join(repo, pubspecLockName), `packages:
  http:
    dependency: "direct main"
    description: {name: http}
    source: hosted
    version: "1.0.0"
  dio:
    dependency: transitive
    description: {name: dio}
    source: hosted
    version: "5.0.0"
`)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), `import 'package:dio/dio.dart' as dio;
void main() {
  dio.Dio();
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "dio",
	})
	if err != nil {
		t.Fatalf(analyseErrorFormat, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if !hasRiskCueCode(dep, "undeclared-package-import") {
		t.Fatalf("expected undeclared-package-import cue, got %#v", dep.RiskCues)
	}
	if !hasRecommendationCode(dep, "declare-missing-dependency") {
		t.Fatalf("expected declare-missing-dependency recommendation, got %#v", dep.Recommendations)
	}
}

func TestDartAdapterSkipsPathDependenciesAndWarnsOnMissingLock(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), `name: app
dependencies:
  local_pkg:
    path: ../local_pkg
  collection: ^1.18.0
`)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), `import 'package:local_pkg/local_pkg.dart' as local;
import 'package:collection/collection.dart' as coll;

void main() {
  local.run();
  coll.ListEquality().equals([1], [1]);
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     20,
	})
	if err != nil {
		t.Fatalf(analyseErrorFormat, err)
	}
	if !containsWarning(reportData.Warnings, "pubspec.lock not found") {
		t.Fatalf("expected missing lock warning, got %#v", reportData.Warnings)
	}
	if _, ok := findDependency(reportData.Dependencies, "local_pkg"); ok {
		t.Fatalf("expected local path dependency to be skipped from output")
	}
	if _, ok := findDependency(reportData.Dependencies, "collection"); !ok {
		t.Fatalf("expected collection dependency in report")
	}
}

func TestDartSourceAttributionPreviewFlagOffKeepsLegacyBehavior(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), `name: app
dependencies:
  http: ^1.2.0
  git_pkg:
    git:
      url: https://github.com/example/git_pkg.git
      ref: v1.0.0
  local_pkg:
    path: ../local_pkg
  flutter:
    sdk: flutter
  url_launcher: ^6.0.0
dependency_overrides:
  git_pkg:
    git:
      url: https://github.com/example/git_pkg.git
      ref: v1.0.1
flutter:
  uses-material-design: true
`)
	writeFile(t, filepath.Join(repo, pubspecLockName), `packages:
  http:
    dependency: "direct main"
    description: {name: http, url: "https://pub.dev"}
    source: hosted
    version: "1.2.0"
  git_pkg:
    dependency: "direct overridden"
    description: {url: "https://github.com/example/git_pkg.git", ref: "v1.0.1"}
    source: git
    version: "1.0.1"
  local_pkg:
    dependency: "direct main"
    description: {path: ../local_pkg}
    source: path
    version: "0.0.1"
  flutter:
    dependency: "direct main"
    description: flutter
    source: sdk
    version: "0.0.0"
  url_launcher:
    dependency: "direct main"
    description: {name: url_launcher, url: "https://pub.dev"}
    source: hosted
    version: "6.0.0"
  url_launcher_android:
    dependency: transitive
    description: {name: url_launcher_android, url: "https://pub.dev"}
    source: hosted
    version: "6.0.0"
  url_launcher_platform_interface:
    dependency: transitive
    description: {name: url_launcher_platform_interface, url: "https://pub.dev"}
    source: hosted
    version: "2.0.0"
`)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), `import 'package:http/http.dart' as http;
import 'package:url_launcher/url_launcher.dart' as launcher;

void main() {
  http.Client();
  launcher.launchUrl(Uri.parse('https://example.com'));
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     25,
	})
	if err != nil {
		t.Fatalf(analyseErrorFormat, err)
	}

	if _, ok := findDependency(reportData.Dependencies, "local_pkg"); ok {
		t.Fatalf("expected local path dependency to remain excluded with preview flag off")
	}
	gitDep, ok := findDependency(reportData.Dependencies, "git_pkg")
	if !ok {
		t.Fatalf("expected git_pkg dependency in top report")
	}
	if gitDep.Provenance != nil {
		t.Fatalf("expected no provenance with preview flag off, got %#v", gitDep.Provenance)
	}
	urlLauncher, ok := findDependency(reportData.Dependencies, "url_launcher")
	if !ok {
		t.Fatalf("expected url_launcher dependency in top report")
	}
	if hasRiskCueCode(urlLauncher, "flutter-federated-plugin-family") {
		t.Fatalf("expected no federated plugin cue with preview flag off")
	}
}

func TestDartSourceAttributionPreviewFlagOnAddsSourceAndFederatedAttribution(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), `name: app
dependencies:
  http: ^1.2.0
  git_pkg:
    git:
      url: https://github.com/example/git_pkg.git
      ref: v1.0.0
  local_pkg:
    path: ../local_pkg
  flutter:
    sdk: flutter
  url_launcher: ^6.0.0
dependency_overrides:
  git_pkg:
    git:
      url: https://github.com/example/git_pkg.git
      ref: v1.0.1
flutter:
  uses-material-design: true
`)
	writeFile(t, filepath.Join(repo, pubspecLockName), `packages:
  http:
    dependency: "direct main"
    description: {name: http, url: "https://pub.dev"}
    source: hosted
    version: "1.2.0"
  git_pkg:
    dependency: "direct overridden"
    description: {url: "https://github.com/example/git_pkg.git", ref: "v1.0.1"}
    source: git
    version: "1.0.1"
  local_pkg:
    dependency: "direct main"
    description: {path: ../local_pkg}
    source: path
    version: "0.0.1"
  flutter:
    dependency: "direct main"
    description: flutter
    source: sdk
    version: "0.0.0"
  url_launcher:
    dependency: "direct main"
    description: {name: url_launcher, url: "https://pub.dev"}
    source: hosted
    version: "6.0.0"
  url_launcher_android:
    dependency: transitive
    description: {name: url_launcher_android, url: "https://pub.dev"}
    source: hosted
    version: "6.0.0"
  url_launcher_platform_interface:
    dependency: transitive
    description: {name: url_launcher_platform_interface, url: "https://pub.dev"}
    source: hosted
    version: "2.0.0"
`)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), `import 'package:http/http.dart' as http;
import 'package:url_launcher/url_launcher.dart' as launcher;

void main() {
  http.Client();
  launcher.launchUrl(Uri.parse('https://example.com'));
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		TopN:       25,
		Features:   mustDartPreviewFeatureSet(t, true),
		Dependency: "",
	})
	if err != nil {
		t.Fatalf(analyseErrorFormat, err)
	}

	assertProvenanceSource(t, reportData.Dependencies, "http", dependencySourceHosted)
	assertProvenanceSource(t, reportData.Dependencies, "git_pkg", dependencySourceGit)
	assertProvenanceSource(t, reportData.Dependencies, "local_pkg", dependencySourcePath)
	assertProvenanceSource(t, reportData.Dependencies, "flutter", dependencySourceSDK)

	localDep, ok := findDependency(reportData.Dependencies, "local_pkg")
	if !ok {
		t.Fatalf("expected local_pkg dependency when preview flag is enabled")
	}
	if !hasRiskCueCode(localDep, "local-path-dependency") {
		t.Fatalf("expected local path cue, got %#v", localDep.RiskCues)
	}
	if hasRecommendationCode(localDep, "remove-unused-dependency") {
		t.Fatalf("expected local path dependency to skip remove-unused recommendation under preview")
	}

	gitDep, ok := findDependency(reportData.Dependencies, "git_pkg")
	if !ok {
		t.Fatalf("expected git_pkg dependency in report")
	}
	if !hasRiskCueCode(gitDep, "git-dependency-source") {
		t.Fatalf("expected git dependency source cue, got %#v", gitDep.RiskCues)
	}
	if !containsRecommendationMessage(gitDep, "review-dependency-override", "git dependency") {
		t.Fatalf("expected override recommendation context for git dependency, got %#v", gitDep.Recommendations)
	}

	urlLauncher, ok := findDependency(reportData.Dependencies, "url_launcher")
	if !ok {
		t.Fatalf("expected url_launcher dependency in report")
	}
	if !hasRiskCueCode(urlLauncher, "flutter-federated-plugin-family") {
		t.Fatalf("expected federated plugin risk cue, got %#v", urlLauncher.RiskCues)
	}
	if urlLauncher.Provenance == nil || !slices.Contains(urlLauncher.Provenance.Signals, "federated:url_launcher") {
		t.Fatalf("expected federated provenance signal, got %#v", urlLauncher.Provenance)
	}
}

func TestDartSourceAttributionStableDefaultsKeepLocalPathImportUsage(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pubspecYAMLName), `name: app
dependencies:
  local_pkg:
    path: ../local_pkg
`)
	writeFile(t, filepath.Join(repo, pubspecLockName), `packages:
  local_pkg:
    dependency: "direct main"
    description: {path: ../local_pkg}
    source: path
    version: "0.0.1"
`)
	writeFile(t, filepath.Join(repo, "lib", mainDartFileName), `import 'package:local_pkg/local_pkg.dart' as local;

void main() {
  local.run();
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "local_pkg",
		Features:   mustDartStableDefaultFeatureSet(t),
	})
	if err != nil {
		t.Fatalf(analyseErrorFormat, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(expectedOneDependencyReport, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Name != "local_pkg" {
		t.Fatalf("expected local_pkg dependency, got %#v", dep)
	}
	if dep.TotalExportsCount == 0 || dep.UsedExportsCount == 0 {
		t.Fatalf("expected local path import usage under stable defaults, got %#v", dep)
	}
	if !hasRiskCueCode(dep, "local-path-dependency") {
		t.Fatalf("expected local path dependency cue under stable defaults, got %#v", dep.RiskCues)
	}
	if containsWarning(reportData.Warnings, `no imports found for dependency "local_pkg"`) {
		t.Fatalf("expected local path imports to be attributed under stable defaults, got warnings %#v", reportData.Warnings)
	}
}

func findDependency(dependencies []report.DependencyReport, name string) (report.DependencyReport, bool) {
	for _, dependency := range dependencies {
		if dependency.Name == name {
			return dependency, true
		}
	}
	return report.DependencyReport{}, false
}

func hasRiskCueCode(dep report.DependencyReport, code string) bool {
	for _, cue := range dep.RiskCues {
		if cue.Code == code {
			return true
		}
	}
	return false
}

func hasRecommendationCode(dep report.DependencyReport, code string) bool {
	for _, rec := range dep.Recommendations {
		if rec.Code == code {
			return true
		}
	}
	return false
}

func containsRecommendationMessage(dep report.DependencyReport, code string, needle string) bool {
	for _, rec := range dep.Recommendations {
		if rec.Code == code && strings.Contains(strings.ToLower(rec.Message), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func assertProvenanceSource(t *testing.T, dependencies []report.DependencyReport, name string, expected string) {
	t.Helper()
	dep, ok := findDependency(dependencies, name)
	if !ok {
		t.Fatalf("expected dependency %q in report", name)
	}
	if dep.Provenance == nil {
		t.Fatalf("expected provenance for dependency %q", name)
	}
	if dep.Provenance.Source != expected {
		t.Fatalf("expected provenance source %q for %q, got %#v", expected, name, dep.Provenance)
	}
}

func mustDartPreviewFeatureSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()
	registry, err := featureflags.NewRegistry([]featureflags.Flag{{
		Code:      "LOP-FEAT-0001",
		Name:      dartSourceAttributionPreviewFeature,
		Lifecycle: featureflags.LifecyclePreview,
	}})
	if err != nil {
		t.Fatalf("new feature registry: %v", err)
	}
	opts := featureflags.ResolveOptions{Channel: featureflags.ChannelDev}
	if enabled {
		opts.Enable = []string{dartSourceAttributionPreviewFeature}
	}
	features, err := registry.Resolve(opts)
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return features
}

func mustDartStableDefaultFeatureSet(t *testing.T) featureflags.Set {
	t.Helper()
	features, err := featureflags.DefaultRegistry().Resolve(featureflags.ResolveOptions{
		Channel: featureflags.ChannelDev,
	})
	if err != nil {
		t.Fatalf("resolve stable default feature set: %v", err)
	}
	if !features.Enabled(dartSourceAttributionPreviewFeature) {
		t.Fatalf("expected %q enabled by stable defaults", dartSourceAttributionPreviewFeature)
	}
	return features
}

func containsWarning(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(strings.ToLower(warning), strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	testutil.MustWriteFile(t, path, content)
}
