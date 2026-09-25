package kotlinandroid

import (
	"fmt"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

func parseGradleDependencies(repoPath string) []dependencyDescriptor {
	descriptors, _ := parseGradleDependenciesWithWarnings(repoPath)
	return descriptors
}

func parseGradleDependenciesWithWarnings(repoPath string) ([]dependencyDescriptor, []string) {
	catalogResolver, warnings := shared.LoadGradleCatalogResolver(repoPath)
	descriptors, parseWarnings := parseBuildFilesWithPath(repoPath, func(path, content string) ([]dependencyDescriptor, []string) {
		return parseGradleDependencyContentWithCatalog(path, content, catalogResolver)
	}, buildGradleName, buildGradleKTSName)
	warnings = append(warnings, parseWarnings...)
	return descriptors, shared.DedupeWarnings(warnings)
}

func parseGradleDependencyContent(content string) []dependencyDescriptor {
	descriptors := parseGradleDependencyContentForPath(buildGradleName, content)
	descriptors = append(descriptors, parseGradleDependencyContentForPath(buildGradleKTSName, content)...)
	return dedupeDescriptors(descriptors)
}

func parseGradleDependencyContentForPath(path, content string) []dependencyDescriptor {
	coordinates := shared.ParseGradleDependencyCoordinatesForFile(path, content)
	descriptors := make([]dependencyDescriptor, 0)
	for _, coordinate := range coordinates {
		descriptors = append(descriptors, dependencyDescriptor{
			Name:     coordinate.Artifact,
			Group:    coordinate.Group,
			Artifact: coordinate.Artifact,
			Version:  coordinate.Version,
		})
	}
	return dedupeDescriptors(descriptors)
}

func parseGradleDependencyContentWithCatalog(path string, content string, catalogResolver shared.GradleCatalogResolver) ([]dependencyDescriptor, []string) {
	descriptors := parseGradleDependencyContentForPath(path, content)
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

func parseBuildFiles(repoPath string, parser func(content string) []dependencyDescriptor, names ...string) []dependencyDescriptor {
	descriptors, _ := parseBuildFilesWithWarnings(repoPath, parser, names...)
	return descriptors
}

func parseBuildFilesWithWarnings(repoPath string, parser func(content string) []dependencyDescriptor, names ...string) ([]dependencyDescriptor, []string) {
	return parseBuildFilesWithPath(repoPath, func(_, content string) ([]dependencyDescriptor, []string) {
		return parser(content), nil
	}, names...)
}

func parseBuildFilesWithPath(repoPath string, parser func(path, content string) ([]dependencyDescriptor, []string), names ...string) ([]dependencyDescriptor, []string) {
	seen := make(map[string]struct{})
	descriptors := make([]dependencyDescriptor, 0)
	var parseWarnings []string
	discovery, walkErr := discoverBuildFiles(repoPath, func(path, content string) {
		items, warnings := parser(path, content)
		parseWarnings = append(parseWarnings, warnings...)
		for _, descriptor := range detachGradleDescriptors(items) {
			descriptors = appendManifestDescriptor(descriptors, seen, descriptor)
		}
	}, names...)
	warnings := discovery.Warnings
	warnings = append(warnings, parseWarnings...)
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan build files: %v", walkErr))
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
