package thresholds

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func parseConfig(path string, data []byte) (rawConfig, error) {
	var cfg rawConfig
	switch configExtension(path) {
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
		var extra any
		if err := decoder.Decode(&extra); err == nil {
			return rawConfig{}, fmt.Errorf("invalid YAML config: multiple YAML documents")
		} else if err != io.EOF {
			return rawConfig{}, fmt.Errorf("invalid YAML config: %w", err)
		}
	}
	return cfg, nil
}

func configExtension(location string) string {
	if parsed, ok := parseRemoteURL(location); ok {
		return strings.ToLower(filepath.Ext(parsed.Path))
	}
	return strings.ToLower(filepath.Ext(location))
}
