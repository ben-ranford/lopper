package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ben-ranford/lopper/internal/safeio"
	"gopkg.in/yaml.v3"
)

const (
	EnvOn           = "LOPPER_NOTIFY_ON"
	EnvSlackWebhook = "LOPPER_NOTIFY_SLACK_WEBHOOK"
	EnvTeamsWebhook = "LOPPER_NOTIFY_TEAMS_WEBHOOK"

	errInvalidNotificationsConfig = "invalid notifications config: %w"
)

func LoadConfigOverrides(path string) (Overrides, error) {
	if strings.TrimSpace(path) == "" {
		return Overrides{}, nil
	}
	data, err := safeio.ReadFile(path)
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

	if value, ok := lookup(EnvOn); ok {
		trigger, err := ParseTrigger(value)
		if err != nil {
			return Overrides{}, fmt.Errorf("invalid %s value %q: %w", EnvOn, strings.TrimSpace(value), err)
		}
		overrides.GlobalTrigger = &trigger
	}

	if value, ok := lookup(EnvSlackWebhook); ok {
		webhookURL, err := ParseWebhookURL(value, EnvSlackWebhook)
		if err != nil {
			return Overrides{}, err
		}
		overrides.SlackWebhookURL = &webhookURL
	}

	if value, ok := lookup(EnvTeamsWebhook); ok {
		webhookURL, err := ParseWebhookURL(value, EnvTeamsWebhook)
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
		root := map[string]json.RawMessage{}
		if err := json.Unmarshal(data, &root); err != nil {
			return rawConfig{}, fmt.Errorf("invalid JSON config: %w", err)
		}
		notificationsRaw, ok := root["notifications"]
		if !ok {
			return cfg, nil
		}
		decoder := json.NewDecoder(bytes.NewReader(notificationsRaw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg.Notifications); err != nil {
			return rawConfig{}, fmt.Errorf(errInvalidNotificationsConfig, err)
		}
	default:
		root := map[string]any{}
		if err := yaml.Unmarshal(data, &root); err != nil {
			return rawConfig{}, fmt.Errorf("invalid YAML config: %w", err)
		}
		notificationsRaw, ok := root["notifications"]
		if !ok {
			return cfg, nil
		}
		encoded, err := yaml.Marshal(notificationsRaw)
		if err != nil {
			return rawConfig{}, fmt.Errorf(errInvalidNotificationsConfig, err)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(encoded))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg.Notifications); err != nil {
			return rawConfig{}, fmt.Errorf(errInvalidNotificationsConfig, err)
		}
	}
	return cfg, nil
}

func (r *rawNotifications) toOverrides() (Overrides, error) {
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
