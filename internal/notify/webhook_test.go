package notify

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

const decodePayloadErrFmt = "decode payload: %v"

func newPayloadCaptureServer(t *testing.T, payload *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		defer func() {
			if err := r.Body.Close(); err != nil {
				t.Fatalf("close request body: %v", err)
			}
		}()
		if err := json.NewDecoder(r.Body).Decode(payload); err != nil {
			t.Fatalf(decodePayloadErrFmt, err)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

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
	server := newPayloadCaptureServer(t, &payload)
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

func TestWebhookNotifierNotifyBuildPayloadError(t *testing.T) {
	err := NewWebhookNotifier(nil).Notify(context.Background(), Delivery{
		Channel:    ChannelSlack,
		WebhookURL: "https://example.com/hook",
		Report: report.Report{
			Summary: &report.Summary{DependencyCount: 1, TotalExportsCount: 1, UsedPercent: math.NaN()},
		},
	})
	if err == nil {
		t.Fatalf("expected notify to fail when webhook payload JSON encoding fails")
	}
}

func TestWebhookNotifierNotifyTeamsAdaptiveCard(t *testing.T) {
	var payload map[string]any
	server := newPayloadCaptureServer(t, &payload)
	defer server.Close()

	notifier := NewWebhookNotifier(server.Client())
	delta := 1.7
	err := notifier.Notify(context.Background(), Delivery{
		Channel:    ChannelTeams,
		WebhookURL: server.URL,
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

	if payload["type"] != "message" {
		t.Fatalf("expected Teams message envelope, got %#v", payload["type"])
	}

	attachments, ok := payload["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected one attachment, got %#v", payload["attachments"])
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

func TestParseWebhookURLValidations(t *testing.T) {
	if value, err := ParseWebhookURL("   ", "source"); err != nil || value != "" {
		t.Fatalf("expected empty webhook value to be accepted as unset, got value=%q err=%v", value, err)
	}
	if _, err := ParseWebhookURL("ftp://example.com/hook", "source"); err == nil {
		t.Fatalf("expected scheme validation error")
	}
	user := strings.ToLower("WEBHOOK-USER")
	credential := strings.Repeat("x", 12)
	withCredentials := (&url.URL{
		Scheme: "https",
		User:   url.UserPassword(user, credential),
		Host:   "example.com",
		Path:   "/hook",
	}).String()
	if _, err := ParseWebhookURL(withCredentials, "source"); err == nil {
		t.Fatalf("expected user info validation error")
	}
	if _, err := ParseWebhookURL("https://example.com/hook#frag", "source"); err == nil {
		t.Fatalf("expected fragment validation error")
	}
	if _, err := ParseWebhookURL("https:///hook", "source"); err == nil {
		t.Fatalf("expected missing host validation error")
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

func TestBuildWebhookPayloadTeamsTextIncludesOutcome(t *testing.T) {
	delta := -2.5
	payload, err := buildWebhookPayload(Delivery{
		Channel: ChannelTeams,
		Report: report.Report{
			RepoPath: ".",
			Summary: &report.Summary{
				DependencyCount:   4,
				UsedExportsCount:  9,
				TotalExportsCount: 12,
				UsedPercent:       75,
			},
		},
		Outcome: Outcome{
			Breach:               false,
			WasteIncreasePercent: &delta,
		},
	})
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	attachments, ok := envelope["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected single attachment, got %#v", envelope["attachments"])
	}
	attachment, ok := attachments[0].(map[string]any)
	if !ok {
		t.Fatalf("expected attachment object")
	}
	content, ok := attachment["content"].(map[string]any)
	if !ok {
		t.Fatalf("expected adaptive card content")
	}
	body, ok := content["body"].([]any)
	if !ok || len(body) < 6 {
		t.Fatalf("expected adaptive card body text blocks, got %#v", content["body"])
	}
	last, ok := body[len(body)-1].(map[string]any)
	if !ok {
		t.Fatalf("expected text block object")
	}
	if !strings.Contains(last["text"].(string), "-2.5%") {
		t.Fatalf("expected waste delta text, got %#v", last["text"])
	}
}

func TestSummaryUsedPercentAndWasteDeltaHelpers(t *testing.T) {
	percent := summaryUsedPercent(report.Report{
		Dependencies: []report.DependencyReport{
			{UsedExportsCount: 3, TotalExportsCount: 4},
			{UsedExportsCount: 1, TotalExportsCount: 2},
		},
	})
	if percent != "4/6 (66.7%)" {
		t.Fatalf("unexpected summary percent: %q", percent)
	}
	if got := summaryUsedPercent(report.Report{}); got != "n/a" {
		t.Fatalf("expected n/a for missing totals, got %q", got)
	}

	positive := 1.1
	if got := wasteDeltaLabel(&positive); got != "Waste change vs baseline: +1.1%" {
		t.Fatalf("unexpected positive delta label: %q", got)
	}
	negative := -3.0
	if got := wasteDeltaLabel(&negative); got != "Waste change vs baseline: -3.0%" {
		t.Fatalf("unexpected negative delta label: %q", got)
	}
	if got := wasteDeltaLabel(nil); got != "Waste change vs baseline: n/a" {
		t.Fatalf("unexpected nil delta label: %q", got)
	}
}

func TestSummaryDependencyCountThresholdStatusAndRepoPathOrDefault(t *testing.T) {
	if got := summaryDependencyCount(report.Report{Summary: &report.Summary{DependencyCount: 7}}); got != 7 {
		t.Fatalf("expected summary dependency count, got %d", got)
	}
	if got := summaryDependencyCount(report.Report{Dependencies: []report.DependencyReport{{}, {}, {}}}); got != 3 {
		t.Fatalf("expected fallback dependency count, got %d", got)
	}

	if thresholdStatusLabel(true) != "breach" {
		t.Fatalf("expected breach status label")
	}
	if thresholdStatusLabel(false) != "ok" {
		t.Fatalf("expected ok status label")
	}

	if got := repoPathOrDefault("  "); got != "." {
		t.Fatalf("expected fallback value, got %q", got)
	}
	if got := repoPathOrDefault("repo"); got != "repo" {
		t.Fatalf("expected original value, got %q", got)
	}

	delta := 0.0
	got := wasteDeltaLabel(&delta)
	expected := "Waste change vs baseline: " + strconv.FormatFloat(delta, 'f', 1, 64) + "%"
	if got != expected {
		t.Fatalf("expected zero delta label %q, got %q", expected, got)
	}
}

func TestWebhookHelperBranches(t *testing.T) {
	if got := summaryUsedPercent(report.Report{Summary: &report.Summary{TotalExportsCount: 0}}); got != "n/a" {
		t.Fatalf("expected n/a when summary has no totals, got %q", got)
	}

	if value, err := ParseWebhookURL("https://example.com/hook", "source"); err != nil || value != "https://example.com/hook" {
		t.Fatalf("expected valid webhook URL to round-trip, value=%q err=%v", value, err)
	}
	if _, err := ParseWebhookURL("https://[::1", "source"); err == nil {
		t.Fatalf("expected webhook URL parse failure to be reported")
	}

	if got := RedactWebhookURL("   "); got != "<redacted-webhook>" {
		t.Fatalf("expected empty webhook URL to use fallback redaction, got %q", got)
	}
	if got := RedactWebhookURL("//hooks.slack.com/services/A/B/SECRET"); got != "https://hooks.slack.com/..." {
		t.Fatalf("expected schemeless webhook URL to default to https, got %q", got)
	}
}
