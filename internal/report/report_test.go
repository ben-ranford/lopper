package report

import "testing"

func TestParseFormat(t *testing.T) {
	if _, err := ParseFormat("table"); err != nil {
		t.Fatalf("parse table format: %v", err)
	}
	if _, err := ParseFormat("json"); err != nil {
		t.Fatalf("parse json format: %v", err)
	}
	if _, err := ParseFormat("sarif"); err != nil {
		t.Fatalf("parse sarif format: %v", err)
	}
	if _, err := ParseFormat("pr-comment"); err != nil {
		t.Fatalf("parse pr-comment format: %v", err)
	}
	if _, err := ParseFormat("nope"); err == nil {
		t.Fatalf("expected unknown format error")
	}
}
