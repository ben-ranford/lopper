package kotlinandroid

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

var gradleCoordinatePattern = regexp.MustCompile(`(?m)\b(?:implementation|api|compileOnly|runtimeOnly|kapt|ksp|testImplementation|androidTestImplementation|testRuntimeOnly)\s*\(?\s*(?:platform\()?["']([^:"'\s]+):([^:"'\s]+)(?::([^"'\s]+))?["']\s*\)?\s*\)?`)

var gradleMapInvocationPattern = regexp.MustCompile(`(?ms)\b(?:implementation|api|compileOnly|runtimeOnly|kapt|ksp|testImplementation|androidTestImplementation|testRuntimeOnly)\s*\(?\s*((?:[A-Za-z_][A-Za-z0-9_]*\s*[:=]\s*["'][^"'\n]+["']\s*,?\s*)+)`)

var gradleNamedArgPattern = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*[:=]\s*["']([^"']+)["']`)

func collectManifestDependencyDescriptors(repoPath string) ([]dependencyDescriptor, []string) {
	return parseGradleDependenciesWithWarnings(repoPath)
}

func parseGradleDependencies(repoPath string) []dependencyDescriptor {
	descriptors, _ := parseGradleDependenciesWithWarnings(repoPath)
	return descriptors
}

func parseGradleDependenciesWithWarnings(repoPath string) ([]dependencyDescriptor, []string) {
	catalogResolver, warnings := shared.LoadGradleCatalogResolver(repoPath)
	discovery, walkErr := discoverBuildFiles(repoPath, buildGradleName, buildGradleKTSName)
	warnings = append(warnings, discovery.Warnings...)
	descriptors, parseWarnings := parseGradleManifestFiles(discovery.Files, catalogResolver)
	warnings = append(warnings, parseWarnings...)
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan build files: %v", walkErr))
	}
	return descriptors, shared.DedupeWarnings(warnings)
}

func parseGradleManifestFiles(files []discoveredGradleFile, catalogResolver shared.GradleCatalogResolver) ([]dependencyDescriptor, []string) {
	return parseDiscoveredBuildFilesWithPath(files, func(path, content string) ([]dependencyDescriptor, []string) {
		return parseGradleDependencyContentWithCatalog(path, content, catalogResolver)
	})
}

func parseGradleDependencyContent(content string) []dependencyDescriptor {
	descriptors := make([]dependencyDescriptor, 0)
	descriptors = append(descriptors, parseGradleDependencyMatches(content, gradleCoordinatePattern)...)
	descriptors = append(descriptors, parseGradleMapDependencies(content)...)
	return dedupeDescriptors(descriptors)
}

func parseGradleDependencyContentWithCatalog(path string, content string, catalogResolver shared.GradleCatalogResolver) ([]dependencyDescriptor, []string) {
	descriptors := parseGradleDependencyContent(content)
	catalogDescriptors, warnings := catalogResolver.ParseDependencyReferences(path, content)
	for _, descriptor := range catalogDescriptors {
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     descriptor.Artifact,
			Group:    descriptor.Group,
			Artifact: descriptor.Artifact,
			Version:  descriptor.Version,
		})
	}
	return dedupeDescriptors(descriptors), warnings
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

func parseBuildFiles(repoPath string, parser func(content string) []dependencyDescriptor, names ...string) []dependencyDescriptor {
	descriptors, _ := parseBuildFilesWithWarnings(repoPath, parser, names...)
	return descriptors
}

func parseBuildFilesWithWarnings(repoPath string, parser func(content string) []dependencyDescriptor, names ...string) ([]dependencyDescriptor, []string) {
	discovery, walkErr := discoverBuildFiles(repoPath, names...)
	warnings := append([]string{}, discovery.Warnings...)
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan build files: %v", walkErr))
	}
	return parseDiscoveredBuildFiles(discovery.Files, parser), shared.DedupeWarnings(warnings)
}

func parseDiscoveredBuildFiles(files []discoveredGradleFile, parser func(content string) []dependencyDescriptor) []dependencyDescriptor {
	seen := make(map[string]struct{})
	descriptors := make([]dependencyDescriptor, 0)
	for _, file := range files {
		for _, descriptor := range parser(file.Content) {
			descriptors = appendManifestDescriptor(descriptors, seen, descriptor)
		}
	}
	return descriptors
}

func parseDiscoveredBuildFilesWithPath(files []discoveredGradleFile, parser func(path, content string) ([]dependencyDescriptor, []string)) ([]dependencyDescriptor, []string) {
	seen := make(map[string]struct{})
	descriptors := make([]dependencyDescriptor, 0)
	warnings := make([]string, 0)
	for _, file := range files {
		items, parseWarnings := parser(file.Path, file.Content)
		warnings = append(warnings, parseWarnings...)
		for _, descriptor := range items {
			descriptors = appendManifestDescriptor(descriptors, seen, descriptor)
		}
	}
	return descriptors, shared.DedupeWarnings(warnings)
}

func appendManifestDescriptor(descriptors []dependencyDescriptor, seen map[string]struct{}, descriptor dependencyDescriptor) []dependencyDescriptor {
	key := descriptorKey(descriptor)
	if _, ok := seen[key]; ok {
		return descriptors
	}
	seen[key] = struct{}{}
	descriptor.FromManifest = true
	return append(descriptors, descriptor)
}
