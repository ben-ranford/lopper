package php

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/report"
)

func TestPHPAdapterDetectWithConfidence(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "composer.json"), `{
  "name": "acme/app",
  "require": {
    "monolog/monolog": "^3.0"
  }
}
`)
	writeFile(t, filepath.Join(repo, "src", "index.php"), "<?php\n")
	writeFile(t, filepath.Join(repo, "packages", "plugin", "composer.json"), `{"name":"acme/plugin"}`)

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
	writeFile(t, filepath.Join(repo, "composer.json"), `{
  "name": "acme/app",
  "require": {
    "php": "^8.2",
    "ext-json": "*",
    "monolog/monolog": "^3.0",
    "symfony/yaml": "^6.0"
  },
  "require-dev": {
    "phpunit/phpunit": "^10.0"
  },
  "autoload": {
    "psr-4": {
      "App\\\\": "src/"
    }
  }
}
`)
	writeFile(t, filepath.Join(repo, "composer.lock"), `{
  "packages": [
    {
      "name": "monolog/monolog",
      "autoload": {"psr-4": {"Monolog\\\\": "src/Monolog"}}
    },
    {
      "name": "symfony/yaml",
      "autoload": {"psr-4": {"Symfony\\\\Component\\\\Yaml\\\\": ""}}
    }
  ],
  "packages-dev": [
    {
      "name": "phpunit/phpunit",
      "autoload": {"psr-4": {"PHPUnit\\\\Framework\\\\": "src"}}
    }
  ]
}
`)
	writeFile(t, filepath.Join(repo, "src", "index.php"), `<?php
use Monolog\\Logger;
use Monolog\\{Handler\\StreamHandler, Formatter\\LineFormatter as LineFmt};
use Symfony\\Component\\Yaml\\Yaml;

$className = "Monolog\\Logger";
class_exists($className);

$logger = new Logger("app");
$yaml = Yaml::parse("foo: bar");
`)

	adapter := NewAdapter()
	depReport, err := adapter.Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "monolog/monolog",
		TopN:       0,
	})
	if err != nil {
		t.Fatalf("analyse dependency: %v", err)
	}
	if len(depReport.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(depReport.Dependencies))
	}
	dep := depReport.Dependencies[0]
	if dep.Language != "php" {
		t.Fatalf("expected php language, got %q", dep.Language)
	}
	if dep.Name != "monolog/monolog" {
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
	writeFile(t, filepath.Join(repo, "composer.json"), `{"require":{"symfony/yaml":"^6.0"}}`)
	writeFile(t, filepath.Join(repo, "composer.lock"), `{
  "packages": [
    {
      "name": "symfony/yaml",
      "autoload": {"psr-4": {"Symfony\\\\Component\\\\Yaml\\\\": ""}}
    }
  ]
}
`)
	writeFile(t, filepath.Join(repo, "src", "index.php"), `<?php
use Symfony\\Component\\Yaml\\Yaml;
Yaml::parse("foo: bar");
`)
	writeFile(t, filepath.Join(repo, "packages", "nested", "composer.json"), `{"name":"acme/nested"}`)
	writeFile(t, filepath.Join(repo, "packages", "nested", "src", "nested.php"), `<?php
use Symfony\\Component\\Yaml\\Yaml;
Yaml::parse("foo: bar");
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "symfony/yaml",
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
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

func hasRiskCueCode(dep report.DependencyReport, code string) bool {
	for _, cue := range dep.RiskCues {
		if cue.Code == code {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
