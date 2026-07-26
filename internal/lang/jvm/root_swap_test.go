package jvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ben-ranford/lopper/internal/language"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestJVMDetectPinsRootAcrossRepoSwap(t *testing.T) {
	repo := canonicalRepoPath(t)
	writeJVMPomFile(t, repo, "<project></project>\n")

	withJVMDetectRootSignalsHook(t, func(rootPath string) error {
		swapRepoPath(t, rootPath, func(replacement string) {
			testutil.MustWriteFile(t, filepath.Join(replacement, "modules", "replacement", "src", "main", "java", "Main.java"), "class Main {}\n")
		})
		return nil
	})

	detection, err := NewAdapter().DetectWithConfidence(context.Background(), repo)
	if err != nil {
		t.Fatalf("detect with pinned root: %v", err)
	}
	if !detection.Matched {
		t.Fatalf("expected original root signal to remain visible, got %#v", detection)
	}
	if detection.Confidence != 65 {
		t.Fatalf("expected original pom signal and original pom traversal hit only, got %#v", detection)
	}
	if len(detection.Roots) != 1 || detection.Roots[0] != repo {
		t.Fatalf("expected replacement source tree to stay unread, got %#v", detection.Roots)
	}
}

func TestJVMAnalysePinsRootAcrossRepoSwap(t *testing.T) {
	repo := canonicalRepoPath(t)
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), `
dependencies {
  implementation "org.junit.jupiter:junit-jupiter-api:5.10.0"
}
`)
	testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", "com", "example", "Main.java"), `
package com.example;

import org.junit.jupiter.api.Test;

class Main {
  @Test void ok() {}
}
`)

	withJVMAnalyseRootOpenHook(t, func(rootPath string) error {
		swapRepoPath(t, rootPath, func(replacement string) {
			testutil.MustWriteFile(t, filepath.Join(replacement, buildGradleName), `
dependencies {
  implementation "com.evil:replacement:1.0.0"
}
`)
			testutil.MustWriteFile(t, filepath.Join(replacement, "src", "main", "java", "com", "evil", "Replacement.java"), `
package com.evil;

import com.evil.Replacement;

class Replacement {}
`)
		})
		return nil
	})

	result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
	if err != nil {
		t.Fatalf("analyse with pinned root: %v", err)
	}
	if len(result.Dependencies) != 1 || result.Dependencies[0].Name != "junit-jupiter-api" {
		t.Fatalf("expected original dependency only, got %#v", result.Dependencies)
	}
}

func TestJVMAnalysePinsCatalogResolverAcrossRepoSwap(t *testing.T) {
	for _, settingsFileName := range []string{"settings.gradle", "settings.gradle.kts"} {
		t.Run(settingsFileName, func(t *testing.T) {
			repo := canonicalRepoPath(t)
			testutil.MustWriteFile(t, filepath.Join(repo, settingsFileName), `
dependencyResolutionManagement {
  versionCatalogs {
    create("testLibs") {
      from(files("gradle/test-libs.versions.toml"))
    }
  }
}
`)
			testutil.MustWriteFile(t, filepath.Join(repo, buildGradleKTSName), `
dependencies {
  implementation(testLibs.junit.jupiter)
}
`)
			testutil.MustWriteFile(t, filepath.Join(repo, "gradle", "test-libs.versions.toml"), `
[versions]
junit = "5.10.0"

[libraries]
junit-jupiter = { group = "org.junit.jupiter", name = "junit-jupiter-api", version.ref = "junit" }
`)
			testutil.MustWriteFile(t, filepath.Join(repo, "src", "main", "java", "com", "example", "Main.java"), `
package com.example;

import org.junit.jupiter.api.Test;

class Main {
  @Test void ok() {}
}
`)

			withJVMAnalyseRootOpenHook(t, func(rootPath string) error {
				swapRepoPath(t, rootPath, func(replacement string) {
					testutil.MustWriteFile(t, filepath.Join(replacement, settingsFileName), `
dependencyResolutionManagement {
  versionCatalogs {
    create("testLibs") {
      from(files("gradle/evil.versions.toml"))
    }
  }
}
`)
					testutil.MustWriteFile(t, filepath.Join(replacement, buildGradleKTSName), `
dependencies {
  implementation(testLibs.junit.jupiter)
}
`)
					testutil.MustWriteFile(t, filepath.Join(replacement, "gradle", "evil.versions.toml"), `
[libraries]
junit-jupiter = { group = "com.evil", name = "replacement", version = "1.0.0" }
`)
					testutil.MustWriteFile(t, filepath.Join(replacement, "src", "main", "java", "com", "evil", "Replacement.java"), `
package com.evil;

import com.evil.Replacement;

class Replacement {}
`)
				})
				return nil
			})

			result, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 10})
			if err != nil {
				t.Fatalf("analyse with pinned catalog root: %v", err)
			}
			if len(result.Dependencies) != 1 || result.Dependencies[0].Name != "junit-jupiter-api" {
				t.Fatalf("expected original catalog dependency only, got %#v", result.Dependencies)
			}
		})
	}
}

func TestJVMAnalysePropagatesRootOpenHookError(t *testing.T) {
	repo := canonicalRepoPath(t)
	testutil.MustWriteFile(t, filepath.Join(repo, buildGradleName), `dependencies { implementation("org.junit.jupiter:junit-jupiter-api:5.10.0") }`)
	hookErr := errors.New("analyse root hook failed")
	withJVMAnalyseRootOpenHook(t, func(string) error { return hookErr })

	_, err := NewAdapter().Analyse(context.Background(), language.Request{RepoPath: repo, TopN: 1})
	if !errors.Is(err, hookErr) {
		t.Fatalf("expected analyse root hook error, got %v", err)
	}
}

func withJVMDetectRootSignalsHook(t *testing.T, hook func(string) error) {
	t.Helper()
	original := afterJVMDetectRootSignals
	afterJVMDetectRootSignals = hook
	t.Cleanup(func() {
		afterJVMDetectRootSignals = original
	})
}

func withJVMAnalyseRootOpenHook(t *testing.T, hook func(string) error) {
	t.Helper()
	original := afterJVMAnalyseRootOpen
	afterJVMAnalyseRootOpen = hook
	t.Cleanup(func() {
		afterJVMAnalyseRootOpen = original
	})
}

func swapRepoPath(t *testing.T, repoPath string, writeReplacement func(string)) {
	t.Helper()
	originalPath := repoPath + "-original"
	if err := os.Rename(repoPath, originalPath); err != nil {
		t.Skipf("repo swap unsupported: %v", err)
	}
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("recreate swapped repo root: %v", err)
	}
	writeReplacement(repoPath)
}
