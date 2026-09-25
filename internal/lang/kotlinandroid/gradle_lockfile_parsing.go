package kotlinandroid

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

var gradleLockCoordinatePattern = regexp.MustCompile(`^\s*([^:#=\s]+):([^:#=\s]+):([^=\s]+)(?:\s*=.*)?$`)

func parseGradleLockfiles(repoPath string) ([]dependencyDescriptor, bool, []string) {
	var descriptors []dependencyDescriptor
	discovery, walkErr := discoverGradleLockfiles(repoPath, func(_ string, content string) {
		descriptors = append(descriptors, detachGradleDescriptors(parseGradleLockfileContent(content))...)
	})
	warnings := discovery.Warnings
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan lockfiles: %v", walkErr))
	}
	return dedupeDescriptors(descriptors), discovery.Matched, shared.DedupeWarnings(warnings)
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
