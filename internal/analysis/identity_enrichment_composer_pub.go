package analysis

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"

	phplang "github.com/ben-ranford/lopper/internal/lang/php"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	composerIdentityManifestName = "composer.json"
	composerIdentityLockName     = "composer.lock"
	pubIdentityManifestYAMLName  = "pubspec.yaml"
	pubIdentityManifestYMLName   = "pubspec.yml"
	pubIdentityLockName          = "pubspec.lock"
)

var exactComposerVersionPattern = regexp.MustCompile(`(?i)^v?[0-9]+(?:\.[0-9]+){0,3}(?:[-_.]?(?:dev|alpha|a|beta|b|rc|patch|pl|p)[-_.]?[0-9]*)?(?:\+[0-9a-z]+(?:[-_.][0-9a-z]+)*)?$`)

type composerIdentityManifest struct {
	Require    map[string]string `json:"require"`
	RequireDev map[string]string `json:"require-dev"`
}

type composerIdentityLock struct {
	Packages    []composerIdentityPackage `json:"packages"`
	PackagesDev []composerIdentityPackage `json:"packages-dev"`
}

type composerIdentityPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type identityManifestLockFiles struct {
	manifestPath string
	lockPath     string
}

type composerIdentityFiles = identityManifestLockFiles

type pubIdentityManifest struct {
	Dependencies        map[string]any `yaml:"dependencies"`
	DevDependencies     map[string]any `yaml:"dev_dependencies"`
	DependencyOverrides map[string]any `yaml:"dependency_overrides"`
}

type pubIdentityLock struct {
	Packages map[string]pubIdentityLockedPackage `yaml:"packages"`
}

type pubIdentityLockedPackage struct {
	Dependency  string `yaml:"dependency"`
	Description any    `yaml:"description"`
	Source      string `yaml:"source"`
	Version     string `yaml:"version"`
}

type pubIdentityFiles struct {
	manifestPaths []string
	lockPath      string
}

type identityManifestPin struct {
	name    string
	version string
	source  string
}

func collectComposerIdentityEvidenceFromPaths(repoPath string, index identityIndex, paths []string, warnings *identityWarningCollector) {
	filesByDirectory := groupComposerIdentityFiles(paths)
	for _, directory := range sortedIdentityMapKeys(filesByDirectory) {
		files := filesByDirectory[directory]
		declared, pins := readComposerIdentityDeclarations(repoPath, files.manifestPath, warnings)
		resolved := collectComposerLockIdentityEvidence(repoPath, files.lockPath, declared, index, warnings)
		for _, pin := range pins {
			if _, ok := resolved[pin.name]; !ok {
				addComposerIdentityEvidence(index, pin.name, pin.version, identityStatusDeclared, pin.source)
			}
		}
	}
}

func groupComposerIdentityFiles(paths []string) map[string]composerIdentityFiles {
	return groupIdentityManifestLockFiles(paths, composerIdentityManifestName, composerIdentityLockName)
}

func groupIdentityManifestLockFiles(paths []string, manifestName, lockName string) map[string]identityManifestLockFiles {
	filesByDirectory := make(map[string]identityManifestLockFiles)
	for _, path := range paths {
		directory := filepath.Dir(path)
		files := filesByDirectory[directory]
		switch filepath.Base(path) {
		case manifestName:
			files.manifestPath = path
		case lockName:
			files.lockPath = path
		}
		filesByDirectory[directory] = files
	}
	return filesByDirectory
}

func readComposerIdentityDeclarations(repoPath, path string, warnings *identityWarningCollector) (map[string]struct{}, []identityManifestPin) {
	declared := make(map[string]struct{})
	if path == "" {
		return declared, nil
	}
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return declared, nil
	}
	document := composerIdentityManifest{}
	if err := json.Unmarshal(data, &document); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return declared, nil
	}
	source := relativeIdentitySource(repoPath, path)
	pins := make([]identityManifestPin, 0)
	pins = appendComposerIdentityDeclarations(declared, pins, document.Require, source)
	pins = appendComposerIdentityDeclarations(declared, pins, document.RequireDev, source)
	return declared, pins
}

func appendComposerIdentityDeclarations(declared map[string]struct{}, pins []identityManifestPin, dependencies map[string]string, source string) []identityManifestPin {
	for _, rawName := range sortedIdentityMapKeys(dependencies) {
		name, ok := normalizeComposerIdentityName(rawName)
		if !ok {
			continue
		}
		declared[name] = struct{}{}
		if version, exact := exactComposerVersionConstraint(dependencies[rawName]); exact {
			pins = append(pins, identityManifestPin{name: name, version: version, source: source})
		}
	}
	return pins
}

func collectComposerLockIdentityEvidence(repoPath, path string, declared map[string]struct{}, index identityIndex, warnings *identityWarningCollector) map[string]struct{} {
	resolved := make(map[string]struct{})
	if path == "" {
		return resolved
	}
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return resolved
	}
	document := composerIdentityLock{}
	if err := json.Unmarshal(data, &document); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return resolved
	}
	packages := append(append([]composerIdentityPackage(nil), document.Packages...), document.PackagesDev...)
	sort.Slice(packages, func(i, j int) bool {
		if packages[i].Name == packages[j].Name {
			return packages[i].Version < packages[j].Version
		}
		return packages[i].Name < packages[j].Name
	})
	source := relativeIdentitySource(repoPath, path)
	for _, item := range packages {
		name, ok := normalizeComposerIdentityName(item.Name)
		if !ok {
			continue
		}
		if _, ok := declared[name]; !ok {
			continue
		}
		version := normalizeComposerResolvedVersion(item.Version)
		if version == "" {
			continue
		}
		addComposerIdentityEvidence(index, name, version, identityStatusResolved, source)
		resolved[name] = struct{}{}
	}
	return resolved
}

func normalizeComposerIdentityName(value string) (string, bool) {
	return phplang.NormalizeComposerDependency(value)
}

func exactComposerVersionConstraint(value string) (string, bool) {
	version := strings.TrimSpace(value)
	if !exactComposerVersionPattern.MatchString(version) {
		return "", false
	}
	return trimNumericVersionPrefix(version), true
}

func normalizeComposerResolvedVersion(value string) string {
	version := strings.TrimSpace(value)
	if version == "" || strings.ContainsAny(version, " \t\r\n") {
		return ""
	}
	return trimNumericVersionPrefix(version)
}

func trimNumericVersionPrefix(version string) string {
	if len(version) > 1 && (version[0] == 'v' || version[0] == 'V') && version[1] >= '0' && version[1] <= '9' {
		return version[1:]
	}
	return version
}

func addComposerIdentityEvidence(index identityIndex, name, version, status, source string) {
	addIdentityEvidence(index, identityEvidence{
		Language: "php", Ecosystem: "composer", Name: name, Version: version,
		Status: status, Source: source, Confidence: "high",
	})
}

func collectPubIdentityEvidenceFromPaths(repoPath string, index identityIndex, paths []string, warnings *identityWarningCollector) {
	filesByDirectory := groupPubIdentityFiles(paths)
	for _, directory := range sortedIdentityMapKeys(filesByDirectory) {
		files := filesByDirectory[directory]
		declared := make(map[string]struct{})
		pins := make([]identityManifestPin, 0)
		for _, path := range files.manifestPaths {
			manifestPins := readPubIdentityDeclarations(repoPath, path, declared, warnings)
			pins = append(pins, manifestPins...)
		}
		resolved, nonHosted := collectPubLockIdentityEvidence(repoPath, files.lockPath, declared, index, warnings)
		for _, pin := range pins {
			_, isResolved := resolved[pin.name]
			_, isNonHosted := nonHosted[pin.name]
			if !isResolved && !isNonHosted {
				addPubIdentityEvidence(index, pin.name, pin.version, identityStatusDeclared, pin.source)
			}
		}
	}
}

func groupPubIdentityFiles(paths []string) map[string]pubIdentityFiles {
	filesByDirectory := make(map[string]pubIdentityFiles)
	for _, path := range paths {
		directory := filepath.Dir(path)
		files := filesByDirectory[directory]
		switch strings.ToLower(filepath.Base(path)) {
		case pubIdentityManifestYAMLName, pubIdentityManifestYMLName:
			files.manifestPaths = append(files.manifestPaths, path)
		case pubIdentityLockName:
			files.lockPath = path
		}
		filesByDirectory[directory] = files
	}
	return filesByDirectory
}

func readPubIdentityDeclarations(repoPath, path string, declared map[string]struct{}, warnings *identityWarningCollector) []identityManifestPin {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return nil
	}
	document := pubIdentityManifest{}
	if err := yaml.Unmarshal(data, &document); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return nil
	}
	source := relativeIdentitySource(repoPath, path)
	pins := make([]identityManifestPin, 0)
	pins = appendPubIdentityDeclarations(declared, pins, document.Dependencies, source)
	pins = appendPubIdentityDeclarations(declared, pins, document.DevDependencies, source)
	pins = appendPubIdentityDeclarations(declared, pins, document.DependencyOverrides, source)
	return pins
}

func appendPubIdentityDeclarations(declared map[string]struct{}, pins []identityManifestPin, dependencies map[string]any, source string) []identityManifestPin {
	for _, rawName := range sortedIdentityMapKeys(dependencies) {
		name := normalizePubIdentityName(rawName)
		if name == "" {
			continue
		}
		declared[name] = struct{}{}
		if version, exact := exactPubManifestVersion(dependencies[rawName]); exact {
			pins = append(pins, identityManifestPin{name: name, version: version, source: source})
		}
	}
	return pins
}

func collectPubLockIdentityEvidence(repoPath, path string, declared map[string]struct{}, index identityIndex, warnings *identityWarningCollector) (map[string]struct{}, map[string]struct{}) {
	resolved := make(map[string]struct{})
	nonHosted := make(map[string]struct{})
	if path == "" {
		return resolved, nonHosted
	}
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return resolved, nonHosted
	}
	document := pubIdentityLock{}
	if err := yaml.Unmarshal(data, &document); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return resolved, nonHosted
	}
	source := relativeIdentitySource(repoPath, path)
	for _, rawName := range sortedIdentityMapKeys(document.Packages) {
		item := document.Packages[rawName]
		name, manifestDeclared := resolvePubIdentityLockName(declared, rawName, item.Description)
		if name == "" || (!manifestDeclared && !isDirectPubLockDependency(item.Dependency)) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(item.Source), "hosted") {
			nonHosted[name] = struct{}{}
			continue
		}
		version, ok := exactPubVersion(item.Version)
		if !ok {
			continue
		}
		addPubIdentityEvidence(index, name, version, identityStatusResolved, source)
		resolved[name] = struct{}{}
	}
	return resolved, nonHosted
}

func isDirectPubLockDependency(value string) bool {
	classification := strings.ToLower(strings.TrimSpace(value))
	return classification == "direct" || strings.HasPrefix(classification, "direct ")
}

func resolvePubIdentityLockName(declared map[string]struct{}, rawName string, description any) (string, bool) {
	candidates := []string{pubIdentityDescriptionName(description), normalizePubIdentityName(rawName)}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, ok := declared[candidate]; ok {
			return candidate, true
		}
	}
	for _, candidate := range candidates {
		if candidate != "" {
			return candidate, false
		}
	}
	return "", false
}

func pubIdentityDescriptionName(description any) string {
	fields, ok := description.(map[string]any)
	if !ok {
		return ""
	}
	name, _ := fields["name"].(string)
	return normalizePubIdentityName(name)
}

func exactPubManifestVersion(value any) (string, bool) {
	if version, ok := value.(string); ok {
		return exactPubVersion(version)
	}
	metadata, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	for _, field := range []string{"git", "path", "sdk"} {
		if _, exists := metadata[field]; exists {
			return "", false
		}
	}
	version, _ := metadata["version"].(string)
	return exactPubVersion(version)
}

func exactPubVersion(value string) (string, bool) {
	version := strings.TrimSpace(value)
	coreVersion := version
	if separator := strings.IndexAny(coreVersion, "-+"); separator >= 0 {
		coreVersion = coreVersion[:separator]
	}
	if version == "" || strings.Count(coreVersion, ".") != 2 || strings.HasPrefix(strings.ToLower(version), "v") || !semver.IsValid("v"+version) {
		return "", false
	}
	return version, true
}

func normalizePubIdentityName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func addPubIdentityEvidence(index identityIndex, name, version, status, source string) {
	addIdentityEvidence(index, identityEvidence{
		Language: "dart", Ecosystem: "pub", Name: name, Version: version,
		Status: status, Source: source, Confidence: "high",
	})
}

func sortedIdentityMapKeys[T any](items map[string]T) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
