package dart

import (
	"fmt"
	"strings"
)

func dependencyInfoFromSpec(dependency string, value any, hasPluginMetadata *bool) dependencyInfo {
	info := dependencyInfo{
		PluginLike: isLikelyFlutterPluginPackage(dependency),
	}
	fields, ok := toStringMap(value)
	if !ok {
		return info
	}
	if asString(fields["path"]) != "" {
		info.LocalPath = true
	}
	if sdkValue := asString(fields["sdk"]); strings.EqualFold(sdkValue, "flutter") {
		info.FlutterSDK = true
	}
	if hasPluginMetadataValue(fields) {
		info.PluginLike = true
		if hasPluginMetadata != nil {
			*hasPluginMetadata = true
		}
	}
	return info
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
		strings.Contains(dependency, "_platform_interface"):
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
