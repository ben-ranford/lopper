package analysis

import (
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
)

const (
	adapterPreviewFeatureSuffix = "-adapter-preview"
)

func adapterFeatureFilter(features featureflags.Set) language.AdapterFilter {
	adapterFeatures := adapterFeatureFlags(featureflags.DefaultRegistry())
	return func(adapter language.Adapter) bool {
		if adapter == nil {
			return false
		}
		featureName, ok := adapterFeatures[normalizeAdapterID(adapter.ID())]
		if !ok {
			return true
		}
		return features.Enabled(featureName)
	}
}

func adapterFeatureFlags(registry *featureflags.Registry) map[string]string {
	if registry == nil {
		registry = featureflags.DefaultRegistry()
	}
	flags := registry.Flags()
	features := make(map[string]string, len(flags))
	for _, flag := range flags {
		adapterID, ok := previewAdapterID(flag.Name)
		if !ok {
			continue
		}
		features[adapterID] = flag.Name
	}
	return features
}

func previewAdapterID(featureName string) (string, bool) {
	normalized := normalizeAdapterID(featureName)
	if !strings.HasSuffix(normalized, adapterPreviewFeatureSuffix) {
		return "", false
	}
	adapterID := strings.TrimSuffix(normalized, adapterPreviewFeatureSuffix)
	if adapterID == "" {
		return "", false
	}
	return adapterID, true
}

func normalizeAdapterID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
