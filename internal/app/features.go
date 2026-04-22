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
