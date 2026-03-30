package notify

import "testing"

func TestOverridesWithoutWebhookTargetsNilReceiver(t *testing.T) {
	var overrides *Overrides
	if got := overrides.WithoutWebhookTargets(); got != (Overrides{}) {
		t.Fatalf("expected empty overrides for nil receiver, got %#v", got)
	}
}

func TestOverridesWithoutWebhookTargetsClearsWebhookURLs(t *testing.T) {
	globalTrigger := TriggerAlways
	slackTrigger := TriggerRegression
	teamsTrigger := TriggerImprovement
	slackURL := "https://hooks.slack.com/services/T/B/K"
	teamsURL := "https://example.com/teams"

	overrides := Overrides{
		GlobalTrigger:   &globalTrigger,
		SlackWebhookURL: &slackURL,
		SlackTrigger:    &slackTrigger,
		TeamsWebhookURL: &teamsURL,
		TeamsTrigger:    &teamsTrigger,
	}

	filtered := overrides.WithoutWebhookTargets()

	if filtered.SlackWebhookURL != nil || filtered.TeamsWebhookURL != nil {
		t.Fatalf("expected webhook URLs to be removed, got %#v", filtered)
	}
	if filtered.GlobalTrigger != overrides.GlobalTrigger {
		t.Fatalf("expected global trigger to be preserved")
	}
	if filtered.SlackTrigger != overrides.SlackTrigger {
		t.Fatalf("expected slack trigger to be preserved")
	}
	if filtered.TeamsTrigger != overrides.TeamsTrigger {
		t.Fatalf("expected teams trigger to be preserved")
	}
	if overrides.SlackWebhookURL == nil || overrides.TeamsWebhookURL == nil {
		t.Fatalf("expected original overrides to remain unchanged, got %#v", overrides)
	}
}
