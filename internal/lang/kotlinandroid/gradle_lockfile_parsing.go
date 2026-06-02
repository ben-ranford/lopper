package kotlinandroid

import (
	"regexp"
	"strings"
)

var gradleLockCoordinatePattern = regexp.MustCompile(`^\s*([^:#=\s]+):([^:#=\s]+):([^=\s]+)(?:\s*=.*)?$`)

func parseGradleLockfiles(repoPath string) ([]dependencyDescriptor, bool, []string) {
	parser := func(files []discoveredGradleFile) ([]dependencyDescriptor, []string) {
		return parseGradleLockfileFiles(files), nil
	}
	return collectGradleFileDescriptorsWithWarnings(repoPath, discoverGradleLockfiles, parser, "lockfiles")
}

func parseGradleLockfileFiles(files []discoveredGradleFile) []dependencyDescriptor {
	descriptors := make([]dependencyDescriptor, 0)
	for _, file := range files {
		descriptors = append(descriptors, parseGradleLockfileContent(file.Content)...)
	}
	return dedupeDescriptors(descriptors)
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
