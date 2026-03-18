package notify

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

type fakeNotifier struct {
	err      error
	calls    int
	delivery Delivery
}

const testWebhookURL = "https://hooks.slack.com/services/T000/B000/SECRET"

func (f *fakeNotifier) Notify(_ context.Context, delivery Delivery) error {
	f.calls++
	f.delivery = delivery
	if f.err != nil {
		return f.err
	}
	return nil
}

func TestDispatcherDispatchesByTrigger(t *testing.T) {
	slack := &fakeNotifier{}
	teams := &fakeNotifier{}
	dispatcher := NewDispatcher(map[Channel]Notifier{
		ChannelSlack: slack,
		ChannelTeams: teams,
	})
	cfg := DefaultConfig()
	cfg.Slack.WebhookURL = testWebhookURL
	cfg.Slack.Trigger = TriggerBreach
	cfg.Teams.WebhookURL = "https://outlook.office.com/webhook/token"
	cfg.Teams.Trigger = TriggerRegression

	dispatcher.Dispatch(context.Background(), cfg, report.Report{}, Outcome{Breach: true})
	if slack.calls != 1 {
		t.Fatalf("expected slack to be called once, got %d", slack.calls)
	}
	if teams.calls != 0 {
		t.Fatalf("expected teams not to be called without baseline delta, got %d", teams.calls)
	}
}

func TestDispatcherRedactsWebhookURLInWarnings(t *testing.T) {
	webhook := testWebhookURL
	leaky := &fakeNotifier{err: fmt.Errorf("post failed for %s", webhook)}
	dispatcher := NewDispatcher(map[Channel]Notifier{
		ChannelSlack: leaky,
	})
	cfg := DefaultConfig()
	cfg.Slack.WebhookURL = webhook

	warnings := dispatcher.Dispatch(context.Background(), cfg, report.Report{}, Outcome{})
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %d", len(warnings))
	}
	if strings.Contains(warnings[0], "SECRET") {
		t.Fatalf("expected warning to redact webhook secret, got %q", warnings[0])
	}
	if !strings.Contains(warnings[0], "https://hooks.slack.com/...") {
		t.Fatalf("expected warning to include redacted webhook host, got %q", warnings[0])
	}
}

func TestNewDefaultDispatcherAndMissingNotifierWarning(t *testing.T) {
	dispatcher := NewDefaultDispatcher()
	if _, ok := dispatcher.notifiers[ChannelSlack].(*SlackNotifier); !ok {
		t.Fatalf("expected default slack notifier to use SlackNotifier")
	}
	if _, ok := dispatcher.notifiers[ChannelTeams].(*WebhookNotifier); !ok {
		t.Fatalf("expected default teams notifier to use WebhookNotifier")
	}
	cfg := DefaultConfig()
	cfg.Slack.WebhookURL = "https://hooks.slack.com/services/A/B/C"
	cfg.Slack.Trigger = TriggerAlways

	// Remove default slack notifier to exercise the missing-notifier path.
	dispatcher.notifiers[ChannelSlack] = nil

	warnings := dispatcher.Dispatch(context.Background(), cfg, report.Report{}, Outcome{})
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %d", len(warnings))
	}
	if !strings.Contains(warnings[0], "notifier is not configured") {
		t.Fatalf("expected missing-notifier warning, got %q", warnings[0])
	}
}

func TestSanitizeErrorMessageVariants(t *testing.T) {
	webhook := "https://hooks.slack.com/services/T000/B000/SECRET"
	if got := sanitizeErrorMessage(nil, webhook); got != "request failed" {
		t.Fatalf("expected nil error fallback, got %q", got)
	}

	rawErr := fmt.Errorf("delivery failed for %s", webhook)
	if got := sanitizeErrorMessage(rawErr, ""); !strings.Contains(got, webhook) {
		t.Fatalf("expected webhook to remain when source URL is empty, got %q", got)
	}

	encodedWebhook := url.QueryEscape(webhook)
	encodedErr := fmt.Errorf("delivery failed for %s", encodedWebhook)
	if got := sanitizeErrorMessage(encodedErr, webhook); strings.Contains(got, "SECRET") {
		t.Fatalf("expected encoded webhook to be redacted, got %q", got)
	}
}

func TestDispatcherNilReceiverReturnsNilWarnings(t *testing.T) {
	var dispatcher *Dispatcher
	if warnings := dispatcher.Dispatch(context.Background(), DefaultConfig(), report.Report{}, Outcome{}); len(warnings) != 0 {
		t.Fatalf("expected nil dispatcher to return nil warnings, got %#v", warnings)
	}
}
