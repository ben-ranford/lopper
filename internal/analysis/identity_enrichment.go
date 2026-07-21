package analysis

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/modfile"
	"gopkg.in/yaml.v3"

	dartlang "github.com/ben-ranford/lopper/internal/lang/dart"
	pythonlang "github.com/ben-ranford/lopper/internal/lang/python"
	"github.com/ben-ranford/lopper/internal/lang/shared"
	swiftlang "github.com/ben-ranford/lopper/internal/lang/swift"
	"github.com/ben-ranford/lopper/internal/report"
	"github.com/ben-ranford/lopper/internal/safeio"
)

const (
	identityStatusDeclared    = "declared"
	identityStatusResolved    = "resolved"
	identityStatusUnknown     = "unknown"
	identityStatusConflicting = "conflicting"
	identityPURLUnavailable   = "unavailable"
	identityDiscoveryFailed   = "discovery failed"
	identityReadFailed        = "read failed"
	identityParseFailed       = "parse failed"
	identityInvalidXML        = "invalid XML"
	goModFileName             = "go.mod"
	goWorkFileName            = "go.work"
	nodePackageManifestFile   = "package.json"
	npmAliasPrefix            = "npm:"
	packageLockFileName       = "package-lock.json"
	pnpmLockFileName          = "pnpm-lock.yaml"
	poetryLockFileName        = "poetry.lock"
	pythonPipfileName         = "Pipfile"
	pythonProjectFileName     = "pyproject.toml"
	cargoManifestFileName     = "Cargo.toml"
	cargoLockFileName         = "Cargo.lock"
	kotlinAndroidLanguageName = "kotlin-android"
	uvLockFileName            = "uv.lock"
	dotnetCentralFileName     = "Directory.Packages.props"
	dotnetLockFileName        = "packages.lock.json"
	carthageResolvedFileName  = "Cartfile.resolved"
)

type identityEvidence struct {
	Language   string
	Ecosystem  string
	LookupName string
	Name       string
	Namespace  string
	Version    string
	Status     string
	Source     string
	Confidence string
}

type identityIndex map[string][]identityEvidence

type identityCoordinate struct {
	ecosystem string
	name      string
	namespace string
}

type dependencyIdentityState struct {
	ecosystem      string
	name           string
	namespace      string
	version        string
	status         string
	source         string
	confidence     string
	evidenceLabels []string
	conflicts      []string
	coordinates    map[identityCoordinate]string
	versions       map[string]string
}

type identityManifestSnapshot struct {
	goModFiles         []string
	goWorkFiles        []string
	jsLockfiles        []string
	pythonFiles        []string
	pomFiles           []string
	gradleBuildFiles   []string
	gradleLockFiles    []string
	swiftFiles         []string
	podLockFiles       []string
	carthageLockFiles  []string
	cargoManifestFiles []string
	cargoLockFiles     []string
	dotnetProjectFiles []string
	dotnetCentralFiles []string
	dotnetLockFiles    []string
	composerFiles      []string
	pubFiles           []string
	rubyFiles          []string
	elixirFiles        []string
}

type identityEvidenceLanguages struct {
	python   bool
	dotnet   bool
	composer bool
	pub      bool
	ruby     bool
	elixir   bool
}

func annotateDependencyIdentities(repoPath string, reportData *report.Report) {
	if reportData == nil || len(reportData.Dependencies) == 0 {
		return
	}
	languages := identityEvidenceLanguages{
		python:   hasDependencyLanguage(reportData.Dependencies, "python"),
		dotnet:   hasDependencyLanguage(reportData.Dependencies, "dotnet"),
		composer: hasDependencyLanguage(reportData.Dependencies, "php"),
		pub:      hasDependencyLanguage(reportData.Dependencies, "dart"),
		ruby:     hasDependencyLanguage(reportData.Dependencies, "ruby"),
		elixir:   hasDependencyLanguage(reportData.Dependencies, "elixir"),
	}
	index, warnings := collectIdentityEvidence(repoPath, languages)
	for i := range reportData.Dependencies {
		dep := &reportData.Dependencies[i]
		evidence := identityEvidenceForDependency(index, *dep)
		dep.Identity = buildDependencyIdentity(*dep, evidence)
	}
	reportData.Warnings = sortedUnique(append(reportData.Warnings, warnings...))
}

func hasDependencyLanguage(dependencies []report.DependencyReport, language string) bool {
	for _, dependency := range dependencies {
		if strings.EqualFold(strings.TrimSpace(dependency.Language), language) {
			return true
		}
	}
	return false
}

func collectIdentityEvidence(repoPath string, languages identityEvidenceLanguages) (identityIndex, []string) {
	index := identityIndex{}
	warnings := newIdentityWarningCollector(repoPath)
	snapshot := discoverIdentityManifestSnapshot(repoPath, warnings)
	if languages.python {
		discoverPythonIdentityManifests(repoPath, &snapshot, warnings)
	}
	if languages.dotnet {
		discoverDotNetIdentityManifests(repoPath, &snapshot, warnings)
	}
	if languages.composer {
		discoverComposerIdentityManifests(repoPath, &snapshot, warnings)
	}
	if languages.pub {
		discoverPubIdentityManifests(repoPath, &snapshot, warnings)
	}
	if languages.ruby {
		discoverRubyIdentityManifests(repoPath, &snapshot, warnings)
	}
	if languages.elixir {
		discoverElixirIdentityManifests(repoPath, &snapshot, warnings)
	}
	if languages.hasDedicatedDiscovery() {
		sortIdentityManifestSnapshot(&snapshot)
	}
	collectGoIdentityEvidenceFromSnapshot(repoPath, index, snapshot, warnings)
	collectJSIdentityEvidenceFromSnapshot(repoPath, index, snapshot, warnings)
	if languages.python {
		collectPythonIdentityEvidenceFromPaths(repoPath, index, snapshot.pythonFiles, warnings)
	}
	collectJVMIdentityEvidenceFromSnapshot(repoPath, index, snapshot, warnings)
	collectSwiftIdentityEvidenceFromPaths(repoPath, index, snapshot.swiftFiles, snapshot.podLockFiles, snapshot.carthageLockFiles, warnings)
	collectCargoIdentityEvidenceFromSnapshot(repoPath, index, snapshot, warnings)
	if languages.dotnet {
		collectDotNetIdentityEvidenceFromSnapshot(repoPath, index, snapshot, warnings)
	}
	if languages.composer {
		collectComposerIdentityEvidenceFromPaths(repoPath, index, snapshot.composerFiles, warnings)
	}
	if languages.pub {
		collectPubIdentityEvidenceFromPaths(repoPath, index, snapshot.pubFiles, warnings)
	}
	if languages.ruby {
		collectRubyIdentityEvidenceFromPaths(repoPath, index, snapshot.rubyFiles, warnings)
	}
	if languages.elixir {
		collectElixirIdentityEvidenceFromPaths(repoPath, index, snapshot.elixirFiles, warnings)
	}
	return index, warnings.list()
}

func (l *identityEvidenceLanguages) hasDedicatedDiscovery() bool {
	return l.python || l.dotnet || l.composer || l.pub || l.ruby || l.elixir
}

func discoverIdentityManifestSnapshot(repoPath string, warnings *identityWarningCollector) identityManifestSnapshot {
	snapshot := identityManifestSnapshot{}
	if err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			warnings.addFailure("discovery", path, identityDiscoveryFailed, err)
			return nil
		}
		if entry.IsDir() {
			if path != repoPath && shouldSkipIdentityDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel := relativeIdentitySource(repoPath, path)
		recordDiscoveredIdentityManifest(&snapshot, rel, path, entry.Name())
		return nil
	}); err != nil {
		return identityManifestSnapshot{}
	}
	sortIdentityManifestSnapshot(&snapshot)
	return snapshot
}

func recordDiscoveredIdentityManifest(snapshot *identityManifestSnapshot, relPath, path, base string) {
	switch base {
	case goModFileName:
		appendPackageIdentityManifest(&snapshot.goModFiles, relPath, path)
	case goWorkFileName:
		appendPackageIdentityManifest(&snapshot.goWorkFiles, relPath, path)
	case packageLockFileName, "yarn.lock", pnpmLockFileName:
		appendPackageIdentityManifest(&snapshot.jsLockfiles, relPath, path)
	case "pom.xml":
		snapshot.pomFiles = append(snapshot.pomFiles, path)
	case "build.gradle", "build.gradle.kts":
		snapshot.gradleBuildFiles = append(snapshot.gradleBuildFiles, path)
	case "gradle.lockfile":
		snapshot.gradleLockFiles = append(snapshot.gradleLockFiles, path)
	case "Package.resolved":
		snapshot.swiftFiles = append(snapshot.swiftFiles, path)
	case "Podfile.lock":
		snapshot.podLockFiles = append(snapshot.podLockFiles, path)
	case carthageResolvedFileName:
		snapshot.carthageLockFiles = append(snapshot.carthageLockFiles, path)
	case cargoManifestFileName:
		snapshot.cargoManifestFiles = append(snapshot.cargoManifestFiles, path)
	case cargoLockFileName:
		snapshot.cargoLockFiles = append(snapshot.cargoLockFiles, path)
	}
}

func discoverPythonIdentityManifests(repoPath string, snapshot *identityManifestSnapshot, warnings *identityWarningCollector) {
	snapshot.pythonFiles = append(snapshot.pythonFiles, discoverAdapterIdentityManifests(repoPath, warnings, pythonlang.ShouldSkipDirectory, isPythonIdentityManifest)...)
}

func discoverComposerIdentityManifests(repoPath string, snapshot *identityManifestSnapshot, warnings *identityWarningCollector) {
	for _, name := range []string{composerIdentityManifestName, composerIdentityLockName} {
		path := filepath.Join(repoPath, name)
		if _, err := os.Lstat(path); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				warnings.addFailure("discovery", path, identityDiscoveryFailed, err)
			}
			continue
		}
		snapshot.composerFiles = append(snapshot.composerFiles, path)
	}
}

func discoverPubIdentityManifests(repoPath string, snapshot *identityManifestSnapshot, warnings *identityWarningCollector) {
	snapshot.pubFiles = append(snapshot.pubFiles, discoverAdapterIdentityManifests(repoPath, warnings, dartlang.ShouldSkipDirectory, isPubIdentityManifest)...)
}

func discoverAdapterIdentityManifests(repoPath string, warnings *identityWarningCollector, shouldSkip func(string) bool, isManifest func(string) bool) []string {
	paths := make([]string, 0)
	if err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			warnings.addFailure("discovery", path, identityDiscoveryFailed, err)
			return nil
		}
		if entry.IsDir() {
			if path != repoPath && shouldSkip(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isManifest(entry.Name()) {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		warnings.addFailure("discovery", repoPath, identityDiscoveryFailed, err)
	}
	return paths
}

func isPythonIdentityManifest(name string) bool {
	switch name {
	case poetryLockFileName, uvLockFileName, pythonPipfileName, "Pipfile.lock", pythonProjectFileName, "requirements.txt":
		return true
	default:
		return false
	}
}

func isPubIdentityManifest(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case pubIdentityManifestYAMLName, pubIdentityManifestYMLName, pubIdentityLockName:
		return true
	default:
		return false
	}
}

func discoverDotNetIdentityManifests(repoPath string, snapshot *identityManifestSnapshot, warnings *identityWarningCollector) {
	if err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			warnings.addFailure("discovery", path, identityDiscoveryFailed, err)
			return nil
		}
		if entry.IsDir() {
			if path != repoPath && shouldSkipDotNetIdentityDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		recordDiscoveredDotNetIdentityManifest(snapshot, relativeIdentitySource(repoPath, path), path, entry.Name())
		return nil
	}); err != nil {
		warnings.addFailure("discovery", repoPath, identityDiscoveryFailed, err)
	}
}

func recordDiscoveredDotNetIdentityManifest(snapshot *identityManifestSnapshot, relPath, path, base string) {
	switch {
	case strings.EqualFold(base, dotnetCentralFileName):
		appendDotNetIdentityManifest(&snapshot.dotnetCentralFiles, relPath, path)
	case isDotNetLockFileName(base):
		appendDotNetIdentityManifest(&snapshot.dotnetLockFiles, relPath, path)
	default:
		ext := strings.ToLower(filepath.Ext(base))
		if ext == ".csproj" || ext == ".fsproj" {
			appendDotNetIdentityManifest(&snapshot.dotnetProjectFiles, relPath, path)
		}
	}
}

func appendDotNetIdentityManifest(paths *[]string, relPath, path string) {
	if shouldIncludeDotNetIdentityManifest(relPath) {
		*paths = append(*paths, path)
	}
}

func appendPackageIdentityManifest(paths *[]string, relPath, path string) {
	if shouldIncludePackageIdentityManifest(relPath) {
		*paths = append(*paths, path)
	}
}

func sortIdentityManifestSnapshot(snapshot *identityManifestSnapshot) {
	sort.Strings(snapshot.goModFiles)
	sort.Strings(snapshot.goWorkFiles)
	sort.Strings(snapshot.jsLockfiles)
	sort.Strings(snapshot.pythonFiles)
	sort.Strings(snapshot.pomFiles)
	sort.Strings(snapshot.gradleBuildFiles)
	sort.Strings(snapshot.gradleLockFiles)
	sort.Strings(snapshot.swiftFiles)
	sort.Strings(snapshot.podLockFiles)
	sort.Strings(snapshot.carthageLockFiles)
	sort.Strings(snapshot.cargoManifestFiles)
	sort.Strings(snapshot.cargoLockFiles)
	sort.Strings(snapshot.dotnetProjectFiles)
	sort.Strings(snapshot.dotnetCentralFiles)
	sort.Strings(snapshot.dotnetLockFiles)
	sort.Strings(snapshot.composerFiles)
	sort.Strings(snapshot.pubFiles)
	sort.Strings(snapshot.rubyFiles)
	sort.Strings(snapshot.elixirFiles)
}

func buildDependencyIdentity(dep report.DependencyReport, evidence []identityEvidence) *report.DependencyIdentity {
	state := newDependencyIdentityState(dep, len(evidence))
	for _, item := range evidence {
		state.apply(item)
	}
	state.resolveCoordinates()
	state.resolveVersion()

	purl, purlStatus := state.packageURL()
	return &report.DependencyIdentity{
		Ecosystem:     state.ecosystem,
		Name:          state.name,
		Namespace:     state.namespace,
		Version:       state.version,
		VersionStatus: state.status,
		PURL:          purl,
		PURLStatus:    purlStatus,
		Source:        state.sourceOrDefault(),
		Confidence:    state.confidence,
		Evidence:      sortedUnique(state.evidenceLabels),
		Conflicts:     state.conflicts,
	}
}

func newDependencyIdentityState(dep report.DependencyReport, evidenceCount int) dependencyIdentityState {
	ecosystem := ecosystemForLanguage(dep.Language)
	return dependencyIdentityState{
		ecosystem:      ecosystem,
		name:           canonicalIdentityName(dep.Language, ecosystem, dep.Name),
		status:         identityStatusUnknown,
		confidence:     "low",
		evidenceLabels: make([]string, 0, evidenceCount+1),
		conflicts:      make([]string, 0),
		coordinates:    make(map[identityCoordinate]string),
		versions:       make(map[string]string),
	}
}

func (s *dependencyIdentityState) apply(item identityEvidence) {
	s.applyCoordinates(item)
	s.applySource(item.Source)
	s.applyConfidence(item.Confidence)
	s.applyVersion(item.Version, item.Source)
	s.applyStatus(item.Status)
}

func (s *dependencyIdentityState) applyCoordinates(item identityEvidence) {
	if item.Name == "" {
		return
	}
	coordinate := identityCoordinate{ecosystem: item.Ecosystem, name: item.Name, namespace: item.Namespace}
	s.coordinates[coordinate] = item.Source
}

func (s *dependencyIdentityState) applySource(source string) {
	if source == "" {
		return
	}
	s.evidenceLabels = append(s.evidenceLabels, source)
	if s.source == "" {
		s.source = source
	}
}

func (s *dependencyIdentityState) applyConfidence(confidence string) {
	if confidence != "" && confidenceRank(confidence) > confidenceRank(s.confidence) {
		s.confidence = confidence
	}
}

func (s *dependencyIdentityState) applyVersion(version, source string) {
	if version != "" {
		s.versions[version] = source
	}
}

func (s *dependencyIdentityState) applyStatus(status string) {
	switch {
	case status == identityStatusResolved:
		s.status = identityStatusResolved
	case status == identityStatusDeclared && s.status == identityStatusUnknown:
		s.status = identityStatusDeclared
	}
}

func (s *dependencyIdentityState) resolveCoordinates() {
	if len(s.coordinates) == 1 {
		for coordinate := range s.coordinates {
			s.ecosystem = coordinate.ecosystem
			s.name = coordinate.name
			s.namespace = coordinate.namespace
		}
		return
	}
	if len(s.coordinates) < 2 {
		return
	}
	s.status = identityStatusConflicting
	for coordinate, source := range s.coordinates {
		label := coordinate.ecosystem + ":" + coordinate.name
		if coordinate.namespace != "" {
			label = coordinate.ecosystem + ":" + coordinate.namespace + "/" + coordinate.name
		}
		s.conflicts = append(s.conflicts, label+" from "+source)
	}
	sort.Strings(s.conflicts)
}

func (s *dependencyIdentityState) resolveVersion() {
	if len(s.coordinates) > 1 {
		return
	}
	switch len(s.versions) {
	case 0:
		return
	case 1:
		for version := range s.versions {
			s.version = version
		}
	default:
		s.status = identityStatusConflicting
		for version, source := range s.versions {
			s.conflicts = append(s.conflicts, version+" from "+source)
		}
		sort.Strings(s.conflicts)
	}
}

func (s *dependencyIdentityState) packageURL() (string, string) {
	if s.status == identityStatusConflicting || s.ecosystem == "" || s.name == "" || s.version == "" {
		return "", identityPURLUnavailable
	}
	purl := packageURL(s.ecosystem, s.namespace, s.name, s.version)
	if purl == "" {
		return "", identityPURLUnavailable
	}
	return purl, identityStatusResolved
}

func (s *dependencyIdentityState) sourceOrDefault() string {
	if s.source == "" {
		return "language-adapter"
	}
	return s.source
}

func identityKey(languageID, name string) string {
	return strings.ToLower(strings.TrimSpace(languageID)) + "\x00" + normalizeIdentityNameForLanguage(languageID, name)
}

func qualifiedIdentityKey(languageID, namespace, name string) string {
	return strings.ToLower(strings.TrimSpace(languageID)) + "\x00" + normalizeIdentityName(namespace) + "\x00" + normalizeIdentityNameForLanguage(languageID, name)
}

func normalizeIdentityName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func normalizeIdentityNameForLanguage(languageID, name string) string {
	if canonicalIdentityEcosystem(languageID, ecosystemForLanguage(languageID)) == "cargo" {
		return normalizeCargoIdentityLookupName(name)
	}
	return canonicalIdentityName(languageID, ecosystemForLanguage(languageID), name)
}

func canonicalIdentityName(languageID, ecosystem, name string) string {
	canonicalEcosystem := canonicalIdentityEcosystem(languageID, ecosystem)
	if canonicalEcosystem == "cargo" {
		return strings.TrimSpace(name)
	}
	canonical := report.CanonicalPackageNameForEcosystem(canonicalEcosystem, name)
	if canonical != "" {
		return canonical
	}
	return strings.TrimSpace(name)
}

func canonicalIdentityEcosystem(languageID, ecosystem string) string {
	if canonical := report.CanonicalPackageEcosystem(ecosystem); canonical != "" {
		return canonical
	}
	return report.CanonicalPackageEcosystem(ecosystemForLanguage(languageID))
}

func identityEvidenceForDependency(index identityIndex, dep report.DependencyReport) []identityEvidence {
	if key, ok := qualifiedIdentityLookupKey(dep); ok {
		if evidence := index[key]; len(evidence) != 0 {
			return evidence
		}
	}
	evidence := index[identityKey(dep.Language, dep.Name)]
	if !isQualifiedIdentityRequired(dep.Language, evidence) {
		return evidence
	}
	return nil
}

func qualifiedIdentityLookupKey(dep report.DependencyReport) (string, bool) {
	languageID := strings.ToLower(strings.TrimSpace(dep.Language))
	if languageID != "jvm" && languageID != kotlinAndroidLanguageName {
		return "", false
	}
	namespace := ""
	name := strings.TrimSpace(dep.Name)
	if dep.Identity != nil {
		if identityName := strings.TrimSpace(dep.Identity.Name); identityName != "" {
			name = identityName
		}
		namespace = strings.TrimSpace(dep.Identity.Namespace)
	}
	if namespace == "" {
		group, artifact, ok := parseQualifiedMavenName(name)
		if !ok {
			return "", false
		}
		namespace = group
		name = artifact
	}
	return qualifiedIdentityKey(languageID, namespace, name), namespace != "" && name != ""
}

func parseQualifiedMavenName(value string) (string, string, bool) {
	group, artifact, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return "", "", false
	}
	group = strings.TrimSpace(group)
	artifact = strings.TrimSpace(artifact)
	return group, artifact, group != "" && artifact != ""
}

func isQualifiedIdentityRequired(languageID string, evidence []identityEvidence) bool {
	languageID = strings.ToLower(strings.TrimSpace(languageID))
	if (languageID != "jvm" && languageID != kotlinAndroidLanguageName) || len(evidence) < 2 {
		return false
	}
	namespaces := map[string]struct{}{}
	for _, item := range evidence {
		namespace := normalizeIdentityName(item.Namespace)
		if namespace == "" {
			continue
		}
		namespaces[namespace] = struct{}{}
		if len(namespaces) > 1 {
			return true
		}
	}
	return false
}

func addIdentityEvidence(index identityIndex, item identityEvidence) {
	if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Language) == "" {
		return
	}
	item.Language = strings.ToLower(strings.TrimSpace(item.Language))
	item.Ecosystem = canonicalIdentityEcosystem(item.Language, strings.TrimSpace(item.Ecosystem))
	lookupName := strings.TrimSpace(item.LookupName)
	item.Name = canonicalIdentityName(item.Language, item.Ecosystem, item.Name)
	if lookupName == "" {
		lookupName = item.Name
	}
	item.Namespace = strings.TrimSpace(item.Namespace)
	item.Version = strings.TrimSpace(item.Version)
	item.Source = filepath.ToSlash(strings.TrimSpace(item.Source))
	item.Confidence = strings.TrimSpace(item.Confidence)
	if item.Status == "" {
		item.Status = identityStatusDeclared
	}
	keys := []string{identityKey(item.Language, lookupName)}
	if item.Namespace != "" {
		keys = append(keys, qualifiedIdentityKey(item.Language, item.Namespace, item.Name))
	}
	for _, key := range keys {
		if hasEquivalentIdentityEvidence(index[key], item) {
			continue
		}
		index[key] = append(index[key], item)
	}
}

func hasEquivalentIdentityEvidence(existing []identityEvidence, item identityEvidence) bool {
	for _, candidate := range existing {
		if candidate.Version == item.Version &&
			candidate.Status == item.Status &&
			candidate.Ecosystem == item.Ecosystem &&
			candidate.Namespace == item.Namespace &&
			candidate.Name == item.Name {
			return true
		}
	}
	return false
}

func ecosystemForLanguage(languageID string) string {
	switch strings.ToLower(strings.TrimSpace(languageID)) {
	case "js-ts":
		return "npm"
	case "python":
		return "pypi"
	case "go":
		return "golang"
	case "jvm", kotlinAndroidLanguageName:
		return "maven"
	case "swift":
		return "swift"
	case "rust":
		return "cargo"
	case "dotnet":
		return "nuget"
	case "php":
		return "composer"
	case "dart":
		return "pub"
	case "ruby":
		return "gem"
	case "elixir":
		return "hex"
	default:
		return strings.ToLower(strings.TrimSpace(languageID))
	}
}

func confidenceRank(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func packageURL(ecosystem, namespace, name, version string) string {
	ecosystem = report.CanonicalPackageEcosystem(ecosystem)
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if ecosystem == "" || name == "" || version == "" {
		return ""
	}
	namespace, name = normalizedPURLCoordinates(ecosystem, namespace, name)
	if purl, ok := scopedNPMPackageURL(ecosystem, namespace, name, version); ok {
		return purl
	}
	if namespace == "" {
		return canonicalPackageURL(ecosystem, name, version)
	}
	return namespacedPackageURL(ecosystem, namespace, name, version)
}

func normalizedPURLCoordinates(ecosystem, namespace, name string) (string, string) {
	if ecosystem == "composer" && namespace == "" {
		vendor, packageName, ok := strings.Cut(name, "/")
		if ok && vendor != "" && packageName != "" {
			return vendor, packageName
		}
	}
	if ecosystem == "golang" && namespace == "" {
		return splitPURLNamespaceName(name)
	}
	return namespace, name
}

func scopedNPMPackageURL(ecosystem, namespace, name, version string) (string, bool) {
	if ecosystem != "npm" || namespace != "" || !strings.HasPrefix(name, "@") {
		return "", false
	}
	scope, pkg, ok := strings.Cut(name, "/")
	if !ok || scope == "" || pkg == "" {
		return "", false
	}
	return "pkg:npm/" + escapeNPMScopePURL(scope) + "/" + escapePURL(pkg) + "@" + escapePURL(version), true
}

func namespacedPackageURL(ecosystem, namespace, name, version string) string {
	if ecosystem == "golang" {
		return report.CanonicalPURL("pkg:" + escapePURL(ecosystem) + "/" + escapePURLPathSegments(namespace, name) + "@" + escapePURL(version))
	}
	return "pkg:" + escapePURL(ecosystem) + "/" + escapePURL(namespace) + "/" + escapePURL(name) + "@" + escapePURL(version)
}

func canonicalPackageURL(ecosystem, name, version string) string {
	purl := "pkg:" + escapePURL(ecosystem) + "/" + escapePURL(name) + "@" + escapePURL(version)
	if ecosystem == "golang" {
		return report.CanonicalPURL(purl)
	}
	return purl
}

func splitPURLNamespaceName(name string) (string, string) {
	name = strings.TrimSpace(name)
	lastSlash := strings.LastIndexByte(name, '/')
	if lastSlash <= 0 || lastSlash+1 >= len(name) {
		return "", name
	}
	return name[:lastSlash], name[lastSlash+1:]
}

func escapePURLPathSegments(namespace, name string) string {
	segments := make([]string, 0, strings.Count(namespace, "/")+1)
	for _, segment := range strings.Split(strings.TrimSpace(namespace), "/") {
		if segment == "" {
			continue
		}
		segments = append(segments, escapePURL(segment))
	}
	if trimmedName := strings.TrimSpace(name); trimmedName != "" {
		segments = append(segments, escapePURL(trimmedName))
	}
	return strings.Join(segments, "/")
}

func escapePURL(value string) string {
	return strings.ReplaceAll(url.PathEscape(strings.TrimSpace(value)), "+", "%2B")
}

func escapeNPMScopePURL(value string) string {
	return strings.ReplaceAll(escapePURL(value), "@", "%40")
}

func collectGoIdentityEvidence(repoPath string, index identityIndex) {
	snapshot := discoverIdentityManifestSnapshot(repoPath, nil)
	collectGoIdentityEvidenceFromSnapshot(repoPath, index, snapshot, nil)
}

type goWorkspaceReplacements struct {
	moduleDirs            map[string]struct{}
	replacements          map[dependencyVersion]struct{}
	replacementsUncertain bool
}

func collectGoIdentityEvidenceFromSnapshot(repoPath string, index identityIndex, snapshot identityManifestSnapshot, warnings *identityWarningCollector) {
	workspaces := loadGoWorkspaceReplacements(repoPath, snapshot.goWorkFiles, warnings)
	collectGoIdentityEvidenceFromPaths(repoPath, index, snapshot.goModFiles, workspaces, warnings)
}

func collectGoIdentityEvidenceFromPaths(repoPath string, index identityIndex, paths []string, workspaces []goWorkspaceReplacements, warnings *identityWarningCollector) {
	for _, path := range paths {
		data, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			warnings.addFailure("read", path, identityReadFailed, err)
			continue
		}
		source := relativeIdentitySource(repoPath, path)
		content := string(data)
		replacements, err := parseGoModReplacements(content)
		if err != nil {
			warnings.addFailure("parse", path, identityParseFailed, err)
			continue
		}
		for _, item := range parseGoModRequires(content) {
			if goModRequirementIsReplaced(item, replacements) || goWorkspaceRequirementIsReplaced(filepath.Dir(path), item, workspaces) {
				continue
			}
			addIdentityEvidence(index, identityEvidence{
				Language:   "go",
				Ecosystem:  "golang",
				Name:       item.name,
				Version:    item.version,
				Status:     identityStatusResolved,
				Source:     source,
				Confidence: "high",
			})
		}
	}
}

func loadGoWorkspaceReplacements(repoPath string, paths []string, warnings *identityWarningCollector) []goWorkspaceReplacements {
	workspaces := make([]goWorkspaceReplacements, 0, len(paths))
	for _, path := range paths {
		data, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			warnings.addFailure("read", path, identityReadFailed, err)
			continue
		}
		workspace, err := parseGoWorkspaceReplacements(path, data)
		if err != nil {
			warnings.addFailure("parse", path, identityParseFailed, err)
			if len(workspace.moduleDirs) == 0 {
				continue
			}
		}
		workspaces = append(workspaces, workspace)
	}
	return workspaces
}

func parseGoWorkspaceReplacements(path string, data []byte) (goWorkspaceReplacements, error) {
	file, err := modfile.ParseWork(path, data, nil)
	if err == nil {
		return goWorkspaceReplacementsFromFile(path, file), nil
	}
	return parseGoWorkspaceReplacementFallback(path, string(data))
}

func parseGoWorkspaceReplacementFallback(path, content string) (goWorkspaceReplacements, error) {
	useFile, useErr := parseGoWorkUseDirectives(path, goDirectiveLines(content, "use"))
	workspace := goWorkspaceReplacementsFromFile(path, useFile)
	replaceFile, err := parseGoWorkDirectives(path, goDirectiveLines(content, "replace"))
	if err != nil {
		workspace.replacementsUncertain = true
		return workspace, errors.Join(useErr, err)
	}
	workspace.replacements = goReplacementSet(replaceFile.Replace)
	return workspace, useErr
}

func parseGoWorkDirectives(path string, lines []string) (*modfile.WorkFile, error) {
	payload := strings.Join(lines, "\n")
	if payload != "" {
		payload += "\n"
	}
	return modfile.ParseWork(path, []byte(payload), nil)
}

func parseGoWorkUseDirectives(path string, lines []string) (*modfile.WorkFile, error) {
	file, err := parseGoWorkDirectives(path, lines)
	if err == nil {
		return file, nil
	}

	recovered := &modfile.WorkFile{}
	inBlock := false
	for _, line := range lines {
		candidate := ""
		switch {
		case goDirectiveBlockStart(line, "use"):
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "":
			candidate = "use " + line
		case goDirectiveLine(line, "use"):
			candidate = line
		}
		if candidate == "" {
			continue
		}
		parsed, candidateErr := parseGoWorkDirectives(path, []string{candidate})
		if candidateErr == nil {
			recovered.Use = append(recovered.Use, parsed.Use...)
		}
	}
	return recovered, err
}

func goWorkspaceReplacementsFromFile(path string, file *modfile.WorkFile) goWorkspaceReplacements {
	workspaceDir := filepath.Dir(path)
	moduleDirs := make(map[string]struct{}, len(file.Use))
	for _, use := range file.Use {
		moduleDir := use.Path
		if !filepath.IsAbs(moduleDir) {
			moduleDir = filepath.Join(workspaceDir, moduleDir)
		}
		moduleDirs[filepath.Clean(moduleDir)] = struct{}{}
	}
	return goWorkspaceReplacements{
		moduleDirs:   moduleDirs,
		replacements: goReplacementSet(file.Replace),
	}
}

func goWorkspaceRequirementIsReplaced(moduleDir string, requirement dependencyVersion, workspaces []goWorkspaceReplacements) bool {
	moduleDir = filepath.Clean(moduleDir)
	for _, workspace := range workspaces {
		if !goWorkspaceContainsModule(workspace.moduleDirs, moduleDir) {
			continue
		}
		if workspace.replacementsUncertain || goModRequirementIsReplaced(requirement, workspace.replacements) {
			return true
		}
	}
	return false
}

func goWorkspaceContainsModule(moduleDirs map[string]struct{}, moduleDir string) bool {
	if _, ok := moduleDirs[moduleDir]; ok {
		return true
	}
	moduleInfo, err := os.Stat(moduleDir)
	if err != nil {
		return false
	}
	for workspaceModuleDir := range moduleDirs {
		workspaceInfo, statErr := os.Stat(workspaceModuleDir)
		if statErr == nil && os.SameFile(moduleInfo, workspaceInfo) {
			return true
		}
	}
	return false
}

func findGoModFiles(repoPath string) []string {
	return discoverIdentityManifestSnapshot(repoPath, nil).goModFiles
}

func shouldSkipPackageIdentityDir(name string) bool {
	if shouldSkipIdentityDir(name) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "cache", ".cache", "generated":
		return true
	default:
		return false
	}
}

func shouldIncludePackageIdentityManifest(relPath string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(relPath)), "/") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case ".cache", "cache", "generated":
			return false
		}
	}
	return true
}

func shouldIncludeDotNetIdentityManifest(relPath string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(relPath)), "/") {
		if shouldSkipDotNetIdentityDir(part) {
			return false
		}
	}
	return true
}

func shouldSkipDotNetIdentityDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".idea", ".vscode", "node_modules", "vendor", "bin", "obj", "dist", "build", "packages":
		return true
	default:
		return false
	}
}

type dependencyVersion struct {
	name    string
	version string
}

type jsLockPackages map[string]map[string]struct{}

type pnpmLockIdentity struct {
	lookupName string
	name       string
	version    string
}

func parseGoModRequires(content string) []dependencyVersion {
	items := make([]dependencyVersion, 0)
	inRequireBlock := false
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.Split(line, "//")[0])
		switch {
		case line == "":
			continue
		case goDirectiveBlockStart(line, "require"):
			inRequireBlock = true
			continue
		case inRequireBlock && line == ")":
			inRequireBlock = false
			continue
		}
		fields := strings.Fields(line)
		if inRequireBlock {
			if len(fields) >= 2 {
				items = append(items, dependencyVersion{name: fields[0], version: fields[1]})
			}
			continue
		}
		if !goDirectiveLine(line, "require") || len(fields) < 3 {
			continue
		}
		items = append(items, dependencyVersion{name: fields[1], version: fields[2]})
	}
	return items
}

func parseGoModReplacements(content string) (map[dependencyVersion]struct{}, error) {
	file, err := modfile.Parse(goModFileName, []byte(content), nil)
	if err == nil {
		return goReplacementSet(file.Replace), nil
	}
	return parseGoModReplacementFallback(content)
}

func parseGoModReplacementFallback(content string) (map[dependencyVersion]struct{}, error) {
	lines := goModReplacementLines(content)
	if len(lines) == 0 {
		return map[dependencyVersion]struct{}{}, nil
	}
	payload := "module example.com/lopper-replacement-fallback\n\n" + strings.Join(lines, "\n") + "\n"
	file, err := modfile.Parse(goModFileName, []byte(payload), nil)
	if err != nil {
		return nil, err
	}
	return goReplacementSet(file.Replace), nil
}

func goModReplacementLines(content string) []string {
	return goDirectiveLines(content, "replace")
}

func goDirectiveLines(content, directive string) []string {
	lines := make([]string, 0)
	inBlock := false
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.Split(rawLine, "//")[0])
		switch {
		case inBlock:
			lines = append(lines, line)
			inBlock = line != ")"
		case goDirectiveBlockStart(line, directive):
			lines = append(lines, line)
			inBlock = true
		case goDirectiveLine(line, directive):
			lines = append(lines, line)
		}
	}
	return lines
}

func goDirectiveBlockStart(line, directive string) bool {
	fields := strings.Fields(line)
	return len(fields) == 2 && fields[0] == directive && fields[1] == "("
}

func goDirectiveLine(line, directive string) bool {
	fields := strings.Fields(line)
	return len(fields) > 0 && fields[0] == directive
}

func goReplacementSet(items []*modfile.Replace) map[dependencyVersion]struct{} {
	replacements := make(map[dependencyVersion]struct{}, len(items))
	for _, replacement := range items {
		replacements[dependencyVersion{name: replacement.Old.Path, version: replacement.Old.Version}] = struct{}{}
	}
	return replacements
}

func goModRequirementIsReplaced(requirement dependencyVersion, replacements map[dependencyVersion]struct{}) bool {
	if _, ok := replacements[requirement]; ok {
		return true
	}
	_, ok := replacements[dependencyVersion{name: requirement.name}]
	return ok
}

func collectJSIdentityEvidence(repoPath string, index identityIndex) {
	collectRootNodeModulesIdentityEvidence(repoPath, index, nil)
	collectJSLockfileIdentityEvidenceFromPaths(repoPath, index, findJSLockfilePaths(repoPath), nil)
}

func collectJSLockfileIdentityEvidence(repoPath string, index identityIndex) {
	collectJSLockfileIdentityEvidenceFromPaths(repoPath, index, findJSLockfilePaths(repoPath), nil)
}

func collectJSIdentityEvidenceFromSnapshot(repoPath string, index identityIndex, snapshot identityManifestSnapshot, warnings *identityWarningCollector) {
	collectRootNodeModulesIdentityEvidence(repoPath, index, warnings)
	collectJSLockfileIdentityEvidenceFromPaths(repoPath, index, snapshot.jsLockfiles, warnings)
}

func collectJSLockfileIdentityEvidenceFromPaths(repoPath string, index identityIndex, paths []string, warnings *identityWarningCollector) {
	for _, path := range paths {
		switch filepath.Base(path) {
		case packageLockFileName:
			collectPackageLockIdentityEvidenceAtPath(repoPath, path, index, warnings)
		case "yarn.lock":
			collectYarnLockIdentityEvidenceAtPath(repoPath, path, index, warnings)
		case pnpmLockFileName:
			collectPNPMLockIdentityEvidenceAtPath(repoPath, path, index, warnings)
		}
	}
}

func findJSLockfilePaths(repoPath string) []string {
	return discoverIdentityManifestSnapshot(repoPath, nil).jsLockfiles
}

func collectRootNodeModulesIdentityEvidence(repoPath string, index identityIndex, warnings *identityWarningCollector) {
	nodeModulesPath := filepath.Join(repoPath, "node_modules")
	// Root node_modules package manifests require a dedicated bounded walk because
	// repo-wide manifest discovery intentionally skips recursive node_modules trees
	// while direct package identities still come from top-level installed packages.
	if _, err := os.Stat(nodeModulesPath); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			warnings.addFailure("discovery", nodeModulesPath, identityDiscoveryFailed, err)
		}
		return
	}
	if err := filepath.WalkDir(nodeModulesPath, func(path string, entry fs.DirEntry, err error) error {
		return collectJSIdentityEvidenceEntry(repoPath, nodeModulesPath, path, entry, err, index, warnings)
	}); err != nil {
		warnings.addFailure("discovery", nodeModulesPath, identityDiscoveryFailed, err)
	}
}

func collectJSIdentityEvidenceEntry(repoPath, nodeModulesPath, path string, entry fs.DirEntry, walkErr error, index identityIndex, warnings *identityWarningCollector) error {
	if walkErr != nil {
		warnings.addFailure("discovery", path, identityDiscoveryFailed, walkErr)
		return nil
	}
	if shouldSkipNestedNodeModules(nodeModulesPath, path, entry) {
		return filepath.SkipDir
	}
	if !isJSIdentityPackageFile(nodeModulesPath, path, entry) {
		return nil
	}
	addJSIdentityPackageEvidence(repoPath, path, index, warnings)
	return nil
}

func shouldSkipNestedNodeModules(nodeModulesPath, path string, entry fs.DirEntry) bool {
	return entry.IsDir() && entry.Name() == "node_modules" && path != nodeModulesPath
}

func isJSIdentityPackageFile(nodeModulesPath, path string, entry fs.DirEntry) bool {
	return !entry.IsDir() && entry.Name() == nodePackageManifestFile && isDirectNodeModulePackage(nodeModulesPath, path)
}

func addJSIdentityPackageEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	data, readErr := safeio.ReadFileUnder(repoPath, path)
	if readErr != nil {
		warnings.addFailure("read", path, identityReadFailed, readErr)
		return
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return
	}
	if strings.TrimSpace(pkg.Name) == "" {
		return
	}
	addIdentityEvidence(index, identityEvidence{
		Language:   "js-ts",
		Ecosystem:  "npm",
		Name:       pkg.Name,
		Version:    pkg.Version,
		Status:     versionStatus(pkg.Version, identityStatusResolved),
		Source:     relativeIdentitySource(repoPath, path),
		Confidence: "high",
	})
}

func collectPackageLockIdentityEvidence(repoPath string, index identityIndex) int {
	return collectPackageLockIdentityEvidenceAtPath(repoPath, filepath.Join(repoPath, packageLockFileName), index, nil)
}

func collectPackageLockIdentityEvidenceAtPath(repoPath, path string, index identityIndex, warnings *identityWarningCollector) int {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return 0
	}
	var doc struct {
		Packages map[string]struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"packages"`
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return 0
	}
	items := jsLockPackages{}
	packagePaths := make([]string, 0, len(doc.Packages))
	for path := range doc.Packages {
		packagePaths = append(packagePaths, path)
	}
	sort.Strings(packagePaths)
	for _, path := range packagePaths {
		name, ok := packageLockDirectPackageName(path, doc.Packages[path].Name)
		if !ok {
			continue
		}
		version := normalizeJSLockVersion(doc.Packages[path].Version)
		if version == "" {
			continue
		}
		addJSLockPackageVersion(items, name, version)
	}
	names := make([]string, 0, len(doc.Dependencies))
	for name := range doc.Dependencies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if hasJSLockPackageName(items, name) {
			continue
		}
		version := normalizeJSLockVersion(doc.Dependencies[name].Version)
		if version == "" {
			continue
		}
		addJSLockPackageVersion(items, name, version)
	}
	return addJSLockEvidence(index, relativeIdentitySource(repoPath, path), items)
}

func packageLockDirectPackageName(path, declaredName string) (string, bool) {
	packageParts, ok := packageLockDirectPackageParts(path)
	if !ok {
		return "", false
	}
	return packageLockResolvedName(packageParts, declaredName)
}

func packageLockDirectPackageParts(path string) ([]string, bool) {
	parts := strings.Split(filepath.ToSlash(strings.TrimSpace(path)), "/")
	nodeModulesIndex := packageLockNodeModulesIndex(parts)
	if nodeModulesIndex < 0 || nodeModulesIndex+1 >= len(parts) {
		return nil, false
	}
	packageParts := parts[nodeModulesIndex+1:]
	if packageLockHasNestedNodeModules(packageParts) {
		return nil, false
	}
	return packageParts, true
}

func packageLockNodeModulesIndex(parts []string) int {
	for i, part := range parts {
		if part == "node_modules" {
			return i
		}
	}
	return -1
}

func packageLockHasNestedNodeModules(parts []string) bool {
	for _, part := range parts {
		if part == "node_modules" {
			return true
		}
	}
	return false
}

func packageLockResolvedName(packageParts []string, declaredName string) (string, bool) {
	name := strings.TrimSpace(declaredName)
	switch len(packageParts) {
	case 1:
		return packageLockSingleSegmentName(packageParts[0], name)
	case 2:
		return packageLockScopedName(packageParts[0], packageParts[1], name)
	default:
		return "", false
	}
}

func packageLockSingleSegmentName(segment, declaredName string) (string, bool) {
	if !isVisibleNodeModuleSegment(segment) {
		return "", false
	}
	if declaredName != "" {
		return declaredName, true
	}
	return segment, true
}

func packageLockScopedName(scope, segment, declaredName string) (string, bool) {
	if !isVisibleNodeModuleScope(scope) || !isVisibleNodeModuleSegment(segment) {
		return "", false
	}
	if declaredName != "" {
		return declaredName, true
	}
	return scope + "/" + segment, true
}

func collectYarnLockIdentityEvidenceAtPath(repoPath, path string, index identityIndex, warnings *identityWarningCollector) int {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return 0
	}
	items := parseYarnLockPackages(string(data))
	return addJSLockEvidence(index, relativeIdentitySource(repoPath, path), items)
}

func parseYarnLockPackages(content string) jsLockPackages {
	items := jsLockPackages{}
	lines := strings.Split(content, "\n")
	for i := 0; i < len(lines); {
		selectors, version, next := parseYarnLockPackageEntry(lines, i)
		i = next
		if version == "" {
			continue
		}
		addYarnLockPackageSelectors(items, selectors, version)
	}
	return items
}

func parseYarnLockPackageEntry(lines []string, index int) (selectors, version string, next int) {
	line := lines[index]
	trimmed := strings.TrimSpace(line)
	if !isYarnLockPackageHeader(line, trimmed) {
		return "", "", index + 1
	}
	selectors = strings.TrimSuffix(trimmed, ":")
	version, next = parseYarnLockPackageVersion(lines, index+1)
	return selectors, version, next
}

func isYarnLockPackageHeader(line, trimmed string) bool {
	return trimmed != "" &&
		!strings.HasPrefix(trimmed, "#") &&
		!strings.HasPrefix(line, " ") &&
		!strings.HasPrefix(line, "\t") &&
		strings.HasSuffix(trimmed, ":")
}

func parseYarnLockPackageVersion(lines []string, index int) (string, int) {
	version := ""
	for index < len(lines) {
		line := lines[index]
		if strings.TrimSpace(line) == "" {
			index++
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			break
		}
		entry := strings.TrimSpace(line)
		for _, prefix := range []string{"version ", "version:"} {
			if strings.HasPrefix(entry, prefix) {
				version = normalizeJSLockVersion(strings.Trim(strings.TrimSpace(entry[len(prefix):]), `"'`))
				break
			}
		}
		index++
	}
	return version, index
}

func addYarnLockPackageSelectors(items jsLockPackages, selectors, version string) {
	for _, selector := range strings.Split(selectors, ",") {
		name := parseYarnSelectorName(selector)
		if name == "" {
			continue
		}
		addJSLockPackageVersion(items, name, version)
	}
}

func parseYarnSelectorName(selector string) string {
	selector = strings.Trim(selector, ` "'`)
	if selector == "" {
		return ""
	}
	if aliasAt := strings.Index(selector, "@"+npmAliasPrefix); aliasAt >= 0 {
		if name, version, ok := parsePNPMAliasReference(selector[aliasAt+1:]); ok && version != "" {
			return name
		}
	}
	if strings.HasPrefix(selector, "@") {
		if idx := strings.LastIndex(selector, "@"); idx > 0 {
			return selector[:idx]
		}
		return ""
	}
	if idx := strings.Index(selector, "@"); idx > 0 {
		return selector[:idx]
	}
	return ""
}

func collectPNPMLockIdentityEvidence(repoPath string, index identityIndex) int {
	return collectPNPMLockIdentityEvidenceAtPath(repoPath, filepath.Join(repoPath, pnpmLockFileName), index, nil)
}

func collectPNPMLockIdentityEvidenceAtPath(repoPath, path string, index identityIndex, warnings *identityWarningCollector) int {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return 0
	}
	var doc struct {
		Specifiers           map[string]string `yaml:"specifiers"`
		Dependencies         map[string]any    `yaml:"dependencies"`
		DevDependencies      map[string]any    `yaml:"devDependencies"`
		OptionalDependencies map[string]any    `yaml:"optionalDependencies"`
		Importers            map[string]struct {
			Specifiers           map[string]string `yaml:"specifiers"`
			Dependencies         map[string]any    `yaml:"dependencies"`
			DevDependencies      map[string]any    `yaml:"devDependencies"`
			OptionalDependencies map[string]any    `yaml:"optionalDependencies"`
		} `yaml:"importers"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return 0
	}
	items := make([]pnpmLockIdentity, 0)
	addPNPMImporterDependencies(&items, doc.Dependencies, doc.Specifiers)
	addPNPMImporterDependencies(&items, doc.DevDependencies, doc.Specifiers)
	addPNPMImporterDependencies(&items, doc.OptionalDependencies, doc.Specifiers)
	importerNames := make([]string, 0, len(doc.Importers))
	for name := range doc.Importers {
		importerNames = append(importerNames, name)
	}
	sort.Strings(importerNames)
	for _, importer := range importerNames {
		entry := doc.Importers[importer]
		addPNPMImporterDependencies(&items, entry.Dependencies, entry.Specifiers)
		addPNPMImporterDependencies(&items, entry.DevDependencies, entry.Specifiers)
		addPNPMImporterDependencies(&items, entry.OptionalDependencies, entry.Specifiers)
	}
	return addPNPMLockEvidence(index, relativeIdentitySource(repoPath, path), items)
}

func addPNPMImporterDependencies(items *[]pnpmLockIdentity, deps map[string]any, specifiers map[string]string) {
	names := make([]string, 0, len(deps))
	for name := range deps {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, lookupName := range names {
		value := deps[lookupName]
		if specifier := strings.TrimSpace(specifiers[lookupName]); specifier != "" {
			if _, scalar := value.(string); scalar {
				value = map[string]any{"specifier": specifier, "version": value}
			}
		}
		name, version := parsePNPMDependencyIdentity(lookupName, value)
		if version == "" {
			continue
		}
		*items = append(*items, pnpmLockIdentity{lookupName: lookupName, name: name, version: version})
	}
}

func parsePNPMDependencyIdentity(lookupName string, value any) (string, string) {
	rawVersion := parsePNPMDependencyVersion(value)
	specifier := parsePNPMDependencySpecifier(value)
	if isPNPMNonRegistryReference(specifier) || isPNPMNonRegistryReference(rawVersion) {
		return lookupName, ""
	}
	if name, selectorVersion, ok := parsePNPMAliasReference(specifier); ok {
		if _, scalar := value.(string); scalar && selectorVersion == "" {
			return name, ""
		}
		return name, normalizePNPMLockVersion(rawVersion, name)
	}
	if strings.HasPrefix(strings.TrimSpace(specifier), npmAliasPrefix) {
		return lookupName, ""
	}
	if name, version, ok := parsePNPMAliasReference(rawVersion); ok {
		return name, normalizeJSLockVersion(version)
	}
	return lookupName, normalizePNPMLockVersion(rawVersion, lookupName)
}

func isPNPMNonRegistryReference(value string) bool {
	value = strings.TrimSpace(value)
	separator := strings.IndexByte(value, ':')
	if separator <= 0 {
		return false
	}
	parsed, err := url.Parse(value[:separator] + ":")
	return err == nil && parsed.Scheme != "" && !strings.EqualFold(parsed.Scheme, strings.TrimSuffix(npmAliasPrefix, ":"))
}

func parsePNPMDependencySpecifier(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		specifier, _ := typed["specifier"].(string)
		return specifier
	default:
		return ""
	}
}

func parsePNPMAliasReference(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, npmAliasPrefix) {
		return "", "", false
	}
	reference := strings.TrimSpace(strings.TrimPrefix(value, npmAliasPrefix))
	if reference == "" || strings.HasSuffix(reference, "@") {
		return "", "", false
	}
	name := reference
	version := ""
	versionAt := strings.LastIndexByte(reference, '@')
	if strings.HasPrefix(reference, "@") {
		if scopeEnd := strings.IndexByte(reference, '/'); versionAt > scopeEnd && scopeEnd > 1 {
			name = reference[:versionAt]
			version = reference[versionAt+1:]
		}
	} else if versionAt > 0 {
		name = reference[:versionAt]
		version = reference[versionAt+1:]
	}
	if !validPNPMAliasTarget(name) {
		return "", "", false
	}
	return name, version, true
}

func validPNPMAliasTarget(name string) bool {
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return false
	}
	if !strings.HasPrefix(name, "@") {
		return !strings.ContainsAny(name, "@/")
	}
	scope, packageName, ok := strings.Cut(name, "/")
	if !ok || len(scope) <= 1 || packageName == "" {
		return false
	}
	return !strings.Contains(scope[1:], "@") && !strings.ContainsAny(packageName, "@/")
}

func normalizePNPMLockVersion(version, name string) string {
	version = normalizeJSLockVersion(version)
	legacyPrefix := "/" + name + "/"
	switch {
	case strings.HasPrefix(version, legacyPrefix):
		version = strings.TrimPrefix(version, legacyPrefix)
	case strings.HasPrefix(version, "/"):
		return ""
	case strings.HasPrefix(version, name+"@"):
		version = strings.TrimPrefix(version, name+"@")
	}
	if peerAt := strings.IndexByte(version, '_'); peerAt > 0 {
		version = version[:peerAt]
	}
	if strings.Contains(version, "@") {
		return ""
	}
	return strings.TrimSpace(version)
}

func parsePNPMDependencyVersion(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		version, _ := typed["version"].(string)
		return version
	default:
		return ""
	}
}

func addPNPMLockEvidence(index identityIndex, source string, items []pnpmLockIdentity) int {
	lookupNames := make(map[string]struct{}, len(items))
	for _, item := range items {
		addIdentityEvidence(index, identityEvidence{
			Language:   "js-ts",
			Ecosystem:  "npm",
			LookupName: item.lookupName,
			Name:       item.name,
			Version:    item.version,
			Status:     identityStatusResolved,
			Source:     source,
			Confidence: "high",
		})
		lookupNames[item.lookupName] = struct{}{}
	}
	return len(lookupNames)
}

func normalizeJSLockVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	if idx := strings.Index(version, "("); idx > 0 {
		version = version[:idx]
	}
	return strings.TrimSpace(strings.TrimPrefix(version, npmAliasPrefix))
}

func addJSLockPackageVersion(items jsLockPackages, name, version string) {
	if items[name] == nil {
		items[name] = map[string]struct{}{}
	}
	items[name][version] = struct{}{}
}

func hasJSLockPackageName(items jsLockPackages, name string) bool {
	versions, ok := items[name]
	return ok && len(versions) > 0
}

func sortedJSLockVersions(versions map[string]struct{}) []string {
	items := make([]string, 0, len(versions))
	for version := range versions {
		items = append(items, version)
	}
	sort.Strings(items)
	return items
}

func addJSLockEvidence(index identityIndex, source string, items jsLockPackages) int {
	if len(items) == 0 {
		return 0
	}
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	count := 0
	for _, name := range names {
		versions := sortedJSLockVersions(items[name])
		if len(versions) == 0 {
			continue
		}
		for _, version := range versions {
			addIdentityEvidence(index, identityEvidence{
				Language:   "js-ts",
				Ecosystem:  "npm",
				Name:       name,
				Version:    version,
				Status:     versionStatus(version, identityStatusResolved),
				Source:     source,
				Confidence: "high",
			})
		}
		count++
	}
	return count
}

func isDirectNodeModulePackage(nodeModulesPath, packagePath string) bool {
	rel, err := filepath.Rel(nodeModulesPath, packagePath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	switch len(parts) {
	case 2:
		return parts[1] == nodePackageManifestFile && isVisibleNodeModuleSegment(parts[0])
	case 3:
		return parts[2] == nodePackageManifestFile && isVisibleNodeModuleScope(parts[0]) && isVisibleNodeModuleSegment(parts[1])
	default:
		return false
	}
}

func isVisibleNodeModuleScope(value string) bool {
	if !strings.HasPrefix(value, "@") || len(value) < 2 {
		return false
	}
	return isVisibleNodeModuleSegment(strings.TrimPrefix(value, "@"))
}

func isVisibleNodeModuleSegment(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.HasPrefix(value, ".")
}

func collectPythonIdentityEvidence(repoPath string, index identityIndex) {
	snapshot := identityManifestSnapshot{}
	discoverPythonIdentityManifests(repoPath, &snapshot, nil)
	sort.Strings(snapshot.pythonFiles)
	collectPythonIdentityEvidenceFromPaths(repoPath, index, snapshot.pythonFiles, nil)
}

func collectPythonIdentityEvidenceFromPaths(repoPath string, index identityIndex, paths []string, warnings *identityWarningCollector) {
	for _, path := range paths {
		switch filepath.Base(path) {
		case poetryLockFileName, uvLockFileName:
			collectPythonTOMLLockEvidence(repoPath, path, index, warnings)
		case "Pipfile.lock":
			collectPipfileLockEvidence(repoPath, path, index, warnings)
		case "requirements.txt":
			collectRequirementsEvidence(repoPath, path, index, warnings)
		}
	}
	for _, path := range paths {
		switch filepath.Base(path) {
		case pythonProjectFileName:
			collectPyprojectManifestEvidence(repoPath, path, index, warnings)
		case pythonPipfileName:
			collectPipfileManifestEvidence(repoPath, path, index, warnings)
		}
	}
}

func collectPythonTOMLLockEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return
	}
	source := relativeIdentitySource(repoPath, path)
	packages, _ := doc["package"].([]any)
	for _, entry := range packages {
		table, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name, _ := table["name"].(string)
		version, _ := table["version"].(string)
		addPythonEvidence(index, name, version, source, "high")
	}
}

func collectPipfileLockEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return
	}
	source := relativeIdentitySource(repoPath, path)
	for _, section := range []string{"default", "develop"} {
		raw, ok := doc[section]
		if !ok {
			continue
		}
		var packages map[string]struct {
			Version string `json:"version"`
		}
		if err := json.Unmarshal(raw, &packages); err != nil {
			warnings.addSectionParseFailure(path, section)
			continue
		}
		for name, item := range packages {
			version := strings.TrimPrefix(strings.TrimSpace(item.Version), "==")
			addPythonEvidence(index, name, version, source, "high")
		}
	}
}

var requirementVersionPattern = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)\s*(\[[A-Za-z0-9._,\s-]+\])?\s*==\s*([^;\s#]+)`)

func collectRequirementsEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return
	}
	source := relativeIdentitySource(repoPath, path)
	for _, line := range strings.Split(string(data), "\n") {
		matches := requirementVersionPattern.FindStringSubmatch(line)
		if len(matches) != 4 {
			continue
		}
		addPythonEvidence(index, matches[1], matches[3], source, "medium")
	}
}

func addPythonEvidence(index identityIndex, name, version, source, confidence string) {
	addIdentityEvidence(index, identityEvidence{
		Language:   "python",
		Ecosystem:  "pypi",
		Name:       report.CanonicalPackageNameForEcosystem("pypi", name),
		Version:    version,
		Status:     versionStatus(version, identityStatusResolved),
		Source:     source,
		Confidence: confidence,
	})
}

func collectJVMIdentityEvidence(repoPath string, index identityIndex) {
	collectJVMIdentityEvidenceFromSnapshot(repoPath, index, discoverIdentityManifestSnapshot(repoPath, nil), nil)
}

func collectJVMIdentityEvidenceFromSnapshot(repoPath string, index identityIndex, snapshot identityManifestSnapshot, warnings *identityWarningCollector) {
	for _, path := range snapshot.pomFiles {
		collectPomIdentityEvidence(repoPath, path, index, warnings)
	}
	collectGradleIdentityEvidenceFromPaths(repoPath, index, snapshot.gradleBuildFiles, snapshot.gradleLockFiles, warnings)
}

type pomModel struct {
	Properties          pomProperties   `xml:"properties"`
	Dependencies        []pomDependency `xml:"dependencies>dependency"`
	ManagedDependencies []pomDependency `xml:"dependencyManagement>dependencies>dependency"`
}

type pomDependency struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
}

type pomProperties struct {
	Entries []pomProperty `xml:",any"`
}

type pomProperty struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

func collectPomIdentityEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return
	}
	var pom pomModel
	if err := xml.Unmarshal(data, &pom); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return
	}
	properties := pom.Properties.values()
	managedVersions := make(map[string][]string, len(pom.ManagedDependencies))
	for _, dep := range pom.ManagedDependencies {
		group := resolveMavenVersion(dep.GroupID, properties)
		artifact := resolveMavenVersion(dep.ArtifactID, properties)
		key := mavenCoordinateKey(group, artifact)
		managedVersions[key] = append(managedVersions[key], resolveMavenVersion(dep.Version, properties))
	}
	source := relativeIdentitySource(repoPath, path)
	directCoordinates := make(map[string]struct{}, len(pom.Dependencies))
	for _, dep := range pom.Dependencies {
		group := resolveMavenVersion(dep.GroupID, properties)
		artifact := resolveMavenVersion(dep.ArtifactID, properties)
		key := mavenCoordinateKey(group, artifact)
		directCoordinates[key] = struct{}{}
		version := resolveMavenVersion(dep.Version, properties)
		versions := []string{version}
		if strings.TrimSpace(dep.Version) == "" && len(managedVersions[key]) != 0 {
			versions = managedVersions[key]
		}
		for _, resolvedVersion := range versions {
			addMavenEvidence(index, group, artifact, resolvedVersion, source, identityStatusDeclared)
		}
	}
	for _, dep := range pom.ManagedDependencies {
		group := resolveMavenVersion(dep.GroupID, properties)
		artifact := resolveMavenVersion(dep.ArtifactID, properties)
		if _, ok := directCoordinates[mavenCoordinateKey(group, artifact)]; ok {
			continue
		}
		addMavenEvidence(index, group, artifact, resolveMavenVersion(dep.Version, properties), source, identityStatusDeclared)
	}
}

func mavenCoordinateKey(group, artifact string) string {
	return strings.TrimSpace(group) + "\x00" + strings.TrimSpace(artifact)
}

func collectGradleIdentityEvidence(repoPath, path string, index identityIndex) {
	collectGradleIdentityEvidenceFromPaths(repoPath, index, []string{path}, nil, nil)
}

func collectGradleIdentityEvidenceFromPaths(repoPath string, index identityIndex, buildPaths, lockPaths []string, warnings *identityWarningCollector) {
	sort.Strings(buildPaths)
	sort.Strings(lockPaths)
	declarations := collectGradleDeclarationEvidence(repoPath, index, buildPaths, warnings)
	collectGradleLockIdentityEvidenceFromPaths(repoPath, index, lockPaths, declarations, warnings)
}

func collectGradleDeclarationEvidence(repoPath string, index identityIndex, buildPaths []string, warnings *identityWarningCollector) map[string]map[string]struct{} {
	declarations := make(map[string]map[string]struct{}, len(buildPaths))
	for _, path := range buildPaths {
		data, err := safeio.ReadFileUnder(repoPath, path)
		if err != nil {
			warnings.addFailure("read", path, identityReadFailed, err)
			continue
		}
		source := relativeIdentitySource(repoPath, path)
		projectDir := filepath.ToSlash(filepath.Dir(path))
		for _, coordinate := range shared.ParseGradleDependencyCoordinatesForFile(path, string(data)) {
			addMavenEvidence(index, coordinate.Group, coordinate.Artifact, coordinate.Version, source, identityStatusDeclared)
			if declarations[projectDir] == nil {
				declarations[projectDir] = map[string]struct{}{}
			}
			declarations[projectDir][gradleCoordinateKey(coordinate.Group, coordinate.Artifact)] = struct{}{}
		}
	}
	return declarations
}

func gradleCoordinateKey(group, artifact string) string {
	return strings.TrimSpace(group) + "\x00" + strings.TrimSpace(artifact)
}

var gradleLockPattern = regexp.MustCompile(`^([A-Za-z0-9_.-]+):([A-Za-z0-9_.-]+):([^=\s]+)=`)

func collectGradleLockIdentityEvidence(repoPath, path string, index identityIndex) {
	buildPaths := []string{
		filepath.Join(filepath.Dir(path), "build.gradle"),
		filepath.Join(filepath.Dir(path), "build.gradle.kts"),
	}
	declarations := collectGradleDeclarationEvidence(repoPath, identityIndex{}, buildPaths, nil)
	collectGradleLockIdentityEvidenceFromPaths(repoPath, index, []string{path}, declarations, nil)
}

func collectGradleLockIdentityEvidenceFromPaths(repoPath string, index identityIndex, lockPaths []string, declarations map[string]map[string]struct{}, warnings *identityWarningCollector) {
	for _, path := range lockPaths {
		collectGradleLockIdentityEvidenceFromPath(repoPath, path, index, declarations[filepath.ToSlash(filepath.Dir(path))], warnings)
	}
}

func collectGradleLockIdentityEvidenceFromPath(repoPath, path string, index identityIndex, declarations map[string]struct{}, warnings *identityWarningCollector) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return
	}
	source := relativeIdentitySource(repoPath, path)
	for _, line := range strings.Split(string(data), "\n") {
		matches := gradleLockPattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(matches) != 4 {
			continue
		}
		if len(declarations) == 0 {
			continue
		}
		if _, ok := declarations[gradleCoordinateKey(matches[1], matches[2])]; !ok {
			continue
		}
		addMavenEvidence(index, matches[1], matches[2], matches[3], source, identityStatusResolved)
	}
}

func addMavenEvidence(index identityIndex, group, artifact, version, source, status string) {
	group = strings.TrimSpace(group)
	artifact = strings.TrimSpace(artifact)
	if group == "" || artifact == "" {
		return
	}
	version = sanitizeMavenVersion(version)
	for _, languageID := range []string{"jvm", kotlinAndroidLanguageName} {
		addIdentityEvidence(index, identityEvidence{
			Language:   languageID,
			Ecosystem:  "maven",
			Name:       artifact,
			Namespace:  group,
			Version:    strings.TrimSpace(version),
			Status:     versionStatus(version, status),
			Source:     source,
			Confidence: "high",
		})
	}
}

func (p *pomProperties) values() map[string]string {
	values := make(map[string]string, len(p.Entries))
	for _, entry := range p.Entries {
		name := strings.TrimSpace(entry.XMLName.Local)
		if name == "" {
			continue
		}
		values[name] = strings.TrimSpace(entry.Value)
	}
	return values
}

func resolveMavenVersion(version string, properties map[string]string) string {
	resolved := strings.TrimSpace(version)
	for range 8 {
		key, ok := mavenPropertyKey(resolved)
		if !ok {
			return resolved
		}
		next := strings.TrimSpace(properties[key])
		if next == "" || next == resolved {
			return ""
		}
		resolved = next
	}
	if _, ok := mavenPropertyKey(resolved); ok {
		return ""
	}
	return resolved
}

func sanitizeMavenVersion(version string) string {
	version = strings.TrimSpace(version)
	if _, ok := mavenPropertyKey(version); ok {
		return ""
	}
	return version
}

func mavenPropertyKey(version string) (string, bool) {
	version = strings.TrimSpace(version)
	if strings.HasPrefix(version, "${") && strings.HasSuffix(version, "}") {
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(version, "${"), "}"))
		return key, key != ""
	}
	return "", false
}

func collectSwiftIdentityEvidence(repoPath string, index identityIndex) {
	snapshot := discoverIdentityManifestSnapshot(repoPath, nil)
	collectSwiftIdentityEvidenceFromPaths(repoPath, index, snapshot.swiftFiles, snapshot.podLockFiles, snapshot.carthageLockFiles, nil)
}

func collectSwiftIdentityEvidenceFromPaths(repoPath string, index identityIndex, swiftFiles, podLockFiles, carthageLockFiles []string, warnings *identityWarningCollector) {
	for _, path := range swiftFiles {
		collectSwiftPackageResolvedEvidence(repoPath, path, index, warnings)
	}
	for _, path := range podLockFiles {
		collectPodfileLockEvidence(repoPath, path, index, warnings)
	}
	for _, path := range carthageLockFiles {
		collectCarthageResolvedEvidence(repoPath, path, index, warnings)
	}
}

type swiftResolvedDoc struct {
	Pins []struct {
		Identity string `json:"identity"`
		Package  string `json:"package"`
		Location string `json:"location"`
		State    struct {
			Version string `json:"version"`
		} `json:"state"`
	} `json:"pins"`
	Object struct {
		Pins []struct {
			Identity      string `json:"identity"`
			Package       string `json:"package"`
			RepositoryURL string `json:"repositoryURL"`
			State         struct {
				Version string `json:"version"`
			} `json:"state"`
		} `json:"pins"`
	} `json:"object"`
}

func collectSwiftPackageResolvedEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return
	}
	var doc swiftResolvedDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return
	}
	for _, pin := range doc.Pins {
		name := firstNonBlankString(pin.Identity, pin.Package, deriveSwiftName(pin.Location))
		addSwiftEvidence(index, name, pin.State.Version, "swift", relativeIdentitySource(repoPath, path))
	}
	for _, pin := range doc.Object.Pins {
		name := firstNonBlankString(pin.Identity, pin.Package, deriveSwiftName(pin.RepositoryURL))
		addSwiftEvidence(index, name, pin.State.Version, "swift", relativeIdentitySource(repoPath, path))
	}
}

type podLockDoc struct {
	Pods []any `yaml:"PODS"`
}

func collectPodfileLockEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return
	}
	var doc podLockDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		warnings.addFailure("parse", path, identityParseFailed, err)
		return
	}
	for _, item := range doc.Pods {
		name, version := parsePodLockEntry(item)
		addSwiftEvidence(index, name, version, "cocoapods", relativeIdentitySource(repoPath, path))
	}
}

func collectCarthageResolvedEvidence(repoPath, path string, index identityIndex, warnings *identityWarningCollector) {
	data, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		warnings.addFailure("read", path, identityReadFailed, err)
		return
	}
	source := relativeIdentitySource(repoPath, path)
	for _, identity := range swiftlang.ParseCarthageResolvedIdentities(data) {
		addSwiftEvidence(index, identity.Name, identity.Version, "swift", source)
	}
}

var podLockEntryPattern = regexp.MustCompile(`^\s*([^(\s]+)(?:\s+\(([^)]+)\))?`)

func parsePodLockEntry(item any) (string, string) {
	switch value := item.(type) {
	case string:
		matches := podLockEntryPattern.FindStringSubmatch(value)
		if len(matches) >= 2 {
			version := ""
			if len(matches) >= 3 {
				version = matches[2]
			}
			return strings.TrimSpace(matches[1]), version
		}
	case map[string]any:
		for key := range value {
			return parsePodLockEntry(key)
		}
	}
	return "", ""
}

func addSwiftEvidence(index identityIndex, name, version, ecosystem, source string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	addIdentityEvidence(index, identityEvidence{
		Language:   "swift",
		Ecosystem:  ecosystem,
		Name:       normalizeSwiftName(name),
		Version:    version,
		Status:     versionStatus(version, identityStatusResolved),
		Source:     source,
		Confidence: "high",
	})
}

func deriveSwiftName(value string) string {
	value = strings.TrimSuffix(strings.TrimSpace(value), ".git")
	if value == "" {
		return ""
	}
	return filepath.Base(value)
}

func normalizeSwiftName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func versionStatus(version, populatedStatus string) string {
	if strings.TrimSpace(version) == "" {
		return identityStatusUnknown
	}
	return populatedStatus
}

func shouldSkipIdentityDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".venv", "venv", "target", "build", ".gradle", ".dart_tool":
		return true
	default:
		return false
	}
}

func relativeIdentitySource(repoPath, path string) string {
	rel, err := filepath.Rel(repoPath, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

type identityWarningCollector struct {
	repoPath string
	warnings []string
	seen     map[string]struct{}
}

func newIdentityWarningCollector(repoPath string) *identityWarningCollector {
	return &identityWarningCollector{
		repoPath: repoPath,
		seen:     map[string]struct{}{},
	}
}

func (c *identityWarningCollector) addFailure(kind, path, operation string, err error) {
	if c == nil || err == nil {
		return
	}
	source := relativeIdentitySource(c.repoPath, path)
	c.append(fmt.Sprintf("identity manifest %s for %s: %s", operation, source, identityWarningDetail(kind, path, err)))
}

func (c *identityWarningCollector) addSectionParseFailure(path, section string) {
	if c == nil {
		return
	}
	source := fmt.Sprintf("%s %s section", relativeIdentitySource(c.repoPath, path), strings.TrimSpace(section))
	c.append(fmt.Sprintf("identity manifest parse failed for %s: %s", source, identityParseLabel(path)))
}

func (c *identityWarningCollector) list() []string {
	return sortedUnique(c.warnings)
}

func (c *identityWarningCollector) append(warning string) {
	if c == nil || warning == "" {
		return
	}
	if _, ok := c.seen[warning]; ok {
		return
	}
	c.seen[warning] = struct{}{}
	c.warnings = append(c.warnings, warning)
}

func identityWarningDetail(kind, path string, err error) string {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return "permission denied"
	case errors.Is(err, fs.ErrNotExist):
		return "not found"
	case kind == "parse":
		return identityParseLabel(path)
	default:
		return "I/O error"
	}
}

func identityParseLabel(path string) string {
	switch strings.ToLower(strings.TrimSpace(filepath.Base(path))) {
	case nodePackageManifestFile, packageLockFileName, "package.resolved", "pipfile.lock", composerIdentityLockName:
		return "invalid JSON"
	case pnpmLockFileName, "podfile.lock", pubIdentityLockName:
		return "invalid YAML"
	case "pom.xml", "directory.packages.props":
		return identityInvalidXML
	case poetryLockFileName, uvLockFileName, "pipfile", "cargo.lock":
		return "invalid TOML"
	}
	switch strings.ToLower(strings.TrimSpace(filepath.Ext(path))) {
	case ".json":
		return "invalid JSON"
	case ".yaml", ".yml":
		return "invalid YAML"
	case ".xml":
		return identityInvalidXML
	case ".csproj", ".fsproj":
		return identityInvalidXML
	case ".toml":
		return "invalid TOML"
	default:
		return "invalid manifest"
	}
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	items := append([]string{}, values...)
	sort.Strings(items)
	unique := items[:1]
	for i := 1; i < len(items); i++ {
		if items[i] != items[i-1] {
			unique = append(unique, items[i])
		}
	}
	return unique
}

func firstNonBlankString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func identityPreviewEnabled(req Request) bool {
	return req.Features.Enabled(report.DependencyIdentityPreviewFeature)
}
