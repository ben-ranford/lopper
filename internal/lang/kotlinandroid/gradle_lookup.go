package kotlinandroid

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
)

type dependencyDescriptor struct {
	Name         string
	Group        string
	Artifact     string
	Version      string
	FromManifest bool
	FromLockfile bool
}

type dependencyLookups struct {
	Prefixes             map[string]string
	Aliases              map[string]string
	Ambiguous            map[string][]string
	DeclaredDependencies map[string]struct{}
	HasLockfile          bool
}

func collectDeclaredDependencies(repoPath string) ([]dependencyDescriptor, dependencyLookups, []string) {
	manifestDescriptors, manifestWarnings := parseGradleDependenciesWithWarnings(repoPath)
	lockfileDescriptors, hasLockfile, lockWarnings := parseGradleLockfiles(repoPath)

	descriptors := mergeDescriptors(manifestDescriptors, lockfileDescriptors)
	lookups := buildDescriptorLookups(descriptors)
	lookups.HasLockfile = hasLockfile
	return descriptors, lookups, append(manifestWarnings, lockWarnings...)
}

func mergeDescriptors(manifest, lockfile []dependencyDescriptor) []dependencyDescriptor {
	items := make(map[string]dependencyDescriptor)
	for _, descriptor := range manifest {
		key := descriptorKey(descriptor)
		descriptor.FromManifest = true
		items[key] = descriptor
	}
	for _, descriptor := range lockfile {
		key := descriptorKey(descriptor)
		descriptor.FromLockfile = true
		current, ok := items[key]
		if ok {
			current.FromLockfile = true
			if current.Version == "" {
				current.Version = descriptor.Version
			}
			items[key] = current
			continue
		}
		items[key] = descriptor
	}

	merged := make([]dependencyDescriptor, 0, len(items))
	for _, descriptor := range items {
		merged = append(merged, descriptor)
	}
	sort.Slice(merged, func(i, j int) bool {
		return compareDependencyDescriptors(merged[i], merged[j]) < 0
	})
	return merged
}

func compareDependencyDescriptors(left, right dependencyDescriptor) int {
	if cmp := strings.Compare(left.Name, right.Name); cmp != 0 {
		return cmp
	}
	if cmp := strings.Compare(left.Group, right.Group); cmp != 0 {
		return cmp
	}
	return strings.Compare(left.Artifact, right.Artifact)
}

func descriptorKey(descriptor dependencyDescriptor) string {
	if descriptor.Group == "" {
		return descriptor.Name
	}
	return descriptor.Group + ":" + descriptor.Artifact
}

func buildDescriptorLookups(descriptors []dependencyDescriptor) dependencyLookups {
	lookups := dependencyLookups{
		Prefixes:             make(map[string]string),
		Aliases:              make(map[string]string),
		Ambiguous:            make(map[string][]string),
		DeclaredDependencies: make(map[string]struct{}),
	}
	for _, descriptor := range descriptors {
		name := normalizeDependencyID(descriptor.Name)
		lookups.DeclaredDependencies[name] = struct{}{}
		addGroupLookups(lookups, name, descriptor.Group)
		addArtifactLookups(lookups, name, descriptor.Group, descriptor.Artifact)
	}
	return lookups
}

type lookupKeyStrategy func(group string, artifact string) ([]string, []string)

func addGroupLookups(lookups dependencyLookups, name string, group string) {
	addLookupByStrategy(lookups, name, group, "", groupLookupStrategy)
}

func addArtifactLookups(lookups dependencyLookups, name string, group string, artifact string) {
	addLookupByStrategy(lookups, name, group, artifact, artifactLookupStrategy)
}

func addLookupByStrategy(lookups dependencyLookups, name string, group string, artifact string, strategy lookupKeyStrategy) {
	prefixKeys, aliasKeys := strategy(group, artifact)
	for _, key := range prefixKeys {
		recordLookup(lookups.Prefixes, lookups.Ambiguous, key, name)
	}
	for _, key := range aliasKeys {
		recordLookup(lookups.Aliases, lookups.Ambiguous, key, name)
	}
}

func recordLookup(target map[string]string, ambiguous map[string][]string, key string, value string) {
	if key == "" {
		return
	}
	if existing, ok := target[key]; ok {
		if existing == value {
			return
		}
		merged := append([]string{existing, value}, ambiguous[key]...)
		ambiguous[key] = uniqueSortedStrings(merged)
		return
	}
	target[key] = value
}

func uniqueSortedStrings(values []string) []string {
	set := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		set[value] = struct{}{}
	}
	items := make([]string, 0, len(set))
	for value := range set {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func groupLookupStrategy(group, _ string) ([]string, []string) {
	group = strings.TrimSpace(group)
	if group == "" {
		return nil, nil
	}
	aliasSet := map[string]struct{}{group: {}}
	parts := strings.Split(group, ".")
	if len(parts) >= 2 {
		aliasSet[parts[0]+"."+parts[1]] = struct{}{}
	}
	if len(parts) > 0 {
		aliasSet[parts[len(parts)-1]] = struct{}{}
	}
	aliases := make([]string, 0, len(aliasSet))
	for alias := range aliasSet {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return []string{group}, aliases
}

func artifactLookupStrategy(group, artifact string) ([]string, []string) {
	artifact = strings.ReplaceAll(strings.TrimSpace(artifact), "-", ".")
	if artifact == "" {
		return nil, nil
	}
	group = strings.TrimSpace(group)
	aliases := []string{artifact}
	if group == "" {
		return nil, aliases
	}
	return []string{group + "." + artifact}, aliases
}

var gradleCoordinatePattern = regexp.MustCompile(`(?m)\b(?:implementation|api|compileOnly|runtimeOnly|kapt|ksp|testImplementation|androidTestImplementation|testRuntimeOnly)\s*\(?\s*(?:platform\()?["']([^:"'\s]+):([^:"'\s]+)(?::([^"'\s]+))?["']\s*\)?\s*\)?`)

var gradleMapInvocationPattern = regexp.MustCompile(`(?ms)\b(?:implementation|api|compileOnly|runtimeOnly|kapt|ksp|testImplementation|androidTestImplementation|testRuntimeOnly)\s*\(?\s*((?:[A-Za-z_][A-Za-z0-9_]*\s*[:=]\s*["'][^"'\n]+["']\s*,?\s*)+)`)

var gradleNamedArgPattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*[:=]\s*["']([^"']+)["']`)

func parseGradleDependencies(repoPath string) []dependencyDescriptor {
	descriptors, _ := parseGradleDependenciesWithWarnings(repoPath)
	return descriptors
}

func parseGradleDependenciesWithWarnings(repoPath string) ([]dependencyDescriptor, []string) {
	parser := func(content string) []dependencyDescriptor {
		return parseGradleDependencyContent(content)
	}
	return parseBuildFilesWithWarnings(repoPath, parser, buildGradleName, buildGradleKTSName)
}

func parseGradleDependencyContent(content string) []dependencyDescriptor {
	descriptors := make([]dependencyDescriptor, 0)
	descriptors = append(descriptors, parseGradleDependencyMatches(content, gradleCoordinatePattern)...)
	descriptors = append(descriptors, parseGradleMapDependencies(content)...)
	return dedupeDescriptors(descriptors)
}

func parseGradleMapDependencies(content string) []dependencyDescriptor {
	matches := gradleMapInvocationPattern.FindAllStringSubmatch(content, -1)
	descriptors := make([]dependencyDescriptor, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		args := match[1]
		group := ""
		artifact := ""
		version := ""
		for _, pair := range gradleNamedArgPattern.FindAllStringSubmatch(args, -1) {
			if len(pair) != 3 {
				continue
			}
			key := strings.ToLower(strings.TrimSpace(pair[1]))
			value := strings.TrimSpace(pair[2])
			switch key {
			case "group":
				group = value
			case "name":
				artifact = value
			case "version":
				version = value
			}
		}
		if group == "" || artifact == "" {
			continue
		}
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     artifact,
			Group:    group,
			Artifact: artifact,
			Version:  version,
		})
	}
	return descriptors
}

func parseGradleDependencyMatches(content string, pattern *regexp.Regexp) []dependencyDescriptor {
	matches := pattern.FindAllStringSubmatch(content, -1)
	descriptors := make([]dependencyDescriptor, 0, len(matches))
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		group := strings.TrimSpace(match[1])
		artifact := strings.TrimSpace(match[2])
		version := ""
		if len(match) > 3 {
			version = strings.TrimSpace(match[3])
		}
		if group == "" || artifact == "" {
			continue
		}
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     artifact,
			Group:    group,
			Artifact: artifact,
			Version:  version,
		})
	}
	return descriptors
}

var gradleLockCoordinatePattern = regexp.MustCompile(`^\s*([^:#=\s]+):([^:#=\s]+):([^=\s]+)(?:\s*=.*)?$`)

func parseGradleLockfiles(repoPath string) ([]dependencyDescriptor, bool, []string) {
	descriptors := make([]dependencyDescriptor, 0)
	warnings := make([]string, 0)
	hasLockfile := false

	err := filepath.WalkDir(repoPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(entry.Name()) != gradleLockfileName {
			return nil
		}
		hasLockfile = true
		content, readErr := safeio.ReadFileUnder(repoPath, path)
		if readErr != nil {
			relPath := path
			if rel, relErr := filepath.Rel(repoPath, path); relErr == nil {
				relPath = rel
			}
			warnings = append(warnings, fmt.Sprintf("unable to read %s: %v", relPath, readErr))
			return nil
		}
		descriptors = append(descriptors, parseGradleLockfileContent(string(content))...)
		return nil
	})
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan lockfiles: %v", err))
	}
	return dedupeDescriptors(descriptors), hasLockfile, warnings
}

func parseGradleLockfileContent(content string) []dependencyDescriptor {
	lines := strings.Split(content, "\n")
	descriptors := make([]dependencyDescriptor, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		matches := gradleLockCoordinatePattern.FindStringSubmatch(trimmed)
		if len(matches) != 4 {
			continue
		}
		group := strings.TrimSpace(matches[1])
		artifact := strings.TrimSpace(matches[2])
		version := strings.TrimSpace(matches[3])
		if group == "" || artifact == "" {
			continue
		}
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     artifact,
			Group:    group,
			Artifact: artifact,
			Version:  version,
		})
	}
	return descriptors
}

func dedupeDescriptors(descriptors []dependencyDescriptor) []dependencyDescriptor {
	if len(descriptors) == 0 {
		return nil
	}
	items := make(map[string]dependencyDescriptor)
	for _, descriptor := range descriptors {
		if descriptor.Group == "" || descriptor.Artifact == "" {
			continue
		}
		key := descriptorKey(descriptor)
		current, ok := items[key]
		if !ok {
			items[key] = descriptor
			continue
		}
		if current.Version == "" && descriptor.Version != "" {
			current.Version = descriptor.Version
		}
		items[key] = current
	}
	deduped := make([]dependencyDescriptor, 0, len(items))
	for _, descriptor := range items {
		deduped = append(deduped, descriptor)
	}
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Name == deduped[j].Name {
			return deduped[i].Group < deduped[j].Group
		}
		return deduped[i].Name < deduped[j].Name
	})
	return deduped
}

func parseBuildFiles(repoPath string, parser func(content string) []dependencyDescriptor, names ...string) []dependencyDescriptor {
	descriptors, _ := parseBuildFilesWithWarnings(repoPath, parser, names...)
	return descriptors
}

func parseBuildFilesWithWarnings(repoPath string, parser func(content string) []dependencyDescriptor, names ...string) ([]dependencyDescriptor, []string) {
	collector := buildFileCollector{
		repoPath: repoPath,
		parser:   parser,
		names:    names,
		seen:     make(map[string]struct{}),
	}
	err := filepath.WalkDir(repoPath, collector.visit)
	if err != nil {
		return collector.descriptors, collector.warnings
	}
	return collector.descriptors, collector.warnings
}

type buildFileCollector struct {
	repoPath    string
	parser      func(content string) []dependencyDescriptor
	names       []string
	seen        map[string]struct{}
	descriptors []dependencyDescriptor
	warnings    []string
}

func (c *buildFileCollector) visit(path string, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
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
	content, err := safeio.ReadFileUnder(c.repoPath, path)
	if err != nil {
		relPath := path
		if rel, relErr := filepath.Rel(c.repoPath, path); relErr == nil {
			relPath = rel
		}
		c.warnings = append(c.warnings, fmt.Sprintf("unable to read %s: %v", relPath, err))
		return nil
	}
	for _, descriptor := range c.parser(string(content)) {
		c.recordDescriptor(descriptor)
	}
	return nil
}

func (c *buildFileCollector) recordDescriptor(descriptor dependencyDescriptor) {
	key := descriptorKey(descriptor)
	if _, ok := c.seen[key]; ok {
		return
	}
	c.seen[key] = struct{}{}
	descriptor.FromManifest = true
	c.descriptors = append(c.descriptors, descriptor)
}

func matchesBuildFile(fileName string, names []string) bool {
	for _, name := range names {
		if fileName == strings.ToLower(name) {
			return true
		}
	}
	return false
}
