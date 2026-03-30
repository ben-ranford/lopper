package kotlinandroid

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
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
	manifestDescriptors, manifestWarnings := collectManifestDependencyDescriptors(repoPath)
	lockfileDescriptors, hasLockfile, lockWarnings := collectLockfileDependencyDescriptors(repoPath)

	descriptors := mergeDescriptors(manifestDescriptors, lockfileDescriptors)
	lookups := buildDependencyLookupIndex(descriptors)
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
