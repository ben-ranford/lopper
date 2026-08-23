package php

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

const testComposerJSON = "composer.json"
const testComposerLock = "composer.lock"
const testIndexPHP = "index.php"
const testMonologDependency = "monolog/monolog"
const testPHPHeader = "<?php\n"
const testExpectedOneDependencyReportFmt = "expected one dependency report, got %d"
const testAnalyseErrFmt = "analyse: %v"

func writeTestComposerPackage(t *testing.T, repo string, dependency string, namespace string) {
	t.Helper()
	writeFile(t, filepath.Join(repo, testComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, dependency))
	writeFile(t, filepath.Join(repo, testComposerLock), fmt.Sprintf(`{
  "packages": [
    {
      "name": %q,
      "autoload": {"psr-4": {%q: "src/"}}
    }
  ]
}
`, dependency, namespace+`\`))
}

func TestPHPAdapterDetectWithConfidence(t *testing.T) {
	repo := t.TempDir()
	composerTemplate := `{
  "name": "acme/app",
  "require": {
    %q: "^3.0"
  }
}
`
	composerContent := fmt.Sprintf(composerTemplate, testMonologDependency)
	writeFile(t, filepath.Join(repo, testComposerJSON), composerContent)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader)
	writeFile(t, filepath.Join(repo, "packages", "plugin", testComposerJSON), `{"name":"acme/plugin"}`)

	adapter := NewAdapter()
	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected php adapter to match")
	}
	if detection.Confidence < 35 {
		t.Fatalf("expected confidence >= 35, got %d", detection.Confidence)
	}
	if len(detection.Roots) < 2 {
		t.Fatalf("expected nested composer roots, got %#v", detection.Roots)
	}
}

func TestPHPAdapterAnalyseDependencyAndTopN(t *testing.T) {
	repo := t.TempDir()
	composerTemplate := `{
  "name": "acme/app",
  "require": {
    "php": "^8.2",
    "ext-json": "*",
    %q: "^3.0",
    "symfony/yaml": "^6.0"
  },
  "require-dev": {
    "phpunit/phpunit": "^10.0"
  },
  "autoload": {
    "psr-4": {
      "App\\": "src/"
    }
  }
}
`
	composerContent := fmt.Sprintf(composerTemplate, testMonologDependency)
	writeFile(t, filepath.Join(repo, testComposerJSON), composerContent)
	lockTemplate := `{
  "packages": [
    {
      "name": %q,
      "autoload": {"psr-4": {"Monolog\\": "src/Monolog"}}
    },
    {
      "name": "symfony/yaml",
      "autoload": {"psr-4": {"Symfony\\Component\\Yaml\\": ""}}
    }
  ],
  "packages-dev": [
    {
      "name": "phpunit/phpunit",
      "autoload": {"psr-4": {"PHPUnit\\Framework\\": "src"}}
    }
  ]
}
`
	lockContent := fmt.Sprintf(lockTemplate, testMonologDependency)
	writeFile(t, filepath.Join(repo, testComposerLock), lockContent)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), `<?php
use Monolog\Logger;
use Monolog\{Handler\StreamHandler, Formatter\LineFormatter as LineFmt};
use Symfony\Component\Yaml\Yaml;

$className = "Monolog\Logger";
class_exists($className);

$logger = new Logger("app");
$yaml = Yaml::parse("foo: bar");
`)

	adapter := NewAdapter()
	depReport, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: testMonologDependency,
		TopN:       0,
	})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(depReport.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(depReport.Dependencies))
	}
	dep := depReport.Dependencies[0]
	if dep.Language != "php" {
		t.Fatalf("expected php language, got %q", dep.Language)
	}
	if dep.Name != testMonologDependency {
		t.Fatalf("unexpected dependency: %q", dep.Name)
	}
	if dep.TotalExportsCount == 0 {
		t.Fatalf("expected imports to be discovered")
	}
	if !hasRiskCueCode(dep, "grouped-use-import") {
		t.Fatalf("expected grouped-use-import cue, got %#v", dep.RiskCues)
	}
	if !hasRiskCueCode(dep, "dynamic-loading") {
		t.Fatalf("expected dynamic-loading cue, got %#v", dep.RiskCues)
	}

	topReport, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     10,
	})
	if err != nil {
		t.Fatalf("analyse topN: %v", err)
	}
	if len(topReport.Dependencies) == 0 {
		t.Fatalf("expected top dependencies")
	}
	names := make([]string, 0, len(topReport.Dependencies))
	for _, dep := range topReport.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "phpunit/phpunit") {
		t.Fatalf("expected declared require-dev dependency in top report, got %#v", names)
	}
	if !containsWarning(topReport.Warnings, "dynamic loading/reflection patterns") {
		t.Fatalf("expected dynamic warning, got %#v", topReport.Warnings)
	}
}

func TestPHPAdapterSkipsNestedComposerPackages(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, testComposerJSON), `{"require":{"symfony/yaml":"^6.0"}}`)
	writeFile(t, filepath.Join(repo, testComposerLock), `{
  "packages": [
    {
      "name": "symfony/yaml",
      "autoload": {"psr-4": {"Symfony\\Component\\Yaml\\": ""}}
    }
  ]
}
`)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), `<?php
use Symfony\Component\Yaml\Yaml;
Yaml::parse("foo: bar");
`)
	writeFile(t, filepath.Join(repo, "packages", "nested", testComposerJSON), `{"name":"acme/nested"}`)
	writeFile(t, filepath.Join(repo, "packages", "nested", "src", "nested.php"), `<?php
use Symfony\Component\Yaml\Yaml;
Yaml::parse("foo: bar");
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "symfony/yaml",
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency, got %d", len(reportData.Dependencies))
	}
	if reportData.Dependencies[0].UsedExportsCount != 1 {
		t.Fatalf("expected nested package to be skipped, used count=%d", reportData.Dependencies[0].UsedExportsCount)
	}
	if !containsWarning(reportData.Warnings, "nested composer package") {
		t.Fatalf("expected nested package warning, got %#v", reportData.Warnings)
	}
}

func TestPHPAdapterParsesNamespaceReferencesWithoutUseStatement(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, testComposerJSON), fmt.Sprintf(`{"require":{%q:"^3.0"}}`, testMonologDependency))
	lockTemplate := `{
  "packages": [
    {
      "name": %q,
      "autoload": {"psr-4": {"Monolog\\": "src/Monolog"}}
    }
  ]
}
`
	lockContent := fmt.Sprintf(lockTemplate, testMonologDependency)
	writeFile(t, filepath.Join(repo, testComposerLock), lockContent)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
$logger = new \Monolog\Logger("app");
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: testMonologDependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected namespace reference usage to be counted")
	}
	if containsWarning(reportData.Warnings, "no imports found") {
		t.Fatalf("did not expect no-import warning for namespace reference usage: %#v", reportData.Warnings)
	}
}

func TestPHPAdapterCountsNamespaceReferenceInsideClosureUseCapture(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
$callback = function ()
    use ($service) {
        \Vendor\Package\Used::run();
    };
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.UsedExportsCount != 1 || dep.TotalExportsCount != 1 {
		t.Fatalf("expected closure body namespace reference to be counted, report=%#v", dep)
	}
	if containsWarning(reportData.Warnings, "no imports found") {
		t.Fatalf("did not expect closure use capture to hide dependency usage: %#v", reportData.Warnings)
	}
}

func TestPHPAdapterCountsClassBodyTraitUseAsActiveDependency(t *testing.T) {
	reportData := analysePHPDependencySource(t, testPHPHeader+`
namespace App;

final class Service
{
    use \Vendor\Package\FeatureTrait;
}
`)

	dep := singlePHPDependencyReport(t, reportData.Dependencies)
	assertActivePHPTraitDependency(t, dep, []string{`Vendor\Package\FeatureTrait`})
	if containsWarning(reportData.Warnings, "no imports found") {
		t.Fatalf("did not expect no-import warning for active trait use: %#v", reportData.Warnings)
	}
}

func TestPHPAdapterCountsSameLineAttributedClassTraitUseAsActiveDependency(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
#[SomeAttribute]
final class Service { use \Vendor\Package\FeatureTrait; }
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	dep := singlePHPDependencyReport(t, reportData.Dependencies)
	assertActivePHPTraitDependency(t, dep, []string{`Vendor\Package\FeatureTrait`})
}

func TestPHPAdapterIgnoresHeredocBracesBeforeClassBodyTraitUse(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
final class Service
{
    public function template(): string
    {
        return <<<'HTML'
}
HTML;
    }

    use \Vendor\Package\FeatureTrait;
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	dep := singlePHPDependencyReport(t, reportData.Dependencies)
	assertActivePHPTraitDependency(t, dep, []string{`Vendor\Package\FeatureTrait`})
}

func TestPHPAdapterCountsTraitUseAfterSameLineSecondHeredoc(t *testing.T) {
	reportData := analysePHPDependencySource(t, testPHPHeader+`
final class Service
{
    public function template(): string
    {
        return <<<ONE
}
ONE; $second = <<<'TWO'
}
TWO;
    }

    use \Vendor\Package\FeatureTrait {
        handle as private;
    }
}
`)

	dep := singlePHPDependencyReport(t, reportData.Dependencies)
	assertActivePHPTraitDependency(t, dep, []string{`Vendor\Package\FeatureTrait`})
	if containsWarning(reportData.Warnings, "no imports found") {
		t.Fatalf("did not expect no-import warning for trait use after same-line heredoc: %#v", reportData.Warnings)
	}
}

func TestPHPAdapterCountsClassBodyTraitUseAfterLongDeclaration(t *testing.T) {
	reportData := analysePHPDependencySource(t, testPHPHeader+`
namespace App;

final class Service `+strings.Repeat(" ", 2200)+`
{
    use \Vendor\Package\FeatureTrait;
}
`)

	dep := singlePHPDependencyReport(t, reportData.Dependencies)
	assertActivePHPTraitDependency(t, dep, []string{`Vendor\Package\FeatureTrait`})
}

func TestPHPAdapterCountsAnonymousClassTraitUseAsActiveDependency(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
namespace App;

function makeService($factory) {
    return new class($factory, function () { return new \stdClass(); }) extends \stdClass {
        use \Vendor\Package\FeatureTrait;
    };
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	dep := singlePHPDependencyReport(t, reportData.Dependencies)
	assertActivePHPTraitDependency(t, dep, []string{`Vendor\Package\FeatureTrait`})
	if containsWarning(reportData.Warnings, "no imports found") {
		t.Fatalf("did not expect no-import warning for anonymous class trait use: %#v", reportData.Warnings)
	}
}

func TestPHPAdapterCountsTraitAdaptationBlocksAsActiveDependency(t *testing.T) {
	for _, tc := range []struct {
		name        string
		source      string
		wantModules []string
	}{
		{
			name: "alias",
			source: testPHPHeader + `
final class Service
{
    use Vendor\Package\FeatureTrait {
        handle as public handleFeature;
    }
}
`,
			wantModules: []string{`Vendor\Package\FeatureTrait`},
		},
		{
			name: "insteadof",
			source: testPHPHeader + `
final class Service
{
    use Vendor\Package\FeatureTrait, Vendor\Package\OtherTrait {
        Vendor\Package\FeatureTrait::handle insteadof Vendor\Package\OtherTrait;
        Vendor\Package\OtherTrait::handle as otherHandle;
    }
}
`,
			wantModules: []string{`Vendor\Package\FeatureTrait`, `Vendor\Package\OtherTrait`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			const dependency = "vendor/package"
			writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
			writeFile(t, filepath.Join(repo, "src", testIndexPHP), tc.source)

			reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
				RepoPath:   repo,
				Dependency: dependency,
			})
			if err != nil {
				t.Fatalf(testAnalyseErrFmt, err)
			}
			dep := singlePHPDependencyReport(t, reportData.Dependencies)
			assertActivePHPTraitDependency(t, dep, tc.wantModules)
			if containsWarning(reportData.Warnings, "no imports found") {
				t.Fatalf("did not expect no-import warning for trait adaptation block: %#v", reportData.Warnings)
			}
		})
	}
}

func TestPHPAdapterKeepsSemicolonSeparatedUseImportUnused(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, testComposerLock), `{
  "packages": [
    {
      "name": "foo/a",
      "autoload": {"psr-4": {"Foo\\": "src/Foo"}}
    },
    {
      "name": "vendor/package",
      "autoload": {"psr-4": {"Vendor\\Package\\": "src/Vendor"}}
    }
  ]
}
`)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
use Foo\A; use Vendor\Package\B;

$a = new A();
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.UsedExportsCount != 0 || dep.TotalExportsCount != 1 {
		t.Fatalf("expected same-line second use declaration to remain unused, report=%#v", dep)
	}
	if len(dep.UnusedImports) != 1 || dep.UnusedImports[0].Module != `Vendor\Package\B` {
		t.Fatalf("expected unused same-line import for vendor package, got %#v", dep.UnusedImports)
	}
	if !hasRecommendation(dep, "remove-unused-dependency") {
		t.Fatalf("expected remove-unused recommendation for unused same-line import, got %#v", dep.Recommendations)
	}
}

func TestPHPAdapterResolvesNamespaceRelativeTraitUseAsLocal(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, testComposerJSON), fmt.Sprintf(`{
  "require": {%q: "^1.0"},
  "autoload": {"psr-4": {"App\\": "src/"}}
}`, dependency))
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
namespace App;

use Vendor\Package\UnusedExternal;

final class Service
{
    use Vendor\Package\FeatureTrait;
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.UsedExportsCount != 0 || dep.TotalExportsCount != 1 {
		t.Fatalf("expected namespace-relative trait use to stay local and leave only the unused external import, report=%#v", dep)
	}
	if len(dep.UnusedImports) != 1 || dep.UnusedImports[0].Module != `Vendor\Package\UnusedExternal` {
		t.Fatalf("expected only the external import to be unused, got %#v", dep.UnusedImports)
	}
	if !hasRecommendation(dep, "remove-unused-dependency") {
		t.Fatalf("expected remove-unused recommendation when namespace-relative trait use is local, got %#v", dep.Recommendations)
	}
}

func TestPHPAdapterResolvesSameLineNamespaceRelativeTraitUseAsLocal(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, testComposerJSON), fmt.Sprintf(`{
  "require": {%q: "^1.0"},
  "autoload": {"psr-4": {"App\\": "src/"}}
}`, dependency))
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
namespace App; use Vendor\Package\UnusedExternal; final class Service { use Vendor\Package\FeatureTrait; }
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	dep := singlePHPDependencyReport(t, reportData.Dependencies)
	if dep.UsedExportsCount != 0 || dep.TotalExportsCount != 1 {
		t.Fatalf("expected same-line namespace-relative trait use to stay local, report=%#v", dep)
	}
	if len(dep.UnusedImports) != 1 || dep.UnusedImports[0].Module != `Vendor\Package\UnusedExternal` {
		t.Fatalf("expected only the external import to be unused, got %#v", dep.UnusedImports)
	}
	if !hasRecommendation(dep, "remove-unused-dependency") {
		t.Fatalf("expected remove-unused recommendation when same-line namespace-relative trait use is local, got %#v", dep.Recommendations)
	}
}

func TestPHPAdapterIgnoresNamespaceDeclarationAsDependencyUsage(t *testing.T) {
	reportData := analysePHPDependencySource(t, testPHPHeader+`
namespace Vendor\Package;

final class LocalThing {}
`)

	assertNoPHPDependencyUsage(t, reportData, "namespace declaration only")
}

func TestPHPAdapterIgnoresSameLineDeclareNamespaceDeclarationAsDependencyUsage(t *testing.T) {
	reportData := analysePHPDependencySource(t, `<?php declare(strict_types=1); namespace Vendor\Package;

final class LocalThing {}
`)

	assertNoPHPDependencyUsage(t, reportData, "same-line declare namespace declaration")
}

func TestPHPAdapterIgnoresMalformedGroupedUseStatement(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
use Vendor\Package\{Client, Broken;
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.UsedExportsCount != 0 || dep.TotalExportsCount != 0 {
		t.Fatalf("expected malformed grouped use to produce no dependency usage, report=%#v", dep)
	}
	if hasRiskCueCode(dep, "grouped-use-import") {
		t.Fatalf("did not expect grouped-use-import cue from malformed grouped use, got %#v", dep.RiskCues)
	}
	for _, rec := range dep.Recommendations {
		if rec.Code == "prefer-explicit-imports" {
			t.Fatalf("did not expect explicit-import recommendation from malformed grouped use, got %#v", dep.Recommendations)
		}
	}
	if !containsWarning(reportData.Warnings, "no imports found") {
		t.Fatalf("expected no-import warning for malformed grouped use, got %#v", reportData.Warnings)
	}
}

func TestPHPAdapterMarksUsageIncompleteWhenComposerLockIsOversized(t *testing.T) {
	repo := t.TempDir()
	const declaredDependency = "vendor/lib"
	writeFile(t, filepath.Join(repo, testComposerJSON), fmt.Sprintf(`{"require":{%q:"^1.0"}}`, declaredDependency))
	writeFile(t, filepath.Join(repo, testComposerLock), "{}"+strings.Repeat(" ", int(testMaxComposerLockBytes)))
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), `<?php
use Vendor\Lib\Client;
$client = new Client();
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     1,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if !containsWarning(reportData.Warnings, "skipped composer.lock because it exceeds") {
		t.Fatalf("expected oversized lock warning, got %#v", reportData.Warnings)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one top dependency, got %d", len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Name != declaredDependency {
		t.Fatalf("expected declared dependency in top report, got %q", dep.Name)
	}
	if !dep.UsageIncomplete {
		t.Fatalf("expected oversized composer.lock to make usage coverage incomplete")
	}
	if dep.RemovalCandidate != nil {
		t.Fatalf("expected incomplete usage to suppress removal-candidate scoring, got %#v", dep.RemovalCandidate)
	}
	for _, rec := range dep.Recommendations {
		if rec.Code == "remove-unused-dependency" || rec.Code == "low-usage-dependency" {
			t.Fatalf("did not expect removal recommendation with incomplete usage: %#v", dep.Recommendations)
		}
	}
}

func TestPHPAdapterAppliesShortOpenTagConfigToDirectorySubtree(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, "src", ".user.ini"), "short_open_tag = On\n")
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), `<? use Vendor\Package\Client;
$client = new Client();
`)
	writeFile(t, filepath.Join(repo, "templates", testIndexPHP), `<? use Vendor\Package\TemplateOnly;
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.UsedExportsCount != 1 || len(dep.UsedImports) != 1 || dep.UsedImports[0].Module != `Vendor\Package\Client` {
		t.Fatalf("expected scoped short-open config to expose only src usage, got %#v", dep)
	}
	if len(dep.UnusedImports) != 0 {
		t.Fatalf("did not expect short-tag imports outside configured subtree, got %#v", dep.UnusedImports)
	}
}

func TestPHPAdapterScopesOversizedShortOpenTagConfigIncompleteToAffectedSubtree(t *testing.T) {
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, "docs", ".user.ini"), "short_open_tag = On\n"+strings.Repeat(" ", int(testMaxPHPConfigBytes)))
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), `<?php
use Vendor\Package\Client;
$client = new Client();
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if reportData.UsageIncomplete {
		t.Fatalf("did not expect unrelated oversized config to mark report incomplete")
	}
	if !containsWarning(reportData.Warnings, "skipped PHP short_open_tag config docs") {
		t.Fatalf("expected oversized config warning, got %#v", reportData.Warnings)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(reportData.Dependencies))
	}
	if reportData.Dependencies[0].UsageIncomplete {
		t.Fatalf("did not expect unrelated oversized config to mark dependency incomplete")
	}
}

func TestPHPAdapterParsesUseStatementsInlineWithPHPOpenTag(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, testComposerJSON), fmt.Sprintf(`{"require":{%q:"^3.0"}}`, testMonologDependency))
	lockTemplate := `{
  "packages": [
    {
      "name": %q,
      "autoload": {"psr-4": {"Monolog\\": "src/Monolog"}}
    }
  ]
}
`
	lockContent := fmt.Sprintf(lockTemplate, testMonologDependency)
	writeFile(t, filepath.Join(repo, testComposerLock), lockContent)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), `<?php use Monolog\Logger; $logger = new Logger("app");
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: testMonologDependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.TotalExportsCount == 0 {
		t.Fatalf("expected inline open-tag use import to be counted")
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected inline open-tag use import usage to be counted")
	}
	if containsWarning(reportData.Warnings, "no imports found") {
		t.Fatalf("did not expect no-import warning for inline open-tag use import: %#v", reportData.Warnings)
	}
}

func TestPHPAdapterIgnoresNamespaceMentionsInCommentsAndStrings(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, testComposerJSON), fmt.Sprintf(`{"require":{%q:"^3.0"}}`, testMonologDependency))
	lockTemplate := `{
  "packages": [
    {
      "name": %q,
      "autoload": {"psr-4": {"Monolog\\": "src/Monolog"}}
    }
  ]
}
`
	lockContent := fmt.Sprintf(lockTemplate, testMonologDependency)
	writeFile(t, filepath.Join(repo, testComposerLock), lockContent)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), testPHPHeader+`
$className = "\\Monolog\\Logger";
// \Monolog\Logger should not count as usage
# \Monolog\Logger should not count as usage
/* \Monolog\Logger should not count as usage */
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: testMonologDependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.UsedExportsCount != 0 || dep.TotalExportsCount != 0 {
		t.Fatalf("expected no namespace imports to be counted from comments/strings, report=%#v", dep)
	}
	if !containsWarning(reportData.Warnings, "no imports found") {
		t.Fatalf("expected no-import warning when only comments/strings mention namespaces, got %#v", reportData.Warnings)
	}
}

func hasRiskCueCode(dep report.DependencyReport, code string) bool {
	for _, cue := range dep.RiskCues {
		if cue.Code == code {
			return true
		}
	}
	return false
}

func hasRecommendation(dep report.DependencyReport, code string) bool {
	for _, recommendation := range dep.Recommendations {
		if recommendation.Code == code {
			return true
		}
	}
	return false
}

func analysePHPDependencySource(t *testing.T, source string) report.Report {
	t.Helper()
	repo := t.TempDir()
	const dependency = "vendor/package"
	writeTestComposerPackage(t, repo, dependency, `Vendor\Package`)
	writeFile(t, filepath.Join(repo, "src", testIndexPHP), source)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: dependency,
	})
	if err != nil {
		t.Fatalf(testAnalyseErrFmt, err)
	}
	return reportData
}

func singlePHPDependencyReport(t *testing.T, deps []report.DependencyReport) report.DependencyReport {
	t.Helper()
	if len(deps) != 1 {
		t.Fatalf(testExpectedOneDependencyReportFmt, len(deps))
	}
	return deps[0]
}

func assertNoPHPDependencyUsage(t *testing.T, reportData report.Report, warningContext string) {
	t.Helper()
	dep := singlePHPDependencyReport(t, reportData.Dependencies)
	if dep.UsedExportsCount != 0 || dep.TotalExportsCount != 0 {
		t.Fatalf("expected %s to produce no dependency usage, report=%#v", warningContext, dep)
	}
	if !containsWarning(reportData.Warnings, "no imports found") {
		t.Fatalf("expected no-import warning for %s, got %#v", warningContext, reportData.Warnings)
	}
}

func assertActivePHPTraitDependency(t *testing.T, dep report.DependencyReport, wantModules []string) {
	t.Helper()
	if dep.UsedExportsCount != len(wantModules) || dep.TotalExportsCount != len(wantModules) || dep.UsedPercent != 100 {
		t.Fatalf("expected trait use to count as full dependency usage, report=%#v", dep)
	}
	if len(dep.UsedImports) != len(wantModules) {
		t.Fatalf("expected used trait imports %v, got %#v", wantModules, dep.UsedImports)
	}
	gotModules := make([]string, 0, len(dep.UsedImports))
	for _, used := range dep.UsedImports {
		gotModules = append(gotModules, used.Module)
	}
	wantModules = slices.Clone(wantModules)
	slices.Sort(gotModules)
	slices.Sort(wantModules)
	if !slices.Equal(gotModules, wantModules) {
		t.Fatalf("expected used trait modules %v, got %v", wantModules, gotModules)
	}
	if len(dep.UnusedImports) != 0 {
		t.Fatalf("did not expect unused imports for active trait use, got %#v", dep.UnusedImports)
	}
	if hasRecommendation(dep, "remove-unused-dependency") || hasRecommendation(dep, "low-usage-dependency") {
		t.Fatalf("did not expect dependency removal or low-usage recommendation for trait use, got %#v", dep.Recommendations)
	}
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
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
