package notify

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestNewWebhookNotifierUsesDefaultClient(t *testing.T) {
	notifier := NewWebhookNotifier(nil)
	if notifier.Client == nil {
		t.Fatalf("expected default HTTP client")
	}
	if notifier.Client.Timeout != 5*time.Second {
		t.Fatalf("expected 5s timeout, got %s", notifier.Client.Timeout)
	}
}

func TestWebhookNotifierNotifySuccess(t *testing.T) {
	capture := newPayloadCaptureServer(t)
	defer capture.Close()

	notifier := NewWebhookNotifier(capture.Client())
	summary := &report.Summary{DependencyCount: 3, UsedPercent: 61.5}
	err := notifier.Notify(context.Background(), Delivery{
		Channel:    ChannelSlack,
		WebhookURL: capture.URL,
		Trigger:    TriggerAlways,
		Report: report.Report{
			RepoPath: ".",
			Summary:  summary,
		},
		Outcome: Outcome{Breach: true},
	})
	if err != nil {
		t.Fatalf("notify success: %v", err)
	}
	if capture.payload["tool"] != "lopper" {
		t.Fatalf("expected tool field, got %#v", capture.payload["tool"])
	}
	if capture.payload["channel"] != string(ChannelSlack) {
		t.Fatalf("expected channel field, got %#v", capture.payload["channel"])
	}
}

func TestWebhookNotifierNotifyBuildPayloadError(t *testing.T) {
	err := NewWebhookNotifier(nil).Notify(context.Background(), Delivery{
		Channel:    ChannelSlack,
		WebhookURL: exampleHookURL,
		Report: report.Report{
			Summary: &report.Summary{DependencyCount: 1, TotalExportsCount: 1, UsedPercent: math.NaN()},
		},
	})
	if err == nil {
		t.Fatalf("expected notify to fail when webhook payload JSON encoding fails")
	}
}

func TestWebhookNotifierNotifyTeamsAdaptiveCard(t *testing.T) {
	capture := newPayloadCaptureServer(t)
	defer capture.Close()

	notifier := NewWebhookNotifier(capture.Client())
	delta := 1.7
	err := notifier.Notify(context.Background(), Delivery{
		Channel:    ChannelTeams,
		WebhookURL: capture.URL,
		Trigger:    TriggerBreach,
		Report: report.Report{
			RepoPath: ".",
			Summary: &report.Summary{
				DependencyCount:   3,
				UsedExportsCount:  8,
				TotalExportsCount: 10,
				UsedPercent:       80,
			},
		},
		Outcome: Outcome{
			Breach:               true,
			WasteIncreasePercent: &delta,
		},
	})
	if err != nil {
		t.Fatalf("notify success: %v", err)
	}

	if capture.payload["type"] != "message" {
		t.Fatalf("expected Teams message envelope, got %#v", capture.payload["type"])
	}

	attachments, ok := capture.payload["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected one attachment, got %#v", capture.payload["attachments"])
	}
	attachment, ok := attachments[0].(map[string]any)
	if !ok {
		t.Fatalf("expected attachment object")
	}
	if attachment["contentType"] != "application/vnd.microsoft.card.adaptive" {
		t.Fatalf("expected adaptive card content type, got %#v", attachment["contentType"])
	}

	content, ok := attachment["content"].(map[string]any)
	if !ok {
		t.Fatalf("expected attachment content object")
	}
	if content["type"] != "AdaptiveCard" {
		t.Fatalf("expected adaptive card payload, got %#v", content["type"])
	}
}

func TestWebhookNotifierNotifyFailures(t *testing.T) {
	assertNotifyFailures(t, NewWebhookNotifier(nil))
}
