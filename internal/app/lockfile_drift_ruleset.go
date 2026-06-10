package app

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

const (
	pyprojectManifestName                          = "pyproject.toml"
	lockfileDriftEcosystemExpansionPreviewFlagName = "lockfile-drift-ecosystem-expansion-preview"
	dotnetCSharpProjectManifestExt                 = ".csproj"
	dotnetFSharpProjectManifestExt                 = ".fsproj"
	pyprojectPoetrySection                         = "tool.poetry"
	pyprojectUVSection                             = "tool.uv"
)

type lockfileRule struct {
	manager               string
	manifest              string
	manifestNames         []string
	manifestExts          []string
	manifestLabel         string
	lockfiles             []string
	remedy                string
	previewFeatureFlag    string
	manifestMatcherLabel  string
	manifestMatcherNeedle string
	manifestMatcher       func(repoPath, dir string) (bool, error)
}

type presentLockfile struct {
	name string
	info fs.FileInfo
}

var lockfileRules = []lockfileRule{
	{manager: "npm", manifest: "package.json", lockfiles: []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lockb"}, remedy: "run npm install for package-lock.json/npm-shrinkwrap.json, yarn install for yarn.lock, pnpm install for pnpm-lock.yaml, or bun install for bun.lockb; then commit the updated manifest and lockfile"},
	{manager: "Bundler", manifest: "Gemfile", lockfiles: []string{"Gemfile.lock"}, remedy: "run bundle install (or bundle lock) and commit the updated Gemfile and Gemfile.lock"},
	{manager: "Composer", manifest: "composer.json", lockfiles: []string{"composer.lock"}, remedy: "run composer update --lock (or composer install) and commit the updated files"},
	{manager: "Cargo", manifest: "Cargo.toml", lockfiles: []string{"Cargo.lock"}, remedy: "run cargo generate-lockfile (or cargo build) and commit the updated files"},
	{manager: "Go modules", manifest: "go.mod", lockfiles: []string{"go.sum"}, remedy: "run go mod tidy and commit the updated files"},
	{manager: "Pipenv", manifest: "Pipfile", lockfiles: []string{"Pipfile.lock"}, remedy: "run pipenv lock and commit the updated files"},
	{
		manager:               "Poetry",
		manifest:              pyprojectManifestName,
		manifestLabel:         "Poetry configuration in pyproject.toml",
		lockfiles:             []string{"poetry.lock"},
		remedy:                "run poetry lock and commit the updated files",
		manifestMatcherLabel:  pyprojectPoetrySection,
		manifestMatcherNeedle: pyprojectSectionNeedle(pyprojectPoetrySection),
		manifestMatcher:       pyprojectSectionMatcher(pyprojectPoetrySection),
	},
	{
		manager:               "uv",
		manifest:              pyprojectManifestName,
		manifestLabel:         "uv configuration in pyproject.toml",
		lockfiles:             []string{"uv.lock"},
		remedy:                "run uv lock and commit the updated files",
		manifestMatcherLabel:  pyprojectUVSection,
		manifestMatcherNeedle: pyprojectSectionNeedle(pyprojectUVSection),
		manifestMatcher:       pyprojectSectionMatcher(pyprojectUVSection),
	},
	{
		manager:            ".NET",
		manifest:           "Directory.Packages.props",
		manifestExts:       []string{dotnetCSharpProjectManifestExt, dotnetFSharpProjectManifestExt},
		manifestLabel:      ".NET project manifest (*.csproj, *.fsproj) or Directory.Packages.props",
		lockfiles:          []string{"packages.lock.json"},
		remedy:             "run dotnet restore --use-lock-file (or dotnet restore for existing lock mode) and commit the updated files",
		previewFeatureFlag: lockfileDriftEcosystemExpansionPreviewFlagName,
	},
	{
		manager:            "Dart",
		manifest:           "pubspec.yaml",
		manifestNames:      []string{"pubspec.yml"},
		lockfiles:          []string{"pubspec.lock"},
		remedy:             "run dart pub get (or flutter pub get) and commit the updated files",
		previewFeatureFlag: lockfileDriftEcosystemExpansionPreviewFlagName,
	},
	{
		manager:            "Elixir",
		manifest:           "mix.exs",
		lockfiles:          []string{"mix.lock"},
		remedy:             "run mix deps.get and commit the updated files",
		previewFeatureFlag: lockfileDriftEcosystemExpansionPreviewFlagName,
	},
	{
		manager:            "SwiftPM",
		manifest:           "Package.swift",
		lockfiles:          []string{"Package.resolved"},
		remedy:             "run swift package resolve and commit the updated files",
		previewFeatureFlag: lockfileDriftEcosystemExpansionPreviewFlagName,
	},
}

func activeLockfileRules(features featureflags.Set) []lockfileRule {
	active := make([]lockfileRule, 0, len(lockfileRules))
	for _, rule := range lockfileRules {
		previewFlag := strings.TrimSpace(rule.previewFeatureFlag)
		if previewFlag != "" && !features.Enabled(previewFlag) {
			continue
		}
		active = append(active, rule)
	}
	return active
}

func findRuleLockfiles(files map[string]fs.FileInfo, names []string) []presentLockfile {
	lockfiles := make([]presentLockfile, 0, len(names))
	for _, name := range names {
		info, ok := files[name]
		if !ok {
			continue
		}
		lockfiles = append(lockfiles, presentLockfile{name: name, info: info})
	}
	return lockfiles
}

func findRuleManifests(files map[string]fs.FileInfo, rule lockfileRule) []string {
	manifests := make([]string, 0, 1+len(rule.manifestNames))
	seen := make(map[string]struct{})
	manifests = appendManifestIfPresent(manifests, seen, files, rule.manifest)
	for _, name := range rule.manifestNames {
		manifests = appendManifestIfPresent(manifests, seen, files, name)
	}
	for _, name := range findManifestExtMatches(files, rule.manifestExts) {
		manifests = appendManifestIfPresent(manifests, seen, files, name)
	}

	sort.Strings(manifests)
	return manifests
}

func appendManifestIfPresent(manifests []string, seen map[string]struct{}, files map[string]fs.FileInfo, name string) []string {
	if _, exists := seen[name]; exists {
		return manifests
	}
	if _, exists := files[name]; !exists {
		return manifests
	}
	seen[name] = struct{}{}
	return append(manifests, name)
}

func findManifestExtMatches(files map[string]fs.FileInfo, manifestExts []string) []string {
	lowerExts := normalizedManifestExts(manifestExts)
	if len(lowerExts) == 0 {
		return nil
	}

	matches := make([]string, 0, len(files))
	for name := range files {
		if manifestMatchesAnyExt(name, lowerExts) {
			matches = append(matches, name)
		}
	}
	return matches
}

func normalizedManifestExts(manifestExts []string) []string {
	lowerExts := make([]string, 0, len(manifestExts))
	for _, ext := range manifestExts {
		normalizedExt := strings.ToLower(strings.TrimSpace(ext))
		if normalizedExt == "" {
			continue
		}
		lowerExts = append(lowerExts, normalizedExt)
	}
	return lowerExts
}

func manifestMatchesAnyExt(name string, manifestExts []string) bool {
	lowerName := strings.ToLower(name)
	for _, ext := range manifestExts {
		if strings.HasSuffix(lowerName, ext) {
			return true
		}
	}
	return false
}

func manifestDescription(rule lockfileRule) string {
	if strings.TrimSpace(rule.manifestLabel) != "" {
		return rule.manifestLabel
	}
	return rule.manifest
}

func pyprojectSectionMatcher(section string) func(repoPath, dir string) (bool, error) {
	needle := pyprojectSectionNeedle(section)
	return func(repoPath, dir string) (bool, error) {
		content, err := readFileUnderFn(repoPath, filepath.Join(dir, pyprojectManifestName))
		if err != nil {
			return false, fmt.Errorf("read %s for %s lockfile drift detection: %w", pyprojectManifestName, section, err)
		}
		return pyprojectSectionNeedleMatchesContent(needle, content), nil
	}
}

func pyprojectSectionNeedle(section string) string {
	return "[" + strings.ToLower(strings.TrimSpace(section)) + "]"
}

func manifestMatcherNeedle(rule lockfileRule) string {
	return strings.TrimSpace(rule.manifestMatcherNeedle)
}

func pyprojectSectionNeedleMatchesContent(needle string, content []byte) bool {
	return strings.Contains(strings.ToLower(string(content)), needle)
}
