package kotlinandroid

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestGradleManifestDiscoveryAndParsingStages(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, "gradle", "libs.versions.toml"), `
[libraries]
androidx-core-ktx = { module = "androidx.core:core-ktx", version = "1.13.1" }
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "app", buildGradleKTSName), `
dependencies {
  implementation(libs.androidx.core.ktx)
}
`)
	brokenBuildLink := filepath.Join(repo, "broken", buildGradleName)
	if err := os.MkdirAll(filepath.Dir(brokenBuildLink), 0o755); err != nil {
		t.Fatalf("mkdir broken dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(repo, "missing-build.gradle"), brokenBuildLink); err != nil {
		t.Fatalf("create broken build symlink: %v", err)
	}

	catalogResolver, catalogWarnings := shared.LoadGradleCatalogResolver(repo)
	if len(catalogWarnings) != 0 {
		t.Fatalf("catalog warnings: %v", catalogWarnings)
	}
	var descriptors []dependencyDescriptor
	var parseWarnings []string
	readCount := 0
	discovery, walkErr := discoverBuildFiles(repo, func(path, content string) {
		readCount++
		descriptors, parseWarnings = parseGradleDependencyContentWithCatalog(path, content, catalogResolver)
		for i := range descriptors {
			descriptors[i].FromManifest = true
		}
	}, buildGradleName, buildGradleKTSName)
	if walkErr != nil {
		t.Fatalf("discover build files: %v", walkErr)
	}
	if readCount != 1 {
		t.Fatalf("expected one readable build file, got %#v", readCount)
	}
	if len(discovery.Warnings) == 0 {
		t.Fatalf("expected unreadable build warning from discovery stage")
	}

	if len(parseWarnings) != 0 {
		t.Fatalf("unexpected manifest parse warnings: %#v", parseWarnings)
	}
	if len(descriptors) != 1 {
		t.Fatalf("expected one manifest descriptor, got %#v", descriptors)
	}
	descriptor := descriptors[0]
	if descriptor.Group != "androidx.core" || descriptor.Artifact != "core-ktx" || !descriptor.FromManifest {
		t.Fatalf("unexpected parsed manifest descriptor: %#v", descriptor)
	}
}

func TestGradleLockfileDiscoveryAndParsingStages(t *testing.T) {
	repo := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repo, gradleLockfileName), "androidx.core:core-ktx:1.13.1=compileClasspath\n")
	brokenLockLink := filepath.Join(repo, "broken", gradleLockfileName)
	if err := os.MkdirAll(filepath.Dir(brokenLockLink), 0o755); err != nil {
		t.Fatalf("mkdir broken dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(repo, "missing.lock"), brokenLockLink); err != nil {
		t.Fatalf("create broken lock symlink: %v", err)
	}

	var descriptors []dependencyDescriptor
	readCount := 0
	discovery, walkErr := discoverGradleLockfiles(repo, func(_ string, content string) {
		readCount++
		descriptors = parseGradleLockfileContent(content)
	})
	if walkErr != nil {
		t.Fatalf("discover lockfiles: %v", walkErr)
	}
	if !discovery.Matched {
		t.Fatalf("expected lockfile discovery to record a matched entry")
	}
	if readCount != 1 {
		t.Fatalf("expected one readable lockfile, got %#v", readCount)
	}
	if len(discovery.Warnings) == 0 {
		t.Fatalf("expected unreadable lockfile warning from discovery stage")
	}

	if len(descriptors) != 1 {
		t.Fatalf("expected one lockfile descriptor, got %#v", descriptors)
	}
	descriptor := descriptors[0]
	if descriptor.Group != "androidx.core" || descriptor.Artifact != "core-ktx" || descriptor.Version != "1.13.1" {
		t.Fatalf("unexpected parsed lockfile descriptor: %#v", descriptor)
	}
}

func TestBuildDependencyLookupIndexStage(t *testing.T) {
	const alphaCore = "alpha-core"

	lookups := buildDependencyLookupIndex([]dependencyDescriptor{
		{Name: alphaCore, Group: "com.example.alpha", Artifact: alphaCore},
		{Name: "alpha-runtime", Group: "org.sample.alpha", Artifact: "alpha-runtime"},
	})
	if got := lookups.Prefixes["com.example.alpha"]; got != alphaCore {
		t.Fatalf("expected group prefix lookup for alpha-core, got %q", got)
	}
	if got := lookups.Aliases["alpha.core"]; got != alphaCore {
		t.Fatalf("expected artifact alias lookup for alpha-core, got %q", got)
	}
	if _, ok := lookups.Ambiguous["alpha"]; !ok {
		t.Fatalf("expected ambiguous short alias for overlapping alpha groups, got %#v", lookups.Ambiguous)
	}
}
