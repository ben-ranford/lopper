package app

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/gitexec"
)

const (
	manifestFileName         = "package.json"
	lockfileName             = "package-lock.json"
	newUntrackedFileName     = "new-untracked.txt"
	poetryLockName           = "poetry.lock"
	dotnetProjectManifest    = "App.csproj"
	dotnetCentralManifest    = "Directory.Packages.props"
	dotnetLockfileName       = "packages.lock.json"
	dartManifestName         = "pubspec.yaml"
	dartLockfileName         = "pubspec.lock"
	elixirManifestName       = "mix.exs"
	elixirLockfileName       = "mix.lock"
	swiftManifestName        = "Package.swift"
	swiftLockfileName        = "Package.resolved"
	detectLockfileDriftFmt   = "detect lockfile drift: %v"
	demoPackageJSON          = "{\n  \"name\": \"demo\"\n}\n"
	demoPackageJSONUpdated   = "{\n  \"name\": \"demo\",\n  \"version\": \"1.0.1\"\n}\n"
	demoPackageJSONUpdatedV2 = "{\n  \"name\": \"demo\",\n  \"version\": \"2.0.0\"\n}\n"
	nestedManifestPath       = "nested/package.json"
	gitBinaryPath            = "/usr/bin/git"
	gitExecutableNotFoundErr = "git executable not found"
)

func TestDetectLockfileDriftGitManifestChangeWithoutLockfileChange(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, lockfileName), demoPackageJSON)
	initGitRepo(t, repo)

	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSONUpdated)

	warnings, err := detectLockfileDrift(context.Background(), repo, false)
	if err != nil {
		t.Fatalf(detectLockfileDriftFmt, err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], "package.json changed while no matching lockfile changed") {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
}

func TestDetectLockfileDriftRubyManifestChangeWithoutLockfileChange(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "Gemfile"), "source 'https://rubygems.org'\ngem 'httparty'\n")
	writeFile(t, filepath.Join(repo, "Gemfile.lock"), "GEM\n  specs:\n    httparty (0.22.0)\n")
	initGitRepo(t, repo)

	writeFile(t, filepath.Join(repo, "Gemfile"), "source 'https://rubygems.org'\ngem 'httparty'\ngem 'rack'\n")

	warnings, err := detectLockfileDrift(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("detect lockfile drift: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], "Bundler in .: Gemfile changed while no matching lockfile changed") {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
	if !strings.Contains(warnings[0], "bundle install") {
		t.Fatalf("expected Bundler remediation text, got %q", warnings[0])
	}
}

func TestDetectLockfileDriftPreviewEcosystemsManifestChangeWithoutLockfileChange(t *testing.T) {
	cases := []struct {
		name            string
		manifest        string
		lockfile        string
		initialManifest string
		initialLockfile string
		updatedManifest string
		wantWarning     string
		wantRemedy      string
	}{
		{
			name:            "dotnet project",
			manifest:        dotnetProjectManifest,
			lockfile:        dotnetLockfileName,
			initialManifest: "<Project Sdk=\"Microsoft.NET.Sdk\"><ItemGroup><PackageReference Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n",
			initialLockfile: "{\"version\":1,\"dependencies\":{}}\n",
			updatedManifest: "<Project Sdk=\"Microsoft.NET.Sdk\"><ItemGroup><PackageReference Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /><PackageReference Include=\"Serilog\" Version=\"3.1.0\" /></ItemGroup></Project>\n",
			wantWarning:     ".NET in .: App.csproj changed while no matching lockfile changed",
			wantRemedy:      "dotnet restore --use-lock-file",
		},
		{
			name:            "dotnet central package management",
			manifest:        dotnetCentralManifest,
			lockfile:        dotnetLockfileName,
			initialManifest: "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n",
			initialLockfile: "{\"version\":1,\"dependencies\":{}}\n",
			updatedManifest: "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /><PackageVersion Include=\"Serilog\" Version=\"3.1.0\" /></ItemGroup></Project>\n",
			wantWarning:     ".NET in .: Directory.Packages.props changed while no matching lockfile changed",
			wantRemedy:      "dotnet restore --use-lock-file",
		},
		{
			name:            "dart",
			manifest:        dartManifestName,
			lockfile:        dartLockfileName,
			initialManifest: "name: demo\ndependencies:\n  http: ^1.2.0\n",
			initialLockfile: "packages:\n  http:\n    version: \"1.2.0\"\n",
			updatedManifest: "name: demo\ndependencies:\n  http: ^1.3.0\n",
			wantWarning:     "Dart in .: pubspec.yaml changed while no matching lockfile changed",
			wantRemedy:      "dart pub get",
		},
		{
			name:            "elixir",
			manifest:        elixirManifestName,
			lockfile:        elixirLockfileName,
			initialManifest: "defmodule Demo.MixProject do\n  use Mix.Project\n  def project, do: [app: :demo, version: \"0.1.0\", deps: deps()]\n  defp deps, do: [{:jason, \"~> 1.4\"}]\nend\n",
			initialLockfile: "%{\"jason\" => {:hex, :jason, \"1.4.1\", \"checksum\", [:mix], [], \"hexpm\", \"checksum\"}}\n",
			updatedManifest: "defmodule Demo.MixProject do\n  use Mix.Project\n  def project, do: [app: :demo, version: \"0.1.0\", deps: deps()]\n  defp deps, do: [{:jason, \"~> 1.4\"}, {:plug, \"~> 1.15\"}]\nend\n",
			wantWarning:     "Elixir in .: mix.exs changed while no matching lockfile changed",
			wantRemedy:      "mix deps.get",
		},
		{
			name:            "swift package manager",
			manifest:        swiftManifestName,
			lockfile:        swiftLockfileName,
			initialManifest: "// swift-tools-version: 5.9\nimport PackageDescription\nlet package = Package(name: \"Demo\", dependencies: [.package(url: \"https://github.com/apple/swift-argument-parser\", from: \"1.3.0\")], targets: [.target(name: \"Demo\")])\n",
			initialLockfile: "{\"pins\":[],\"version\":2}\n",
			updatedManifest: "// swift-tools-version: 5.9\nimport PackageDescription\nlet package = Package(name: \"Demo\", dependencies: [.package(url: \"https://github.com/apple/swift-argument-parser\", from: \"1.3.0\"), .package(url: \"https://github.com/pointfreeco/swift-dependencies\", from: \"1.3.0\")], targets: [.target(name: \"Demo\")])\n",
			wantWarning:     "SwiftPM in .: Package.swift changed while no matching lockfile changed",
			wantRemedy:      "swift package resolve",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, tc.manifest), tc.initialManifest)
			writeFile(t, filepath.Join(repo, tc.lockfile), tc.initialLockfile)
			initGitRepo(t, repo)

			writeFile(t, filepath.Join(repo, tc.manifest), tc.updatedManifest)

			warnings, err := detectLockfileDriftWithFeatures(context.Background(), repo, false, lockfileDriftFeatureSet(t, true))
			assertSingleLockfileDriftWarning(t, warnings, err, tc.wantWarning, tc.wantRemedy)
		})
	}
}

func TestDetectLockfileDriftPreviewEcosystemsMissingLockfile(t *testing.T) {
	cases := []struct {
		name         string
		manifest     string
		manifestBody string
		wantWarning  string
		wantRemedy   string
	}{
		{
			name:         "dotnet project",
			manifest:     dotnetProjectManifest,
			manifestBody: "<Project Sdk=\"Microsoft.NET.Sdk\"><ItemGroup><PackageReference Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n",
			wantWarning:  ".NET in .: App.csproj exists but no matching lockfile (packages.lock.json) was found",
			wantRemedy:   "dotnet restore --use-lock-file",
		},
		{
			name:         "dotnet central package management",
			manifest:     dotnetCentralManifest,
			manifestBody: "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n",
			wantWarning:  ".NET in .: Directory.Packages.props exists but no matching lockfile (packages.lock.json) was found",
			wantRemedy:   "dotnet restore --use-lock-file",
		},
		{
			name:         "dart",
			manifest:     dartManifestName,
			manifestBody: "name: demo\ndependencies:\n  http: ^1.2.0\n",
			wantWarning:  "Dart in .: pubspec.yaml exists but no matching lockfile (pubspec.lock) was found",
			wantRemedy:   "dart pub get",
		},
		{
			name:         "elixir",
			manifest:     elixirManifestName,
			manifestBody: "defmodule Demo.MixProject do\n  use Mix.Project\n  def project, do: [app: :demo, version: \"0.1.0\", deps: deps()]\n  defp deps, do: [{:jason, \"~> 1.4\"}]\nend\n",
			wantWarning:  "Elixir in .: mix.exs exists but no matching lockfile (mix.lock) was found",
			wantRemedy:   "mix deps.get",
		},
		{
			name:         "swift package manager",
			manifest:     swiftManifestName,
			manifestBody: "// swift-tools-version: 5.9\nimport PackageDescription\nlet package = Package(name: \"Demo\", dependencies: [.package(url: \"https://github.com/apple/swift-argument-parser\", from: \"1.3.0\")], targets: [.target(name: \"Demo\")])\n",
			wantWarning:  "SwiftPM in .: Package.swift exists but no matching lockfile (Package.resolved) was found",
			wantRemedy:   "swift package resolve",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, tc.manifest), tc.manifestBody)

			warnings, err := detectLockfileDriftWithFeatures(context.Background(), repo, false, lockfileDriftFeatureSet(t, true))
			assertSingleLockfileDriftWarning(t, warnings, err, tc.wantWarning, tc.wantRemedy)
		})
	}
}

func TestDetectLockfileDriftDotnetCentralProjectLockfilesAvoidRootMissingWarning(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, dotnetCentralManifest), "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n")
	writeFile(t, filepath.Join(repo, "src", "App", dotnetProjectManifest), "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>\n")
	writeFile(t, filepath.Join(repo, "src", "App", dotnetLockfileName), "{\"version\":1,\"dependencies\":{}}\n")

	warnings, err := detectLockfileDriftWithFeatures(context.Background(), repo, false, lockfileDriftFeatureSet(t, true))
	if err != nil {
		t.Fatalf(detectLockfileDriftFmt, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings when only project-level .NET lockfiles are present, got %#v", warnings)
	}
}

func TestDetectLockfileDriftDotnetCentralProjectLockfileManifestChange(t *testing.T) {
	t.Run("warns when central manifest changes without project lockfile changes", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, dotnetCentralManifest), "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetProjectManifest), "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetLockfileName), "{\"version\":1,\"dependencies\":{}}\n")
		initGitRepo(t, repo)

		writeFile(t, filepath.Join(repo, dotnetCentralManifest), "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /><PackageVersion Include=\"Serilog\" Version=\"3.1.0\" /></ItemGroup></Project>\n")

		warnings, err := detectLockfileDriftWithFeatures(context.Background(), repo, false, lockfileDriftFeatureSet(t, true))
		assertSingleLockfileDriftWarning(t, warnings, err, ".NET in .: Directory.Packages.props changed while no matching lockfile changed", "dotnet restore --use-lock-file")
	})

	t.Run("does not warn when central manifest and project lockfile both change", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, dotnetCentralManifest), "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /></ItemGroup></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetProjectManifest), "<Project Sdk=\"Microsoft.NET.Sdk\"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetLockfileName), "{\"version\":1,\"dependencies\":{}}\n")
		initGitRepo(t, repo)

		writeFile(t, filepath.Join(repo, dotnetCentralManifest), "<Project><ItemGroup><PackageVersion Include=\"Newtonsoft.Json\" Version=\"13.0.3\" /><PackageVersion Include=\"Serilog\" Version=\"3.1.0\" /></ItemGroup></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetLockfileName), "{\"version\":1,\"dependencies\":{\"Serilog\":\"3.1.0\"}}\n")

		warnings, err := detectLockfileDriftWithFeatures(context.Background(), repo, false, lockfileDriftFeatureSet(t, true))
		if err != nil {
			t.Fatalf(detectLockfileDriftFmt, err)
		}
		if len(warnings) != 0 {
			t.Fatalf("expected no warnings when project lockfiles changed with Directory.Packages.props, got %#v", warnings)
		}
	})
}

func TestDetectLockfileDriftEcosystemExpansionPreviewDisabledPreservesCurrentBehavior(t *testing.T) {
	t.Run("preview ecosystems stay disabled", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, dartManifestName), "name: demo\ndependencies:\n  http: ^1.2.0\n")
		writeFile(t, filepath.Join(repo, dartLockfileName), "packages:\n  http:\n    version: \"1.2.0\"\n")
		initGitRepo(t, repo)

		writeFile(t, filepath.Join(repo, dartManifestName), "name: demo\ndependencies:\n  http: ^1.3.0\n")

		warnings, err := detectLockfileDriftWithFeatures(context.Background(), repo, false, lockfileDriftFeatureSet(t, false))
		if err != nil {
			t.Fatalf(detectLockfileDriftFmt, err)
		}
		if len(warnings) != 0 {
			t.Fatalf("expected no preview warning when feature is disabled, got %#v", warnings)
		}
	})

	t.Run("preview missing lockfiles stay disabled", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, dartManifestName), "name: demo\ndependencies:\n  http: ^1.2.0\n")

		warnings, err := detectLockfileDriftWithFeatures(context.Background(), repo, false, lockfileDriftFeatureSet(t, false))
		if err != nil {
			t.Fatalf(detectLockfileDriftFmt, err)
		}
		if len(warnings) != 0 {
			t.Fatalf("expected no preview missing-lockfile warning when feature is disabled, got %#v", warnings)
		}
	})

	t.Run("existing ecosystems stay unchanged", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
		writeFile(t, filepath.Join(repo, lockfileName), demoPackageJSON)
		initGitRepo(t, repo)

		writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSONUpdated)

		warnings, err := detectLockfileDriftWithFeatures(context.Background(), repo, false, lockfileDriftFeatureSet(t, false))
		if err != nil {
			t.Fatalf(detectLockfileDriftFmt, err)
		}
		if len(warnings) != 1 {
			t.Fatalf("expected existing npm warning, got %#v", warnings)
		}
		if !strings.Contains(warnings[0], "npm in .: package.json changed while no matching lockfile changed") {
			t.Fatalf("unexpected warning: %q", warnings[0])
		}
	})
}

func TestDetectLockfileDriftSkipsLopperCache(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, ".lopper-cache", "nested", manifestFileName), "{\n  \"name\": \"cache-only\"\n}\n")

	warnings, err := detectLockfileDrift(context.Background(), repo, false)
	if err != nil {
		t.Fatalf(detectLockfileDriftFmt, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings from .lopper-cache contents, got %#v", warnings)
	}
}

func TestEvaluateLockfileDriftPolicyFailFormatsSinglePrefix(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, "composer.lock"), "{}\n")

	warnings, err := evaluateLockfileDriftPolicy(context.Background(), repo, "fail")
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected ErrLockfileDrift, got %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected fail policy to stop after first warning, got %#v", warnings)
	}
	if strings.Count(err.Error(), "lockfile drift detected") != 1 {
		t.Fatalf("expected single lockfile drift prefix in error, got %q", err.Error())
	}
}

func TestEvaluateLockfileDriftPolicyOff(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)

	warnings, err := evaluateLockfileDriftPolicy(context.Background(), repo, "off")
	if err != nil {
		t.Fatalf("evaluate off policy: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for off policy, got %#v", warnings)
	}
}

func TestFormatLockfileDriftErrorNoWarnings(t *testing.T) {
	err := formatLockfileDriftError(nil)
	if !errors.Is(err, ErrLockfileDrift) {
		t.Fatalf("expected ErrLockfileDrift for empty warnings, got %v", err)
	}
}

func TestSanitizedGitEnvPinsSafePath(t *testing.T) {
	t.Setenv("PATH", "/tmp/user-bin:/Users/test/bin")
	t.Setenv("GIT_DIR", "/tmp/fake-git-dir")
	t.Setenv("GIT_WORK_TREE", "/tmp/fake-worktree")
	t.Setenv("GIT_INDEX_FILE", "/tmp/fake-index")
	t.Setenv("KEEP_ME", "1")

	env := sanitizedGitEnv()

	if !containsEnv(env, gitexec.SafeSystemPath) {
		t.Fatalf("expected safe path %q in env, got %#v", gitexec.SafeSystemPath, env)
	}
	if containsEnvPrefix(env, "PATH=") && !containsEnv(env, gitexec.SafeSystemPath) {
		t.Fatalf("expected only pinned PATH in env, got %#v", env)
	}
	if containsEnvPrefix(env, "GIT_DIR=") || containsEnvPrefix(env, "GIT_WORK_TREE=") || containsEnvPrefix(env, "GIT_INDEX_FILE=") {
		t.Fatalf("expected git override vars to be stripped, got %#v", env)
	}
	if !containsEnv(env, "KEEP_ME=1") {
		t.Fatalf("expected unrelated env vars to be preserved, got %#v", env)
	}
}

func TestDetectLockfileDriftStopOnFirst(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	writeFile(t, filepath.Join(repo, "composer.lock"), "{}\n")

	warnings, err := detectLockfileDrift(context.Background(), repo, true)
	if err != nil {
		t.Fatalf("detect lockfile drift stop on first: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning in stop-on-first mode, got %#v", warnings)
	}
}

func TestDetectLockfileDriftContextCancelled(t *testing.T) {
	repo := t.TempDir()
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := detectLockfileDrift(cancelledCtx, repo, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestDetectLockfileDriftInvalidRepoPath(t *testing.T) {
	_, err := detectLockfileDrift(context.Background(), filepath.Join(t.TempDir(), "missing"), false)
	if err == nil {
		t.Fatalf("expected normalize/walk error for missing repo path")
	}
}

func TestDetectLockfileDriftWithFeaturesPropagatesGitContextError(t *testing.T) {
	repo := t.TempDir()
	original := collectLockfileGitContextFn
	collectLockfileGitContextFn = func(context.Context, string) (lockfileGitContext, error) {
		return lockfileGitContext{}, errors.New("forced git context failure")
	}
	defer func() { collectLockfileGitContextFn = original }()

	_, err := detectLockfileDriftWithFeatures(context.Background(), repo, false, featureflags.Set{})
	if err == nil || !strings.Contains(err.Error(), "forced git context failure") {
		t.Fatalf("expected git context failure, got %v", err)
	}
}

func TestDetectLockfileDriftWithFeaturesNormalizePathError(t *testing.T) {
	original := normalizeRepoPathFn
	normalizeRepoPathFn = func(string) (string, error) { return "", errors.New("forced normalize error") }
	defer func() { normalizeRepoPathFn = original }()

	_, err := detectLockfileDriftWithFeatures(context.Background(), t.TempDir(), false, featureflags.Set{})
	if err == nil || !strings.Contains(err.Error(), "forced normalize error") {
		t.Fatalf("expected normalize path error, got %v", err)
	}
}

func TestReadDirectoryFilesMissingPath(t *testing.T) {
	_, err := readDirectoryFiles(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatalf("expected readDirectoryFiles to fail for missing path")
	}
}

func TestScanLockfileDriftMissingRepoPath(t *testing.T) {
	_, err := scanLockfileDrift(context.Background(), filepath.Join(t.TempDir(), "missing"), lockfileGitContext{}, false, lockfileRules)
	if err == nil {
		t.Fatalf("expected scanLockfileDrift to fail for missing repo path")
	}
}

func TestGitHelperErrors(t *testing.T) {
	repo := t.TempDir()
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil {
		t.Fatalf("expected tracked changes command to fail outside git repo")
	}
	if _, err := gitUntrackedFiles(context.Background(), repo); err == nil {
		t.Fatalf("expected untracked files command to fail outside git repo")
	}
	if isGitWorktree(context.Background(), repo) {
		t.Fatalf("expected non-git temp dir to not be worktree")
	}
}

func TestGitHelperNilContextErrors(t *testing.T) {
	repo := t.TempDir()
	//nolint:staticcheck // Deliberate nil context validation coverage.
	if _, err := gitTrackedChanges(nil, repo); err == nil {
		t.Fatalf("expected tracked changes command with nil context to fail outside git repo")
	}
	//nolint:staticcheck // Deliberate nil context validation coverage.
	if _, err := gitUntrackedFiles(nil, repo); err == nil {
		t.Fatalf("expected untracked files command with nil context to fail outside git repo")
	}
	//nolint:staticcheck // Deliberate nil context validation coverage.
	if isGitWorktree(nil, repo) {
		t.Fatalf("expected non-git temp dir to not be worktree with nil context")
	}
}

func TestGitHelpersWhenGitUnavailable(t *testing.T) {
	original := resolveGitBinaryPathFn
	resolveGitBinaryPathFn = func() (string, error) { return "", errors.New(gitExecutableNotFoundErr) }
	defer func() { resolveGitBinaryPathFn = original }()

	repo := t.TempDir()
	if isGitWorktree(context.Background(), repo) {
		t.Fatalf("expected git worktree=false when git is unavailable")
	}
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil || !strings.Contains(err.Error(), gitExecutableNotFoundErr) {
		t.Fatalf("expected tracked changes error for missing git executable, got %v", err)
	}
	if _, err := gitUntrackedFiles(context.Background(), repo); err == nil || !strings.Contains(err.Error(), gitExecutableNotFoundErr) {
		t.Fatalf("expected untracked files error for missing git executable, got %v", err)
	}
}

func TestGitHelpersFallbackExecutableBranch(t *testing.T) {
	original := resolveGitBinaryPathFn
	resolveGitBinaryPathFn = func() (string, error) { return gitBinaryPath, nil }
	defer func() { resolveGitBinaryPathFn = original }()

	repo := t.TempDir()
	if isGitWorktree(context.Background(), repo) {
		t.Fatalf("expected non-git temp dir to not be worktree when fallback git is used")
	}
	if _, err := gitTrackedChanges(context.Background(), repo); err == nil {
		t.Fatalf("expected tracked changes command to fail outside git repo with fallback git")
	}
	if _, err := gitUntrackedFiles(context.Background(), repo); err == nil {
		t.Fatalf("expected untracked files command to fail outside git repo with fallback git")
	}
}

func TestGitChangedFilesOutsideGitRepo(t *testing.T) {
	repo := t.TempDir()
	changed, hasGit, err := gitChangedFiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("gitChangedFiles outside git repo: %v", err)
	}
	if hasGit {
		t.Fatalf("expected hasGit=false for non-git repo")
	}
	if len(changed) != 0 {
		t.Fatalf("expected no changed files for non-git repo, got %#v", changed)
	}
}

func TestGitChangedFilesInGitRepo(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	initGitRepo(t, repo)

	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSONUpdatedV2)
	writeFile(t, filepath.Join(repo, newUntrackedFileName), "untracked\n")

	changed, hasGit, err := gitChangedFiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("gitChangedFiles in git repo: %v", err)
	}
	if !hasGit {
		t.Fatalf("expected hasGit=true for git repo")
	}
	assertChangedPathsPresent(t, changed, manifestFileName, newUntrackedFileName)
}

func TestGitChangedFilesHandlesRepoWithNoHEAD(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	runGit(t, repo, "add", manifestFileName)
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSONUpdated)
	writeFile(t, filepath.Join(repo, newUntrackedFileName), "untracked\n")

	changed, hasGit, err := gitChangedFiles(context.Background(), repo)
	if err != nil {
		t.Fatalf("expected gitChangedFiles to succeed when HEAD is missing, got %v", err)
	}
	if !hasGit {
		t.Fatalf("expected hasGit=true when inside git worktree")
	}
	assertChangedPathsPresent(t, changed, manifestFileName, newUntrackedFileName)
}

func TestDetectLockfileDriftNoHeadDoesNotReturnGitError(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, manifestFileName), demoPackageJSON)
	runGit(t, repo, "init")

	warnings, err := detectLockfileDrift(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("expected detectLockfileDrift to continue when HEAD is missing, got %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning for missing lockfile, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], "no matching lockfile") {
		t.Fatalf("unexpected warning: %#v", warnings)
	}
}

func TestDetectDriftForRuleCases(t *testing.T) {
	repo := t.TempDir()
	manifest := filepath.Join(repo, manifestFileName)
	lock := filepath.Join(repo, lockfileName)
	writeFile(t, manifest, demoPackageJSON)
	writeFile(t, lock, demoPackageJSON)
	manifestInfo, err := os.Stat(manifest)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	lockInfo, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("stat lockfile: %v", err)
	}

	rule := lockfileRule{
		manager:   "npm",
		manifest:  manifestFileName,
		lockfiles: []string{lockfileName},
		remedy:    "run npm install",
	}
	files := map[string]fs.FileInfo{
		manifestFileName: manifestInfo,
		lockfileName:     lockInfo,
	}
	missingManifest := map[string]fs.FileInfo{lockfileName: lockInfo}
	missingLockfile := map[string]fs.FileInfo{manifestFileName: manifestInfo}
	cases := []struct {
		name         string
		files        map[string]fs.FileInfo
		changed      map[string]struct{}
		hasGit       bool
		wantWarnings int
		wantSubstr   string
	}{
		{name: "non-git-context", files: files, changed: map[string]struct{}{manifestFileName: {}}, hasGit: false, wantWarnings: 0},
		{name: "manifest-not-changed", files: files, changed: map[string]struct{}{lockfileName: {}}, hasGit: true, wantWarnings: 0},
		{name: "manifest-and-lockfile-changed", files: files, changed: map[string]struct{}{manifestFileName: {}, lockfileName: {}}, hasGit: true, wantWarnings: 0},
		{name: "manifest-only-changed", files: files, changed: map[string]struct{}{manifestFileName: {}}, hasGit: true, wantWarnings: 1, wantSubstr: "changed while no matching lockfile changed"},
		{name: "manifest-without-lockfile", files: missingLockfile, changed: nil, hasGit: false, wantWarnings: 1, wantSubstr: "no matching lockfile"},
		{name: "lockfile-without-manifest", files: missingManifest, changed: nil, hasGit: false, wantWarnings: 1, wantSubstr: "exists without package.json"},
	}
	runDetectDriftCases(t, repo, rule, cases)
}

func runDetectDriftCases(t *testing.T, repo string, rule lockfileRule, cases []struct {
	name         string
	files        map[string]fs.FileInfo
	changed      map[string]struct{}
	hasGit       bool
	wantWarnings int
	wantSubstr   string
}) {
	t.Helper()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			warnings, err := detectDriftForRule(repo, repo, tc.files, rule, tc.changed, tc.hasGit)
			if err != nil {
				t.Fatalf("detectDriftForRule: %v", err)
			}
			if len(warnings) != tc.wantWarnings {
				t.Fatalf("expected %d warnings, got %#v", tc.wantWarnings, warnings)
			}
			if tc.wantSubstr != "" && !strings.Contains(warnings[0], tc.wantSubstr) {
				t.Fatalf("expected warning containing %q, got %#v", tc.wantSubstr, warnings)
			}
		})
	}
}

func TestDetectLockfileDriftIgnoresGenericPyprojectWithoutManagerSignals(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, pyprojectManifestName), "[project]\nname = \"demo\"\n")

	warnings, err := detectLockfileDrift(context.Background(), repo, false)
	if err != nil {
		t.Fatalf(detectLockfileDriftFmt, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for generic pyproject.toml, got %#v", warnings)
	}
}

func TestDetectLockfileDriftPythonManagerSignals(t *testing.T) {
	t.Run("poetry manifest requires poetry lock", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, pyprojectManifestName), "[tool.poetry]\nname = \"demo\"\nversion = \"0.1.0\"\n")

		warnings, err := detectLockfileDrift(context.Background(), repo, false)
		if err != nil {
			t.Fatalf(detectLockfileDriftFmt, err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "Poetry") || !strings.Contains(warnings[0], poetryLockName) {
			t.Fatalf("expected Poetry warning for missing %s, got %#v", poetryLockName, warnings)
		}
	})

	t.Run("uv manifest change requires uv lock update", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, pyprojectManifestName), "[project]\nname = \"demo\"\n\n[tool.uv]\n")
		writeFile(t, filepath.Join(repo, "uv.lock"), "version = 1\n")
		initGitRepo(t, repo)

		writeFile(t, filepath.Join(repo, pyprojectManifestName), "[project]\nname = \"demo\"\nversion = \"0.1.0\"\n\n[tool.uv]\n")

		warnings, err := detectLockfileDrift(context.Background(), repo, false)
		if err != nil {
			t.Fatalf(detectLockfileDriftFmt, err)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], "uv") || !strings.Contains(warnings[0], "uv lock") {
			t.Fatalf("expected uv warning for changed pyproject.toml without uv.lock update, got %#v", warnings)
		}
	})
}

func TestLockfileDriftHelpers(t *testing.T) {
	repo := t.TempDir()
	nestedDir := filepath.Join(repo, "nested")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested dir: %v", err)
	}

	if got := relativeDir(repo, nestedDir); got != "nested" {
		t.Fatalf("expected relative dir nested, got %q", got)
	}
	if got := relativeFilePath(repo, nestedDir, manifestFileName); got != nestedManifestPath {
		t.Fatalf("expected relative file path nested/package.json, got %q", got)
	}
	if !isPathChanged(map[string]struct{}{nestedManifestPath: {}}, nestedManifestPath) {
		t.Fatalf("expected changed path to be detected")
	}
	if isPathChanged(map[string]struct{}{"other": {}}, nestedManifestPath) {
		t.Fatalf("expected unchanged path not to be detected")
	}

	lines := parseGitOutputLines([]byte("a\nb\n"))
	if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("unexpected parsed git output lines: %#v", lines)
	}
	if got := parseGitOutputLines([]byte("")); len(got) != 0 {
		t.Fatalf("expected empty git output lines, got %#v", got)
	}
	merged := mergeGitPaths([]string{"a", "b"}, []string{"b", "c"})
	if len(merged) != 3 || merged[0] != "a" || merged[1] != "b" || merged[2] != "c" {
		t.Fatalf("unexpected merged git paths: %#v", merged)
	}
}

func TestFindRuleLockfiles(t *testing.T) {
	repo := t.TempDir()
	manifest := filepath.Join(repo, manifestFileName)
	lock := filepath.Join(repo, lockfileName)
	writeFile(t, manifest, demoPackageJSON)
	writeFile(t, lock, demoPackageJSON)
	manifestInfo, err := os.Stat(manifest)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	lockInfo, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("stat lockfile: %v", err)
	}
	files := map[string]fs.FileInfo{
		manifestFileName: manifestInfo,
		lockfileName:     lockInfo,
	}
	found := findRuleLockfiles(files, []string{lockfileName, "missing.lock"})
	if len(found) != 1 || found[0].name != lockfileName {
		t.Fatalf("unexpected lockfiles found: %#v", found)
	}
}

func TestIsDotnetCentralOnlyRuleManifest(t *testing.T) {
	cases := []struct {
		name      string
		rule      lockfileRule
		manifests []string
		want      bool
	}{
		{
			name:      "central manifest only",
			rule:      lockfileRule{manager: ".NET", manifest: dotnetCentralManifest},
			manifests: []string{dotnetCentralManifest},
			want:      true,
		},
		{
			name:      "central manifest with project manifest",
			rule:      lockfileRule{manager: ".NET", manifest: dotnetCentralManifest},
			manifests: []string{dotnetCentralManifest, dotnetProjectManifest},
			want:      false,
		},
		{
			name:      "project manifest only",
			rule:      lockfileRule{manager: ".NET", manifest: dotnetCentralManifest},
			manifests: []string{dotnetProjectManifest},
			want:      false,
		},
		{
			name:      "different manager",
			rule:      lockfileRule{manager: "npm", manifest: dotnetCentralManifest},
			manifests: []string{dotnetCentralManifest},
			want:      false,
		},
		{
			name:      "case-insensitive central manifest",
			rule:      lockfileRule{manager: ".NET", manifest: dotnetCentralManifest},
			manifests: []string{"directory.packages.props"},
			want:      true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := isDotnetCentralOnlyRuleManifest(tc.rule, tc.manifests)
			if got != tc.want {
				t.Fatalf("isDotnetCentralOnlyRuleManifest() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDirContainsDotnetProjectManifest(t *testing.T) {
	t.Run("finds project manifest", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, dotnetProjectManifest), "<Project></Project>\n")

		hasManifest, err := dirContainsDotnetProjectManifest(dir)
		if err != nil {
			t.Fatalf("dirContainsDotnetProjectManifest: %v", err)
		}
		if !hasManifest {
			t.Fatalf("expected %s to be detected", dotnetProjectManifest)
		}
	})

	t.Run("returns false when no project manifest exists", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "packages.lock.json"), "{}\n")

		hasManifest, err := dirContainsDotnetProjectManifest(dir)
		if err != nil {
			t.Fatalf("dirContainsDotnetProjectManifest: %v", err)
		}
		if hasManifest {
			t.Fatalf("expected false when no project manifest exists")
		}
	})

	t.Run("detects fsharp project manifest", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "App.fsproj"), "<Project></Project>\n")

		hasManifest, err := dirContainsDotnetProjectManifest(dir)
		if err != nil {
			t.Fatalf("dirContainsDotnetProjectManifest: %v", err)
		}
		if !hasManifest {
			t.Fatalf("expected App.fsproj to be detected")
		}
	})

	t.Run("returns error for missing directory", func(t *testing.T) {
		_, err := dirContainsDotnetProjectManifest(filepath.Join(t.TempDir(), "missing"))
		if err == nil {
			t.Fatalf("expected missing directory error")
		}
	})
}

func TestFindDotnetProjectLockfiles(t *testing.T) {
	t.Run("finds lockfiles next to project manifests and skips irrelevant directories", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, "src", "App", dotnetProjectManifest), "<Project></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetLockfileName), "{}\n")
		writeFile(t, filepath.Join(repo, "src", "NoProject", dotnetLockfileName), "{}\n")
		writeFile(t, filepath.Join(repo, "node_modules", "Ignored", dotnetProjectManifest), "<Project></Project>\n")
		writeFile(t, filepath.Join(repo, "node_modules", "Ignored", dotnetLockfileName), "{}\n")

		lockfiles, err := findDotnetProjectLockfiles(repo)
		if err != nil {
			t.Fatalf("findDotnetProjectLockfiles: %v", err)
		}
		if len(lockfiles) != 1 {
			t.Fatalf("expected one project lockfile, got %#v", lockfiles)
		}
		if lockfiles[0].name != "src/App/packages.lock.json" {
			t.Fatalf("unexpected lockfile path %q", lockfiles[0].name)
		}
	})

	t.Run("returns error for missing root directory", func(t *testing.T) {
		_, err := findDotnetProjectLockfiles(filepath.Join(t.TempDir(), "missing"))
		if err == nil {
			t.Fatalf("expected missing directory error")
		}
	})
}

func TestFindDistributedRuleLockfiles(t *testing.T) {
	t.Run("returns existing lockfiles when already present", func(t *testing.T) {
		repo := t.TempDir()
		snapshot := lockfileDirSnapshot{repoPath: repo, path: repo}
		existing := []presentLockfile{{name: dotnetLockfileName}}

		lockfiles, err := findDistributedRuleLockfiles(snapshot, lockfileRule{manager: ".NET", manifest: dotnetCentralManifest}, []string{dotnetCentralManifest}, existing)
		if err != nil {
			t.Fatalf("findDistributedRuleLockfiles: %v", err)
		}
		if len(lockfiles) != 1 || lockfiles[0].name != dotnetLockfileName {
			t.Fatalf("expected existing lockfile to be preserved, got %#v", lockfiles)
		}
	})

	t.Run("ignores non-dotnet central rules", func(t *testing.T) {
		repo := t.TempDir()
		snapshot := lockfileDirSnapshot{repoPath: repo, path: repo}

		lockfiles, err := findDistributedRuleLockfiles(snapshot, lockfileRule{manager: "npm", manifest: manifestFileName}, []string{manifestFileName}, nil)
		if err != nil {
			t.Fatalf("findDistributedRuleLockfiles: %v", err)
		}
		if len(lockfiles) != 0 {
			t.Fatalf("expected no distributed lockfiles for non-dotnet rules, got %#v", lockfiles)
		}
	})

	t.Run("discovers project lockfiles for central package manifest", func(t *testing.T) {
		repo := t.TempDir()
		writeFile(t, filepath.Join(repo, dotnetCentralManifest), "<Project></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetProjectManifest), "<Project></Project>\n")
		writeFile(t, filepath.Join(repo, "src", "App", dotnetLockfileName), "{}\n")
		snapshot := lockfileDirSnapshot{repoPath: repo, path: repo}

		lockfiles, err := findDistributedRuleLockfiles(snapshot, lockfileRule{manager: ".NET", manifest: dotnetCentralManifest}, []string{dotnetCentralManifest}, nil)
		if err != nil {
			t.Fatalf("findDistributedRuleLockfiles: %v", err)
		}
		if len(lockfiles) != 1 || lockfiles[0].name != "src/App/packages.lock.json" {
			t.Fatalf("unexpected distributed lockfiles: %#v", lockfiles)
		}
	})

	t.Run("returns no lockfiles when no project lockfiles exist", func(t *testing.T) {
		repo := t.TempDir()
		snapshot := lockfileDirSnapshot{repoPath: repo, path: repo}

		lockfiles, err := findDistributedRuleLockfiles(snapshot, lockfileRule{manager: ".NET", manifest: dotnetCentralManifest}, []string{dotnetCentralManifest}, nil)
		if err != nil {
			t.Fatalf("findDistributedRuleLockfiles: %v", err)
		}
		if len(lockfiles) != 0 {
			t.Fatalf("expected no distributed lockfiles, got %#v", lockfiles)
		}
	})

	t.Run("returns discovery error when snapshot path is missing", func(t *testing.T) {
		root := t.TempDir()
		snapshot := lockfileDirSnapshot{repoPath: root, path: filepath.Join(root, "missing")}

		_, err := findDistributedRuleLockfiles(snapshot, lockfileRule{manager: ".NET", manifest: dotnetCentralManifest}, []string{dotnetCentralManifest}, nil)
		if err == nil {
			t.Fatalf("expected distributed lockfile discovery error")
		}
	})
}

func TestGitExecutableAvailable(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing-git")
	if gitexec.ExecutableAvailable(missingPath) {
		t.Fatalf("expected missing path to be unavailable")
	}
	filePath := filepath.Join(t.TempDir(), "git-file")
	writeFile(t, filePath, "#!/bin/sh\n")
	if gitexec.ExecutableAvailable(filePath) {
		t.Fatalf("expected non-executable file to be unavailable")
	}
	if err := os.Chmod(filePath, 0o700); err != nil {
		t.Fatalf("chmod file executable: %v", err)
	}
	if !gitexec.ExecutableAvailable(filePath) {
		t.Fatalf("expected executable file to be available")
	}
}

func assertChangedPathsPresent(t *testing.T, changed map[string]struct{}, expectedPaths ...string) {
	t.Helper()
	for _, path := range expectedPaths {
		if _, ok := changed[path]; !ok {
			t.Fatalf("expected %s to be detected as changed, got %#v", path, changed)
		}
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func initGitRepo(t *testing.T, repo string) {
	t.Helper()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test User")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "init")
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	commandArgs := append([]string{"-C", repo}, args...)
	command := exec.Command(gitBinaryPath, commandArgs...)
	command.Env = sanitizedGitEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(output))
	}
}

func containsEnv(env []string, expected string) bool {
	for _, entry := range env {
		if entry == expected {
			return true
		}
	}
	return false
}

func containsEnvPrefix(env []string, prefix string) bool {
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return true
		}
	}
	return false
}

func assertSingleLockfileDriftWarning(t *testing.T, warnings []string, err error, wantWarning, wantRemedy string) {
	t.Helper()
	if err != nil {
		t.Fatalf(detectLockfileDriftFmt, err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %#v", warnings)
	}
	if !strings.Contains(warnings[0], wantWarning) {
		t.Fatalf("unexpected warning: %q", warnings[0])
	}
	if !strings.Contains(warnings[0], wantRemedy) {
		t.Fatalf("expected remediation text %q, got %q", wantRemedy, warnings[0])
	}
}

func lockfileDriftFeatureSet(t *testing.T, enabled bool) featureflags.Set {
	t.Helper()

	registry := featureflags.DefaultRegistry()
	options := featureflags.ResolveOptions{
		Channel: featureflags.ChannelRelease,
	}
	if enabled {
		options.Enable = []string{lockfileDriftEcosystemExpansionPreviewFlagName}
	} else {
		options.Disable = []string{lockfileDriftEcosystemExpansionPreviewFlagName}
	}

	resolved, err := registry.Resolve(options)
	if err != nil {
		t.Fatalf("resolve feature set: %v", err)
	}
	return resolved
}

// TestShouldSkipMissingLockfile verifies the per-manifest heuristics that
// suppress false-positive "missing lockfile" warnings. Each case writes a
// single manifest to a temp repo and checks whether a warning mentioning the
// expected lockfile is present or absent.
func TestShouldSkipMissingLockfile(t *testing.T) {
	cases := []struct {
		name         string
		manifestName string
		manifestBody string
		lockfileHint string
		wantWarning  bool
	}{
		{
			name:         "non-poetry pyproject.toml does not warn",
			manifestName: "pyproject.toml",
			manifestBody: "[build-system]\nrequires = [\"setuptools\"]\n",
			lockfileHint: poetryLockName,
			wantWarning:  false,
		},
		{
			name:         "poetry pyproject.toml warns when the Poetry lockfile is missing",
			manifestName: "pyproject.toml",
			manifestBody: "[tool.poetry]\nname = \"my-pkg\"\n\n[build-system]\nrequires = [\"poetry-core\"]\n",
			lockfileHint: poetryLockName,
			wantWarning:  true,
		},
		{
			name:         "stdlib-only go.mod does not warn",
			manifestName: "go.mod",
			manifestBody: "module example.com/mymod\n\ngo 1.21\n",
			lockfileHint: "go.sum",
			wantWarning:  false,
		},
		{
			name:         "go.mod with require warns when go.sum is missing",
			manifestName: "go.mod",
			manifestBody: "module example.com/mymod\n\ngo 1.21\n\nrequire github.com/some/dep v1.0.0\n",
			lockfileHint: "go.sum",
			wantWarning:  true,
		},
		{
			name:         "library crate does not warn",
			manifestName: "Cargo.toml",
			manifestBody: "[package]\nname = \"my-lib\"\nversion = \"0.1.0\"\n\n[lib]\nname = \"my_lib\"\n",
			lockfileHint: "Cargo.lock",
			wantWarning:  false,
		},
		{
			name:         "binary crate warns when Cargo.lock is missing",
			manifestName: "Cargo.toml",
			manifestBody: "[package]\nname = \"my-bin\"\nversion = \"0.1.0\"\n\n[[bin]]\nname = \"my-bin\"\npath = \"src/main.rs\"\n",
			lockfileHint: "Cargo.lock",
			wantWarning:  true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			writeFile(t, filepath.Join(repo, tc.manifestName), tc.manifestBody)

			warnings, err := detectLockfileDrift(context.Background(), repo, false)
			if err != nil {
				t.Fatalf(detectLockfileDriftFmt, err)
			}
			assertLockfileWarning(t, warnings, tc.lockfileHint, tc.wantWarning)
		})
	}
}

// assertLockfileWarning checks that warnings contains (or does not contain)
// an entry mentioning lockfileHint, depending on wantWarning.
func assertLockfileWarning(t *testing.T, warnings []string, lockfileHint string, wantWarning bool) {
	t.Helper()
	found := false
	for _, w := range warnings {
		if strings.Contains(w, lockfileHint) {
			found = true
			break
		}
	}
	if wantWarning && !found {
		t.Fatalf("expected warning mentioning %q, got %#v", lockfileHint, warnings)
	}
	if !wantWarning && found {
		t.Fatalf("unexpected warning mentioning %q in %#v", lockfileHint, warnings)
	}
}
