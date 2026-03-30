package js

import (
	"fmt"
	"sort"
	"strings"
)

func resolveExportsEntryPaths(value any, profile runtimeProfile, scope string, surface *ExportSurface) []string {
	paths, _ := resolveExportNode(value, profile, scope, surface)
	return paths
}

func resolveExportNode(value any, profile runtimeProfile, scope string, surface *ExportSurface) ([]string, bool) {
	switch typed := value.(type) {
	case string:
		return resolveStringExportNode(typed, scope, surface)
	case []any:
		return resolveArrayExportNode(typed, profile, scope, surface)
	case map[string]any:
		return resolveMapExportNode(typed, profile, scope, surface)
	default:
		return nil, false
	}
}

func resolveStringExportNode(value string, scope string, surface *ExportSurface) ([]string, bool) {
	if !isLikelyCodeAsset(value) {
		if surface != nil {
			surface.Warnings = append(surface.Warnings, fmt.Sprintf("skipping non-js export target at %s: %s", scope, value))
		}
		return nil, false
	}
	return []string{value}, true
}

func resolveArrayExportNode(values []any, profile runtimeProfile, scope string, surface *ExportSurface) ([]string, bool) {
	for idx, item := range values {
		paths, ok := resolveExportNode(item, profile, fmt.Sprintf("%s[%d]", scope, idx), surface)
		if ok && len(paths) > 0 {
			return paths, true
		}
	}
	return nil, false
}

func resolveMapExportNode(node map[string]any, profile runtimeProfile, scope string, surface *ExportSurface) ([]string, bool) {
	if len(node) == 0 {
		return nil, false
	}
	if hasSubpathExportKeys(node) {
		return resolveSubpathExportMap(node, profile, scope, surface)
	}
	if hasConditionKeys(node) {
		return resolveConditionalExportMap(node, profile, scope, surface)
	}
	return resolveObjectExportMap(node, profile, scope, surface)
}

func resolveSubpathExportMap(node map[string]any, profile runtimeProfile, scope string, surface *ExportSurface) ([]string, bool) {
	collected := make(map[string]struct{})
	keys := sortedObjectKeys(node)
	for _, key := range keys {
		if !isSubpathExportKey(key) {
			continue
		}
		paths, ok := resolveExportNode(node[key], profile, fmt.Sprintf("%s.%s", scope, key), surface)
		if !ok {
			continue
		}
		for _, path := range paths {
			collected[path] = struct{}{}
		}
	}
	return sortedMapKeys(collected), len(collected) > 0
}

func resolveObjectExportMap(node map[string]any, profile runtimeProfile, scope string, surface *ExportSurface) ([]string, bool) {
	collected := make(map[string]struct{})
	for _, key := range sortedObjectKeys(node) {
		paths, ok := resolveExportNode(node[key], profile, fmt.Sprintf("%s.%s", scope, key), surface)
		if !ok {
			continue
		}
		for _, path := range paths {
			collected[path] = struct{}{}
		}
	}
	return sortedMapKeys(collected), len(collected) > 0
}

func resolveConditionalExportMap(node map[string]any, profile runtimeProfile, scope string, surface *ExportSurface) ([]string, bool) {
	matches := matchingConditionKeys(node, profile)
	if len(matches) == 0 {
		return nil, false
	}
	if len(matches) > 1 && surface != nil {
		surface.Warnings = append(surface.Warnings, fmt.Sprintf("ambiguous export conditions at %s for profile %q: matched %s; selected %q", scope, profile.name, strings.Join(matches, ", "), matches[0]))
	}
	for _, key := range matches {
		paths, ok := resolveExportNode(node[key], profile, fmt.Sprintf("%s.%s", scope, key), surface)
		if ok && len(paths) > 0 {
			return paths, true
		}
	}
	return nil, false
}

func matchingConditionKeys(node map[string]any, profile runtimeProfile) []string {
	items := make([]string, 0, len(profile.conditions))
	for _, key := range profile.conditions {
		if _, ok := node[key]; ok {
			items = append(items, key)
		}
	}
	return items
}

func hasConditionKeys(node map[string]any) bool {
	for key := range node {
		if looksLikeConditionKey(key) {
			return true
		}
	}
	return false
}

func hasSubpathExportKeys(node map[string]any) bool {
	for key := range node {
		if isSubpathExportKey(key) {
			return true
		}
	}
	return false
}

func isSubpathExportKey(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), ".")
}

func sortedObjectKeys(node map[string]any) []string {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapKeys(node map[string]struct{}) []string {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
