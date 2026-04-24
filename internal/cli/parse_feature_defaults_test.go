package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/featureflags"
	"github.com/ben-ranford/lopper/internal/thresholds"
)

func TestResolveDefaultFeatureSetUsesBuildContext(t *testing.T) {
	t.Run("rolling build enables preview and stable defaults", func(t *testing.T) {
		withFeatureRegistry(t, featureflags.ChannelRolling, nil)
		features, err := resolveDefaultFeatureSet()
		if err != nil {
			t.Fatalf("resolve rolling default feature set: %v", err)
		}
		if !features.Enabled("preview-flag") || !features.Enabled("stable-flag") {
			t.Fatalf("expected rolling defaults to enable preview and stable flags")
		}
	})

	t.Run("release lock default-on includes preview", func(t *testing.T) {
		lock := &featureflags.ReleaseLock{Release: "v1.4.2", DefaultOn: []string{"preview-flag"}}
		withFeatureRegistry(t, featureflags.ChannelRelease, lock)
		features, err := resolveDefaultFeatureSet()
		if err != nil {
			t.Fatalf("resolve release default feature set: %v", err)
		}
		if !features.Enabled("preview-flag") || !features.Enabled("stable-flag") {
			t.Fatalf("expected release lock defaults to include preview and stable flags")
		}
	})
}

func TestResolveFeatureBuildContextErrors(t *testing.T) {
	t.Run("registry validation failure", func(t *testing.T) {
		oldValidate := validateFeatureRegistry
		validateFeatureRegistry = func() error { return errors.New("registry invalid") }
		t.Cleanup(func() { validateFeatureRegistry = oldValidate })

		if _, err := resolveDefaultFeatureSet(); err == nil || !strings.Contains(err.Error(), "registry invalid") {
			t.Fatalf("expected registry validation error, got %v", err)
		}
	})

	t.Run("invalid build channel", func(t *testing.T) {
		oldValidate := validateFeatureRegistry
		oldBuildChannel := featureBuildChannel
		validateFeatureRegistry = func() error { return nil }
		featureBuildChannel = func() string { return "not-a-channel" }
		t.Cleanup(func() {
			validateFeatureRegistry = oldValidate
			featureBuildChannel = oldBuildChannel
		})

		if _, _, err := resolveFeatureBuildContext(); err == nil {
			t.Fatalf("expected invalid build channel error")
		}
	})

	t.Run("release lock resolution failure", func(t *testing.T) {
		oldValidate := validateFeatureRegistry
		oldBuildChannel := featureBuildChannel
		oldReleaseVersion := featureReleaseVersion
		oldReleaseLockProvider := featureReleaseLockProvider
		validateFeatureRegistry = func() error { return nil }
		featureBuildChannel = func() string { return string(featureflags.ChannelRelease) }
		featureReleaseVersion = func() string { return "v1.4.2" }
		featureReleaseLockProvider = func(string) (*featureflags.ReleaseLock, error) { return nil, errors.New("lock failure") }
		t.Cleanup(func() {
			validateFeatureRegistry = oldValidate
			featureBuildChannel = oldBuildChannel
			featureReleaseVersion = oldReleaseVersion
			featureReleaseLockProvider = oldReleaseLockProvider
		})

		if _, err := resolveDefaultFeatureSet(); err == nil || !strings.Contains(err.Error(), "lock failure") {
			t.Fatalf("expected release lock failure error, got %v", err)
		}
	})
}

func TestResolveAnalyseFeaturesPropagatesBuildContextErrors(t *testing.T) {
	oldValidate := validateFeatureRegistry
	validateFeatureRegistry = func() error { return errors.New("registry invalid") }
	t.Cleanup(func() { validateFeatureRegistry = oldValidate })

	_, err := resolveAnalyseFeatures(map[string]bool{}, analyseFlagValues{}, thresholds.FeatureConfig{})
	if err == nil || !strings.Contains(err.Error(), "registry invalid") {
		t.Fatalf("expected resolveAnalyseFeatures to propagate build-context error, got %v", err)
	}
}
