package analysis

import (
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/language"
)

const (
	powerShellAdapterID             = "powershell"
	powerShellAdapterPreviewFeature = "powershell-adapter-preview"
)

func adapterFeatureFilter(features featureflags.Set) language.AdapterFilter {
	return func(adapter language.Adapter) bool {
		if adapter == nil {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(adapter.ID())) {
		case powerShellAdapterID:
			return features.Enabled(powerShellAdapterPreviewFeature)
		default:
			return true
		}
	}
}
