package dashboard

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Dashboard ConfigDashboard `yaml:"dashboard"`
}

type ConfigDashboard struct {
	Repos         []ConfigRepo    `yaml:"repos"`
	BaselineStore string          `yaml:"baseline_store"`
	Output        string          `yaml:"output"`
	Ownership     ConfigOwnership `yaml:"ownership"`
}

type ConfigRepo struct {
	Path     string `yaml:"path"`
	Name     string `yaml:"name"`
	Language string `yaml:"language"`
	RepoURL  string `yaml:"repoUrl"`
	Branch   string `yaml:"branch"`
	Tag      string `yaml:"tag"`
	Commit   string `yaml:"commit"`
}

type ConfigOwnership struct {
	DefaultOwner  string                `yaml:"default_owner"`
	DefaultTeam   string                `yaml:"default_team"`
	DefaultStatus string                `yaml:"default_status"`
	DefaultDue    string                `yaml:"default_due"`
	Rules         []ConfigOwnershipRule `yaml:"rules"`
}

type ConfigOwnershipRule struct {
	Repo       string `yaml:"repo"`
	PathPrefix string `yaml:"path_prefix"`
	Category   string `yaml:"category"`
	Dependency string `yaml:"dependency"`
	Owner      string `yaml:"owner"`
	Team       string `yaml:"team"`
	Due        string `yaml:"due"`
	Status     string `yaml:"status"`
}

type LoadedConfig struct {
	Path      string
	ConfigDir string
	Dashboard ConfigDashboard
}

func LoadConfig(path string) (LoadedConfig, error) {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return LoadedConfig{}, fmt.Errorf("dashboard config path is required")
	}

	data, err := safeio.ReadFile(trimmedPath)
	if err != nil {
		return LoadedConfig{}, err
	}

	parsed := Config{}
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return LoadedConfig{}, err
	}

	if len(parsed.Dashboard.Repos) == 0 {
		return LoadedConfig{}, fmt.Errorf("dashboard config must define at least one repo")
	}

	return LoadedConfig{
		Path:      trimmedPath,
		ConfigDir: filepath.Dir(trimmedPath),
		Dashboard: parsed.Dashboard,
	}, nil
}
