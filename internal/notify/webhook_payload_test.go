package notify

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/ben-ranford/lopper/internal/report"
)

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
		t.Fatalf(decodePayloadErrFmt, err)
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
	if last["text"] != "Waste change vs baseline: -2.5%" {
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
	if got := summaryUsedPercent(report.Report{Summary: &report.Summary{TotalExportsCount: 0}}); got != "n/a" {
		t.Fatalf("expected n/a when summary has no totals, got %q", got)
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

	delta := 0.0
	got := wasteDeltaLabel(&delta)
	expected := "Waste change vs baseline: " + strconv.FormatFloat(delta, 'f', 1, 64) + "%"
	if got != expected {
		t.Fatalf("expected zero delta label %q, got %q", expected, got)
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
}
