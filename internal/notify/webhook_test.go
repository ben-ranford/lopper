package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		defer func() { _ = r.Body.Close() }()
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewWebhookNotifier(server.Client())
	summary := &report.Summary{DependencyCount: 3, UsedPercent: 61.5}
	err := notifier.Notify(context.Background(), Delivery{
		Channel:    ChannelSlack,
		WebhookURL: server.URL,
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
	if payload["tool"] != "lopper" {
		t.Fatalf("expected tool field, got %#v", payload["tool"])
	}
	if payload["channel"] != string(ChannelSlack) {
		t.Fatalf("expected channel field, got %#v", payload["channel"])
	}
}

func TestWebhookNotifierNotifyFailures(t *testing.T) {
	notifier := NewWebhookNotifier(nil)
	if err := notifier.Notify(context.Background(), Delivery{WebhookURL: "://bad"}); err == nil {
		t.Fatalf("expected request build error")
	}

	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer statusServer.Close()
	if err := notifier.Notify(context.Background(), Delivery{WebhookURL: statusServer.URL}); err == nil {
		t.Fatalf("expected non-2xx status error")
	}

	badURL := "http://127.0.0.1:1"
	if err := notifier.Notify(context.Background(), Delivery{WebhookURL: badURL}); err == nil {
		t.Fatalf("expected send failure for unreachable endpoint")
	}
}

func TestParseWebhookURLValidations(t *testing.T) {
	if _, err := ParseWebhookURL("ftp://example.com/hook", "source"); err == nil {
		t.Fatalf("expected scheme validation error")
	}
	if _, err := ParseWebhookURL("https://user:pass@example.com/hook", "source"); err == nil {
		t.Fatalf("expected user info validation error")
	}
	if _, err := ParseWebhookURL("https://example.com/hook#frag", "source"); err == nil {
		t.Fatalf("expected fragment validation error")
	}
}

func TestRedactWebhookURL(t *testing.T) {
	redacted := RedactWebhookURL("https://hooks.slack.com/services/A/B/SECRET")
	if strings.Contains(redacted, "SECRET") {
		t.Fatalf("expected redacted URL to hide secret path, got %q", redacted)
	}
	if redacted != "https://hooks.slack.com/..." {
		t.Fatalf("unexpected redacted URL: %q", redacted)
	}
	if RedactWebhookURL("not-a-url") != "<redacted-webhook>" {
		t.Fatalf("expected fallback redaction for invalid URL")
	}
}
