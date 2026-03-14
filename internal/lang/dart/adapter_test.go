package dart

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
