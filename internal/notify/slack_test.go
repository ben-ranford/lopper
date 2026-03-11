package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ben-ranford/lopper/internal/report"
)

func TestNewSlackNotifierUsesDefaultClient(t *testing.T) {
	notifier := NewSlackNotifier(nil)
	if notifier.Client == nil {
		t.Fatalf("expected default HTTP client")
	}
	if notifier.Client.Timeout != 5*time.Second {
		t.Fatalf("expected 5s timeout, got %s", notifier.Client.Timeout)
	}
}

func TestSlackNotifierNotifySuccess(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		defer func() {
			if err := r.Body.Close(); err != nil {
				t.Fatalf("close request body: %v", err)
			}
		}()
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifier := NewSlackNotifier(server.Client())
	delta := 2.4
	err := notifier.Notify(context.Background(), Delivery{
		Channel:    ChannelSlack,
		WebhookURL: server.URL,
		Trigger:    TriggerBreach,
		Report: report.Report{
			RepoPath:    "repo/service",
			GeneratedAt: time.Date(2026, 3, 10, 2, 3, 4, 0, time.UTC),
			Summary:     &report.Summary{DependencyCount: 5, UsedPercent: 62.5},
		},
		Outcome: Outcome{
			Breach:               true,
			WasteIncreasePercent: &delta,
		},
	})
	if err != nil {
		t.Fatalf("notify success: %v", err)
	}
	if payload["text"] == "" {
		t.Fatalf("expected top-level fallback text")
	}
	blocks, ok := payload["blocks"].([]any)
	if !ok || len(blocks) < 3 {
		t.Fatalf("expected block kit payload, got %#v", payload["blocks"])
	}
}

func TestBuildSlackPayloadIncludesThresholdStatus(t *testing.T) {
	data, err := buildSlackPayload(Delivery{
		Channel: ChannelSlack,
		Trigger: TriggerRegression,
		Report: report.Report{
			RepoPath: "repo/path",
			Summary:  &report.Summary{DependencyCount: 3, UsedPercent: 45.5},
		},
		Outcome: Outcome{Breach: true},
	})
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	type field struct {
		Text string `json:"text"`
	}
	type block struct {
		Fields []field `json:"fields"`
	}
	type payload struct {
		Blocks []block `json:"blocks"`
	}

	var decoded payload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	foundStatus := false
	for _, block := range decoded.Blocks {
		for _, field := range block.Fields {
			if field.Text == "*Threshold status*\nBREACHED" {
				foundStatus = true
				break
			}
		}
	}
	if !foundStatus {
		t.Fatalf("expected threshold status field in payload, got %#v", decoded)
	}
}

func TestSlackNotifierNotifyFailures(t *testing.T) {
	assertNotifyFailures(t, NewSlackNotifier(nil))
}
