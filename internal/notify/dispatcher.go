package notify

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/ben-ranford/lopper/internal/report"
)

type Delivery struct {
	Channel    Channel
	WebhookURL string
	Trigger    Trigger
	Report     report.Report
	Outcome    Outcome
}

type Notifier interface {
	Notify(ctx context.Context, delivery Delivery) error
}

type Dispatcher struct {
	notifiers map[Channel]Notifier
}

func NewDispatcher(notifiers map[Channel]Notifier) *Dispatcher {
	copied := make(map[Channel]Notifier, len(notifiers))
	for channel, notifier := range notifiers {
		copied[channel] = notifier
	}
	return &Dispatcher{notifiers: copied}
}

func NewDefaultDispatcher() *Dispatcher {
	notifier := NewWebhookNotifier(nil)
	return NewDispatcher(map[Channel]Notifier{
		ChannelSlack: notifier,
		ChannelTeams: notifier,
	})
}

func (d *Dispatcher) Dispatch(ctx context.Context, cfg Config, reportData report.Report, outcome Outcome) []string {
	if d == nil {
		return nil
	}

	warnings := make([]string, 0)
	deliveries := []Delivery{
		{
			Channel:    ChannelSlack,
			WebhookURL: strings.TrimSpace(cfg.Slack.WebhookURL),
			Trigger:    cfg.Slack.Trigger,
			Report:     reportData,
			Outcome:    outcome,
		},
		{
			Channel:    ChannelTeams,
			WebhookURL: strings.TrimSpace(cfg.Teams.WebhookURL),
			Trigger:    cfg.Teams.Trigger,
			Report:     reportData,
			Outcome:    outcome,
		},
	}

	for _, delivery := range deliveries {
		if delivery.WebhookURL == "" {
			continue
		}
		if !ShouldTrigger(delivery.Trigger, outcome) {
			continue
		}
		notifier, ok := d.notifiers[delivery.Channel]
		if !ok || notifier == nil {
			warnings = append(warnings, fmt.Sprintf("notification skipped for %s (%s): notifier is not configured", delivery.Channel, RedactWebhookURL(delivery.WebhookURL)))
			continue
		}
		if err := notifier.Notify(ctx, delivery); err != nil {
			warnings = append(warnings, fmt.Sprintf(
				"notification delivery failed for %s (%s): %s",
				delivery.Channel,
				RedactWebhookURL(delivery.WebhookURL),
				sanitizeErrorMessage(err, delivery.WebhookURL),
			))
		}
	}

	return warnings
}

func sanitizeErrorMessage(err error, webhookURL string) string {
	if err == nil {
		return "request failed"
	}

	message := err.Error()
	if strings.TrimSpace(webhookURL) == "" {
		return message
	}

	redacted := RedactWebhookURL(webhookURL)
	replacements := []string{
		webhookURL,
		url.QueryEscape(webhookURL),
	}
	for _, candidate := range replacements {
		if candidate == "" {
			continue
		}
		message = strings.ReplaceAll(message, candidate, redacted)
	}
	return message
}
