package jvm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestJVMSeamUnitsComposeForDeclaredDependencyUsage(t *testing.T) {
	repo := t.TempDir()
	writeJVMPomFile(t, repo, `
<project>
  <dependencies>
    <dependency>
      <groupId>org.junit</groupId>
      <artifactId>junit</artifactId>
      <version>4.13.2</version>
    </dependency>
  </dependencies>
</project>
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", "com", "example", "Main.java"), `
package com.example;

import org.junit.Assert;

class Main {
  void check() {
    Assert.assertTrue(true);
  }
}
`)

	adapter := NewAdapter()
	detection, err := adapter.DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with confidence: %v", err)
	}
	if !detection.Matched || detection.Confidence == 0 || len(detection.Roots) == 0 {
		t.Fatalf("expected JVM detection signals, got %#v", detection)
	}

	descriptors, prefixes, aliases, warnings := collectDeclaredDependencies(repo)
	if len(warnings) != 0 {
		t.Fatalf("expected no manifest parse warnings, got %#v", warnings)
	}
	if len(descriptors) != 1 || descriptors[0].Name != "junit" {
		t.Fatalf("expected junit descriptor, got %#v", descriptors)
	}

	scan, err := scanRepo(context.Background(), repo, prefixes, aliases)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}
	if len(scan.Files) != 1 {
		t.Fatalf("expected one scanned source file, got %#v", scan.Files)
	}

	reports, reportWarnings := buildRequestedJVMDependencies(language.Request{Dependency: "junit"}, scan)
	if len(reportWarnings) != 0 {
		t.Fatalf("expected no dependency report warnings, got %#v", reportWarnings)
	}
	if len(reports) != 1 {
		t.Fatalf("expected one dependency report, got %#v", reports)
	}
	report := reports[0]
	if report.Name != "junit" || len(report.UsedImports) == 0 {
		t.Fatalf("expected used junit imports in report, got %#v", report)
	}
}
