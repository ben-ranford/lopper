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
	Repos         []ConfigRepo `yaml:"repos"`
	BaselineStore string       `yaml:"baseline_store"`
	Output        string       `yaml:"output"`
}

type ConfigRepo struct {
	Path     string `yaml:"path"`
	Name     string `yaml:"name"`
	Language string `yaml:"language"`
	RepoURL  string `yaml:"repoUrl"`
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
