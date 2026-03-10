package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

var ErrInvalidWebhookURL = errors.New("invalid webhook URL")

type WebhookNotifier struct {
	Client *http.Client
}

func NewWebhookNotifier(client *http.Client) *WebhookNotifier {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &WebhookNotifier{Client: client}
}

type webhookPayload struct {
	Tool                 string    `json:"tool"`
	Channel              Channel   `json:"channel"`
	RepoPath             string    `json:"repoPath,omitempty"`
	GeneratedAt          time.Time `json:"generatedAt,omitempty"`
	DependencyCount      int       `json:"dependencyCount"`
	UsedPercent          float64   `json:"usedPercent"`
	Trigger              Trigger   `json:"trigger"`
	Breach               bool      `json:"breach"`
	WasteIncreasePercent *float64  `json:"wasteIncreasePercent,omitempty"`
}

func (n *WebhookNotifier) Notify(ctx context.Context, delivery Delivery) error {
	body, err := buildWebhookPayload(delivery)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("build webhook request")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.Client.Do(req)
	if err != nil {
		return errors.New("send webhook request")
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("unexpected webhook response status: %d", resp.StatusCode)
	}

	return nil
}

func buildWebhookPayload(delivery Delivery) ([]byte, error) {
	summary := report.Summary{}
	if delivery.Report.Summary != nil {
		summary = *delivery.Report.Summary
	}

	payload := webhookPayload{
		Tool:                 "lopper",
		Channel:              delivery.Channel,
		RepoPath:             delivery.Report.RepoPath,
		GeneratedAt:          delivery.Report.GeneratedAt,
		DependencyCount:      summary.DependencyCount,
		UsedPercent:          summary.UsedPercent,
		Trigger:              delivery.Trigger,
		Breach:               delivery.Outcome.Breach,
		WasteIncreasePercent: delivery.Outcome.WasteIncreasePercent,
	}
	return json.Marshal(payload)
}

func ParseWebhookURL(raw, source string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("%w for %s %s: parse failed", ErrInvalidWebhookURL, source, RedactWebhookURL(value))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w for %s %s: scheme must be http or https", ErrInvalidWebhookURL, source, RedactWebhookURL(value))
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("%w for %s %s: host is required", ErrInvalidWebhookURL, source, RedactWebhookURL(value))
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w for %s %s: user info is not allowed", ErrInvalidWebhookURL, source, RedactWebhookURL(value))
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("%w for %s %s: fragment is not allowed", ErrInvalidWebhookURL, source, RedactWebhookURL(value))
	}
	return parsed.String(), nil
}

func RedactWebhookURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "<redacted-webhook>"
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "<redacted-webhook>"
	}
	host := parsed.Host
	if host == "" {
		return "<redacted-webhook>"
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/...", scheme, host)
}
