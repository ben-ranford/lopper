package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

var ErrInvalidWebhookURL = errors.New("invalid webhook URL")

const (
	teamsAdaptiveCardSchema = "http://adaptivecards.io/schemas/adaptive-card.json"
	defaultRepoPath         = "."
	redactedWebhookValue    = "<redacted-webhook>"
)

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

type teamsEnvelope struct {
	Type        string            `json:"type"`
	Attachments []teamsAttachment `json:"attachments"`
}

type teamsAttachment struct {
	ContentType string            `json:"contentType"`
	ContentURL  *string           `json:"contentUrl"`
	Content     teamsAdaptiveCard `json:"content"`
}

type teamsAdaptiveCard struct {
	Schema  string          `json:"$schema"`
	Type    string          `json:"type"`
	Version string          `json:"version"`
	Body    []teamsTextItem `json:"body"`
}

type teamsTextItem struct {
	Type   string `json:"type"`
	Size   string `json:"size,omitempty"`
	Weight string `json:"weight,omitempty"`
	Wrap   bool   `json:"wrap,omitempty"`
	Text   string `json:"text"`
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
	if delivery.Channel == ChannelTeams {
		return json.Marshal(buildTeamsEnvelope(delivery))
	}

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

func buildTeamsEnvelope(delivery Delivery) teamsEnvelope {
	return teamsEnvelope{
		Type: "message",
		Attachments: []teamsAttachment{
			{
				ContentType: "application/vnd.microsoft.card.adaptive",
				ContentURL:  nil,
				Content: teamsAdaptiveCard{
					Schema:  teamsAdaptiveCardSchema,
					Type:    "AdaptiveCard",
					Version: "1.4",
					Body: []teamsTextItem{
						{Type: "TextBlock", Size: "Medium", Weight: "Bolder", Text: "Lopper Dependency Analysis"},
						{Type: "TextBlock", Wrap: true, Text: fmt.Sprintf("Repository: %s", repoPathOrDefault(delivery.Report.RepoPath))},
						{Type: "TextBlock", Text: fmt.Sprintf("Dependencies analyzed: %d", summaryDependencyCount(delivery.Report))},
						{Type: "TextBlock", Text: fmt.Sprintf("Overall used exports: %s", summaryUsedPercent(delivery.Report))},
						{Type: "TextBlock", Text: fmt.Sprintf("Threshold status: %s", thresholdStatusLabel(delivery.Outcome.Breach))},
						{Type: "TextBlock", Text: wasteDeltaLabel(delivery.Outcome.WasteIncreasePercent)},
					},
				},
			},
		},
	}
}

func summaryDependencyCount(rep report.Report) int {
	if rep.Summary != nil && rep.Summary.DependencyCount > 0 {
		return rep.Summary.DependencyCount
	}
	return len(rep.Dependencies)
}

func summaryUsedPercent(rep report.Report) string {
	if rep.Summary != nil {
		if rep.Summary.TotalExportsCount <= 0 {
			return "n/a"
		}
		return fmt.Sprintf("%d/%d (%.1f%%)", rep.Summary.UsedExportsCount, rep.Summary.TotalExportsCount, rep.Summary.UsedPercent)
	}

	used := 0
	total := 0
	for _, dependency := range rep.Dependencies {
		used += dependency.UsedExportsCount
		total += dependency.TotalExportsCount
	}
	if total <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d (%.1f%%)", used, total, (float64(used)/float64(total))*100)
}

func thresholdStatusLabel(breached bool) string {
	if breached {
		return "breach"
	}
	return "ok"
}

func wasteDeltaLabel(wasteIncreasePercent *float64) string {
	if wasteIncreasePercent == nil {
		return "Waste change vs baseline: n/a"
	}
	delta := *wasteIncreasePercent
	sign := ""
	if delta > 0 {
		sign = "+"
	}
	return "Waste change vs baseline: " + sign + strconv.FormatFloat(delta, 'f', 1, 64) + "%"
}

func repoPathOrDefault(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultRepoPath
	}
	return trimmed
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
		return redactedWebhookValue
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return redactedWebhookValue
	}
	host := parsed.Host
	if host == "" {
		return redactedWebhookValue
	}
	scheme := parsed.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/...", scheme, host)
}
