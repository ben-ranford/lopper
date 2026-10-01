package report

import "testing"

func TestCriticalRiskCueConfidence(t *testing.T) {
	for _, severity := range []string{"critical", " CRITICAL ", "Critical"} {
		for _, cues := range [][]RiskCue{
			{{Severity: severity}},
			{{Severity: "high"}, {Severity: severity}, {Severity: "medium"}, {Severity: "low"}},
		} {
			got := riskSeverityConfidenceSignal(cues)
			if got.signal.Code != "risk-critical" || got.signal.Score != 40 || got.summary != "risk critical" {
				t.Errorf("severity %q: got %+v", severity, got)
			}
			if got := highestRiskSeverity(cues); got != "critical" {
				t.Errorf("highest severity = %q, want critical", got)
			}
		}
	}
}

func TestRiskCueConfidenceSeverityCompatibility(t *testing.T) {
	for _, tc := range []struct {
		severity, code string
		score          float64
	}{
		{"high", "risk-high", 40}, {"medium", "risk-medium", 65}, {"low", "risk-low", 85}, {"unknown", "no-risk-cues", 100},
	} {
		got := riskSeverityConfidenceSignal([]RiskCue{{Severity: tc.severity}}).signal
		if got.Code != tc.code || got.Score != tc.score {
			t.Errorf("severity %q: got %+v", tc.severity, got)
		}
	}
	codes := orderedConfidenceReasonCodes([]string{"risk-critical"})
	if len(codes) != 1 || codes[0] != "risk-critical" {
		t.Fatalf("critical rationale dropped: %v", codes)
	}
}
