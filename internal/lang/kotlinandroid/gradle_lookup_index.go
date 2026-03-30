package kotlinandroid

import (
	"sort"
	"strings"
)

func buildDescriptorLookups(descriptors []dependencyDescriptor) dependencyLookups {
	return buildDependencyLookupIndex(descriptors)
}

func buildDependencyLookupIndex(descriptors []dependencyDescriptor) dependencyLookups {
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
