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
	if resolved.Slack.Trigger != TriggerBreach {
		t.Fatalf("expected global trigger breach on slack, got %q", resolved.Slack.Trigger)
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

func TestLoadConfigOverridesErrors(t *testing.T) {
	if overrides, err := LoadConfigOverrides(""); err != nil || overrides != (Overrides{}) {
		t.Fatalf("expected empty path to return empty overrides, got overrides=%#v err=%v", overrides, err)
	}

	if _, err := LoadConfigOverrides(filepath.Join(t.TempDir(), "missing.yml")); err == nil {
		t.Fatalf("expected missing file error")
	}

	badPath := filepath.Join(t.TempDir(), ".lopper.yml")
	if err := os.WriteFile(badPath, []byte("notifications: ["), 0o600); err != nil {
		t.Fatalf("write bad yaml: %v", err)
	}
	if _, err := LoadConfigOverrides(badPath); err == nil {
		t.Fatalf("expected invalid YAML parse error")
	}

	invalidTriggerPath := filepath.Join(t.TempDir(), "invalid-trigger.yml")
	invalidTrigger := "notifications:\n  on: invalid\n"
	if err := os.WriteFile(invalidTriggerPath, []byte(invalidTrigger), 0o600); err != nil {
		t.Fatalf("write invalid trigger config: %v", err)
	}
	if _, err := LoadConfigOverrides(invalidTriggerPath); err == nil {
		t.Fatalf("expected invalid notifications.on error")
	}

	invalidChannelPath := filepath.Join(t.TempDir(), "invalid-channel.yml")
	invalidChannel := "notifications:\n  slack:\n    on: invalid\n"
	if err := os.WriteFile(invalidChannelPath, []byte(invalidChannel), 0o600); err != nil {
		t.Fatalf("write invalid channel config: %v", err)
	}
	if _, err := LoadConfigOverrides(invalidChannelPath); err == nil {
		t.Fatalf("expected invalid notifications.slack.on error")
	}

	invalidWebhookPath := filepath.Join(t.TempDir(), "invalid-webhook.yml")
	invalidWebhook := "notifications:\n  teams:\n    webhook: hooks.slack.com/services/A/B/SECRET\n"
	if err := os.WriteFile(invalidWebhookPath, []byte(invalidWebhook), 0o600); err != nil {
		t.Fatalf("write invalid webhook config: %v", err)
	}
	if _, err := LoadConfigOverrides(invalidWebhookPath); err == nil {
		t.Fatalf("expected invalid notifications.teams.webhook error")
	}

	unknownNotificationsFieldPath := filepath.Join(t.TempDir(), "unknown-notifications.yml")
	unknownNotificationsField := "notifications:\n  slack:\n    webhok: https://hooks.slack.com/services/A/B/SECRET\n"
	if err := os.WriteFile(unknownNotificationsFieldPath, []byte(unknownNotificationsField), 0o600); err != nil {
		t.Fatalf("write unknown notifications field config: %v", err)
	}
	if _, err := LoadConfigOverrides(unknownNotificationsFieldPath); err == nil {
		t.Fatalf("expected unknown notifications field parse error")
	}

	unknownNotificationsFieldJSONPath := filepath.Join(t.TempDir(), "unknown-notifications.json")
	unknownNotificationsFieldJSON := `{"notifications":{"teams":{"triger":"always","webhook":"https://outlook.office.com/webhook/JSON"}}}`
	if err := os.WriteFile(unknownNotificationsFieldJSONPath, []byte(unknownNotificationsFieldJSON), 0o600); err != nil {
		t.Fatalf("write unknown notifications field JSON config: %v", err)
	}
	if _, err := LoadConfigOverrides(unknownNotificationsFieldJSONPath); err == nil {
		t.Fatalf("expected unknown notifications field JSON parse error")
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
