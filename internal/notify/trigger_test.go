package notify

import "testing"

func TestParseTrigger(t *testing.T) {
	trigger, err := ParseTrigger("breach")
	if err != nil {
		t.Fatalf("parse trigger: %v", err)
	}
	if trigger != TriggerBreach {
		t.Fatalf("expected breach trigger, got %q", trigger)
	}

	if _, err := ParseTrigger("not-a-trigger"); err == nil {
		t.Fatalf("expected parse error for unknown trigger")
	}
}

func TestShouldTrigger(t *testing.T) {
	regression := 5.2
	improvement := -3.1

	if !ShouldTrigger(TriggerAlways, Outcome{}) {
		t.Fatalf("always trigger should always notify")
	}
	if !ShouldTrigger(TriggerBreach, Outcome{Breach: true}) {
		t.Fatalf("breach trigger should notify on breach")
	}
	if ShouldTrigger(TriggerBreach, Outcome{Breach: false}) {
		t.Fatalf("breach trigger should not notify without breach")
	}
	if !ShouldTrigger(TriggerRegression, Outcome{WasteIncreasePercent: &regression}) {
		t.Fatalf("regression trigger should notify on positive delta")
	}
	if ShouldTrigger(TriggerRegression, Outcome{}) {
		t.Fatalf("regression trigger should require baseline delta")
	}
	if !ShouldTrigger(TriggerImprovement, Outcome{WasteIncreasePercent: &improvement}) {
		t.Fatalf("improvement trigger should notify on negative delta")
	}
	if ShouldTrigger(TriggerImprovement, Outcome{}) {
		t.Fatalf("improvement trigger should require baseline delta")
	}
	if ShouldTrigger(Trigger("unknown"), Outcome{}) {
		t.Fatalf("unknown trigger should not notify")
	}
}

func TestConfigHasTargets(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.HasTargets() {
		t.Fatalf("expected no targets by default")
	}
	cfg.Slack.WebhookURL = "https://hooks.slack.com/services/A/B/C"
	if !cfg.HasTargets() {
		t.Fatalf("expected target after webhook is configured")
	}
}
