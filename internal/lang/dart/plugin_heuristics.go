package dart

import (
	"fmt"
	"slices"
	"strings"
)

const federatedPlatformInterfaceSuffix = "_platform_interface"

var federatedPlatformSuffixes = []string{"_android", "_ios", "_macos", "_linux", "_windows", "_web"}

func dependencyInfoFromSpec(dependency string, value any, hasPluginMetadata *bool) dependencyInfo {
	info := dependencyInfo{
		PluginLike:         isLikelyFlutterPluginPackage(dependency),
		DeclaredInManifest: true,
	}
	fields, ok := toStringMap(value)
	if !ok {
		if strings.TrimSpace(asString(value)) != "" {
			assignDependencySource(&info, dependencySourceHosted, "")
		}
		return info
	}

	if pathValue := strings.TrimSpace(asString(fields["path"])); pathValue != "" {
		info.LocalPath = true
		assignDependencySource(&info, dependencySourcePath, pathValue)
	}

	if gitValue, exists := fields["git"]; exists && gitValue != nil {
		assignDependencySource(&info, dependencySourceGit, dependencySourceDetail(gitValue, "url", "ref", "path"))
	}

	if sdkValue := strings.TrimSpace(asString(fields["sdk"])); sdkValue != "" {
		if strings.EqualFold(sdkValue, "flutter") {
			info.FlutterSDK = true
		}
		assignDependencySource(&info, dependencySourceSDK, sdkValue)
	}

	if hostedValue, exists := fields["hosted"]; exists && hostedValue != nil {
		assignDependencySource(&info, dependencySourceHosted, dependencySourceDetail(hostedValue, "url", "name"))
	}

	if versionValue := strings.TrimSpace(asString(fields["version"])); versionValue != "" {
		assignDependencySource(&info, dependencySourceHosted, "")
	}

	if hasPluginMetadataValue(fields) {
		info.PluginLike = true
		if hasPluginMetadata != nil {
			*hasPluginMetadata = true
		}
	}
	return info
}

func dependencySourceDetail(value any, preferredKeys ...string) string {
	if fields, ok := toStringMap(value); ok {
		for _, key := range preferredKeys {
			if detail := strings.TrimSpace(asString(fields[key])); detail != "" {
				return detail
			}
		}
		return ""
	}
	return strings.TrimSpace(asString(value))
}

func normalizeDependencySource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case dependencySourceHosted:
		return dependencySourceHosted
	case dependencySourceGit:
		return dependencySourceGit
	case dependencySourcePath:
		return dependencySourcePath
	case dependencySourceSDK:
		return dependencySourceSDK
	default:
		return strings.ToLower(strings.TrimSpace(source))
	}
}

func dependencySourcePriority(source string) int {
	switch normalizeDependencySource(source) {
	case dependencySourcePath:
		return 4
	case dependencySourceGit:
		return 3
	case dependencySourceSDK:
		return 2
	case dependencySourceHosted:
		return 1
	default:
		return 0
	}
}

func assignDependencySource(info *dependencyInfo, source, detail string) {
	if info == nil {
		return
	}
	source = normalizeDependencySource(source)
	detail = strings.TrimSpace(detail)
	if source == "" {
		return
	}
	if info.Source == "" || dependencySourcePriority(source) > dependencySourcePriority(info.Source) {
		info.Source = source
		if detail != "" {
			info.SourceDetail = detail
		}
		return
	}
	if info.Source == source && info.SourceDetail == "" {
		info.SourceDetail = detail
	}
}

func annotateFederatedPluginDependencies(dependencies map[string]dependencyInfo, lockPackages map[string]pubspecLockPackage) {
	if len(dependencies) == 0 {
		return
	}

	candidates := collectDependencyCandidates(dependencies, lockPackages)
	if len(candidates) == 0 {
		return
	}

	families := make(map[string]map[string]string)
	for candidate := range candidates {
		family, role, ok := federatedFamilyRole(candidate)
		if !ok {
			continue
		}
		if _, exists := families[family]; !exists {
			families[family] = make(map[string]string)
		}
		families[family][candidate] = role
	}

	for dependency, info := range dependencies {
		dependency = normalizeDependencyID(dependency)
		if dependency == "" {
			continue
		}

		family := dependency
		role := federatedRoleApp
		if parsedFamily, parsedRole, ok := federatedFamilyRole(dependency); ok {
			family = parsedFamily
			role = parsedRole
		}

		familyMembers := families[family]
		if len(familyMembers) == 0 {
			continue
		}

		members := make([]string, 0, len(familyMembers)+1)
		if _, hasRoot := candidates[family]; hasRoot {
			members = append(members, family)
		}
		for member := range familyMembers {
			members = append(members, member)
		}
		members = dedupeStrings(members)
		slices.Sort(members)
		if len(members) < 2 {
			continue
		}

		related := make([]string, 0, len(members)-1)
		for _, member := range members {
			if member != dependency {
				related = append(related, member)
			}
		}
		if len(related) == 0 {
			continue
		}

		info.FederatedPlugin = true
		info.FederatedFamily = family
		info.FederatedRole = role
		if dependency == family {
			info.FederatedRole = federatedRoleApp
		}
		info.FederatedMembers = related
		dependencies[dependency] = info
	}
}

func collectDependencyCandidates(dependencies map[string]dependencyInfo, lockPackages map[string]pubspecLockPackage) map[string]struct{} {
	candidates := make(map[string]struct{}, len(dependencies)+len(lockPackages))
	for dependency := range dependencies {
		normalized := normalizeDependencyID(dependency)
		if normalized != "" {
			candidates[normalized] = struct{}{}
		}
	}
	for rawName, item := range lockPackages {
		if dependency := normalizeDependencyID(rawName); dependency != "" {
			candidates[dependency] = struct{}{}
		}
		if dependency := lockPackageName(item.Description); dependency != "" {
			candidates[dependency] = struct{}{}
		}
	}
	return candidates
}

func federatedFamilyRole(dependency string) (string, string, bool) {
	dependency = normalizeDependencyID(dependency)
	if dependency == "" {
		return "", "", false
	}
	if strings.HasSuffix(dependency, federatedPlatformInterfaceSuffix) {
		family := strings.TrimSuffix(dependency, federatedPlatformInterfaceSuffix)
		if family != "" {
			return family, federatedRolePlatformInterface, true
		}
	}
	for _, suffix := range federatedPlatformSuffixes {
		if strings.HasSuffix(dependency, suffix) {
			family := strings.TrimSuffix(dependency, suffix)
			if family != "" {
				return family, federatedRolePlatform, true
			}
		}
	}
	return "", "", false
}

func hasPluginMetadataValue(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		return hasPluginMetadataStringMap(typed)
	case map[any]any:
		return hasPluginMetadataAnyMap(typed)
	case []any:
		return hasPluginMetadataSlice(typed)
	}
	return false
}

func hasPluginMetadataStringMap(values map[string]any) bool {
	for key, nested := range values {
		if isPluginMetadataKey(key) || hasPluginMetadataValue(nested) {
			return true
		}
	}
	return false
}

func hasPluginMetadataAnyMap(values map[any]any) bool {
	for key, nested := range values {
		if isPluginMetadataKey(fmt.Sprint(key)) || hasPluginMetadataValue(nested) {
			return true
		}
	}
	return false
}

func hasPluginMetadataSlice(values []any) bool {
	for _, item := range values {
		if hasPluginMetadataValue(item) {
			return true
		}
	}
	return false
}

func isPluginMetadataKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "plugin", "pluginclass", "ffiplugin", "platforms":
		return true
	default:
		return false
	}
}

func isLikelyFlutterPluginPackage(dependency string) bool {
	dependency = normalizeDependencyID(dependency)
	switch {
	case strings.HasSuffix(dependency, "_android"),
		strings.HasSuffix(dependency, "_ios"),
		strings.HasSuffix(dependency, "_macos"),
		strings.HasSuffix(dependency, "_linux"),
		strings.HasSuffix(dependency, "_windows"),
		strings.HasSuffix(dependency, "_web"),
		strings.Contains(dependency, federatedPlatformInterfaceSuffix):
		return true
	default:
		return false
	}
}

func toStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[fmt.Sprint(key)] = item
		}
		return converted, true
	default:
		return nil, false
	}
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}
