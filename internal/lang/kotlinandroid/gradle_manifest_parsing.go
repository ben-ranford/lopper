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
	discover := func(path string) (gradleFileDiscoveryResult, error) {
		return discoverBuildFiles(path, buildGradleName, buildGradleKTSName)
	}
	parser := func(files []discoveredGradleFile) ([]dependencyDescriptor, []string) {
		return parseGradleManifestFiles(files, catalogResolver)
	}
	descriptors, _, parseWarnings := collectGradleFileDescriptorsWithWarnings(repoPath, discover, parser, "build files")
	warnings = append(warnings, parseWarnings...)
	return descriptors, shared.DedupeWarnings(warnings)
}

func parseGradleManifestFiles(files []discoveredGradleFile, catalogResolver shared.GradleCatalogResolver) ([]dependencyDescriptor, []string) {
	return parseDiscoveredBuildFilesWithPath(files, func(path, content string) ([]dependencyDescriptor, []string) {
		return parseGradleDependencyContentWithCatalog(path, content, catalogResolver)
	})
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
