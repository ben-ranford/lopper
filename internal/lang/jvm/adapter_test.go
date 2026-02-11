package jvm

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
)

func TestAdapterDetectWithGradleAndJava(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "build.gradle"), "dependencies { implementation 'org.junit.jupiter:junit-jupiter-api:5.10.0' }\n")
	writeFile(t, filepath.Join(repo, "src", "main", "java", "App.java"), "import org.junit.jupiter.api.Test;\nclass App {}\n")

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected jvm detection to match")
	}
	if detection.Confidence <= 0 {
		t.Fatalf("expected confidence > 0, got %d", detection.Confidence)
	}
}

func TestAdapterAnalyseDependency(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "pom.xml"), `
<project>
  <dependencies>
    <dependency>
      <groupId>org.junit.jupiter</groupId>
      <artifactId>junit-jupiter-api</artifactId>
      <version>5.10.0</version>
    </dependency>
  </dependencies>
</project>
`)
	writeFile(t, filepath.Join(repo, "src", "test", "java", "ExampleTest.java"), `
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.Assertions;

class ExampleTest {
  @Test
  void runs() {
    Assertions.assertTrue(true);
  }
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath:   repo,
		Dependency: "junit-jupiter-api",
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) != 1 {
		t.Fatalf("expected one dependency report, got %d", len(reportData.Dependencies))
	}
	dep := reportData.Dependencies[0]
	if dep.Language != "jvm" {
		t.Fatalf("expected language jvm, got %q", dep.Language)
	}
	if dep.UsedExportsCount == 0 {
		t.Fatalf("expected used exports > 0")
	}
}

func TestAdapterAnalyseTopN(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "build.gradle.kts"), `
dependencies {
  implementation("com.squareup.okhttp3:okhttp:4.12.0")
  implementation("org.junit.jupiter:junit-jupiter-api:5.10.0")
}
`)
	writeFile(t, filepath.Join(repo, "src", "main", "kotlin", "Main.kt"), `
import okhttp3.OkHttpClient
import org.junit.jupiter.api.Assertions

fun run() {
  OkHttpClient()
}
`)

	reportData, err := NewAdapter().Analyse(context.Background(), language.Request{
		RepoPath: repo,
		TopN:     5,
	})
	if err != nil {
		t.Fatalf("analyse: %v", err)
	}
	if len(reportData.Dependencies) == 0 {
		t.Fatalf("expected dependencies in top-N report")
	}
	names := make([]string, 0, len(reportData.Dependencies))
	for _, dep := range reportData.Dependencies {
		names = append(names, dep.Name)
	}
	if !slices.Contains(names, "okhttp") {
		t.Fatalf("expected okhttp dependency in %#v", names)
	}
}

func TestAdapterMetadataAndDetect(t *testing.T) {
	adapter := NewAdapter()
	if adapter.ID() != "jvm" {
		t.Fatalf("unexpected adapter id: %q", adapter.ID())
	}
	aliases := adapter.Aliases()
	if !slices.Contains(aliases, "java") || !slices.Contains(aliases, "kotlin") {
		t.Fatalf("unexpected adapter aliases: %#v", aliases)
	}

	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "pom.xml"), "<project/>")
	ok, err := adapter.Detect(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if !ok {
		t.Fatalf("expected detect=true when pom.xml exists")
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
