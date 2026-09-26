package jvm

import (
	"context"
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
)

func collectDeclaredDependencies(repoPath string) ([]dependencyDescriptor, map[string]string, map[string]string, []string) {
	descriptors, warnings := collectBuildDescriptors(repoPath)
	descriptors = dedupeAndSortDescriptors(descriptors)
	prefixes, aliases := buildDescriptorLookups(descriptors)
	return descriptors, prefixes, aliases, warnings
}

func collectDeclaredDependenciesWithinRoot(ctx context.Context, repoPath string, root safeio.Root) ([]dependencyDescriptor, map[string]string, map[string]string, []string, error) {
	descriptors, warnings, err := collectBuildDescriptorsWithinRoot(ctx, repoPath, root)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	descriptors = dedupeAndSortDescriptors(descriptors)
	prefixes, aliases := buildDescriptorLookups(descriptors)
	return descriptors, prefixes, aliases, warnings, nil
}

func collectBuildDescriptors(repoPath string) ([]dependencyDescriptor, []string) {
	catalogResolver, warnings := shared.LoadGradleCatalogResolver(repoPath)
	buildParser := func(path, content string) ([]dependencyDescriptor, []string) {
		switch strings.ToLower(filepath.Base(path)) {
		case pomXMLName:
			return parsePomDependencyContent(relativeBuildFilePath(repoPath, path), content)
		case buildGradleName, buildGradleKTSName:
			descriptors := parseGradleDependencyContent(path, content)
			catalogDescriptors, catalogWarnings := catalogResolver.ParseDependencyReferences(path, content)
			for _, descriptor := range catalogDescriptors {
				descriptors = append(descriptors, dependencyDescriptor{
					Name:     descriptor.Artifact,
					Group:    descriptor.Group,
					Artifact: descriptor.Artifact,
				})
			}
			return dedupeAndSortDescriptors(descriptors), catalogWarnings
		default:
			return nil, nil
		}
	}

	descriptors, parseWarnings := parseBuildFilesWithWarnings(repoPath, buildParser, pomXMLName, buildGradleName, buildGradleKTSName)
	warnings = append(warnings, parseWarnings...)
	return descriptors, shared.DedupeWarnings(warnings)
}

func collectBuildDescriptorsWithinRoot(ctx context.Context, repoPath string, root safeio.Root) ([]dependencyDescriptor, []string, error) {
	catalogResolver, warnings, err := shared.LoadGradleCatalogResolverWithinRoot(ctx, repoPath, root)
	if err != nil {
		return nil, nil, err
	}
	buildParser := func(path, content string) ([]dependencyDescriptor, []string) {
		switch strings.ToLower(filepath.Base(path)) {
		case pomXMLName:
			return parsePomDependencyContent(relativeBuildFilePath(repoPath, path), content)
		case buildGradleName, buildGradleKTSName:
			descriptors := parseGradleDependencyContent(path, content)
			catalogDescriptors, catalogWarnings := catalogResolver.ParseDependencyReferences(path, content)
			for _, descriptor := range catalogDescriptors {
				descriptors = append(descriptors, dependencyDescriptor{
					Name:     descriptor.Artifact,
					Group:    descriptor.Group,
					Artifact: descriptor.Artifact,
				})
			}
			return dedupeAndSortDescriptors(descriptors), catalogWarnings
		default:
			return nil, nil
		}
	}

	descriptors, parseWarnings, err := parseBuildFilesWithWarningsWithinRoot(ctx, repoPath, root, buildParser, pomXMLName, buildGradleName, buildGradleKTSName)
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, parseWarnings...)
	return descriptors, shared.DedupeWarnings(warnings), nil
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
	budget := newPomExpansionBudget()
	directDescriptors, directWarnings := parsePomDependencyList(project.Dependencies, propertyMap, pomDependencyDirect, relativePath, budget)
	managedDescriptors, managedWarnings := parsePomDependencyList(project.DependencyManagement.Dependencies, propertyMap, pomDependencyManaged, relativePath, budget)

	descriptors := make([]dependencyDescriptor, 0, len(directDescriptors)+len(managedDescriptors))
	descriptors = append(descriptors, directDescriptors...)
	descriptors = append(descriptors, managedDescriptors...)

	warnings := make([]string, 0, len(directWarnings)+len(managedWarnings))
	warnings = append(warnings, directWarnings...)
	warnings = append(warnings, managedWarnings...)

	return dedupeAndSortDescriptors(descriptors), shared.DedupeWarnings(warnings)
}

func parsePomDependencyList(dependencies []pomDependencyModel, propertyMap map[string]string, kind pomDependencyKind, relativePath string, budget *pomExpansionBudget) ([]dependencyDescriptor, []string) {
	descriptors := make([]dependencyDescriptor, 0, len(dependencies))
	warnings := make([]string, 0)
	for _, dependency := range dependencies {
		descriptor, warning := parsePomDependencyWithBudget(dependency, propertyMap, kind, relativePath, budget)
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
	return parsePomDependencyWithBudget(dependency, propertyMap, kind, relativePath, newPomExpansionBudget())
}

func parsePomDependencyWithBudget(dependency pomDependencyModel, propertyMap map[string]string, kind pomDependencyKind, relativePath string, budget *pomExpansionBudget) (dependencyDescriptor, string) {
	group, unresolvedGroup := budget.resolve(dependency.GroupID, propertyMap)
	artifact, unresolvedArtifact := budget.resolve(dependency.ArtifactID, propertyMap)
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

	version, unresolvedVersion := budget.resolve(dependency.Version, propertyMap)
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

const (
	maxPomPropertyValueBytes = 64 * 1024
	maxPomPropertyTokens     = 1024
	maxPomExpansionBytes     = 8 * 1024 * 1024
	maxPomExpansionTokens    = 128 * 1024
)

type pomExpansionBudget struct {
	bytesRemaining  int
	tokensRemaining int
}

func newPomExpansionBudget() *pomExpansionBudget {
	return &pomExpansionBudget{maxPomExpansionBytes, maxPomExpansionTokens}
}

func resolvePomPropertyValue(value string, properties map[string]string) (string, bool) {
	return newPomExpansionBudget().resolve(value, properties)
}

func (b *pomExpansionBudget) resolve(value string, properties map[string]string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	unresolved := false
	tokensRemaining := maxPomPropertyTokens
	for iteration := 0; iteration < 8; iteration++ {
		if b.bytesRemaining < maxPomPropertyValueBytes || b.tokensRemaining < maxPomPropertyTokens {
			return "", true
		}
		updated, replaced, missing, tokensUsed := replacePomPropertyTokens(value, properties, tokensRemaining)
		// Charge failed attempts too: a rejected pass may have built a full value.
		chargedBytes := len(updated)
		if updated == "" && missing {
			chargedBytes = maxPomPropertyValueBytes
			tokensUsed = maxPomPropertyTokens
		}
		b.bytesRemaining -= chargedBytes
		b.tokensRemaining -= tokensUsed
		unresolved = unresolved || missing
		if updated == "" && missing {
			return "", true
		}
		value = updated
		tokensRemaining -= tokensUsed
		if !replaced {
			break
		}
	}
	if pomPropertyTokenPattern.MatchString(value) {
		unresolved = true
	}
	return strings.TrimSpace(value), unresolved
}

func replacePomPropertyTokens(value string, properties map[string]string, tokensRemaining int) (string, bool, bool, int) {
	if len(value) > maxPomPropertyValueBytes {
		return "", false, true, 0
	}
	return replacePomPropertyTokensWithinBounds(value, properties, tokensRemaining)
}

func replacePomPropertyTokensWithinBounds(value string, properties map[string]string, tokensRemaining int) (string, bool, bool, int) {
	var tokens [maxPomPropertyTokens]string
	matches := tokens[:0]
	for search := 0; search < len(value); {
		start, end, next, found := nextPomPropertyToken(value, search)
		if !found {
			break
		}
		search = next
		if end-start == 3 {
			continue
		}
		if len(matches) == maxPomPropertyTokens {
			return "", false, true, 0
		}
		matches = append(matches, value[start:end])
	}
	stages := make([]pomTokenStage, len(matches))
	for index, token := range matches {
		stages[index] = pomTokenStage{token: token, index: index}
	}
	sort.Slice(stages, func(left, right int) bool {
		if stages[left].token == stages[right].token {
			return stages[left].index < stages[right].index
		}
		return stages[left].token < stages[right].token
	})
	positions := make(map[string]pomStageRange, len(stages))
	for index, stage := range stages {
		position := positions[stage.token]
		if position.count == 0 {
			position.start = index
		}
		position.count++
		positions[stage.token] = position
	}
	expansion := pomPropertyExpansion{properties: properties, order: matches, stages: stages, positions: positions, tokensRemaining: tokensRemaining}
	if !expansion.appendValue(value, 0) {
		return "", false, true, 0
	}
	return expansion.updated.String(), expansion.tokensUsed != 0, expansion.unresolved, expansion.tokensUsed
}

func nextPomPropertyToken(value string, search int) (int, int, int, bool) {
	open := strings.Index(value[search:], "${")
	if open < 0 {
		return 0, 0, 0, false
	}
	start := search + open
	keyStart := start + 2
	closingBrace := strings.IndexByte(value[keyStart:], '}')
	if closingBrace < 0 {
		return 0, 0, 0, false
	}
	end := keyStart + closingBrace + 1
	return start, end, end, true
}

// Each original token schedules one global replacement. Introduced tokens can
// participate only in later scheduled replacements, preserving Maven's ordered
// passes without rebuilding the whole value for every token.
type pomPropertyExpansion struct {
	properties      map[string]string
	order           []string
	stages          []pomTokenStage
	positions       map[string]pomStageRange
	updated         strings.Builder
	tokensRemaining int
	tokensUsed      int
	unresolved      bool
}

type pomTokenStage struct {
	token string
	index int
}

type pomStageRange struct {
	start int
	count int
}

func (e *pomPropertyExpansion) appendLiteral(value string) bool {
	if len(value) > maxPomPropertyValueBytes-e.updated.Len() {
		return false
	}
	e.updated.WriteString(value)
	return true
}

func (e *pomPropertyExpansion) appendValue(value string, first int) bool {
	for search := 0; search < len(value); {
		start, end, next, found := nextPomPropertyToken(value, search)
		if !found {
			return e.appendLiteral(value[search:])
		}
		if !e.appendLiteral(value[search:start]) {
			return false
		}
		token := value[start:end]
		stage := e.nextStage(token, first)
		if !e.appendToken(token, stage) {
			return false
		}
		search = next
	}
	return true
}

func (e *pomPropertyExpansion) nextStage(token string, first int) int {
	position, ok := e.positions[token]
	if !ok {
		return len(e.order)
	}
	start := position.start
	end := start + position.count
	index := sort.Search(position.count, func(offset int) bool { return e.stages[start+offset].index >= first })
	if start+index == end {
		return len(e.order)
	}
	return e.stages[start+index].index
}

func (e *pomPropertyExpansion) appendToken(token string, stage int) bool {
	if stage == len(e.order) {
		return e.appendLiteral(token)
	}
	replacement, ok := pomPropertyValue(token[2:len(token)-1], e.properties)
	if !ok {
		e.unresolved = true
		return e.appendLiteral(token)
	}
	if e.tokensUsed == e.tokensRemaining || len(replacement) > maxPomPropertyValueBytes {
		return false
	}
	e.tokensUsed++
	return e.appendValue(replacement, stage+1)
}

func pomPropertyReplacement(match []string, properties map[string]string) (string, string, bool) {
	if len(match) != 2 {
		return "", "", false
	}

	key := strings.TrimSpace(match[1])
	replacement, ok := pomPropertyValue(key, properties)
	if !ok {
		return match[0], "", false
	}
	return match[0], replacement, true
}

func pomPropertyValue(key string, properties map[string]string) (string, bool) {
	key = strings.TrimSpace(key)
	replacement, ok := properties[key]
	if !ok {
		return "", false
	}

	replacement = strings.TrimSpace(replacement)
	if replacement == "" {
		return "", false
	}
	return replacement, true
}

func parseGradleDependencies(repoPath string) []dependencyDescriptor {
	descriptors, _ := parseGradleDependenciesWithWarnings(repoPath)
	return descriptors
}

func parseGradleDependenciesWithWarnings(repoPath string) ([]dependencyDescriptor, []string) {
	catalogResolver, warnings := shared.LoadGradleCatalogResolver(repoPath)
	gradleParser := func(path, content string) ([]dependencyDescriptor, []string) {
		descriptors := parseGradleDependencyContent(path, content)
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

func parseGradleDependencyContent(path, content string) []dependencyDescriptor {
	coordinates := shared.ParseGradleDependencyCoordinatesForFile(path, content)
	descriptors := make([]dependencyDescriptor, 0, len(coordinates))
	for _, coordinate := range coordinates {
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     coordinate.Artifact,
			Group:    coordinate.Group,
			Artifact: coordinate.Artifact,
		})
	}
	return descriptors
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

func parseBuildFilesWithWarningsWithinRoot(ctx context.Context, repoPath string, root safeio.Root, parser func(path, content string) ([]dependencyDescriptor, []string), names ...string) ([]dependencyDescriptor, []string, error) {
	collector := buildFileWarningCollector{
		repoPath: repoPath,
		parser:   parser,
		names:    names,
		seen:     make(map[string]struct{}),
	}
	budget := shared.RootedWalkBudget{
		MaxTraversalEntries: maxJVMBuildTraversalEntries,
		MaxFiles:            maxJVMBuildFiles,
		MaxWorkItems:        maxJVMBuildWorkItems,
		CountCandidate: func(path string, entry fs.DirEntry) bool {
			return matchesBuildFile(strings.ToLower(entry.Name()), names)
		},
	}
	err := shared.WalkRepoFilesWithinRootPinned(ctx, repoPath, root, budget, shouldSkipDir, func(file shared.RootedWalkFile) error {
		return collector.visitWithinRoot(file.Parent, file.Leaf, file.Path, file.Entry)
	})
	if err != nil {
		if warning, limited := shared.RootedWalkBudgetWarning("JVM build file scan", budget, err); limited {
			collector.warnings = append(collector.warnings, warning)
		} else {
			return nil, nil, err
		}
	}
	return collector.descriptors, shared.DedupeWarnings(collector.warnings), nil
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

	content, err := safeio.ReadFileUnderLimit(repoPath, path, maxScannableJVMBuildFile)
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
	content, readErr := safeio.ReadFileUnderLimit(c.repoPath, path, maxScannableJVMBuildFile)
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

func (c *buildFileWarningCollector) visitWithinRoot(parent safeio.Root, leaf, path string, entry fs.DirEntry) error {
	if !matchesBuildFile(strings.ToLower(entry.Name()), c.names) {
		return nil
	}
	content, readErr := safeio.ReadFileWithinRootLimit(parent, leaf, maxScannableJVMBuildFile)
	if readErr != nil {
		if !isPureJVMBuildFileReadWarning(readErr) {
			return readErr
		}
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

func isPureJVMBuildFileReadWarning(err error) bool {
	allowed := []error{
		safeio.ErrFileTooLarge,
		safeio.ErrTargetPathSymlink,
		safeio.ErrPathEscapesRoot,
		fs.ErrNotExist,
		fs.ErrPermission,
	}
	return shared.IsPureSentinelError(err, allowed...)
}

func readJVMBuildFileWithinRoot(root safeio.Root, repoPath, path string) ([]byte, error) {
	relativePath, err := filepath.Rel(repoPath, path)
	if err != nil {
		return nil, err
	}
	return safeio.ReadFileWithinRootLimit(root, relativePath, maxScannableJVMBuildFile)
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
