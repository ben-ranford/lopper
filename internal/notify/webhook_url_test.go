package notify

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseWebhookURLValidations(t *testing.T) {
	if value, err := ParseWebhookURL("   ", "source"); err != nil || value != "" {
		t.Fatalf("expected empty webhook value to be accepted as unset, got value=%q err=%v", value, err)
	}
	if value, err := ParseWebhookURL(exampleHookURL, "source"); err != nil || value != exampleHookURL {
		t.Fatalf("expected valid webhook URL to round-trip, value=%q err=%v", value, err)
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
	if _, err := ParseWebhookURL(exampleHookURL+"#frag", "source"); err == nil {
		t.Fatalf("expected fragment validation error")
	}
	if _, err := ParseWebhookURL("https:///hook", "source"); err == nil {
		t.Fatalf("expected missing host validation error")
	}
	if _, err := ParseWebhookURL("https://[::1", "source"); err == nil {
		t.Fatalf("expected webhook URL parse failure to be reported")
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
	if got := RedactWebhookURL("   "); got != "<redacted-webhook>" {
		t.Fatalf("expected empty webhook URL to use fallback redaction, got %q", got)
	}
	if got := RedactWebhookURL("//hooks.slack.com/services/A/B/SECRET"); got != "https://hooks.slack.com/..." {
		t.Fatalf("expected schemeless webhook URL to default to https, got %q", got)
	}
	if RedactWebhookURL("not-a-url") != "<redacted-webhook>" {
		t.Fatalf("expected fallback redaction for invalid URL")
	}
}
