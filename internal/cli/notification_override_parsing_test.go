package cli

import "testing"

func TestCLINotificationOverrideAdditionalBranches(t *testing.T) {
	slackWebhook := "https://example.com/slack-hook"
	teamsWebhook := "ftp://example.com/teams-hook"
	values := analyseFlagValues{
		notifySlack: &slackWebhook,
		notifyTeams: &teamsWebhook,
	}

	overrides, err := cliNotificationOverrides(map[string]bool{"notify-slack": true}, values)
	if err != nil {
		t.Fatalf("resolve slack notification override: %v", err)
	}
	if overrides.SlackWebhookURL == nil || *overrides.SlackWebhookURL != slackWebhook {
		t.Fatalf("expected slack webhook override to be set, got %#v", overrides)
	}

	if _, err := cliNotificationOverrides(map[string]bool{"notify-teams": true}, values); err == nil {
		t.Fatalf("expected invalid teams webhook override to fail")
	}
}
