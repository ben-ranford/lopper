package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/notify"
	"github.com/ben-ranford/lopper/internal/thresholds"
	"github.com/ben-ranford/lopper/internal/version"
)

var (
	featureRegistryProvider    = featureflags.DefaultRegistry
	validateFeatureRegistry    = featureflags.ValidateDefaultRegistry
	featureBuildChannel        = func() string { return version.Current().BuildChannel }
	featureReleaseVersion      = func() string { return version.Current().Version }
	featureReleaseLockProvider = featureflags.DefaultReleaseLock
)

func resolveAnalyseThresholds(values analyseFlagValues, visited map[string]bool) (thresholds.Values, thresholds.PathScope, []string, thresholds.FeatureConfig, string, error) {
	loadResult, err := thresholds.LoadWithPolicy(strings.TrimSpace(*values.repoPath), strings.TrimSpace(*values.configPath))
	if err != nil {
		return thresholds.Values{}, thresholds.PathScope{}, nil, thresholds.FeatureConfig{}, "", err
	}

	resolvedThresholds := loadResult.Resolved
	cliOverrides, err := cliThresholdOverrides(visited, values)
	if err != nil {
		return thresholds.Values{}, thresholds.PathScope{}, nil, thresholds.FeatureConfig{}, "", err
	}
	resolvedThresholds = cliOverrides.Apply(resolvedThresholds)
	if err := resolvedThresholds.Validate(); err != nil {
		return thresholds.Values{}, thresholds.PathScope{}, nil, thresholds.FeatureConfig{}, "", err
	}

	policySources := append([]string{}, loadResult.PolicySources...)
	if hasThresholdOverrides(cliOverrides) {
		policySources = append([]string{"cli"}, policySources...)
	}

	return resolvedThresholds, loadResult.Scope, policySources, loadResult.Features, loadResult.ConfigPath, nil
}

func resolveAnalyseFeatures(visited map[string]bool, values analyseFlagValues, configFeatures thresholds.FeatureConfig) (featureflags.Set, error) {
	channel, lock, err := resolveFeatureBuildContext()
	if err != nil {
		return featureflags.Set{}, err
	}
	return resolveFeatureSet(featureRegistryProvider(), channel, lock, visited, values, configFeatures)
}

func resolveDefaultFeatureSet() (featureflags.Set, error) {
	channel, lock, err := resolveFeatureBuildContext()
	if err != nil {
		return featureflags.Set{}, err
	}
	return featureRegistryProvider().Resolve(featureflags.ResolveOptions{
		Channel: channel,
		Lock:    lock,
	})
}

func resolveFeatureBuildContext() (featureflags.Channel, *featureflags.ReleaseLock, error) {
	if err := validateFeatureRegistry(); err != nil {
		return "", nil, err
	}
	channel, err := featureflags.NormalizeChannel(featureBuildChannel())
	if err != nil {
		return "", nil, err
	}
	var lock *featureflags.ReleaseLock
	if channel == featureflags.ChannelRelease {
		lock, err = featureReleaseLockProvider(featureReleaseVersion())
		if err != nil {
			return "", nil, err
		}
	}
	return channel, lock, nil
}

func resolveFeatureSet(registry *featureflags.Registry, channel featureflags.Channel, lock *featureflags.ReleaseLock, visited map[string]bool, values analyseFlagValues, configFeatures thresholds.FeatureConfig) (featureflags.Set, error) {
	enable := append([]string{}, configFeatures.Enable...)
	disable := append([]string{}, configFeatures.Disable...)
	if visited["enable-feature"] {
		enable = mergePatterns(enable, values.enableFeatures.Values())
	}
	if visited["disable-feature"] {
		disable = mergePatterns(disable, values.disableFeatures.Values())
	}
	return registry.Resolve(featureflags.ResolveOptions{
		Channel: channel,
		Lock:    lock,
		Enable:  enable,
		Disable: disable,
	})
}

func resolveAnalyseNotifications(visited map[string]bool, values analyseFlagValues, resolvedConfigPath string) (notify.Config, error) {
	resolved := notify.DefaultConfig()

	configOverrides, err := notify.LoadConfigOverrides(resolvedConfigPath)
	if err != nil {
		return notify.Config{}, err
	}
	configOverrides = configOverrides.WithoutWebhookTargets()
	resolved = configOverrides.Apply(resolved)

	envOverrides, err := notify.LoadEnvOverrides(os.LookupEnv)
	if err != nil {
		return notify.Config{}, err
	}
	resolved = envOverrides.Apply(resolved)

	cliOverrides, err := cliNotificationOverrides(visited, values)
	if err != nil {
		return notify.Config{}, err
	}
	resolved = cliOverrides.Apply(resolved)

	return resolved, nil
}

func cliThresholdOverrides(visited map[string]bool, values analyseFlagValues) (thresholds.Overrides, error) {
	overrides := thresholds.Overrides{}
	if visited["fail-on-increase"] {
		overrides.FailOnIncreasePercent = values.legacyFailOnIncrease
	}
	if visited["threshold-fail-on-increase"] {
		if overrides.FailOnIncreasePercent != nil && *overrides.FailOnIncreasePercent != *values.thresholdFailOnIncrease {
			return thresholds.Overrides{}, fmt.Errorf("--fail-on-increase and --threshold-fail-on-increase must match when both are provided")
		}
		overrides.FailOnIncreasePercent = values.thresholdFailOnIncrease
	}
	if visited["threshold-low-confidence-warning"] {
		overrides.LowConfidenceWarningPercent = values.thresholdLowConfidenceWarning
	}
	if visited["threshold-min-usage-percent"] {
		overrides.MinUsagePercentForRecommendations = values.thresholdMinUsagePercent
	}
	if visited["threshold-max-uncertain-imports"] {
		overrides.MaxUncertainImportCount = values.thresholdMaxUncertainImports
	}
	if visited["score-weight-usage"] {
		overrides.RemovalCandidateWeightUsage = values.scoreWeightUsage
	}
	if visited["score-weight-impact"] {
		overrides.RemovalCandidateWeightImpact = values.scoreWeightImpact
	}
	if visited["score-weight-confidence"] {
		overrides.RemovalCandidateWeightConfidence = values.scoreWeightConfidence
	}
	if visited["license-deny"] {
		overrides.LicenseDenyList = splitPatternList(*values.licenseDeny)
	}
	if visited["license-fail-on-deny"] {
		overrides.LicenseFailOnDeny = values.licenseFailOnDeny
	}
	if visited["license-provenance-registry"] {
		overrides.LicenseIncludeRegistryProvenance = values.licenseIncludeRegistryProv
	}
	if visited["lockfile-drift-policy"] {
		overrides.LockfileDriftPolicy = values.lockfileDriftPolicy
	}
	return overrides, nil
}

func cliNotificationOverrides(visited map[string]bool, values analyseFlagValues) (notify.Overrides, error) {
	overrides := notify.Overrides{}

	if visited["notify-on"] {
		trigger, err := notify.ParseTrigger(strings.TrimSpace(*values.notifyOn))
		if err != nil {
			return notify.Overrides{}, err
		}
		overrides.GlobalTrigger = &trigger
	}

	if visited["notify-slack"] {
		webhookURL, err := notify.ParseWebhookURL(strings.TrimSpace(*values.notifySlack), "--notify-slack")
		if err != nil {
			return notify.Overrides{}, err
		}
		overrides.SlackWebhookURL = &webhookURL
	}

	if visited["notify-teams"] {
		webhookURL, err := notify.ParseWebhookURL(strings.TrimSpace(*values.notifyTeams), "--notify-teams")
		if err != nil {
			return notify.Overrides{}, err
		}
		overrides.TeamsWebhookURL = &webhookURL
	}

	return overrides, nil
}

func hasThresholdOverrides(overrides thresholds.Overrides) bool {
	return overrides.FailOnIncreasePercent != nil ||
		overrides.LowConfidenceWarningPercent != nil ||
		overrides.MinUsagePercentForRecommendations != nil ||
		overrides.MaxUncertainImportCount != nil ||
		overrides.RemovalCandidateWeightUsage != nil ||
		overrides.RemovalCandidateWeightImpact != nil ||
		overrides.RemovalCandidateWeightConfidence != nil ||
		len(overrides.LicenseDenyList) > 0 ||
		overrides.LicenseFailOnDeny != nil ||
		overrides.LicenseIncludeRegistryProvenance != nil ||
		overrides.LockfileDriftPolicy != nil
}
