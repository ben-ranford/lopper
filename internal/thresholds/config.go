package thresholds

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func Load(repoPath string, explicitPath string) (Overrides, string, error) {
	repoAbs, err := filepath.Abs(repoPath)
	if err != nil {
		return Overrides{}, "", fmt.Errorf("resolve repo path: %w", err)
	}

	configPath, found, err := resolveConfigPath(repoAbs, strings.TrimSpace(explicitPath))
	if err != nil {
		return Overrides{}, "", err
	}
	if !found {
		return Overrides{}, "", nil
	}

	overrides, err := loadOverridesFromPath(configPath)
	if err != nil {
		return Overrides{}, "", err
	}
	return overrides, configPath, nil
}

func resolveConfigPath(repoPath string, explicitPath string) (string, bool, error) {
	if explicitPath != "" {
		candidate := explicitPath
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(repoPath, candidate)
		}
		candidate = filepath.Clean(candidate)
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				return "", false, fmt.Errorf("config file not found: %s", candidate)
			}
			return "", false, fmt.Errorf("read config file %s: %w", candidate, err)
		}
		return candidate, true, nil
	}

	for _, name := range []string{".lopper.yml", ".lopper.yaml", "lopper.json"} {
		candidate := filepath.Join(repoPath, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, fmt.Errorf("read config file %s: %w", candidate, err)
		}
	}

	return "", false, nil
}

func loadOverridesFromPath(path string) (Overrides, error) {
	// #nosec G304 -- config path is either explicitly provided by user or discovered in repo root.
	data, err := os.ReadFile(path)
	if err != nil {
		return Overrides{}, fmt.Errorf("read config file %s: %w", path, err)
	}

	cfg, err := parseConfig(path, data)
	if err != nil {
		return Overrides{}, err
	}
	overrides, err := cfg.toOverrides()
	if err != nil {
		return Overrides{}, fmt.Errorf("parse config file %s: %w", path, err)
	}
	if err := overrides.Validate(); err != nil {
		return Overrides{}, fmt.Errorf("parse config file %s: %w", path, err)
	}
	return overrides, nil
}

func parseConfig(path string, data []byte) (rawConfig, error) {
	var cfg rawConfig
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return rawConfig{}, fmt.Errorf("invalid JSON config: %w", err)
		}
		if decoder.More() {
			return rawConfig{}, fmt.Errorf("invalid JSON config: multiple JSON values")
		}
	default:
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return rawConfig{}, fmt.Errorf("invalid YAML config: %w", err)
		}
	}
	return cfg, nil
}

type rawConfig struct {
	Thresholds rawThresholds `yaml:"thresholds" json:"thresholds"`

	FailOnIncreasePercent             *int `yaml:"fail_on_increase_percent" json:"fail_on_increase_percent"`
	LowConfidenceWarningPercent       *int `yaml:"low_confidence_warning_percent" json:"low_confidence_warning_percent"`
	MinUsagePercentForRecommendations *int `yaml:"min_usage_percent_for_recommendations" json:"min_usage_percent_for_recommendations"`
}

type rawThresholds struct {
	FailOnIncreasePercent             *int `yaml:"fail_on_increase_percent" json:"fail_on_increase_percent"`
	LowConfidenceWarningPercent       *int `yaml:"low_confidence_warning_percent" json:"low_confidence_warning_percent"`
	MinUsagePercentForRecommendations *int `yaml:"min_usage_percent_for_recommendations" json:"min_usage_percent_for_recommendations"`
}

func (cfg rawConfig) toOverrides() (Overrides, error) {
	overrides := Overrides{
		FailOnIncreasePercent:             cfg.FailOnIncreasePercent,
		LowConfidenceWarningPercent:       cfg.LowConfidenceWarningPercent,
		MinUsagePercentForRecommendations: cfg.MinUsagePercentForRecommendations,
	}
	if err := applyNestedOverride("fail_on_increase_percent", &overrides.FailOnIncreasePercent, cfg.Thresholds.FailOnIncreasePercent); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedOverride("low_confidence_warning_percent", &overrides.LowConfidenceWarningPercent, cfg.Thresholds.LowConfidenceWarningPercent); err != nil {
		return Overrides{}, err
	}
	if err := applyNestedOverride("min_usage_percent_for_recommendations", &overrides.MinUsagePercentForRecommendations, cfg.Thresholds.MinUsagePercentForRecommendations); err != nil {
		return Overrides{}, err
	}
	return overrides, nil
}

func applyNestedOverride(name string, target **int, nested *int) error {
	if nested == nil {
		return nil
	}
	if *target != nil {
		return fmt.Errorf("threshold %s is defined more than once", name)
	}
	*target = nested
	return nil
}
