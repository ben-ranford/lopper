package shared

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGradleCatalogResolverResolvesDefaultAndCustomCatalogs(t *testing.T) {
	repo := t.TempDir()
	writeGradleCatalogTestFile(t, filepath.Join(repo, "settings.gradle.kts"), `
dependencyResolutionManagement {
  versionCatalogs {
    create("testLibs") {
      from(files("gradle/test-libs.versions.toml"))
    }
  }
}
`)
	writeGradleCatalogTestFile(t, filepath.Join(repo, "gradle", "libs.versions.toml"), `
[libraries]
okhttp = { module = "com.squareup.okhttp3:okhttp", version = "4.12.0" }
retrofit = { module = "com.squareup.retrofit2:retrofit", version = "2.11.0" }

[bundles]
networking = ["okhttp", "retrofit"]
`)
	writeGradleCatalogTestFile(t, filepath.Join(repo, "gradle", "test-libs.versions.toml"), `
[versions]
junit = "5.10.0"

[libraries]
junit-jupiter = { group = "org.junit.jupiter", name = "junit-jupiter-api", version.ref = "junit" }
`)

	resolver, warnings := LoadGradleCatalogResolver(repo)
	if len(warnings) != 0 {
		t.Fatalf("expected no catalog load warnings, got %#v", warnings)
	}

	dependencies, parseWarnings := resolver.ParseDependencyReferences(filepath.Join(repo, "app", "build.gradle.kts"), `
dependencies {
  implementation(libs.bundles.networking)
  testImplementation(testLibs.findLibrary("junit-jupiter").get())
}
`)
	if len(parseWarnings) != 0 {
		t.Fatalf("expected no catalog parse warnings, got %#v", parseWarnings)
	}

	names := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		names = append(names, dependency.Artifact)
	}
	if !slices.Contains(names, "okhttp") || !slices.Contains(names, "retrofit") || !slices.Contains(names, "junit-jupiter-api") {
		t.Fatalf("expected bundle and custom catalog dependencies, got %#v", dependencies)
	}
}

func TestGradleCatalogResolverEmitsDeterministicWarnings(t *testing.T) {
	repo := t.TempDir()
	writeGradleCatalogTestFile(t, filepath.Join(repo, "gradle", "libs.versions.toml"), `
[libraries]
okhttp = { module = "com.squareup.okhttp3:okhttp", version = "4.12.0" }
`)

	resolver, warnings := LoadGradleCatalogResolver(repo)
	if len(warnings) != 0 {
		t.Fatalf("expected no catalog load warnings, got %#v", warnings)
	}

	dependencies, parseWarnings := resolver.ParseDependencyReferences(filepath.Join(repo, "build.gradle.kts"), `
dependencies {
  implementation(libs.okhttp)
  implementation(libs.missing)
  implementation(libs.bundles.networking)
  implementation(libs.versions.kotlin)
}
`)
	if len(dependencies) != 1 || dependencies[0].Artifact != "okhttp" {
		t.Fatalf("expected only resolvable okhttp dependency, got %#v", dependencies)
	}
	joined := strings.Join(parseWarnings, "\n")
	for _, want := range []string{
		"unable to resolve Gradle version catalog alias libs.missing",
		"unable to resolve Gradle version catalog bundle libs.bundles.networking",
		"unsupported Gradle version catalog reference libs.versions.kotlin",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected warning %q in %q", want, joined)
		}
	}
}

func TestIsGradleVersionCatalogFile(t *testing.T) {
	if !IsGradleVersionCatalogFile("libs.versions.toml") {
		t.Fatalf("expected libs.versions.toml to be treated as a cache-relevant Gradle catalog")
	}
	if IsGradleVersionCatalogFile("Cargo.toml") {
		t.Fatalf("did not expect Cargo.toml to be treated as a Gradle version catalog")
	}
}

func writeGradleCatalogTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
