package notify

import "strings"

type Trigger string

const (
	TriggerAlways      Trigger = "always"
	TriggerBreach      Trigger = "breach"
	TriggerRegression  Trigger = "regression"
	TriggerImprovement Trigger = "improvement"
)

type Channel string

const (
	ChannelSlack Channel = "slack"
	ChannelTeams Channel = "teams"
)

type ChannelConfig struct {
	WebhookURL string
	Trigger    Trigger
}

type Config struct {
	Slack ChannelConfig
	Teams ChannelConfig
}

func DefaultConfig() Config {
	return Config{
		Slack: ChannelConfig{Trigger: TriggerAlways},
		Teams: ChannelConfig{Trigger: TriggerAlways},
	}
}

func (c *Config) HasTargets() bool {
	return strings.TrimSpace(c.Slack.WebhookURL) != "" || strings.TrimSpace(c.Teams.WebhookURL) != ""
}

type Overrides struct {
	GlobalTrigger *Trigger

	SlackWebhookURL *string
	SlackTrigger    *Trigger

	TeamsWebhookURL *string
	TeamsTrigger    *Trigger
}

func (o *Overrides) WithoutWebhookTargets() Overrides {
	if o == nil {
		return Overrides{}
	}
	filtered := *o
	filtered.SlackWebhookURL = nil
	filtered.TeamsWebhookURL = nil
	return filtered
}

func (o *Overrides) Apply(base Config) Config {
	resolved := base

	if o.GlobalTrigger != nil {
		resolved.Slack.Trigger = *o.GlobalTrigger
		resolved.Teams.Trigger = *o.GlobalTrigger
	}

	if o.SlackWebhookURL != nil {
		resolved.Slack.WebhookURL = *o.SlackWebhookURL
	}
	if o.SlackTrigger != nil {
		resolved.Slack.Trigger = *o.SlackTrigger
	}

	if o.TeamsWebhookURL != nil {
		resolved.Teams.WebhookURL = *o.TeamsWebhookURL
	}
	if o.TeamsTrigger != nil {
		resolved.Teams.Trigger = *o.TeamsTrigger
	}

	return resolved
}
