package jvm

import (
	"encoding/xml"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
	"github.com/ben-ranford/lopper/internal/safeio"
)

type dependencyDescriptor struct {
	Name     string
	Group    string
	Artifact string
}

type pomProjectModel struct {
	GroupID              string               `xml:"groupId"`
	ArtifactID           string               `xml:"artifactId"`
	Version              string               `xml:"version"`
	Parent               pomParentModel       `xml:"parent"`
	Properties           pomPropertiesModel   `xml:"properties"`
	Dependencies         []pomDependencyModel `xml:"dependencies>dependency"`
	DependencyManagement struct {
		Dependencies []pomDependencyModel `xml:"dependencies>dependency"`
	} `xml:"dependencyManagement"`
}

type pomParentModel struct {
	GroupID string `xml:"groupId"`
	Version string `xml:"version"`
}

type pomPropertiesModel struct {
	Properties []pomPropertyModel `xml:",any"`
}

type pomPropertyModel struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

type pomDependencyModel struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Type       string `xml:"type"`
	Scope      string `xml:"scope"`
}

type pomDependencyKind int

const (
	pomDependencyDirect pomDependencyKind = iota
	pomDependencyManaged
)

var (
	pomPropertyTokenPattern = regexp.MustCompile(`\$\{([^}]+)\}`)
	gradleDependencyPattern = regexp.MustCompile(`(?m)(?:implementation|api|compileOnly|runtimeOnly|testImplementation|testRuntimeOnly|kapt|annotationProcessor|testAnnotationProcessor|testCompileOnly|debugImplementation|releaseImplementation|kaptTest|kaptAndroidTest|classpath)\s*\(?\s*["']([^:"'\s]+):([^:"'\s]+):[^"'\s]+["']\s*\)?`)
)

func collectDeclaredDependencies(repoPath string) ([]dependencyDescriptor, map[string]string, map[string]string, []string) {
	descriptors := make([]dependencyDescriptor, 0)
	warnings := make([]string, 0)

	pomDescriptors, pomWarnings := parsePomDependenciesWithWarnings(repoPath)
	gradleDescriptors, gradleWarnings := parseGradleDependenciesWithWarnings(repoPath)
	descriptors = append(descriptors, pomDescriptors...)
	descriptors = append(descriptors, gradleDescriptors...)
	warnings = append(warnings, pomWarnings...)
	warnings = append(warnings, gradleWarnings...)

	descriptors = dedupeAndSortDescriptors(descriptors)
	prefixes, aliases := buildDescriptorLookups(descriptors)
	return descriptors, prefixes, aliases, warnings
}

func dedupeAndSortDescriptors(descriptors []dependencyDescriptor) []dependencyDescriptor {
	unique := make(map[string]dependencyDescriptor)
	for _, descriptor := range descriptors {
		key := descriptor.Group + ":" + descriptor.Artifact
		if descriptor.Group == "" {
			key = descriptor.Name
		}
		unique[key] = descriptor
	}
	items := make([]dependencyDescriptor, 0, len(unique))
	for _, descriptor := range unique {
		items = append(items, descriptor)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].Group < items[j].Group
		}
		return items[i].Name < items[j].Name
	})
	return items
}

func buildDescriptorLookups(descriptors []dependencyDescriptor) (map[string]string, map[string]string) {
	prefixes := make(map[string]string)
	aliases := make(map[string]string)
	for _, descriptor := range descriptors {
		name := normalizeDependencyID(descriptor.Name)
		addGroupLookups(prefixes, aliases, name, descriptor.Group)
		addArtifactLookups(prefixes, aliases, name, descriptor.Group, descriptor.Artifact)
	}
	return prefixes, aliases
}

type lookupKeyStrategy func(group string, artifact string) ([]string, []string)

func addGroupLookups(prefixes map[string]string, aliases map[string]string, name string, group string) {
	addLookupByStrategy(prefixes, aliases, name, group, "", groupLookupStrategy)
}

func addArtifactLookups(prefixes map[string]string, aliases map[string]string, name string, group string, artifact string) {
	addLookupByStrategy(prefixes, aliases, name, group, artifact, artifactLookupStrategy)
}

func addLookupByStrategy(prefixes map[string]string, aliases map[string]string, name string, group string, artifact string, strategy lookupKeyStrategy) {
	prefixKeys, aliasKeys := strategy(group, artifact)
	for _, key := range prefixKeys {
		prefixes[key] = name
	}
	for _, key := range aliasKeys {
		aliases[key] = name
	}
}

func groupLookupStrategy(group, _ string) ([]string, []string) {
	if group == "" {
		return nil, nil
	}
	group = strings.TrimSpace(group)
	prefixes := []string{group}
	aliases := []string{group}
	parts := strings.Split(group, ".")
	if len(parts) >= 2 {
		aliases = append(aliases, parts[0]+"."+parts[1], parts[len(parts)-1])
	}
	return prefixes, aliases
}

func artifactLookupStrategy(group, artifact string) ([]string, []string) {
	if artifact == "" {
		return nil, nil
	}
	artifact = strings.ReplaceAll(strings.TrimSpace(artifact), "-", ".")
	prefixes := make([]string, 0, 1)
	aliases := make([]string, 0, 1)
	if group != "" && artifact != "" {
		prefixes = append(prefixes, group+"."+artifact)
	}
	if artifact != "" {
		aliases = append(aliases, artifact)
	}
	return prefixes, aliases
}

func parsePomDependencies(repoPath string) []dependencyDescriptor {
	descriptors, _ := parsePomDependenciesWithWarnings(repoPath)
	return descriptors
}

func parsePomDependenciesWithWarnings(repoPath string) ([]dependencyDescriptor, []string) {
	pomParser := func(path, content string) ([]dependencyDescriptor, []string) {
		return parsePomDependencyContent(relativeBuildFilePath(repoPath, path), content)
	}
	return parseBuildFilesWithWarnings(repoPath, pomParser, pomXMLName)
}

func parsePomDependencyContent(relativePath, content string) ([]dependencyDescriptor, []string) {
	var project pomProjectModel
	if err := xml.Unmarshal([]byte(content), &project); err != nil {
		return nil, []string{fmt.Sprintf("unable to parse Maven POM %s: %v", relativePath, err)}
	}

	propertyMap := buildPomPropertyMap(project)
	directDescriptors, directWarnings := parsePomDependencyList(project.Dependencies, propertyMap, pomDependencyDirect, relativePath)
	managedDescriptors, managedWarnings := parsePomDependencyList(project.DependencyManagement.Dependencies, propertyMap, pomDependencyManaged, relativePath)

	descriptors := make([]dependencyDescriptor, 0, len(directDescriptors)+len(managedDescriptors))
	descriptors = append(descriptors, directDescriptors...)
	descriptors = append(descriptors, managedDescriptors...)

	warnings := make([]string, 0, len(directWarnings)+len(managedWarnings))
	warnings = append(warnings, directWarnings...)
	warnings = append(warnings, managedWarnings...)

	return dedupeAndSortDescriptors(descriptors), shared.DedupeWarnings(warnings)
}

func parsePomDependencyList(dependencies []pomDependencyModel, propertyMap map[string]string, kind pomDependencyKind, relativePath string) ([]dependencyDescriptor, []string) {
	descriptors := make([]dependencyDescriptor, 0, len(dependencies))
	warnings := make([]string, 0)
	for _, dependency := range dependencies {
		descriptor, warning := parsePomDependency(dependency, propertyMap, kind, relativePath)
		if descriptor.Group != "" && descriptor.Artifact != "" {
			descriptors = append(descriptors, descriptor)
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	return descriptors, warnings
}

func parsePomDependency(dependency pomDependencyModel, propertyMap map[string]string, kind pomDependencyKind, relativePath string) (dependencyDescriptor, string) {
	group, unresolvedGroup := resolvePomPropertyValue(dependency.GroupID, propertyMap)
	artifact, unresolvedArtifact := resolvePomPropertyValue(dependency.ArtifactID, propertyMap)
	if unresolvedGroup || unresolvedArtifact || group == "" || artifact == "" {
		return dependencyDescriptor{}, ""
	}

	descriptor := dependencyDescriptor{
		Name:     artifact,
		Group:    group,
		Artifact: artifact,
	}
	if kind != pomDependencyManaged {
		return descriptor, ""
	}

	version, unresolvedVersion := resolvePomPropertyValue(dependency.Version, propertyMap)
	if !isPomImportedBOM(dependency) {
		if version == "" || unresolvedVersion {
			return descriptor, fmt.Sprintf("unable to resolve managed Maven version for %s:%s in %s", group, artifact, relativePath)
		}
		return descriptor, ""
	}

	if version == "" || unresolvedVersion {
		return descriptor, fmt.Sprintf("unable to resolve imported Maven BOM version for %s:%s in %s", group, artifact, relativePath)
	}
	return descriptor, ""
}

func isPomImportedBOM(dependency pomDependencyModel) bool {
	return strings.EqualFold(strings.TrimSpace(dependency.Type), "pom") &&
		strings.EqualFold(strings.TrimSpace(dependency.Scope), "import")
}

func buildPomPropertyMap(project pomProjectModel) map[string]string {
	properties := make(map[string]string)
	for _, property := range project.Properties.Properties {
		key := strings.TrimSpace(property.XMLName.Local)
		value := strings.TrimSpace(property.Value)
		if key == "" || value == "" {
			continue
		}
		properties[key] = value
	}

	groupID := strings.TrimSpace(project.GroupID)
	if groupID == "" {
		groupID = strings.TrimSpace(project.Parent.GroupID)
	}
	version := strings.TrimSpace(project.Version)
	if version == "" {
		version = strings.TrimSpace(project.Parent.Version)
	}
	artifactID := strings.TrimSpace(project.ArtifactID)

	setPomPropertyValue(properties, "project.groupId", groupID)
	setPomPropertyValue(properties, "pom.groupId", groupID)
	setPomPropertyValue(properties, "groupId", groupID)

	setPomPropertyValue(properties, "project.version", version)
	setPomPropertyValue(properties, "pom.version", version)
	setPomPropertyValue(properties, "version", version)

	setPomPropertyValue(properties, "project.artifactId", artifactID)
	setPomPropertyValue(properties, "pom.artifactId", artifactID)
	setPomPropertyValue(properties, "artifactId", artifactID)

	setPomPropertyValue(properties, "project.parent.groupId", strings.TrimSpace(project.Parent.GroupID))
	setPomPropertyValue(properties, "project.parent.version", strings.TrimSpace(project.Parent.Version))
	return properties
}

func setPomPropertyValue(properties map[string]string, key, value string) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	properties[key] = value
}

func resolvePomPropertyValue(value string, properties map[string]string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	unresolved := false
	for iteration := 0; iteration < 8; iteration++ {
		updated, replaced, missing := replacePomPropertyTokens(value, properties)
		unresolved = unresolved || missing
		if !replaced {
			break
		}
		value = updated
	}
	if pomPropertyTokenPattern.MatchString(value) {
		unresolved = true
	}
	return strings.TrimSpace(value), unresolved
}

func replacePomPropertyTokens(value string, properties map[string]string) (string, bool, bool) {
	matches := pomPropertyTokenPattern.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return value, false, false
	}

	updated := value
	replaced := false
	unresolved := false
	for _, match := range matches {
		token, replacement, ok := pomPropertyReplacement(match, properties)
		if !ok {
			unresolved = unresolved || len(match) == 2
			continue
		}
		updated = strings.ReplaceAll(updated, token, replacement)
		replaced = true
	}
	return updated, replaced, unresolved
}

func pomPropertyReplacement(match []string, properties map[string]string) (string, string, bool) {
	if len(match) != 2 {
		return "", "", false
	}

	key := strings.TrimSpace(match[1])
	replacement, ok := properties[key]
	if !ok {
		return match[0], "", false
	}

	replacement = strings.TrimSpace(replacement)
	if replacement == "" {
		return match[0], "", false
	}
	return match[0], replacement, true
}

func parseGradleDependencies(repoPath string) []dependencyDescriptor {
	descriptors, _ := parseGradleDependenciesWithWarnings(repoPath)
	return descriptors
}

func parseGradleDependenciesWithWarnings(repoPath string) ([]dependencyDescriptor, []string) {
	catalogResolver, warnings := shared.LoadGradleCatalogResolver(repoPath)
	gradleParser := func(path, content string) ([]dependencyDescriptor, []string) {
		descriptors := parseGradleMatches(content, gradleDependencyPattern)
		catalogDescriptors, catalogWarnings := catalogResolver.ParseDependencyReferences(path, content)
		for _, descriptor := range catalogDescriptors {
			descriptors = append(descriptors, dependencyDescriptor{
				Name:     descriptor.Artifact,
				Group:    descriptor.Group,
				Artifact: descriptor.Artifact,
			})
		}
		return dedupeAndSortDescriptors(descriptors), catalogWarnings
	}
	descriptors, parseWarnings := parseBuildFilesWithWarnings(repoPath, gradleParser, buildGradleName, buildGradleKTSName)
	warnings = append(warnings, parseWarnings...)
	return descriptors, shared.DedupeWarnings(warnings)
}

func parseGradleMatches(content string, pattern *regexp.Regexp) []dependencyDescriptor {
	return parseDependencyDescriptorsFromMatches(pattern.FindAllStringSubmatch(content, -1))
}

func parseDependencyDescriptorsFromMatches(matches [][]string) []dependencyDescriptor {
	descriptors := make([]dependencyDescriptor, 0, len(matches))
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		group := strings.TrimSpace(match[1])
		artifact := strings.TrimSpace(match[2])
		if group == "" || artifact == "" {
			continue
		}
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     artifact,
			Group:    group,
			Artifact: artifact,
		})
	}
	return descriptors
}

func parseBuildFiles(repoPath string, primaryName string, parser func(content string) []dependencyDescriptor, additionalNames ...string) []dependencyDescriptor {
	names := append([]string{primaryName}, additionalNames...)
	descriptors := make([]dependencyDescriptor, 0)
	seen := make(map[string]struct{})

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return parseBuildFileEntry(repoPath, path, entry, names, parser, seen, &descriptors)
	})
	if err != nil {
		return descriptors
	}
	return descriptors
}

func parseBuildFilesWithWarnings(repoPath string, parser func(path, content string) ([]dependencyDescriptor, []string), names ...string) ([]dependencyDescriptor, []string) {
	collector := buildFileWarningCollector{
		repoPath: repoPath,
		parser:   parser,
		names:    names,
		seen:     make(map[string]struct{}),
	}
	err := filepath.WalkDir(repoPath, collector.visit)
	if err != nil {
		collector.warnings = append(collector.warnings, err.Error())
	}
	return collector.descriptors, shared.DedupeWarnings(collector.warnings)
}

func parseBuildFileEntry(repoPath string, path string, entry fs.DirEntry, names []string, parser func(content string) []dependencyDescriptor, seen map[string]struct{}, descriptors *[]dependencyDescriptor) error {
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	fileName := strings.ToLower(entry.Name())
	if !matchesBuildFile(fileName, names) {
		return nil
	}

	content, err := safeio.ReadFileUnder(repoPath, path)
	if err != nil {
		return nil
	}
	for _, descriptor := range parser(string(content)) {
		key := descriptor.Group + ":" + descriptor.Artifact
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		*descriptors = append(*descriptors, descriptor)
	}
	return nil
}

type buildFileWarningCollector struct {
	repoPath    string
	parser      func(path, content string) ([]dependencyDescriptor, []string)
	names       []string
	seen        map[string]struct{}
	descriptors []dependencyDescriptor
	warnings    []string
}

func (c *buildFileWarningCollector) visit(path string, entry fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if entry.IsDir() {
		if shouldSkipDir(entry.Name()) {
			return filepath.SkipDir
		}
		return nil
	}
	if !matchesBuildFile(strings.ToLower(entry.Name()), c.names) {
		return nil
	}
	content, readErr := safeio.ReadFileUnder(c.repoPath, path)
	if readErr != nil {
		c.warnings = append(c.warnings, formatBuildFileReadWarning(c.repoPath, path, readErr))
		return nil
	}
	items, parseWarnings := c.parser(path, string(content))
	c.warnings = append(c.warnings, parseWarnings...)
	for _, descriptor := range items {
		key := descriptor.Group + ":" + descriptor.Artifact
		if _, ok := c.seen[key]; ok {
			continue
		}
		c.seen[key] = struct{}{}
		c.descriptors = append(c.descriptors, descriptor)
	}
	return nil
}

func formatBuildFileReadWarning(repoPath, path string, err error) string {
	return "unable to read " + relativeBuildFilePath(repoPath, path) + ": " + err.Error()
}

func relativeBuildFilePath(repoPath, path string) string {
	relPath := path
	if rel, relErr := filepath.Rel(repoPath, path); relErr == nil {
		relPath = rel
	}
	return filepath.ToSlash(relPath)
}

func matchesBuildFile(fileName string, names []string) bool {
	for _, name := range names {
		if strings.EqualFold(fileName, name) {
			return true
		}
	}
	return false
}
