package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lopper.yml")
	content := `notifications:
  on: breach
  slack:
    webhook: https://hooks.slack.com/services/A/B/CONFIG
    on: always
  teams:
    webhook: https://outlook.office.com/webhook/CONFIG
    on: improvement
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	overrides, err := LoadConfigOverrides(path)
	if err != nil {
		t.Fatalf("load config overrides: %v", err)
	}

	resolved := overrides.Apply(DefaultConfig())
	if resolved.Slack.Trigger != TriggerAlways {
		t.Fatalf("expected slack trigger always override, got %q", resolved.Slack.Trigger)
	}
	if resolved.Teams.Trigger != TriggerImprovement {
		t.Fatalf("expected teams trigger improvement, got %q", resolved.Teams.Trigger)
	}
	if resolved.Slack.WebhookURL == "" || resolved.Teams.WebhookURL == "" {
		t.Fatalf("expected webhook URLs to be loaded from config")
	}
}

func TestLoadConfigOverridesJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lopper.json")
	content := `{"notifications":{"on":"regression","teams":{"on":"breach","webhook":"https://outlook.office.com/webhook/JSON"}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write json config: %v", err)
	}

	overrides, err := LoadConfigOverrides(path)
	if err != nil {
		t.Fatalf("load json config overrides: %v", err)
	}
	resolved := overrides.Apply(DefaultConfig())
	if resolved.Slack.Trigger != TriggerRegression {
		t.Fatalf("expected global trigger regression on slack, got %q", resolved.Slack.Trigger)
	}
	if resolved.Teams.Trigger != TriggerBreach {
		t.Fatalf("expected teams trigger breach override, got %q", resolved.Teams.Trigger)
	}
}

func writeConfigFile(t *testing.T, dir string, name string, content string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config %s: %v", name, err)
	}
	return path
}

func requireLoadConfigOverridesError(t *testing.T, path string) {
	t.Helper()

	if _, err := LoadConfigOverrides(path); err == nil {
		t.Fatalf("expected LoadConfigOverrides(%q) to fail", path)
	}
}

func TestLoadConfigOverridesErrors(t *testing.T) {
	t.Run("empty path returns empty overrides", func(t *testing.T) {
		if overrides, err := LoadConfigOverrides(""); err != nil || overrides != (Overrides{}) {
			t.Fatalf("expected empty path to return empty overrides, got overrides=%#v err=%v", overrides, err)
		}
	})

	t.Run("missing file fails", func(t *testing.T) {
		requireLoadConfigOverridesError(t, filepath.Join(t.TempDir(), "missing.yml"))
	})

	tempDir := t.TempDir()
	cases := []struct {
		name    string
		file    string
		content string
	}{
		{
			name:    "invalid yaml",
			file:    ".lopper.yml",
			content: "notifications: [",
		},
		{
			name:    "invalid global trigger",
			file:    "invalid-trigger.yml",
			content: "notifications:\n  on: invalid\n",
		},
		{
			name:    "invalid slack trigger",
			file:    "invalid-channel.yml",
			content: "notifications:\n  slack:\n    on: invalid\n",
		},
		{
			name:    "invalid teams trigger",
			file:    "invalid-teams-trigger.yml",
			content: "notifications:\n  teams:\n    on: invalid\n",
		},
		{
			name:    "invalid teams webhook",
			file:    "invalid-webhook.yml",
			content: "notifications:\n  teams:\n    webhook: hooks.slack.com/services/A/B/SECRET\n",
		},
		{
			name:    "invalid slack webhook",
			file:    "invalid-slack-webhook.yml",
			content: "notifications:\n  slack:\n    webhook: outlook.office.com/webhook/SECRET\n",
		},
		{
			name:    "unknown yaml field",
			file:    "unknown-notifications.yml",
			content: "notifications:\n  slack:\n    webhok: https://hooks.slack.com/services/A/B/SECRET\n",
		},
		{
			name:    "unknown json field",
			file:    "unknown-notifications.json",
			content: `{"notifications":{"teams":{"triger":"always","webhook":"https://outlook.office.com/webhook/JSON"}}}`,
		},
		{
			name:    "invalid json",
			file:    "invalid.json",
			content: "{",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfigFile(t, tempDir, tc.file, tc.content)
			requireLoadConfigOverridesError(t, path)
		})
	}
}

func TestLoadConfigOverridesWithoutNotificationsSection(t *testing.T) {
	yamlPath := filepath.Join(t.TempDir(), "lopper.yml")
	yamlConfig := "thresholds:\n  failOnIncreasePercent: 3\n"
	if err := os.WriteFile(yamlPath, []byte(yamlConfig), 0o600); err != nil {
		t.Fatalf("write yaml config without notifications: %v", err)
	}
	yamlOverrides, err := LoadConfigOverrides(yamlPath)
	if err != nil {
		t.Fatalf("load yaml overrides without notifications: %v", err)
	}
	if yamlOverrides != (Overrides{}) {
		t.Fatalf("expected empty yaml overrides without notifications, got %#v", yamlOverrides)
	}

	jsonPath := filepath.Join(t.TempDir(), "lopper.json")
	jsonConfig := `{"thresholds":{"failOnIncreasePercent":3}}`
	if err := os.WriteFile(jsonPath, []byte(jsonConfig), 0o600); err != nil {
		t.Fatalf("write json config without notifications: %v", err)
	}
	jsonOverrides, err := LoadConfigOverrides(jsonPath)
	if err != nil {
		t.Fatalf("load json overrides without notifications: %v", err)
	}
	if jsonOverrides != (Overrides{}) {
		t.Fatalf("expected empty json overrides without notifications, got %#v", jsonOverrides)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv(EnvOn, "regression")
	t.Setenv(EnvSlackWebhook, "https://hooks.slack.com/services/A/B/ENV")
	t.Setenv(EnvTeamsWebhook, "https://outlook.office.com/webhook/ENV")

	overrides, err := LoadEnvOverrides(os.LookupEnv)
	if err != nil {
		t.Fatalf("load env overrides: %v", err)
	}

	resolved := overrides.Apply(DefaultConfig())
	if resolved.Slack.Trigger != TriggerRegression || resolved.Teams.Trigger != TriggerRegression {
		t.Fatalf("expected global trigger to apply to both channels, got slack=%q teams=%q", resolved.Slack.Trigger, resolved.Teams.Trigger)
	}
}

func TestLoadEnvOverridesInvalidValues(t *testing.T) {
	t.Setenv(EnvOn, "bad")
	if _, err := LoadEnvOverrides(os.LookupEnv); err == nil {
		t.Fatalf("expected invalid trigger env error")
	}

	t.Setenv(EnvOn, "")
	t.Setenv(EnvSlackWebhook, "hooks.slack.com/services/A/B/SECRET")
	if _, err := LoadEnvOverrides(os.LookupEnv); err == nil {
		t.Fatalf("expected invalid webhook env error")
	}

	t.Setenv(EnvSlackWebhook, "")
	t.Setenv(EnvTeamsWebhook, "outlook.office.com/webhook/SECRET")
	if _, err := LoadEnvOverrides(os.LookupEnv); err == nil {
		t.Fatalf("expected invalid teams webhook env error")
	}
}

func TestParseWebhookURLRedactionOnError(t *testing.T) {
	_, err := ParseWebhookURL("hooks.slack.com/services/A/B/SECRET", "--notify-slack")
	if err == nil {
		t.Fatalf("expected invalid webhook URL error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("expected error message to avoid leaking secret URL path, got %q", err.Error())
	}
}
