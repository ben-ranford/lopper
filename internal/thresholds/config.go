package thresholds

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	readConfigFileErrFmt = "read config file %s: %w"
	parseConfigErrFmt    = "parse config file %s: %w"
	defaultPolicySource  = "defaults"
)

type LoadResult struct {
	Overrides     Overrides
	Resolved      Values
	Scope         PathScope
	Features      FeatureConfig
	ConfigPath    string
	PolicySources []string
}

type PathScope struct {
	Include []string
	Exclude []string
}

type FeatureConfig struct {
	Enable  []string
	Disable []string
}

func Load(repoPath, explicitPath string) (Overrides, string, error) {
	result, err := LoadWithPolicy(repoPath, explicitPath)
	if err != nil {
		return Overrides{}, "", err
	}
	return result.Overrides, result.ConfigPath, nil
}

func LoadWithPolicy(repoPath, explicitPath string) (LoadResult, error) {
	repoAbs, err := filepath.Abs(repoPath)
	if err != nil {
		return LoadResult{}, fmt.Errorf("resolve repo path: %w", err)
	}
	explicitProvided := strings.TrimSpace(explicitPath) != ""

	configPath, found, err := resolveConfigPath(repoAbs, strings.TrimSpace(explicitPath))
	if err != nil {
		return LoadResult{}, err
	}
	if !found {
		return LoadResult{
			Resolved:      Defaults(),
			Scope:         PathScope{},
			PolicySources: []string{defaultPolicySource},
		}, nil
	}

	resolver := newPackResolver(repoAbs)
	mergeResult, err := resolver.resolveFile(configPath, initialPackTrust(repoAbs, explicitProvided))
	if err != nil {
		return LoadResult{}, err
	}
	if err := mergeResult.overrides.Validate(); err != nil {
		return LoadResult{}, fmt.Errorf(parseConfigErrFmt, configPath, err)
	}

	resolved := mergeResult.overrides.Apply(Defaults())
	if err := resolved.Validate(); err != nil {
		return LoadResult{}, fmt.Errorf(parseConfigErrFmt, configPath, err)
	}

	return LoadResult{
		Overrides:     mergeResult.overrides,
		Resolved:      resolved,
		Scope:         mergeResult.scope,
		Features:      mergeResult.features,
		ConfigPath:    configPath,
		PolicySources: mergeResult.policySourcesHighToLow(),
	}, nil
}

type rawConfig struct {
	Policy rawPolicy `yaml:"policy" json:"policy"`
	Scope  rawScope  `yaml:"scope" json:"scope"`
	// Feature flags are resolved by the cli package; keep this field here so shared config parsing
	// preserves unknown-field validation while accepting the feature section.
	Features rawFeatures `yaml:"features" json:"features"`
	// Notifications are parsed by the notify package; keep this field so threshold parsing accepts shared config files.
	Notifications map[string]any `yaml:"notifications" json:"notifications"`

	Thresholds rawThresholds `yaml:"thresholds" json:"thresholds"`

	FailOnIncreasePercent             *int     `yaml:"fail_on_increase_percent" json:"fail_on_increase_percent"`
	LowConfidenceWarningPercent       *int     `yaml:"low_confidence_warning_percent" json:"low_confidence_warning_percent"`
	MinUsagePercentForRecommendations *int     `yaml:"min_usage_percent_for_recommendations" json:"min_usage_percent_for_recommendations"`
	MaxUncertainImportCount           *int     `yaml:"max_uncertain_import_count" json:"max_uncertain_import_count"`
	RemovalCandidateWeightUsage       *float64 `yaml:"removal_candidate_weight_usage" json:"removal_candidate_weight_usage"`
	RemovalCandidateWeightImpact      *float64 `yaml:"removal_candidate_weight_impact" json:"removal_candidate_weight_impact"`
	RemovalCandidateWeightConfidence  *float64 `yaml:"removal_candidate_weight_confidence" json:"removal_candidate_weight_confidence"`
	LockfileDriftPolicy               *string  `yaml:"lockfile_drift_policy" json:"lockfile_drift_policy"`
	LicenseDeny                       []string `yaml:"license_deny" json:"license_deny"`
	LicenseFailOnDeny                 *bool    `yaml:"license_fail_on_deny" json:"license_fail_on_deny"`
	LicenseIncludeRegistryProvenance  *bool    `yaml:"license_include_registry_provenance" json:"license_include_registry_provenance"`
}

type rawPolicy struct {
	Packs []string `yaml:"packs" json:"packs"`
}

type rawScope struct {
	Include []string `yaml:"include" json:"include"`
	Exclude []string `yaml:"exclude" json:"exclude"`
}

type rawFeatures struct {
	Enable  []string `yaml:"enable" json:"enable"`
	Disable []string `yaml:"disable" json:"disable"`
}

type rawThresholds struct {
	FailOnIncreasePercent             *int     `yaml:"fail_on_increase_percent" json:"fail_on_increase_percent"`
	LowConfidenceWarningPercent       *int     `yaml:"low_confidence_warning_percent" json:"low_confidence_warning_percent"`
	MinUsagePercentForRecommendations *int     `yaml:"min_usage_percent_for_recommendations" json:"min_usage_percent_for_recommendations"`
	MaxUncertainImportCount           *int     `yaml:"max_uncertain_import_count" json:"max_uncertain_import_count"`
	RemovalCandidateWeightUsage       *float64 `yaml:"removal_candidate_weight_usage" json:"removal_candidate_weight_usage"`
	RemovalCandidateWeightImpact      *float64 `yaml:"removal_candidate_weight_impact" json:"removal_candidate_weight_impact"`
	RemovalCandidateWeightConfidence  *float64 `yaml:"removal_candidate_weight_confidence" json:"removal_candidate_weight_confidence"`
	LockfileDriftPolicy               *string  `yaml:"lockfile_drift_policy" json:"lockfile_drift_policy"`
	LicenseDeny                       []string `yaml:"license_deny" json:"license_deny"`
	LicenseFailOnDeny                 *bool    `yaml:"license_fail_on_deny" json:"license_fail_on_deny"`
	LicenseIncludeRegistryProvenance  *bool    `yaml:"license_include_registry_provenance" json:"license_include_registry_provenance"`
}
