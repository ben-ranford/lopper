package kotlinandroid

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ben-ranford/lopper/internal/lang/shared"
)

const gradleReadWarningFormat = "unable to read %s: %v"

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
	manifestDescriptors, lockfileDescriptors, hasLockfile, warnings := collectGradleDeclaredDependencyDescriptors(repoPath)
	descriptors := mergeDescriptors(manifestDescriptors, lockfileDescriptors)
	lookups := buildDependencyLookupIndex(descriptors)
	lookups.HasLockfile = hasLockfile
	return descriptors, lookups, warnings
}

func collectGradleDeclaredDependencyDescriptors(repoPath string) ([]dependencyDescriptor, []dependencyDescriptor, bool, []string) {
	lockfileMatched := false
	discovery, walkErr := discoverGradleFiles(repoPath, func(fileName string) bool {
		if strings.EqualFold(fileName, gradleLockfileName) {
			lockfileMatched = true
		}
		return matchesBuildFile(fileName, []string{buildGradleName, buildGradleKTSName}) || strings.EqualFold(fileName, gradleLockfileName)
	})

	catalogResolver, catalogWarnings := shared.LoadGradleCatalogResolver(repoPath)
	manifestFiles := make([]discoveredGradleFile, 0, len(discovery.Files))
	lockfileFiles := make([]discoveredGradleFile, 0, len(discovery.Files))
	for _, file := range discovery.Files {
		switch {
		case strings.EqualFold(filepath.Base(file.Path), gradleLockfileName):
			lockfileFiles = append(lockfileFiles, file)
		default:
			manifestFiles = append(manifestFiles, file)
		}
	}

	manifestDescriptors, manifestWarnings := parseGradleManifestFiles(manifestFiles, catalogResolver)
	lockfileDescriptors := parseGradleLockfileFiles(lockfileFiles)

	warnings := append([]string{}, discovery.Warnings...)
	warnings = append(warnings, catalogWarnings...)
	warnings = append(warnings, manifestWarnings...)
	if walkErr != nil {
		warnings = append(warnings, fmt.Sprintf("unable to scan build files: %v", walkErr))
	}

	return manifestDescriptors, lockfileDescriptors, lockfileMatched, shared.DedupeWarnings(warnings)
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

func formatGradleReadWarning(repoPath, path string, err error) string {
	relPath := path
	if rel, relErr := filepath.Rel(repoPath, path); relErr == nil {
		relPath = rel
	}
	return fmt.Sprintf(gradleReadWarningFormat, filepath.ToSlash(relPath), err)
}
