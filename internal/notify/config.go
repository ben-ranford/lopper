package notify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	EnvNotifyOn           = "LOPPER_NOTIFY_ON"
	EnvNotifySlackWebhook = "LOPPER_NOTIFY_SLACK_WEBHOOK"
	EnvNotifyTeamsWebhook = "LOPPER_NOTIFY_TEAMS_WEBHOOK"
)

func LoadConfigOverrides(path string) (Overrides, error) {
	if strings.TrimSpace(path) == "" {
		return Overrides{}, nil
	}
	// #nosec G304 -- config path is controlled via validated CLI config resolution.
	data, err := os.ReadFile(path)
	if err != nil {
		return Overrides{}, fmt.Errorf("read notify config %s: %w", path, err)
	}

	cfg, err := parseRawConfig(path, data)
	if err != nil {
		return Overrides{}, fmt.Errorf("parse notify config %s: %w", path, err)
	}

	return cfg.Notifications.toOverrides()
}

func LoadEnvOverrides(lookup func(string) (string, bool)) (Overrides, error) {
	overrides := Overrides{}

	if value, ok := lookup(EnvNotifyOn); ok {
		trigger, err := ParseTrigger(value)
		if err != nil {
			return Overrides{}, fmt.Errorf("invalid %s value %q: %w", EnvNotifyOn, strings.TrimSpace(value), err)
		}
		overrides.GlobalTrigger = &trigger
	}

	if value, ok := lookup(EnvNotifySlackWebhook); ok {
		webhookURL, err := ParseWebhookURL(value, EnvNotifySlackWebhook)
		if err != nil {
			return Overrides{}, err
		}
		overrides.SlackWebhookURL = &webhookURL
	}

	if value, ok := lookup(EnvNotifyTeamsWebhook); ok {
		webhookURL, err := ParseWebhookURL(value, EnvNotifyTeamsWebhook)
		if err != nil {
			return Overrides{}, err
		}
		overrides.TeamsWebhookURL = &webhookURL
	}

	return overrides, nil
}

type rawConfig struct {
	Notifications rawNotifications `yaml:"notifications" json:"notifications"`
}

type rawNotifications struct {
	On    *string            `yaml:"on" json:"on"`
	Slack rawChannelSettings `yaml:"slack" json:"slack"`
	Teams rawChannelSettings `yaml:"teams" json:"teams"`
}

type rawChannelSettings struct {
	Webhook *string `yaml:"webhook" json:"webhook"`
	On      *string `yaml:"on" json:"on"`
}

func parseRawConfig(path string, data []byte) (rawConfig, error) {
	cfg := rawConfig{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		if err := json.Unmarshal(data, &cfg); err != nil {
			return rawConfig{}, fmt.Errorf("invalid JSON config: %w", err)
		}
	default:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return rawConfig{}, fmt.Errorf("invalid YAML config: %w", err)
		}
	}
	return cfg, nil
}

func (r rawNotifications) toOverrides() (Overrides, error) {
	overrides := Overrides{}

	if r.On != nil {
		trigger, err := ParseTrigger(*r.On)
		if err != nil {
			return Overrides{}, fmt.Errorf("invalid notifications.on value %q: %w", strings.TrimSpace(*r.On), err)
		}
		overrides.GlobalTrigger = &trigger
	}

	if r.Slack.Webhook != nil {
		value, err := ParseWebhookURL(*r.Slack.Webhook, "notifications.slack.webhook")
		if err != nil {
			return Overrides{}, err
		}
		overrides.SlackWebhookURL = &value
	}
	if r.Slack.On != nil {
		trigger, err := ParseTrigger(*r.Slack.On)
		if err != nil {
			return Overrides{}, fmt.Errorf("invalid notifications.slack.on value %q: %w", strings.TrimSpace(*r.Slack.On), err)
		}
		overrides.SlackTrigger = &trigger
	}

	if r.Teams.Webhook != nil {
		value, err := ParseWebhookURL(*r.Teams.Webhook, "notifications.teams.webhook")
		if err != nil {
			return Overrides{}, err
		}
		overrides.TeamsWebhookURL = &value
	}
	if r.Teams.On != nil {
		trigger, err := ParseTrigger(*r.Teams.On)
		if err != nil {
			return Overrides{}, fmt.Errorf("invalid notifications.teams.on value %q: %w", strings.TrimSpace(*r.Teams.On), err)
		}
		overrides.TeamsTrigger = &trigger
	}

	return overrides, nil
}
