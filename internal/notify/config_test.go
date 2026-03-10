package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lopper.yml")
	content := strings.Join([]string{
		"notifications:",
		"  on: breach",
		"  slack:",
		"    webhook: https://hooks.slack.com/services/A/B/CONFIG",
		"  teams:",
		"    webhook: https://outlook.office.com/webhook/CONFIG",
		"    on: improvement",
		"",
	}, "\n")
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
}

func TestLoadEnvOverrides(t *testing.T) {
	t.Setenv(EnvNotifyOn, "regression")
	t.Setenv(EnvNotifySlackWebhook, "https://hooks.slack.com/services/A/B/ENV")
	t.Setenv(EnvNotifyTeamsWebhook, "https://outlook.office.com/webhook/ENV")

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
	t.Setenv(EnvNotifyOn, "bad")
	if _, err := LoadEnvOverrides(os.LookupEnv); err == nil {
		t.Fatalf("expected invalid trigger env error")
	}

	t.Setenv(EnvNotifyOn, "")
	t.Setenv(EnvNotifySlackWebhook, "hooks.slack.com/services/A/B/SECRET")
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
