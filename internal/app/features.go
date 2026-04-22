package app

import (
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
)

func (a *App) executeFeatures(req Request) (string, error) {
	if err := featureflags.ValidateDefaultRegistry(); err != nil {
		return "", err
	}
	registry := a.Features
	if registry == nil {
		registry = featureflags.DefaultRegistry()
	}
	manifest, err := registry.Manifest(featureflags.ResolveOptions{Channel: featureflags.ChannelRelease})
	if err != nil {
		return "", err
	}

	switch strings.ToLower(strings.TrimSpace(req.Features.Format)) {
	case "", "table":
		return formatFeatureTable(manifest), nil
	case "json":
		data, err := featureflags.FormatManifest(manifest)
		if err != nil {
			return "", err
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("invalid features format: %s", req.Features.Format)
	}
}

func formatFeatureTable(manifest []featureflags.ManifestEntry) string {
	if len(manifest) == 0 {
		return "No feature flags registered.\n"
	}

	var b strings.Builder
	b.WriteString("CODE           NAME  LIFECYCLE  RELEASE_DEFAULT\n")
	for _, entry := range manifest {
		fmt.Fprintf(&b, "%-14s %-5s %-10s %t\n", entry.Code, entry.Name, entry.Lifecycle, entry.EnabledByDefault)
	}
	return b.String()
}
