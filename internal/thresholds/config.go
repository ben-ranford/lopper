package thresholds

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
	"gopkg.in/yaml.v3"
)

const readConfigFileErrFmt = "read config file %s: %w"

func Load(repoPath, explicitPath string) (Overrides, string, error) {
	repoAbs, err := filepath.Abs(repoPath)
	if err != nil {
		return Overrides{}, "", fmt.Errorf("resolve repo path: %w", err)
	}
	explicitProvided := strings.TrimSpace(explicitPath) != ""

	configPath, found, err := resolveConfigPath(repoAbs, strings.TrimSpace(explicitPath))
	if err != nil {
		return Overrides{}, "", err
	}
	if !found {
		return Overrides{}, "", nil
	}

	overrides, err := loadOverridesFromPath(repoAbs, configPath, explicitProvided)
	if err != nil {
		return Overrides{}, "", err
	}
	return overrides, configPath, nil
}

func resolveConfigPath(repoPath, explicitPath string) (string, bool, error) {
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
			return "", false, fmt.Errorf(readConfigFileErrFmt, candidate, err)
		}
		return candidate, true, nil
	}

	for _, name := range []string{".lopper.yml", ".lopper.yaml", "lopper.json"} {
		candidate := filepath.Join(repoPath, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true, nil
		} else if err != nil && !os.IsNotExist(err) {
			return "", false, fmt.Errorf(readConfigFileErrFmt, candidate, err)
		}
	}

	return "", false, nil
}

func loadOverridesFromPath(repoPath, path string, explicitProvided bool) (Overrides, error) {
	data, err := readConfigFile(repoPath, path, explicitProvided)
	if err != nil {
		return Overrides{}, fmt.Errorf(readConfigFileErrFmt, path, err)
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

func readConfigFile(repoPath, path string, explicitProvided bool) ([]byte, error) {
	if !explicitProvided || isPathUnderRoot(repoPath, path) {
		return safeio.ReadFileUnder(repoPath, path)
	}
	return safeio.ReadFile(path)
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

func isPathUnderRoot(rootPath, targetPath string) bool {
	relative, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
