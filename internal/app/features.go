package app

import (
	"fmt"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/version"
)

func (a *App) executeFeatures(req Request) (string, error) {
	if err := featureflags.ValidateDefaultRegistry(); err != nil {
		return "", err
	}
	registry := a.Features
	if registry == nil {
		registry = featureflags.DefaultRegistry()
	}
	channelValue := strings.TrimSpace(req.Features.Channel)
	if channelValue == "" {
		channelValue = version.Current().BuildChannel
	}
	channel, err := featureflags.NormalizeChannel(channelValue)
	if err != nil {
		return "", err
	}
	var lock *featureflags.ReleaseLock
	if channel == featureflags.ChannelRelease {
		release := strings.TrimSpace(req.Features.Release)
		if release == "" {
			release = version.Current().Version
		}
		lock, err = featureflags.DefaultReleaseLock(release)
		if err != nil {
			return "", err
		}
	}
	manifest, err := registry.Manifest(featureflags.ResolveOptions{Channel: channel, Lock: lock})
	if err != nil {
		return "", err
	}

	var output string
	switch strings.ToLower(strings.TrimSpace(req.Features.Format)) {
	case "", "table":
		output = formatFeatureTable(manifest)
	case "json":
		data, err := featureflags.FormatManifest(manifest)
		if err != nil {
			return "", err
		}
		output = string(data)
	default:
		return "", fmt.Errorf("invalid features format: %s", req.Features.Format)
	}
	return persistCommandOutput(output, req.Features.OutputPath, "feature manifest")
}

func formatFeatureTable(manifest []featureflags.ManifestEntry) string {
	if len(manifest) == 0 {
		return "No feature flags registered.\n"
	}

	var b strings.Builder
	b.WriteString("CODE           NAME  LIFECYCLE  ENABLED_BY_DEFAULT\n")
	for _, entry := range manifest {
		fmt.Fprintf(&b, "%-14s %-5s %-10s %t\n", entry.Code, entry.Name, entry.Lifecycle, entry.EnabledByDefault)
	}
	return b.String()
}
