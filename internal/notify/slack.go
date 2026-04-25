package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

type SlackNotifier struct {
	Client *http.Client
}

func NewSlackNotifier(client *http.Client) *SlackNotifier {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &SlackNotifier{Client: client}
}

type slackPayload struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks,omitempty"`
}

type slackBlock struct {
	Type     string      `json:"type"`
	Text     *slackText  `json:"text,omitempty"`
	Fields   []slackText `json:"fields,omitempty"`
	Elements []slackText `json:"elements,omitempty"`
}

type slackText struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

func (n *SlackNotifier) Notify(ctx context.Context, delivery Delivery) error {
	body, err := buildSlackPayload(delivery)
	if err != nil {
		return err
	}
	return sendWebhookJSON(ctx, n.Client, delivery.WebhookURL, body, "build slack webhook request", "send slack webhook request", "unexpected slack webhook response status: %d")
}

func buildSlackPayload(delivery Delivery) ([]byte, error) {
	summary := delivery.Report.Summary
	if summary == nil {
		summary = report.ComputeSummary(delivery.Report.Dependencies)
	}
	if summary == nil {
		summary = &report.Summary{}
	}

	repoPath := strings.TrimSpace(delivery.Report.RepoPath)
	if repoPath == "" {
		repoPath = "."
	}
	repoName := filepath.Base(repoPath)
	if repoName == "." || repoName == string(filepath.Separator) {
		repoName = repoPath
	}

	status := "PASSED"
	if delivery.Outcome.Breach {
		status = "BREACHED"
	}

	fields := []slackText{
		{Type: "mrkdwn", Text: fmt.Sprintf("*Dependencies*\n%d", summary.DependencyCount)},
		{Type: "mrkdwn", Text: fmt.Sprintf("*Used exports*\n%.1f%%", summary.UsedPercent)},
		{Type: "mrkdwn", Text: fmt.Sprintf("*Threshold status*\n%s", status)},
	}
	if delivery.Outcome.WasteIncreasePercent != nil {
		fields = append(fields, slackText{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*Waste delta*\n%+.2f%%", *delivery.Outcome.WasteIncreasePercent),
		})
	}

	mainText := fmt.Sprintf("[Lopper] Dependency analysis for %s", repoName)
	payload := slackPayload{
		Text: mainText,
		Blocks: []slackBlock{
			{
				Type: "header",
				Text: &slackText{Type: "plain_text", Text: "Lopper Dependency Analysis", Emoji: true},
			},
			{
				Type: "section",
				Text: &slackText{
					Type: "plain_text",
					Text: fmt.Sprintf("Repository: %s\nTrigger: %s", repoPath, delivery.Trigger),
				},
			},
			{
				Type:   "section",
				Fields: fields,
			},
		},
	}
	if !delivery.Report.GeneratedAt.IsZero() {
		payload.Blocks = append(payload.Blocks, slackBlock{
			Type: "context",
			Elements: []slackText{
				{Type: "mrkdwn", Text: fmt.Sprintf("Generated: %s", delivery.Report.GeneratedAt.UTC().Format(time.RFC3339))},
			},
		})
	}

	return json.Marshal(payload)
}
