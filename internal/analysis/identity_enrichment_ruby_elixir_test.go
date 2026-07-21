package analysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/testutil"
)

func TestRubyIdentityUsesDirectRubyGemsLockEntries(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, rubyIdentityLockName), `GIT
  remote: https://example.test/private_gem.git
  revision: 0123456789abcdef
  specs:
    private_gem (1.0.0)

PATH
  remote: vendor/local_gem
  specs:
    local_gem (0.1.0)

GEM
  remote: https://rubygems.org/
  specs:
    httparty (0.22.0)
      mini_mime (>= 1.0.0)
    mini_mime (1.1.5)
    underscore_gem (2.3.4)
    ambiguous_gem (3.0.0)
    platform_gem (5.0.0-x86_64-linux)
    mixed_platform_gem (5.1.0)
    mixed_platform_gem (5.1.0-x86_64-linux)

GIT
  remote: https://example.test/ambiguous_gem.git
  revision: fedcba9876543210
  specs:
    ambiguous_gem (4.0.0)

GEM
  remote: https://gems.example.test/
  specs:
    private_registry_gem (6.0.0)

PLATFORMS
  x86_64-linux

DEPENDENCIES
  httparty (~> 0.22)
  underscore_gem!
  private_gem!
  local_gem!
  ambiguous_gem!
  platform_gem
  mixed_platform_gem
  private_registry_gem
`)
	reportData := identityReportForDependencies("ruby", []string{
		"httparty", "underscore-gem", "private-gem", "local-gem", "ambiguous-gem", "mini-mime",
		"platform-gem", "mixed-platform-gem", "private-registry-gem",
	})

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "ruby", "httparty"), rubyIdentity("httparty", "0.22.0"))
	assertIdentity(t, findIdentityDependency(t, reportData, "ruby", "underscore-gem"), rubyIdentityWithCoordinates("underscore_gem", "2.3.4"))
	for _, name := range []string{"private-gem", "local-gem", "ambiguous-gem", "mini-mime", "platform-gem", "mixed-platform-gem", "private-registry-gem"} {
		assertUnknownIdentity(t, findIdentityDependency(t, reportData, "ruby", name), "gem", name)
	}
	if len(reportData.Warnings) != 0 {
		t.Fatalf("expected valid Bundler evidence to be warning-free, got %#v", reportData.Warnings)
	}
}

func TestElixirIdentityUsesDeclaredHexLockEntries(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityManifestName), `defmodule Demo.MixProject do
  use Mix.Project

  defp deps do
    [
      {:jason, "~> 1.4"},
      {:ecto_sql, "~> 3.0"},
      {:renamed_dep, "~> 2.0", hex: :actual_package},
      {:git_dep, git: "https://example.test/git_dep.git"},
	  {:path_dep, path: "../path_dep"},
	  {:private_hex, "~> 7.0"},
	  {:duplicate_hex, "~> 8.0"}
      # {:commented_dep, "~> 9.0"}
    ]
  end
end
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityLockName), `%{
  "jason": {:hex, :jason, "1.4.5", "checksum", [:mix], [], "hexpm", "checksum"},
  "ecto_sql" => {:hex, :ecto_sql, "3.12.5", "checksum", [:mix], [], "hexpm", "checksum"},
  "renamed_dep": {:hex, :actual_package, "2.1.0", "checksum", [:mix], [], "hexpm", "checksum"},
  "git_dep": {:git, "https://example.test/git_dep.git", "abcdef", []},
  "path_dep": {:path, "../path_dep", []},
  "transitive_dep": {:hex, :transitive_dep, "5.0.0", "checksum", [:mix], [], "hexpm", "checksum"},
  "commented_dep": {:hex, :commented_dep, "9.0.0", "checksum", [:mix], [], "hexpm", "checksum"}
	,"private_hex": {:hex, :private_hex, "7.0.0", "checksum", [:mix], [], "hexpm:acme", "checksum"}
	,"duplicate_hex": {:hex, :duplicate_hex, "8.0.0", "checksum", [:mix], [], "hexpm", "checksum"}
	,"duplicate_hex": {:hex, :duplicate_hex, "8.0.0", "checksum", [:mix], [], "hexpm", "checksum"}
}
`)
	reportData := identityReportForDependencies("elixir", []string{
		"jason", "ecto-sql", "renamed-dep", "git-dep", "path-dep", "transitive-dep", "commented-dep",
		"private-hex", "duplicate-hex",
	})

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "elixir", "jason"), elixirIdentity("jason", "1.4.5"))
	assertIdentity(t, findIdentityDependency(t, reportData, "elixir", "ecto-sql"), elixirIdentityWithCoordinates("ecto_sql", "3.12.5"))
	assertIdentity(t, findIdentityDependency(t, reportData, "elixir", "renamed-dep"), elixirIdentityWithCoordinates("actual_package", "2.1.0"))
	for _, name := range []string{"git-dep", "path-dep", "transitive-dep", "commented-dep", "private-hex", "duplicate-hex"} {
		assertUnknownIdentity(t, findIdentityDependency(t, reportData, "elixir", name), "hex", name)
	}
	if len(reportData.Warnings) != 0 {
		t.Fatalf("expected valid Mix evidence to be warning-free, got %#v", reportData.Warnings)
	}
}

func TestRubyAndElixirIdentityDiscoveryUsesAdapterRoots(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "packages", "ruby-app", rubyIdentityLockName), "GEM\n  remote: https://rubygems.org/\n  specs:\n    nested_gem (1.0.0)\n\nDEPENDENCIES\n  nested_gem\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "services", "elixir-app", elixirIdentityManifestName), "defp deps, do: [{:nested_hex, \"~> 1.0\"}]\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "services", "elixir-app", elixirIdentityLockName), "%{\n  \"nested_hex\" => {:hex, :nested_hex, \"1.0.0\", \"checksum\", [:mix], [], \"hexpm\", \"checksum\"}\n}\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "vendor", "ruby-app", rubyIdentityLockName), "GEM\n  remote: https://rubygems.org/\n  specs:\n    nested_gem (9.0.0)\n\nDEPENDENCIES\n  nested_gem\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "deps", "elixir-app", elixirIdentityManifestName), "defp deps, do: [{:nested_hex, \"~> 9.0\"}]\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "deps", "elixir-app", elixirIdentityLockName), "%{\n  \"nested_hex\": {:hex, :nested_hex, \"9.0.0\", \"checksum\", [:mix], [], \"hexpm\", \"checksum\"}\n}\n")
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "ruby", Name: "nested-gem"},
		{Language: "elixir", Name: "nested-hex"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "ruby", "nested-gem"), rubyIdentityAtSource("nested_gem", "1.0.0", "packages/ruby-app/Gemfile.lock"))
	assertIdentity(t, findIdentityDependency(t, reportData, "elixir", "nested-hex"), elixirIdentityAtSource("nested_hex", "1.0.0", "services/elixir-app/mix.lock"))
	if len(reportData.Warnings) != 0 {
		t.Fatalf("expected nested adapter roots to be warning-free, got %#v", reportData.Warnings)
	}
}

func TestElixirIdentityUsesUmbrellaRootLockWithoutCrossingChildLocks(t *testing.T) {
	repoPath := t.TempDir()
	umbrella := filepath.Join(repoPath, "umbrella")
	testutil.MustWriteFile(t, filepath.Join(umbrella, elixirIdentityManifestName), "def project, do: [apps_path: \"apps\"]\n")
	testutil.MustWriteFile(t, filepath.Join(umbrella, elixirIdentityLockName), `%{
  "phoenix_html": {:hex, :phoenix_html, "4.1.1", "checksum", [:mix], [], "hexpm", "checksum"},
  "boundary_dep": {:hex, :boundary_dep, "1.0.0", "checksum", [:mix], [], "hexpm", "checksum"}
}
`)
	testutil.MustWriteFile(t, filepath.Join(umbrella, "apps", "web", elixirIdentityManifestName), "defp deps, do: [{:phoenix_html, \"~> 4.1\"}]\n")
	testutil.MustWriteFile(t, filepath.Join(umbrella, "apps", "api", elixirIdentityManifestName), "defp deps, do: [{:boundary_dep, \"~> 2.0\"}]\n")
	testutil.MustWriteFile(t, filepath.Join(umbrella, "apps", "api", elixirIdentityLockName), `%{
  "boundary_dep": {:hex, :boundary_dep, "2.0.0", "checksum", [:mix], [], "hexpm", "checksum"}
}
`)
	reportData := identityReportForDependencies("elixir", []string{"phoenix-html", "boundary-dep"})

	annotateDependencyIdentities(repoPath, &reportData)

	assertIdentity(t, findIdentityDependency(t, reportData, "elixir", "phoenix-html"), elixirIdentityAtSource("phoenix_html", "4.1.1", "umbrella/mix.lock"))
	assertIdentity(t, findIdentityDependency(t, reportData, "elixir", "boundary-dep"), elixirIdentityAtSource("boundary_dep", "2.0.0", "umbrella/apps/api/mix.lock"))
}

func TestRubyAndElixirIdentityRejectSameVersionCoordinateCollisions(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, "ruby-one", rubyIdentityLockName), "GEM\n  remote: https://rubygems.org/\n  specs:\n    foo_bar (1.0.0)\n\nDEPENDENCIES\n  foo_bar\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, "ruby-two", rubyIdentityLockName), "GEM\n  remote: https://rubygems.org/\n  specs:\n    foo-bar (1.0.0)\n\nDEPENDENCIES\n  foo-bar\n")
	for directory, packageName := range map[string]string{"elixir-one": "actual_one", "elixir-two": "actual_two"} {
		testutil.MustWriteFile(t, filepath.Join(repoPath, directory, elixirIdentityManifestName), "defp deps, do: [{:renamed_dep, \"~> 1.0\"}]\n")
		testutil.MustWriteFile(t, filepath.Join(repoPath, directory, elixirIdentityLockName), "%{\n  \"renamed_dep\": {:hex, :"+packageName+", \"1.0.0\", \"checksum\", [:mix], [], \"hexpm\", \"checksum\"}\n}\n")
	}
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "ruby", Name: "foo-bar"},
		{Language: "elixir", Name: "renamed-dep"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	for _, dependency := range []report.DependencyReport{
		findIdentityDependency(t, reportData, "ruby", "foo-bar"),
		findIdentityDependency(t, reportData, "elixir", "renamed-dep"),
	} {
		identity := dependency.Identity
		if identity.VersionStatus != identityStatusConflicting || identity.Version != "" || identity.PURLStatus != identityPURLUnavailable || identity.PURL != "" {
			t.Fatalf("expected %s coordinate collision to fail closed, got %#v", dependency.Language, identity)
		}
		if len(identity.Conflicts) != 2 {
			t.Fatalf("expected both %s coordinate conflicts, got %#v", dependency.Language, identity.Conflicts)
		}
	}
}

func TestElixirIdentityRejectsDynamicDependencyLists(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityManifestName), `defp deps, do: [{:dynamic_dep, "~> 1.0"}] ++ extra_deps()
"""
{:heredoc_dep, "~> 2.0"}
"""
~S|deps: [{:sigil_dep, "~> 3.0"}]|
def fixture, do: [deps: [{:helper_dep, "~> 4.0"}]]
`)
	testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityLockName), `%{
  "dynamic_dep": {:hex, :dynamic_dep, "1.0.0", "checksum", [:mix], [], "hexpm", "checksum"},
  "heredoc_dep": {:hex, :heredoc_dep, "2.0.0", "checksum", [:mix], [], "hexpm", "checksum"}
	,"sigil_dep": {:hex, :sigil_dep, "3.0.0", "checksum", [:mix], [], "hexpm", "checksum"}
	,"helper_dep": {:hex, :helper_dep, "4.0.0", "checksum", [:mix], [], "hexpm", "checksum"}
}
`)
	reportData := identityReportForDependencies("elixir", []string{"dynamic-dep", "heredoc-dep", "sigil-dep", "helper-dep"})

	annotateDependencyIdentities(repoPath, &reportData)

	for _, name := range []string{"dynamic-dep", "heredoc-dep", "sigil-dep", "helper-dep"} {
		assertUnknownIdentity(t, findIdentityDependency(t, reportData, "elixir", name), "hex", name)
	}
}

func TestRubyAndElixirIdentityCollectionIsLanguageGated(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, rubyIdentityLockName), "not a Bundler lockfile")
	testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityManifestName), "defp deps, do: [{:jason, \"~> 1.0\"}]")
	testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityLockName), "not a Mix lockfile")
	reportData := identityReportForDependencies("go", []string{"example.com/module"})

	annotateDependencyIdentities(repoPath, &reportData)

	if len(reportData.Warnings) != 0 {
		t.Fatalf("expected unrelated Ruby and Elixir files to be ignored, got %#v", reportData.Warnings)
	}
}

func TestRubyAndElixirIdentityWarnOnMalformedLockfiles(t *testing.T) {
	repoPath := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(repoPath, rubyIdentityLockName), "not a Bundler lockfile")
	testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityManifestName), "defp deps, do: [{:jason, \"~> 1.0\"}]")
	testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityLockName), "not a Mix lockfile")
	reportData := report.Report{Dependencies: []report.DependencyReport{
		{Language: "ruby", Name: "rack"},
		{Language: "elixir", Name: "jason"},
	}}

	annotateDependencyIdentities(repoPath, &reportData)

	assertWarningsExact(t, repoPath, reportData.Warnings, []string{
		"identity manifest parse failed for Gemfile.lock: invalid manifest",
		"identity manifest parse failed for mix.lock: invalid manifest",
	})
}

func TestElixirIdentityRejectsMalformedLockAndManifestSeparators(t *testing.T) {
	hexEntry := `{:hex, :target_dep, "1.0.0", "checksum", [:mix], [], "hexpm", "checksum"}`
	for _, test := range []struct {
		name string
		lock string
	}{
		{name: "leading comma", lock: "%{, \"target_dep\": " + hexEntry + "}"},
		{name: "repeated comma", lock: "%{\"target_dep\": " + hexEntry + ",,}"},
		{name: "missing comma", lock: "%{\"other_dep\": " + hexEntry + " \"target_dep\": " + hexEntry + "}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repoPath := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityManifestName), "defp deps, do: [{:target_dep, \"~> 1.0\"}]\n")
			testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityLockName), test.lock)
			reportData := identityReportForDependencies("elixir", []string{"target-dep"})

			annotateDependencyIdentities(repoPath, &reportData)

			assertUnknownIdentity(t, findIdentityDependency(t, reportData, "elixir", "target-dep"), "hex", "target-dep")
			assertWarningsExact(t, repoPath, reportData.Warnings, []string{
				"identity manifest parse failed for mix.lock: invalid manifest",
			})
		})
	}

	for _, test := range []struct {
		name         string
		manifest     string
		wantResolved bool
	}{
		{name: "invalid separators", manifest: "defp deps, do: [,{:target_dep, \"~> 1.0\"},,]\n"},
		{name: "non dependency term", manifest: "defp deps, do: [{:target_dep, \"~> 1.0\"}, \"not-a-dependency\"]\n"},
		{name: "trailing line comment", manifest: "defp deps, do: [\n  {:target_dep, \"~> 1.0\"},\n  # {:commented_dep, \"~> 9.0\"}\n]\n", wantResolved: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repoPath := t.TempDir()
			testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityManifestName), test.manifest)
			testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityLockName), "%{\"target_dep\": "+hexEntry+",}")
			reportData := identityReportForDependencies("elixir", []string{"target-dep"})

			annotateDependencyIdentities(repoPath, &reportData)

			dependency := findIdentityDependency(t, reportData, "elixir", "target-dep")
			if test.wantResolved {
				assertIdentity(t, dependency, elixirIdentity("target_dep", "1.0.0"))
			} else {
				assertUnknownIdentity(t, dependency, "hex", "target-dep")
			}
			if len(reportData.Warnings) != 0 {
				t.Fatalf("expected a syntactically valid lock and ignored malformed manifest list, got %#v", reportData.Warnings)
			}
		})
	}
}

func TestRubyIdentityCollectorAndParserFailureBranches(t *testing.T) {
	repoPath := t.TempDir()
	warnings := newIdentityWarningCollector(repoPath)
	collectRubyIdentityEvidenceFromPaths(repoPath, identityIndex{}, []string{filepath.Join(repoPath, rubyIdentityLockName)}, warnings)
	assertWarningsExact(t, repoPath, warnings.list(), []string{
		"identity manifest read failed for Gemfile.lock: not found",
	})

	for _, content := range []string{
		"",
		"GEM\n  remote: https://rubygems.org/\n  specs:\n    rack (1.0.0)\n",
		"GEM\n  remote: https://rubygems.org/\n  specs:\n\nDEPENDENCIES\n  missing_gem\n",
	} {
		if _, err := parseRubyIdentityLock([]byte(content)); err == nil {
			t.Fatalf("expected malformed Bundler lockfile to fail: %q", content)
		}
	}

	pluginLock, err := parseRubyIdentityLock([]byte("PLUGIN SOURCE\n  remote: https://plugins.example.test/\n  specs:\n    plugin_gem (1.0.0)\n\nDEPENDENCIES\n  plugin_gem!\n"))
	if err != nil {
		t.Fatalf("parse unsupported but structurally valid plugin source: %v", err)
	}
	index := identityIndex{}
	addRubyIdentityLockEvidence(index, pluginLock, rubyIdentityLockName)
	if len(index) != 0 {
		t.Fatalf("expected plugin sources to stay unknown, got %#v", index)
	}

	parser := rubyIdentityLockParser{lock: rubyIdentityLock{direct: map[string]struct{}{}, specs: map[string][]rubyIdentityLockedSpec{}, platforms: map[string]struct{}{}}}
	parser.addDirectDependency("invalid")
	parser.addPlatform("invalid")
	parser.addRemote("invalid")
	parser.addRemote("  remote: https://rubygems.org/")
	parser.addRemote("  remote: https://mirror.example.test/")
	parser.addLockedSpec("invalid")
	parser.addLockedSpec("    invalid_gem (bad version)")
	if parser.remote != "ambiguous" {
		t.Fatalf("expected multiple remotes to become ambiguous, got %q", parser.remote)
	}
	if rubyIdentitySection("UNKNOWN") != "" || isRubyIdentitySource("") {
		t.Fatal("expected unknown Bundler sections to remain unsupported")
	}
	if hasUnambiguousRubyGemsSource([]rubyIdentityLockedSpec{{source: rubyIdentitySourceGem, remote: publicRubyGemsRemote}}) {
		t.Fatal("expected a blank gem name to fail closed")
	}
	if hasUnambiguousRubyGemsSource([]rubyIdentityLockedSpec{
		{source: rubyIdentitySourceGem, remote: publicRubyGemsRemote, packageName: "one"},
		{source: rubyIdentitySourceGem, remote: publicRubyGemsRemote, packageName: "two"},
	}) {
		t.Fatal("expected normalized-name collisions to fail closed")
	}
}

func TestElixirIdentityManifestAndLockReadFailures(t *testing.T) {
	repoPath := t.TempDir()
	warnings := newIdentityWarningCollector(repoPath)
	if manifest := readElixirIdentityManifest(repoPath, "", warnings); len(manifest.declared) != 0 {
		t.Fatalf("expected an empty manifest path to yield no declarations, got %#v", manifest)
	}
	missingManifest := filepath.Join(repoPath, "missing", elixirIdentityManifestName)
	missingLock := filepath.Join(repoPath, "missing", elixirIdentityLockName)
	readElixirIdentityManifest(repoPath, missingManifest, warnings)
	if locked, ok := readElixirIdentityLock(repoPath, missingLock, warnings); ok || len(locked) != 0 {
		t.Fatalf("expected a missing Mix lock to fail, got %#v, %t", locked, ok)
	}
	assertWarningsExact(t, repoPath, warnings.list(), []string{
		"identity manifest read failed for missing/mix.exs: not found",
		"identity manifest read failed for missing/mix.lock: not found",
	})
}

func TestElixirIdentityDeclarationParserAcceptsOnlyLiteralLists(t *testing.T) {
	for _, content := range []string{
		"def deps(), do: [{:one_line, \"~> 1.0\"}]\n",
		"defp deps do\n  [{:block_dep, \"~> 1.0\"},]\nend\n",
	} {
		declared := parseElixirIdentityDeclarations([]byte(content))
		if len(declared) != 1 {
			t.Fatalf("expected one literal dependency from %q, got %#v", content, declared)
		}
	}
	for _, content := range []string{
		"defp deps, do: [{:dynamic_dep, \"~> 1.0\"}] -- removed_deps\n",
		"defp deps, do: [dynamic_dep()]\n",
		"defp deps, do: [{:unbalanced, \"~> 1.0\"}\n",
		"def fixture, do: [deps: [{:helper_dep, \"~> 1.0\"}]]\n",
		"defp deps, do: [,{:leading_separator, \"~> 1.0\"}]\n",
		"defp deps, do: [{:double_separator, \"~> 1.0\"},,]\n",
	} {
		if declared := parseElixirIdentityDeclarations([]byte(content)); len(declared) != 0 {
			t.Fatalf("expected dynamic or malformed dependencies to fail closed for %q, got %#v", content, declared)
		}
	}
	if declared, literal := parseElixirIdentityDepsList(""); !literal || len(declared) != 0 {
		t.Fatalf("expected an empty literal list to be valid, got %#v, %t", declared, literal)
	}
	for _, tuple := range []string{"", "{:only_name}", "{name, value}", "{:valid, value} trailing", "{:bad, [}", "{:bad,, value}"} {
		if name, ok := parseElixirIdentityDependencyTuple(tuple); ok || name != "" {
			t.Fatalf("expected invalid dependency tuple %q to fail, got %q, %t", tuple, name, ok)
		}
	}
}

func TestElixirIdentityLockParserHandlesGeneratedTermsAndRejectsMalformedInput(t *testing.T) {
	valid := `%{
	  "renamed\u005fdep": {:hex, :"actual-package", "1.2.3-rc.1+build.2", "checksum", [:mix], [], "hexpm", "checksum"},
  "git_dep": {:git, "https://example.test/repo.git", "revision", []}
}`
	locked, err := parseElixirIdentityLock([]byte(valid))
	if err != nil {
		t.Fatalf("parse generated Mix lock terms: %v", err)
	}
	if len(locked) != 2 || locked[0].lookupName != "renamed-dep" || locked[0].packageName != "actual-package" || locked[0].version != "1.2.3-rc.1+build.2" {
		t.Fatalf("unexpected generated Mix lock entries: %#v", locked)
	}

	malformed := []string{
		"",
		"%{\n  not_a_string: {:git, \"url\"}\n}",
		"%{\n  \"bad_separator\" ? {:git, \"url\"}\n}",
		"%{\n  \"not_a_tuple\": []\n}",
		"%{\n  \"bad_source\": {hex, :pkg, \"1.0.0\"}\n}",
		"%{\n  \"short_hex\": {:hex, :pkg, \"1.0.0\"}\n}",
		"%{\n  \"bad_version\": {:hex, :pkg, \"release\", \"checksum\", [], [], \"hexpm\"}\n}",
		"%{\n  \"unbalanced\": {:git, \"url\"}\n",
		"%{} trailing",
	}
	for _, content := range malformed {
		if _, err := parseElixirIdentityLock([]byte(content)); err == nil {
			t.Fatalf("expected malformed Mix lockfile to fail: %q", content)
		}
	}
}

func TestIdentityTermParserDelimiterFailureBranches(t *testing.T) {
	for _, test := range []struct {
		value   string
		opening int
	}{
		{value: "", opening: -1},
		{value: "value", opening: 0},
		{value: "[}", opening: 0},
		{value: "([", opening: 0},
	} {
		if _, ok := matchingIdentityDelimiterEnd(test.value, test.opening); ok {
			t.Fatalf("expected delimiter input %#v to fail", test)
		}
	}
	if closing, ok := matchingIdentityDelimiterEnd("([{value}])", 0); !ok || closing != len("([{value}])")-1 {
		t.Fatalf("expected nested delimiters to balance, got %d, %t", closing, ok)
	}
	for _, test := range []struct{ raw, masked string }{
		{raw: "a", masked: ""},
		{raw: "a, [b", masked: "a, [b"},
		{raw: "a, }", masked: "a, }"},
	} {
		if _, ok := splitIdentityTopLevelTerms(test.raw, test.masked); ok {
			t.Fatalf("expected invalid term split %#v to fail", test)
		}
	}
	if declared, ok := parseElixirIdentityDepsList("{:dep, [}"); ok || len(declared) != 0 {
		t.Fatalf("expected an unbalanced literal list body to fail, got %#v, %t", declared, ok)
	}
}

func TestIdentityTermParserLockValueFailureBranches(t *testing.T) {
	entry := `"key": {:git}`
	if _, err := parseElixirIdentityLockEntries(entry, string(elixirMaskForTest(entry)), 0, len(entry)-1); err == nil {
		t.Fatal("expected a tuple consuming the map boundary to fail")
	}
	if _, err := parseElixirIdentityLockedPackage("key", "{value}", "{]alue}"); err == nil {
		t.Fatal("expected mismatched tuple delimiters to fail")
	}
	if _, _, ok := parseIdentityQuotedStringAt("value", 0); ok {
		t.Fatal("expected a non-quoted value to fail")
	}
	if _, _, ok := parseIdentityQuotedStringAt("\"unterminated\\", 0); ok {
		t.Fatal("expected an unterminated quoted value to fail")
	}
	if atom, ok := parseElixirIdentityAtom(":\"Bad Atom\""); ok || atom != "Bad Atom" {
		t.Fatalf("expected an invalid quoted atom to be rejected after decoding, got %q, %t", atom, ok)
	}
	if atom, ok := parseElixirIdentityAtom("plain"); ok || atom != "" {
		t.Fatalf("expected a non-atom to fail, got %q, %t", atom, ok)
	}
}

func TestIdentityTermParserBoundaryHelpers(t *testing.T) {
	if isImmediateIdentityChild("/tmp/apps", "/tmp/apps") || isImmediateIdentityChild("/tmp/apps", "/tmp/apps/api/nested") {
		t.Fatal("expected same and nested paths not to be immediate children")
	}
	if got := skipIdentityWhitespace(" \t\r\n", 0); got != 4 {
		t.Fatalf("expected whitespace scan to reach the end, got %d", got)
	}
	if position, done, valid := nextElixirIdentityLockEntry("", 0, 0, false); position != 0 || !done || !valid {
		t.Fatalf("expected an empty lock map to end cleanly, got %d, %t, %t", position, done, valid)
	}
	declared := map[string]struct{}{}
	addElixirUmbrellaDeclarations("/repo", "/repo", "../outside", nil, declared, nil)
	if len(declared) != 0 {
		t.Fatalf("expected an escaping umbrella path to be ignored, got %#v", declared)
	}
}

func elixirMaskForTest(value string) []byte {
	masked := []byte(value)
	inString := false
	for index := range masked {
		if masked[index] == '"' {
			inString = !inString
			masked[index] = ' '
			continue
		}
		if inString {
			masked[index] = ' '
		}
	}
	return masked
}

func TestRubyAndElixirIdentitySymlinkEscapesBecomeWarnings(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("symlink fixture is not portable on Windows")
	}
	repoPath := t.TempDir()
	outside := t.TempDir()
	testutil.MustWriteFile(t, filepath.Join(outside, rubyIdentityLockName), "GEM\n  remote: https://rubygems.org/\n  specs:\n    rack (1.0.0)\n\nDEPENDENCIES\n  rack\n")
	testutil.MustWriteFile(t, filepath.Join(outside, elixirIdentityLockName), "%{}\n")
	testutil.MustWriteFile(t, filepath.Join(repoPath, elixirIdentityManifestName), "defp deps, do: [{:jason, \"~> 1.0\"}]\n")
	if err := os.Symlink(filepath.Join(outside, rubyIdentityLockName), filepath.Join(repoPath, rubyIdentityLockName)); err != nil {
		t.Fatalf("symlink Ruby lock: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, elixirIdentityLockName), filepath.Join(repoPath, elixirIdentityLockName)); err != nil {
		t.Fatalf("symlink Elixir lock: %v", err)
	}
	reportData := report.Report{Dependencies: []report.DependencyReport{{Language: "ruby", Name: "rack"}, {Language: "elixir", Name: "jason"}}}

	annotateDependencyIdentities(repoPath, &reportData)

	if joined := strings.Join(reportData.Warnings, "\n"); !strings.Contains(joined, "identity manifest read failed for Gemfile.lock: I/O error") || !strings.Contains(joined, "identity manifest read failed for mix.lock: I/O error") {
		t.Fatalf("expected safe I/O warnings for escaped locks, got %#v", reportData.Warnings)
	}
}

func rubyIdentity(name, version string) report.DependencyIdentity {
	return rubyIdentityWithCoordinates(name, version)
}

func rubyIdentityWithCoordinates(name, version string) report.DependencyIdentity {
	return rubyIdentityAtSource(name, version, rubyIdentityLockName)
}

func rubyIdentityAtSource(name, version, source string) report.DependencyIdentity {
	return report.DependencyIdentity{
		Ecosystem: "gem", Name: name, Version: version, VersionStatus: identityStatusResolved,
		PURL: "pkg:gem/" + name + "@" + version, PURLStatus: identityStatusResolved,
		Source: source, Confidence: "high",
	}
}

func elixirIdentity(name, version string) report.DependencyIdentity {
	return elixirIdentityWithCoordinates(name, version)
}

func elixirIdentityWithCoordinates(name, version string) report.DependencyIdentity {
	return elixirIdentityAtSource(name, version, elixirIdentityLockName)
}

func elixirIdentityAtSource(name, version, source string) report.DependencyIdentity {
	return report.DependencyIdentity{
		Ecosystem: "hex", Name: name, Version: version, VersionStatus: identityStatusResolved,
		PURL: "pkg:hex/" + name + "@" + version, PURLStatus: identityStatusResolved,
		Source: source, Confidence: "high",
	}
}
